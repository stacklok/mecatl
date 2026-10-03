package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// parkingTool is a read-only child tool that signals when it starts executing and
// then parks until its ctx is cancelled — the deterministic "child is mid-drive"
// anchor the per-child cancel tests sequence on. It returns a benign result on
// unwind (ctx-aware, like every real tool), so the cancel surfaces through the
// LOOP's cancellation handling, not a tool error.
type parkingTool struct {
	started chan struct{}
	once    sync.Once
}

func newParkingTool() *parkingTool { return &parkingTool{started: make(chan struct{})} }

func (*parkingTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Wait", Description: "parks until cancelled", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*parkingTool) ReadOnly() bool { return true }
func (p *parkingTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return session.NewToolResult(in.ID, "interrupted"), nil
}

// drainObserving consumes the run's events to completion WITHOUT resolving any ask
// (unlike drainApproving), invoking observe for each event. Watchdog-bounded so a
// wedge fails rather than hanging the suite.
func drainObserving(t *testing.T, r *agent.Run, observe func(session.Event)) []session.Event {
	t.Helper()
	var evs []session.Event
	deadline := time.After(10 * time.Second)
	ch := r.Events()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
			if observe != nil {
				observe(ev)
			}
		case <-deadline:
			r.Cancel()
			t.Fatalf("run did not terminate within the deadline (possible wedge)")
			return evs
		}
	}
}

// subagentResultOf returns the LAST tool result on the parent stream (the Subagent
// tool's folded-back result in these single-delegation tests).
func subagentResultOf(t *testing.T, evs []session.Event) *session.ToolResult {
	t.Helper()
	var res *session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			res = ev.ToolResult
		}
	}
	if res == nil {
		t.Fatalf("no tool result on the parent stream")
	}
	return res
}

