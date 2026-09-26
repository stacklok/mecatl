package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type pendingApprovalWatchResult struct {
	event client.PendingApprovalEvent
	err   error
}

type pendingApprovalFixtureWatch struct {
	mu           sync.Mutex
	closed       bool
	boundarySent bool
	once         sync.Once
	events       chan pendingApprovalWatchResult
	done         chan struct{}
}

func (w *pendingApprovalFixtureWatch) Recv() (client.PendingApprovalEvent, error) {
	if w.events == nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		if !w.boundarySent {
			w.boundarySent = true
			return client.PendingApprovalEvent{Kind: client.PendingApprovalEventBoundary}, nil
		}
		return client.PendingApprovalEvent{}, context.Canceled
	}
	select {
	case result := <-w.events:
		return result.event, result.err
	case <-w.done:
		return client.PendingApprovalEvent{}, context.Canceled
	}
}
func (w *pendingApprovalFixtureWatch) Close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		if w.done != nil {
			close(w.done)
		}
	})
}
func (w *pendingApprovalFixtureWatch) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

type pendingApprovalFixtureController struct {
	mu             sync.Mutex
	watch          *pendingApprovalFixtureWatch
	watchCalls     int
	resolveCalls   int
	cancelCalls    int
	verdict        client.Verdict
	resolvedFor    []client.PendingApproval
	cancelledFor   []client.PendingApproval
	resolved       chan struct{}
	resolveStarted chan struct{}
	resolveRelease chan struct{}
	resolveErr     error
	watchStarted   chan struct{}
	watchRelease   chan struct{}
}

func (f *pendingApprovalFixtureController) WatchPendingApprovalRun(context.Context, client.PendingApproval) (PendingApprovalWatch, error) {
	f.mu.Lock()
	f.watchCalls++
	started, release, watch := f.watchStarted, f.watchRelease, f.watch
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		<-release
	}
	return watch, nil
}
func (f *pendingApprovalFixtureController) ResolvePendingApproval(ctx context.Context, approval client.PendingApproval, verdict client.Verdict) error {
	f.mu.Lock()
	f.resolveCalls++
	f.verdict = verdict
	f.resolvedFor = append(f.resolvedFor, approval)
	started, release, resolved, err := f.resolveStarted, f.resolveRelease, f.resolved, f.resolveErr
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if resolved != nil {
		resolved <- struct{}{}
	}
	return err
}
func (f *pendingApprovalFixtureController) CancelPendingRun(_ context.Context, approval client.PendingApproval) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	f.cancelledFor = append(f.cancelledFor, approval)
	return nil
}

func (f *pendingApprovalFixtureController) counts() (int, int, client.Verdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolveCalls, f.cancelCalls, f.verdict
}

