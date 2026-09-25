package ui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func receivePendingTest[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), scaleWait(5*time.Second))
	defer cancel()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatalf("pending recovery test barrier: %v", ctx.Err())
		var zero T
		return zero
	}
}

func TestPendingApprovalRecoveryLateControlCannotOverwriteLaterAsk(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	controller := &pendingApprovalFixtureController{
		watch: &pendingApprovalFixtureWatch{}, resolveStarted: started, resolveRelease: release,
	}
	m := pendingRecoveryModel(t, controller, "")
	old := m.pendingRecovery.approval

	next, choose := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = next.(Model)
	result := make(chan tea.Msg, 1)
	var execute func(tea.Cmd)
	execute = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, child := range msg {
				execute(child)
			}
		case pendingApprovalResolvedMsg:
			result <- msg
		}
	}
	go execute(choose)
	receivePendingTest(t, started)
	var releaseOnce sync.Once
	releaseControl := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseControl()

	// The first choice removes the approval surface before its acknowledgement.
	// Repeating the same key while that RPC is blocked must not submit it again.
	next, repeated := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = next.(Model)
	repeatedDone := make(chan struct{})
	go func() {
		execute(repeated)
		close(repeatedDone)
	}()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), scaleWait(5*time.Second))
	defer cancelWait()
	select {
	case <-repeatedDone:
	case <-started:
		t.Fatal("repeated approval input started a second RPC")
	case <-waitCtx.Done():
		t.Fatalf("repeated approval input did not settle: %v", waitCtx.Err())
	}
	if calls, _, _ := controller.counts(); calls != 1 {
		t.Fatalf("resolve calls while acknowledgement blocked = %d, want one", calls)
	}

	later := old
	later.AskID, later.Tool, later.Cursor = "ask-later", "Shell", "cursor-later"
	next, _ = m.Update(pendingApprovalWatchMsg{generation: m.pendingRecovery.generation, event: client.PendingApprovalEvent{
		Kind: client.PendingApprovalEventAsk, Approval: &later,
	}})
	m = next.(Model)
	releaseControl()
	next, _ = m.Update(receivePendingTest(t, result))
	m = next.(Model)
	if m.pendingRecovery == nil || m.pendingRecovery.approval.AskID != later.AskID || m.pendingRecovery.resolved {
		t.Fatalf("stale old control changed later ask: %+v", m.pendingRecovery)
	}
	if calls, _, _ := controller.counts(); calls != 1 {
		t.Fatalf("resolve calls = %d, want one", calls)
	}
	controller.mu.Lock()
	got := append([]client.PendingApproval(nil), controller.resolvedFor...)
	controller.mu.Unlock()
	if len(got) != 1 || !samePendingApproval(got[0], old) {
		t.Fatalf("resolved identities = %+v, want old %+v", got, old)
	}
}

func TestPendingApprovalRecoveryQuitDuringBlockedResolve(t *testing.T) {
	for _, direct := range []bool{false, true} {
		name := "double-ctrl-c"
		if direct {
			name = "quit-now"
		}
		t.Run(name, func(t *testing.T) {
			controller := &pendingApprovalFixtureController{
				watch:          &pendingApprovalFixtureWatch{},
				resolveStarted: make(chan struct{}, 1), resolveRelease: make(chan struct{}),
			}
			m := pendingRecoveryModel(t, controller, "")
			recovery := m.pendingRecovery
			t.Cleanup(recovery.cancel)
			t.Cleanup(controller.watch.Close)
			next, choose := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
			m = next.(Model)
			result := make(chan pendingApprovalResolvedMsg, 1)
			var execute func(tea.Cmd)
			execute = func(cmd tea.Cmd) {
				if cmd == nil {
					return
				}
				switch msg := cmd().(type) {
				case tea.BatchMsg:
					for _, child := range msg {
						execute(child)
					}
				case pendingApprovalResolvedMsg:
					result <- msg
				}
			}
			go execute(choose)
			receivePendingTest(t, controller.resolveStarted)
			var quit tea.Cmd
			if direct {
				next, quit = m.quitNow()
			} else {
				next, _ = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
				m = next.(Model)
				if !m.quitArmed || recovery.ctx.Err() != nil || controller.watch.isClosed() {
					t.Fatal("first ctrl+c must arm exit without cancelling the owner choice")
				}
				next, quit = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			}
			m = next.(Model)
			if quit == nil {
				t.Fatal("blocked Resolve prevented exit")
			}
			msg := quit()
			if _, ok := msg.(tea.QuitMsg); !ok {
				t.Fatalf("exit command = %T, want tea.QuitMsg", msg)
			}
			if m.pendingRecovery != nil || !controller.watch.isClosed() {
				t.Fatal("exit did not retire recovery and close watch")
			}
			ack := receivePendingTest(t, result)
			if !errors.Is(ack.err, context.Canceled) {
				t.Fatalf("blocked Resolve did not observe owned context cancellation: %v", ack.err)
			}
			// Either a cancelled RPC or a late successful acknowledgement is stale.
			for _, err := range []error{ack.err, nil} {
				ack.err = err
				next, cmd := m.Update(ack)
				if cmd != nil || next.(Model).pendingRecovery != nil || next.(Model).phase != m.phase {
					t.Fatal("late acknowledgement reactivated retired recovery")
				}
			}
			if resolves, cancels, verdict := controller.counts(); resolves != 1 || cancels != 0 || verdict != client.VerdictAllowOnce {
				t.Fatalf("exit controls: resolve=%d cancel=%d verdict=%v", resolves, cancels, verdict)
			}
		})
	}
}