// TestCancelChildMidDrive cancels a subagent parked mid-tool: the Subagent result is a
// SUCCESS-with-note "[subagent cancelled by user]" carrying the child's partial text
// and the resumable agentId trailer — never a tool error (an error would teach the
// model the delegation mechanism failed) — and the PARENT run completes cleanly.
func TestCancelChildMidDrive(t *testing.T) {
	park := newParkingTool()
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("partial findings so far"),
			mockllm.ToolCallChunk(toolCall("w1", "Wait", `{}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("child: never reached"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, park)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	gotChild := make(chan string, 1)
	var cancelOK bool
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		childID := <-gotChild
		<-park.started // the child is genuinely mid-drive (parked in its tool)
		cancelOK = r.CancelChild(childID)
	}()

	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			select {
			case gotChild <- ev.Subagent.ChildID:
			default:
			}
		}
	})
	cancelDone.Wait()

	if !cancelOK {
		t.Fatalf("CancelChild must return true for a live child")
	}
	res := subagentResultOf(t, evs)
	if res.IsError {
		t.Fatalf("a client-cancelled subagent must be a SUCCESS-with-note, not a tool error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "[subagent cancelled by user]") {
		t.Fatalf("result must carry the cancelled-by-user note, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "partial findings so far") {
		t.Fatalf("result must carry the child's partial text, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "agentId: subagent-s1-p1") {
		t.Fatalf("result must keep the resumable agentId trailer, got %q", res.Content)
	}
	if got := lastResult(t, evs); got.Stop == session.StopError || got.Stop == session.StopCancelled {
		t.Fatalf("the PARENT run must complete cleanly after a per-child cancel, got stop %q", got.Stop)
	}
}

// TestCancelChildWhileParkedOnAsk is the full parked-ask unwind e2e: the child
// surfaces a Shell ask to the interactive parent and parks; CancelChild then (1)
// unwinds the child to a cancelled-by-user note result, (2) emits a
// permission.retract for the surfaced askID on the parent stream, and (3) makes a
// LATE approval a no-op (the router entry was unregistered BEFORE the retract was
// emitted — fail-safe ordering), so the command NEVER executes.
func TestCancelChildWhileParkedOnAsk(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: never reached"),
	)
	childEngine := shellChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run it"}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	type askInfo struct{ askID, childID string }
	askCh := make(chan askInfo, 1)
	var childID string
	var cancelOK bool
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		info := <-askCh
		cancelOK = r.CancelChild(info.childID)
	}()

	var surfacedAskID string
	var retracts []string
	evs := drainObserving(t, r, func(ev session.Event) {
		switch {
		case ev.Type == session.EvSubagentStart && ev.Subagent != nil:
			childID = ev.Subagent.ChildID
		case ev.Type == session.EvPermissionAsk && ev.Ask != nil:
			surfacedAskID = ev.Ask.AskID
			select {
			case askCh <- askInfo{askID: ev.Ask.AskID, childID: childID}:
			default:
			}
		case ev.Type == session.EvPermissionRetract && ev.Ask != nil:
			retracts = append(retracts, ev.Ask.AskID)
		}
	})
	cancelDone.Wait()

	if !cancelOK {
		t.Fatalf("CancelChild must return true for a child parked on an ask")
	}
	if surfacedAskID == "" {
		t.Fatalf("expected a surfaced parent permission ask")
	}
	if len(retracts) != 1 || retracts[0] != surfacedAskID {
		t.Fatalf("expected exactly one permission.retract for the surfaced askID %q, got %v", surfacedAskID, retracts)
	}
	res := subagentResultOf(t, evs)
	if res.IsError || !strings.Contains(res.Content, "[subagent cancelled by user]") {
		t.Fatalf("cancelled parked child must yield the cancelled-by-user note result, got isError=%v %q", res.IsError, res.Content)
	}
	// The LATE approval (the user answered as the retract landed): a safe no-op —
	// the router entry is gone, the parent's own registry doesn't know the id, and
	// the command must never run.
	r.Approve(surfacedAskID, session.VerdictAllowOnce)
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("a late approval after CancelChild must be a no-op; command ran: %v", got)
	}
	if got := lastResult(t, evs); got.Stop == session.StopError {
		t.Fatalf("parent run must not fail: %q", got.Error)
	}
}

// TestChildTimeoutRetractsSurfacedAskMidRun is the ghost-ask pin that justifies
// the child-terminal retraction chokepoint: a FOREGROUND Subagent with a
// per-call timeout_ms surfaces an ask and parks; the timeout — NOT CancelChild —
// unwinds it, and the child's registry terminal (the deferred finishChildRun in
// run()) must retract the still-pending ask MID-RUN: the permission.retract
// lands on the parent stream BEFORE the Subagent call's own ToolResult (and so
// before the terminal EvResult), a late verdict is a no-op, and the run
// completes normally with the time-budget error as the model-visible outcome.
func TestChildTimeoutRetractsSurfacedAskMidRun(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: never reached"),
	)
	task := agent.NewSubagentTool(shellChildEngine(childLLM, bash),
		agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		// 100ms is comfortably past the in-memory fork + first mock turn + the
		// surfaced-ask emit (all sub-ms), kept small so the test pays minimal
		// real-clock time; the ask MUST surface before the deadline.
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run it","timeout_ms":100}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var askID string
	var retracts []string
	evs := drainObserving(t, r, func(ev session.Event) {
		switch {
		case ev.Type == session.EvPermissionAsk && ev.Ask != nil:
			askID = ev.Ask.AskID // deliberately never answered: the timeout unwinds it
		case ev.Type == session.EvPermissionRetract && ev.Ask != nil:
			retracts = append(retracts, ev.Ask.AskID)
		}
	})

	if askID == "" {
		t.Fatalf("expected the child's ask to surface on the parent stream")
	}
	if len(retracts) != 1 || retracts[0] != askID {
		t.Fatalf("the timed-out child's parked ask must be retracted exactly once, got %v (want [%s])", retracts, askID)
	}
	// MID-RUN ordering: the retract is emitted by the deferred registry terminal
	// inside the Subagent call, so it precedes the call's OWN ToolResult — the
	// run was still going when the client's modal was dismissed.
	retractIdx, callResIdx, resultIdx := -1, -1, -1
	for i, ev := range evs {
		switch {
		case ev.Type == session.EvPermissionRetract:
			retractIdx = i
		case ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "p1":
			callResIdx = i
		case ev.Type == session.EvResult:
			resultIdx = i
		}
	}
	if retractIdx == -1 || callResIdx == -1 || retractIdx > callResIdx {
		t.Fatalf("permission.retract (idx %d) must precede the Subagent ToolResult (idx %d) — mid-run retraction", retractIdx, callResIdx)
	}
	if retractIdx > resultIdx {
		t.Fatalf("permission.retract (idx %d) must precede the terminal EvResult (idx %d)", retractIdx, resultIdx)
	}
	res := subagentResultOf(t, evs)
	if !strings.Contains(res.Content, "time budget") {
		t.Fatalf("the timed-out call must surface the time-budget error, got %q", res.Content)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("parent run must complete normally after the child timeout, got stop %q", got.Stop)
	}
	// Late verdict: a safe no-op — the router entry is gone (unregistered before
	// the retract), the command must never run.
	r.Approve(askID, session.VerdictAllowOnce)
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("a late approval after the timeout retract must be a no-op; command ran: %v", got)
	}
}

// TestCancelChildAfterDoneAndUnknownNoOp pins the idempotence edges: CancelChild on
// a child that already finished returns false (the finished-as-you-pressed race is
// benign), and an unknown id returns false.
func TestCancelChildAfterDoneAndUnknownNoOp(t *testing.T) {
	childLLM := mockllm.New(mockllm.TextTurn("child: done"))
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainWithTimeout(t, r)

	if got := lastResult(t, evs); got.Stop == session.StopError {
		t.Fatalf("setup run failed: %q", got.Error)
	}
	if r.CancelChild("subagent-s1-p1") {
		t.Fatalf("CancelChild on an already-done child must return false")
	}
	if r.CancelChild("subagent-nonexistent") {
		t.Fatalf("CancelChild on an unknown id must return false")
	}
}

// TestCancelChildPersistResumeRoundTrip proves the loss-mitigation that makes cancel
// safe: a client-cancelled child is PERSISTED (state cancelled) and a later `resume`
// recovers it (cancelled→Interrupt) and continues the same conversation under the
// same agentId — the cancel-then-resume-with-a-narrower-prompt workflow.
func TestCancelChildPersistResumeRoundTrip(t *testing.T) {
	store := memstore.New()
	park := newParkingTool()
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("partial work"),
			mockllm.ToolCallChunk(toolCall("w1", "Wait", `{}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("RESUMED_ANSWER"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, park)),
		agent.WithSubagentStore(store))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"long investigation"}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	gotChild := make(chan string, 1)
	go func() {
		childID := <-gotChild
		<-park.started
		r.CancelChild(childID)
	}()
	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			select {
			case gotChild <- ev.Subagent.ChildID:
			default:
			}
		}
	})
	res := subagentResultOf(t, evs)
	if !strings.Contains(res.Content, "[subagent cancelled by user]") {
		t.Fatalf("expected the cancelled-by-user note, got %q", res.Content)
	}

	// Persisted after the cancel, in the cancelled state.
	loaded, err := store.Load(context.Background(), "subagent-s1-p1")
	if err != nil || loaded == nil {
		t.Fatalf("client-cancelled child must be persisted: %v", err)
	}
	if loaded.State != session.StateCancelled {
		t.Fatalf("persisted child state = %q, want %q", loaded.State, session.StateCancelled)
	}

	// Resume the cancelled child (cancelled→Interrupt recovery) on a direct call.
	resumed := runOneSubagent(t, task, "p2", resumeArgs("subagent-s1-p1", "pick up where you left off"))
	if resumed.IsError {
		t.Fatalf("resume after client-cancel errored: %q", resumed.Content)
	}
	if !strings.Contains(resumed.Content, "RESUMED_ANSWER") {
		t.Fatalf("resumed child must continue the conversation, got %q", resumed.Content)
	}
	if got := extractAgentID(t, resumed.Content); got != "subagent-s1-p1" {
		t.Fatalf("resumed agentId = %q, want subagent-s1-p1 (same handle)", got)
	}
}