func pendingRecoveryModel(t *testing.T, controller *pendingApprovalFixtureController, prompt string) Model {
	t.Helper()
	approval := client.PendingApproval{SessionID: "session", RunID: "run", AskID: "ask", Tool: "Write", Args: `{"path":"x"}`, Reason: "write?", Cursor: "cursor"}
	selection := &client.ResumeSelection{
		Row:        client.SessionListItem{ID: "session", Kind: client.SessionKindMain},
		Transcript: client.SessionTranscript{SessionID: "session", Complete: true, Kind: client.SessionKindMain},
		Snapshot:   client.SessionSnapshot{State: "awaiting"}, Pending: &approval,
	}
	m := newTestModelFromDeps(Deps{
		Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), Resume: selection,
		PendingApprovals: controller, InitialPrompt: prompt, NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	opened := m.openPendingApprovalWatchCmd()()
	next, _ := m.Update(opened)
	return next.(Model)
}

func applyPendingCommand(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	var messages []tea.Msg
	var collect func(tea.Cmd, int)
	collect = func(next tea.Cmd, depth int) {
		if next == nil || depth > 4 {
			return
		}
		msg := next()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				collect(child, depth+1)
			}
			return
		}
		switch msg.(type) {
		case pendingApprovalResolvedMsg, pendingApprovalCancelledMsg:
			messages = append(messages, msg)
		}
	}
	collect(cmd, 0)
	for _, msg := range messages {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestPendingApprovalRecoveryUIExplicitControls(t *testing.T) {
	controller := &pendingApprovalFixtureController{watch: &pendingApprovalFixtureWatch{}}
	m := pendingRecoveryModel(t, controller, "keep this draft")
	if controller.resolveCalls != 0 || controller.cancelCalls != 0 || m.prompt.Value() != "keep this draft" {
		t.Fatalf("startup sent control or lost draft: resolve=%d cancel=%d draft=%q", controller.resolveCalls, controller.cancelCalls, m.prompt.Value())
	}
	surface := approvalSurfaceOf(t, m)
	if surface.ask.offerAlways {
		t.Fatal("recovered approval offered Allow Always")
	}

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = applyPendingCommand(t, next.(Model), cmd)
	if controller.resolveCalls != 1 || controller.verdict != client.VerdictAllowOnce {
		t.Fatalf("allow controls = %d, %v", controller.resolveCalls, controller.verdict)
	}
	// The modal is gone, so a repeated key cannot submit the verdict twice.
	next, cmd = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = applyPendingCommand(t, next.(Model), cmd)
	if controller.resolveCalls != 1 {
		t.Fatalf("repeated verdict calls = %d, want 1", controller.resolveCalls)
	}

	next, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = applyPendingCommand(t, next.(Model), cmd)
	if controller.cancelCalls != 1 {
		t.Fatalf("ctrl+c cancel calls = %d, want 1", controller.cancelCalls)
	}
	controller.mu.Lock()
	cancelled := append([]client.PendingApproval(nil), controller.cancelledFor...)
	controller.mu.Unlock()
	if len(cancelled) != 1 || cancelled[0].SessionID != "session" || cancelled[0].RunID != "run" || cancelled[0].AskID != "ask" {
		t.Fatalf("cancel identities = %+v", cancelled)
	}
}

func TestPendingApprovalRecoveryTeatestPhase(t *testing.T) {
	watch := &pendingApprovalFixtureWatch{events: make(chan pendingApprovalWatchResult, 2), done: make(chan struct{})}
	watch.events <- pendingApprovalWatchResult{event: client.PendingApprovalEvent{Kind: client.PendingApprovalEventBoundary}}
	controller := &pendingApprovalFixtureController{watch: watch, resolved: make(chan struct{}, 1)}
	converser, converseService := newResumeBufConverser(t)
	approval := client.PendingApproval{SessionID: "session", RunID: "run", AskID: "ask", Tool: "Write", Args: `{"path":"x"}`, Cursor: "cursor"}
	progress := newProgress()
	model := newTestModelFromDeps(Deps{
		Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), PendingApprovals: controller, Conv: converser,
		Resume: &client.ResumeSelection{
			Row:        client.SessionListItem{ID: "session", Kind: client.SessionKindMain},
			Transcript: client.SessionTranscript{SessionID: "session", Complete: true, Kind: client.SessionKindMain},
			Snapshot:   client.SessionSnapshot{State: "awaiting"}, Pending: &approval,
		},
		InitialPrompt: "draft only", NoAltScreen: true, onPhase: progress.record,
	})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 30))
	progress.wait(t, phaseAwaitingApproval, 5*time.Second)
	if resolves, cancels, _ := controller.counts(); resolves != 0 || cancels != 0 {
		t.Fatalf("startup controls = resolve %d cancel %d", resolves, cancels)
	}
	if _, prompts := converseService.snapshot(); len(prompts) != 0 {
		t.Fatalf("approval recovery automatically prompted: %q", prompts)
	}

	tm.Send(tea.KeyPressMsg{Code: 'd', Text: "d"})
	select {
	case <-controller.resolved:
	case <-time.After(5 * time.Second):
		t.Fatal("deny control was not delivered")
	}
	progress.wait(t, phaseRunning, 5*time.Second)
	if resolves, _, verdict := controller.counts(); resolves != 1 || verdict != client.VerdictDeny {
		t.Fatalf("deny controls = %d, %v", resolves, verdict)
	}
	watch.events <- pendingApprovalWatchResult{event: client.PendingApprovalEvent{
		Kind: client.PendingApprovalEventTerminal, Message: client.ResultMsg{Stop: "end_turn"},
	}}
	progress.wait(t, phaseIdle, 5*time.Second)
	if _, prompts := converseService.snapshot(); len(prompts) != 0 {
		t.Fatalf("verdict automatically prompted: %q", prompts)
	}
	tm.Send(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	tm.Type("manual follow-up")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	progress.waitRunComplete(t, 2, 5*time.Second)
	if _, prompts := converseService.snapshot(); len(prompts) != 1 || prompts[0] != "manual follow-up" {
		t.Fatalf("manual prompt control = %q", prompts)
	}
	if err := tm.Quit(); err != nil {
		t.Fatalf("quit teatest: %v", err)
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))
	final := tm.FinalModel(t).(Model)
	if final.prompt.Value() != "" || !watch.isClosed() {
		t.Fatalf("terminal recovery draft=%q watch closed=%t", final.prompt.Value(), watch.isClosed())
	}
}

func TestPendingApprovalRecoveryEscLeavesAskUnchanged(t *testing.T) {
	controller := &pendingApprovalFixtureController{watch: &pendingApprovalFixtureWatch{}}
	m := pendingRecoveryModel(t, controller, "")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc did not quit pending recovery")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("esc command = %T, want tea.QuitMsg", cmd())
	}
	if controller.resolveCalls != 0 || controller.cancelCalls != 0 || !controller.watch.isClosed() {
		t.Fatalf("esc changed ask or leaked watch: resolve=%d cancel=%d closed=%t", controller.resolveCalls, controller.cancelCalls, controller.watch.isClosed())
	}
}