func TestPendingApprovalRecoveryFailureRetiresQueuedMessages(t *testing.T) {
	watch := &pendingApprovalFixtureWatch{}
	controller := &pendingApprovalFixtureController{watch: watch, resolveErr: errors.New("rejected")}
	m := pendingRecoveryModel(t, controller, "")
	generation := m.pendingRecovery.generation

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = applyPendingCommand(t, next.(Model), cmd)
	if m.pendingRecovery != nil || m.phase != phaseFatal || !watch.isClosed() {
		t.Fatalf("failed control did not retire recovery: recovery=%+v phase=%v closed=%t", m.pendingRecovery, m.phase, watch.isClosed())
	}
	queued := []tea.Msg{
		pendingApprovalWatchMsg{generation: generation, event: client.PendingApprovalEvent{Kind: client.PendingApprovalEventOther, Message: client.ToolProgressMsg{Text: "late"}}},
		pendingApprovalWatchMsg{generation: generation, event: client.PendingApprovalEvent{Kind: client.PendingApprovalEventAsk, Approval: &client.PendingApproval{AskID: "late"}}},
		pendingApprovalWatchMsg{generation: generation, event: client.PendingApprovalEvent{Kind: client.PendingApprovalEventTerminal, Message: client.ResultMsg{Stop: "end_turn"}}},
	}
	for _, msg := range queued {
		next, cmd = m.Update(msg)
		m = next.(Model)
		if cmd != nil || m.pendingRecovery != nil || m.phase != phaseFatal {
			t.Fatalf("late message %T rearmed recovery: recovery=%+v phase=%v cmd=%v", msg, m.pendingRecovery, m.phase, cmd != nil)
		}
	}
	if calls, _, _ := controller.counts(); calls != 1 {
		t.Fatalf("failed verdict resent %d times", calls)
	}
}

func TestPendingApprovalRecoveryQuitBeforeOpenClosesLateWatch(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	watch := &pendingApprovalFixtureWatch{}
	controller := &pendingApprovalFixtureController{watch: watch, watchStarted: started, watchRelease: release}
	approval := client.PendingApproval{SessionID: "session", RunID: "run", AskID: "ask", Cursor: "cursor"}
	m := newTestModelFromDeps(Deps{
		Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), PendingApprovals: controller,
		Resume:      &client.ResumeSelection{Row: client.SessionListItem{ID: "session", Kind: client.SessionKindMain}, Transcript: client.SessionTranscript{SessionID: "session", Complete: true}, Pending: &approval},
		NoAltScreen: true,
	})
	cmd := m.openPendingApprovalWatchCmd()
	opened := make(chan tea.Msg, 1)
	go func() { opened <- cmd() }()
	receivePendingTest(t, started)
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseOpen()
	next, quit := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(Model)
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("quit command = %T", quit())
	}
	releaseOpen()
	msg := receivePendingTest(t, opened)
	if !watch.isClosed() {
		t.Fatal("watch returned after cancellation was not closed by opener")
	}
	next, _ = m.Update(msg)
	m = next.(Model)
	if m.pendingRecovery != nil {
		t.Fatal("late open republished retired recovery")
	}
}