// TestCancelChildNaturalCompletionRace drives CancelChild concurrently with the
// child's natural completion, repeatedly: whichever wins, the run terminates, the
// registry never double-closes. Odd iterations synchronize cancellation on
// EvSubagentStart, so they must render either the cancelled-by-user note or the
// plain child answer, both with the resumable agentId trailer. Even iterations
// fire immediately and additionally allow the supported queued-before-slot
// cancellation error. Run with -race this pins the registry's concurrency
// contract at the Run level.
func TestCancelChildNaturalCompletionRace(t *testing.T) {
	for i := 0; i < 10; i++ {
		childLLM := mockllm.New(mockllm.TextTurn("child: quick answer"))
		task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)))
		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
			mockllm.TextTurn("parent: done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

		started := make(chan struct{}, 1)
		done := make(chan struct{})
		syncOnStart := i%2 == 1
		go func() {
			defer close(done)
			if syncOnStart {
				// Wait until the child is REGISTERED (EvSubagentStart is emitted after
				// registration), so the cancel lands inside the live window.
				<-started
			}
			// Both outcomes are legal (true = caught it live, false = not yet
			// registered / already done).
			_ = r.CancelChild("subagent-s1-p1")
		}()
		evs := drainObserving(t, r, func(ev session.Event) {
			if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
				select {
				case started <- struct{}{}:
				default:
				}
			}
		})
		<-done
		if got := lastResult(t, evs); got.Stop == session.StopError {
			t.Fatalf("iteration %d: run must never fail under the race: %q", i, got.Error)
		}
		res := subagentResultOf(t, evs)
		// The immediate-cancel iterations can also catch a registered child while
		// it waits to acquire its concurrency slot. That supported pre-start path
		// is a tool error because no child drive ran (TestCancelChildMidGateWait).
		if res.IsError {
			if !syncOnStart && res.Content == "Subagent: subagent was cancelled by the user while waiting for a concurrency slot" {
				continue
			}
			t.Fatalf("iteration %d: unexpected Subagent tool error under the race: %q", i, res.Content)
		}
		// Started children have exactly two legal renderings, both with the trailer.
		if !strings.Contains(res.Content, "agentId: subagent-s1-p1") {
			t.Fatalf("iteration %d: result must carry the resumable trailer, got %q", i, res.Content)
		}
		noted := strings.Contains(res.Content, "[subagent cancelled by user]")
		plain := strings.Contains(res.Content, "child: quick answer")
		if !noted && !plain {
			t.Fatalf("iteration %d: result must be one of the two legal renderings (note or plain answer), got %q", i, res.Content)
		}
	}
}

// TestParentRunCancelNoClientNoteOnStream is the D6 NEGATIVE at the stream level:
// a PARENT-RUN cancel (esc — Run.Cancel, NOT CancelChild) must produce NO
// "[subagent cancelled by user]" text anywhere in the drained events/results.
// The deterministic mutation-killer for the rendering itself is the internal
// TestParentRunCancelKeepsUnNotedRendering; this pins the stream surface.
func TestParentRunCancelNoClientNoteOnStream(t *testing.T) {
	park := newParkingTool()
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("partial work"),
			mockllm.ToolCallChunk(toolCall("w1", "Wait", `{}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("child: never reached"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, park)))
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent: never reached"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	go func() {
		<-park.started
		r.Cancel() // the WHOLE-run cancel: every child dies as today, clientCancelled false
	}()
	evs := drainWithTimeout(t, r)

	res := lastResult(t, evs)
	if res.Stop != session.StopCancelled {
		t.Fatalf("parent run must end cancelled, got %q", res.Stop)
	}
	for _, ev := range evs {
		if strings.Contains(ev.Text, "[subagent cancelled by user]") {
			t.Fatalf("parent-run cancel leaked the client-cancel note in event text: %+v", ev)
		}
		if ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, "[subagent cancelled by user]") {
			t.Fatalf("parent-run cancel leaked the client-cancel note in a tool result: %q", ev.ToolResult.Content)
		}
		if ev.Result != nil && strings.Contains(ev.Result.Text, "[subagent cancelled by user]") {
			t.Fatalf("parent-run cancel leaked the client-cancel note in the terminal result: %q", ev.Result.Text)
		}
	}
}

// TestResumeWithinRunReRegistersAndIsCancellable is the REACHABLE-PATH A5 e2e:
// one parent run executes Subagent then Subagent{resume: same id} under REAL
// parentCaps (the dispatcher path), so the resume RE-REGISTERS the done entry —
// overwrite, fresh doneCh — and the RESUMED child is itself client-cancellable.
// A registry that kept the stale done entry would return false from CancelChild
// here and the run would wedge on the parked tool (the watchdog fails it).
func TestResumeWithinRunReRegistersAndIsCancellable(t *testing.T) {
	store := memstore.New()
	park := newParkingTool()
	childLLM := mockllm.New(
		mockllm.TextTurn("first answer"),
		mockllm.ChunksTurn(
			mockllm.TextChunk("resumed partial"),
			mockllm.ToolCallChunk(toolCall("w1", "Wait", `{}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, park)),
		agent.WithSubagentStore(store))
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"first task"}`)),
		mockllm.ToolCallTurn(toolCall("p2", "Subagent", resumeArgs("subagent-s1-p1", "continue, but deeper"))),
		mockllm.TextTurn("parent: done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var cancelOK bool
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		<-park.started // only the RESUMED drive parks, so this is run #2 for the id
		cancelOK = r.CancelChild("subagent-s1-p1")
	}()
	evs := drainWithTimeout(t, r)
	cancelDone.Wait()

	if !cancelOK {
		t.Fatalf("the RESUMED child must be cancellable: re-registration (A5 overwrite) did not flow through the real parentCaps path")
	}
	var first, second *session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			switch ev.ToolResult.CallID {
			case "p1":
				v := *ev.ToolResult
				first = &v
			case "p2":
				v := *ev.ToolResult
				second = &v
			}
		}
	}
	if first == nil || !strings.Contains(first.Content, "first answer") {
		t.Fatalf("first run must fold back its answer, got %+v", first)
	}
	if second == nil || second.IsError {
		t.Fatalf("resumed run must fold back a success-with-note, got %+v", second)
	}
	if !strings.Contains(second.Content, "[subagent cancelled by user]") ||
		!strings.Contains(second.Content, "agentId: subagent-s1-p1") {
		t.Fatalf("resumed+cancelled run must carry the note + the SAME resumable trailer, got %q", second.Content)
	}
	if got := lastResult(t, evs); got.Stop == session.StopError || got.Stop == session.StopCancelled {
		t.Fatalf("the PARENT run must complete cleanly, got stop %q", got.Stop)
	}
}

// TestStaleVerdictAfterCancelResumeDoesNotResolveNewAsk is the CWE-863 regression
// (the askID re-mint hazard): cancel a child parked on an ask, `resume` the SAME
// child id within the SAME parent run — Counters reset on Interrupt and the
// provider re-mints the SAME call id, so without the per-Run askID suffix the new
// ask's id would COLLIDE with the retracted one. Replaying the OLD (retracted)
// askID as an APPROVAL must NOT resolve the new ask: the command never runs, and
// the run converges only when the NEW askID is answered.
func TestStaleVerdictAfterCancelResumeDoesNotResolveNewAsk(t *testing.T) {
	store := memstore.New()
	bash := &fakeShell{}
	childLLM := mockllm.New(
		// Run 1: parks on the surfaced substitution ask, then is cancelled.
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
		// Resume: the provider re-mints the SAME call id for the same intent.
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: adapted after denial"),
	)
	childEngine := shellChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run it"}`)),
		mockllm.ToolCallTurn(toolCall("p2", "Subagent", resumeArgs("subagent-s1-p1", "try again"))),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var oldAskID, newAskID string
	asks := 0
	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
			return
		}
		asks++
		switch asks {
		case 1:
			// Park #1: capture the askID, then cancel the child (retracts the ask).
			oldAskID = ev.Ask.AskID
			if !r.CancelChild("subagent-s1-p1") {
				t.Errorf("CancelChild must succeed for the parked child")
			}
		case 2:
			// Park #2 (the resumed child re-asks). The id must NOT collide with the
			// retracted one (the per-Run suffix), or the stale replay below would
			// resolve it.
			newAskID = ev.Ask.AskID
			// THE ATTACK: replay the OLD (retracted) askID as an approval. It must be
			// an unknown-ask no-op everywhere — never resolve the new ask.
			r.Approve(oldAskID, session.VerdictAllowOnce)
			// Now answer the REAL ask with a deny so the run converges without ever
			// executing the command.
			r.Approve(newAskID, session.VerdictDeny)
		}
	})

	if asks != 2 {
		t.Fatalf("expected two surfaced asks (fresh + resumed), got %d", asks)
	}
	if oldAskID == newAskID {
		t.Fatalf("the resumed run re-minted the SAME askID %q — the per-Run suffix must make runs disjoint", oldAskID)
	}
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("the stale approval resolved the new ask: command ran %v (CWE-863)", got)
	}
	if got := lastResult(t, evs); got.Stop == session.StopError {
		t.Fatalf("parent run must not fail: %q", got.Error)
	}
}