func TestPendingApprovalRecoveryReconcilesSnapshotToolCalls(t *testing.T) {
	approval := client.PendingApproval{SessionID: "session", RunID: "run", AskID: "ask", Cursor: "cursor"}
	transcript := client.SessionTranscript{SessionID: "session", Complete: true, Messages: []client.ConversationMessage{{
		Role: "assistant", Text: "working", ToolCalls: []client.ConvToolCall{
			{ID: "call-1", Name: "Write", Args: `{"path":"old"}`},
			{ID: "call-2", Name: "Shell", Args: `{"command":"queued"}`},
		},
	}}}
	controller := &pendingApprovalFixtureController{watch: &pendingApprovalFixtureWatch{}}
	m := newTestModelFromDeps(Deps{
		Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), PendingApprovals: controller,
		Resume: &client.ResumeSelection{Row: client.SessionListItem{ID: "session", Kind: client.SessionKindMain}, Transcript: transcript, Pending: &approval}, NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	opened := m.openPendingApprovalWatchCmd()()
	next, _ := m.Update(opened)
	m = next.(Model)
	generation := m.pendingRecovery.generation

	next, _ = m.Update(pendingApprovalWatchMsg{generation: generation, event: client.PendingApprovalEvent{
		Kind: client.PendingApprovalEventOther, Message: client.ToolCallMsg{ID: "call-1", Name: "Write", Args: `{"path":"actual"}`},
	}})
	m = next.(Model)
	var tools []block
	for _, b := range m.conv.blocks {
		if b.kind == blockTool {
			tools = append(tools, b)
		}
	}
	if len(tools) != 2 || tools[0].toolArgs != `{"path":"actual"}` || tools[1].toolID != "call-2" {
		t.Fatalf("reconciled tools = %+v", tools)
	}

	// Once the recovered occurrence is consumed, a later legitimate reuse is a new card.
	next, _ = m.Update(pendingApprovalWatchMsg{generation: generation, event: client.PendingApprovalEvent{
		Kind: client.PendingApprovalEventOther, Message: client.ToolCallMsg{ID: "call-1", Name: "Read", Args: `{"path":"later"}`},
	}})
	m = next.(Model)
	tools = tools[:0]
	for _, b := range m.conv.blocks {
		if b.kind == blockTool {
			tools = append(tools, b)
		}
	}
	if len(tools) != 3 || tools[2].toolName != "Read" {
		t.Fatalf("later reused call identity was globally deduplicated: %+v", tools)
	}
}

func TestPendingApprovalRecoverySkipsUnmappedTelemetryOnlyAfterChoice(t *testing.T) {
	for _, state := range []string{"before-choice", "resolving", "resolved", "later-ask"} {
		t.Run(state, func(t *testing.T) {
			controller := &pendingApprovalFixtureController{watch: &pendingApprovalFixtureWatch{}}
			m := pendingRecoveryModel(t, controller, "draft only")
			t.Cleanup(m.pendingRecovery.cancel)
			t.Cleanup(controller.watch.Close)
			if state != "before-choice" {
				next, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
				m = next.(Model)
				if state != "resolving" {
					m = applyPendingCommand(t, m, cmd)
				}
			}
			if state == "later-ask" {
				later := m.pendingRecovery.approval
				later.AskID = "later"
				next, _ := m.Update(pendingApprovalWatchMsg{generation: m.pendingRecovery.generation, event: client.PendingApprovalEvent{
					Kind: client.PendingApprovalEventAsk, Approval: &later,
				}})
				m = next.(Model)
			}
			beforePhase := m.phase
			resolves, cancels, _ := controller.counts()
			next, cmd := m.Update(pendingApprovalWatchMsg{generation: m.pendingRecovery.generation, event: client.PendingApprovalEvent{
				Kind: client.PendingApprovalEventOther,
			}})
			m = next.(Model)
			if state == "before-choice" || state == "later-ask" {
				if m.phase != phaseFatal || m.pendingRecovery != nil || !controller.watch.isClosed() || cmd != nil {
					t.Fatal("uncertain pending permission state did not fail closed")
				}
			} else {
				if m.phase != beforePhase || m.pendingRecovery == nil || controller.watch.isClosed() || cmd == nil {
					t.Fatal("incidental telemetry changed run state or failed to rearm watch")
				}
				next, _ = m.Update(pendingApprovalWatchMsg{generation: m.pendingRecovery.generation, event: client.PendingApprovalEvent{
					Kind: client.PendingApprovalEventTerminal, Message: client.ResultMsg{Stop: "end_turn"},
				}})
				m = next.(Model)
				if m.phase != phaseIdle || m.pendingRecovery != nil || !controller.watch.isClosed() {
					t.Fatal("terminal continuation did not settle recovery")
				}
			}
			if gotResolves, gotCancels, _ := controller.counts(); gotResolves != resolves || gotCancels != cancels || m.prompt.Value() != "draft only" {
				t.Fatal("telemetry changed controls or draft")
			}
		})
	}
}

func TestPendingApprovalRecoveryMalformedOpenReleasesWatch(t *testing.T) {
	for _, result := range []pendingApprovalWatchResult{
		{event: client.PendingApprovalEvent{}},
		{event: client.PendingApprovalEvent{Kind: client.PendingApprovalEventAsk}},
		{err: errors.New("stream failed")},
	} {
		watch := &pendingApprovalFixtureWatch{events: make(chan pendingApprovalWatchResult, 1), done: make(chan struct{})}
		watch.events <- result
		controller := &pendingApprovalFixtureController{watch: watch}
		approval := client.PendingApproval{SessionID: "session", RunID: "run", AskID: "ask", Cursor: "cursor"}
		m := newTestModelFromDeps(Deps{
			Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), PendingApprovals: controller,
			Resume: &client.ResumeSelection{Row: client.SessionListItem{ID: "session"}, Transcript: client.SessionTranscript{Complete: true}, Pending: &approval},
		})
		next, _ := m.Update(m.openPendingApprovalWatchCmd()())
		m = next.(Model)
		if !watch.isClosed() || m.pendingRecovery != nil || m.phase != phaseFatal {
			t.Fatalf("malformed open leaked: result=%+v closed=%t recovery=%+v phase=%v", result, watch.isClosed(), m.pendingRecovery, m.phase)
		}
	}
}
