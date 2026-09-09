package agent_test

import (
	"context"
	"errors"
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

// bgGateTool is a read-only child tool that signals when it starts executing and
// then parks until the test releases it (or its ctx dies) — the deterministic
// "background child is still working" anchor. It returns a benign result either
// way so the cancel path surfaces through the LOOP, never as a tool error.
type bgGateTool struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBgGateTool() *bgGateTool {
	return &bgGateTool{started: make(chan struct{}), release: make(chan struct{})}
}

func (*bgGateTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Gate", Description: "parks until released", Schema: emptyObjSchema}
}
func (*bgGateTool) ReadOnly() bool { return true }
func (g *bgGateTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.release:
		return session.NewToolResult(in.ID, "released"), nil
	case <-ctx.Done():
		return session.NewToolResult(in.ID, "interrupted"), nil
	}
}

// releaseOnce releases the gate exactly once (safe from any goroutine/event path).
func (g *bgGateTool) releaseOnce() {
	select {
	case <-g.release:
	default:
		close(g.release)
	}
}

// probeTool is a trivial read-only parent tool proving the parent keeps working
// in the same turn the background Subagent started.
type probeTool struct{ out string }

func (*probeTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Probe", Description: "returns a canned probe result", Schema: emptyObjSchema}
}
func (*probeTool) ReadOnly() bool { return true }
func (p *probeTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	out := p.out
	if out == "" {
		out = "probe-ok"
	}
	return session.NewToolResult(in.ID, out), nil
}

var emptyObjSchema = []byte(`{"type":"object"}`)

// awaitSignalTool is a read-only parent tool that parks until the given channel
// closes (or ctx dies) — the deterministic "the background child has genuinely
// reached its park" anchor a scripted parent turn can sequence on.
type awaitSignalTool struct{ ch <-chan struct{} }

func (*awaitSignalTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "AwaitChild", Description: "parks until the child signals", Schema: emptyObjSchema}
}
func (*awaitSignalTool) ReadOnly() bool { return true }
func (a *awaitSignalTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	select {
	case <-a.ch:
		return session.NewToolResult(in.ID, "child parked"), nil
	case <-ctx.Done():
		return session.NewToolResult(in.ID, "cancelled"), nil
	}
}

// resultByCallID indexes every EvToolResult by its CallID.
func resultByCallID(evs []session.Event) map[session.ToolCallID]*session.ToolResult {
	out := map[session.ToolCallID]*session.ToolResult{}
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			out[ev.ToolResult.CallID] = ev.ToolResult
		}
	}
	return out
}

// TestBackgroundSubagentHappyPath is the headline e2e through the REAL loop: the
// model starts a background subagent and keeps working IN THE SAME TURN (a second
// tool call), the Subagent call returns the immediate started-result (agentId
// trailer FIRST line, body says collect-via-SubagentStatus), the child keeps
// driving while the parent takes its next turn, and SubagentStatus{agent_id,
// wait_ms} parks until the child finishes and delivers the rendered body —
// the SOLE body channel (A2).
func TestBackgroundSubagentHappyPath(t *testing.T) {
	gate := newBgGateTool()
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("CHILD FINDINGS: the bug is in parser.go"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"investigate","background":true}`),
			toolCall("p2", "Probe", `{}`),
		),
		mockllm.ToolCallTurn(toolCall("p3", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool(), &probeTool{})})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var sawBackgroundStart bool
	var sawEnd bool
	evs := drainObserving(t, r, func(ev session.Event) {
		switch {
		case ev.Type == session.EvSubagentStart && ev.Subagent != nil:
			sawBackgroundStart = ev.Subagent.Background
		case ev.Type == session.EvSubagentEnd && ev.Subagent != nil:
			sawEnd = true
		case ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.Name == "SubagentStatus":
			// The parent has genuinely progressed to its NEXT turn while the child is
			// still parked on the gate: only now let the child finish, so the wait_ms
			// park is exercised for real.
			gate.releaseOnce()
		}
	})

	if !sawBackgroundStart {
		t.Fatalf("subagent.start must carry Background=true for a background child")
	}
	results := resultByCallID(evs)

	// The Subagent call returned IMMEDIATELY with the started-result: agentId
	// trailer on the FIRST line (the existing trailer convention), the amended D7
	// body, and NO child findings (the body has exactly one channel).
	started := results["p1"]
	if started == nil || started.IsError {
		t.Fatalf("background Subagent call must return a non-error started-result, got %+v", started)
	}
	if !strings.HasPrefix(started.Content, "agentId: subagent-s1-p1") {
		t.Fatalf("started-result must lead with the agentId trailer line, got %q", started.Content)
	}
	for _, want := range []string{"started in the background", "SubagentStatus", "wait_ms", "cancelled if it is still running when this run ends"} {
		if !strings.Contains(started.Content, want) {
			t.Fatalf("started-result must mention %q, got %q", want, started.Content)
		}
	}
	if strings.Contains(started.Content, "CHILD FINDINGS") {
		t.Fatalf("the started-result must NOT carry the child's body, got %q", started.Content)
	}

	// The same-turn second tool call ran (the model kept working).
	if probe := results["p2"]; probe == nil || !strings.Contains(probe.Content, "probe-ok") {
		t.Fatalf("the same-turn Probe call must have run alongside the background start, got %+v", probe)
	}

	// SubagentStatus collected the rendered body (with its own agentId trailer,
	// via renderSubagentResult — the foreground-identical rendering).
	collected := results["p3"]
	if collected == nil || collected.IsError {
		t.Fatalf("SubagentStatus collection must succeed, got %+v", collected)
	}
	if !strings.Contains(collected.Content, "CHILD FINDINGS: the bug is in parser.go") {
		t.Fatalf("collected body must carry the child's findings, got %q", collected.Content)
	}
	if !strings.Contains(collected.Content, "agentId: subagent-s1-p1") {
		t.Fatalf("collected body must keep the agentId trailer, got %q", collected.Content)
	}

	if !sawEnd {
		t.Fatalf("subagent.end must be emitted for a background child")
	}

	// A5 ordering pin: the SYNCHRONOUS subagent.start{background:true} must
	// precede the started ToolResult on the stream (the start is emitted inside
	// the tool BEFORE it returns; the result is emitted by the dispatcher after).
	startIdx, startedIdx := -1, -1
	for i, ev := range evs {
		switch {
		case startIdx == -1 && ev.Type == session.EvSubagentStart && ev.Subagent != nil && ev.Subagent.Background:
			startIdx = i
		case startedIdx == -1 && ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "p1":
			startedIdx = i
		}
	}
	if startIdx == -1 || startedIdx == -1 || startIdx > startedIdx {
		t.Fatalf("subagent.start (idx %d) must precede the started-result (idx %d) — A5 sync-start ordering", startIdx, startedIdx)
	}

	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("parent run must end cleanly, got stop %q (err %q)", got.Stop, got.Error)
	}
}

// TestBackgroundSubagentAlreadyDelivered pins exactly-once delivery: the second
// SubagentStatus collection of the same agent_id reports "already delivered" and
// does NOT re-bloat the context with a second copy of the body.
func TestBackgroundSubagentAlreadyDelivered(t *testing.T) {
	gate := newBgGateTool()
	gate.releaseOnce() // child finishes on its own; this test is about delivery accounting
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("CHILD FINDINGS: once only"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.ToolCallTurn(toolCall("p3", "SubagentStatus", `{"agent_id":"subagent-s1-p1"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)
	results := resultByCallID(evs)

	first := results["p2"]
	if first == nil || !strings.Contains(first.Content, "CHILD FINDINGS: once only") {
		t.Fatalf("first collection must deliver the body, got %+v", first)
	}
	second := results["p3"]
	if second == nil {
		t.Fatalf("no result for the second collection")
	}
	if !strings.Contains(second.Content, "already delivered") || !strings.Contains(second.Content, "subagent-s1-p1") {
		t.Fatalf("second collection must report already-delivered with the id, got %q", second.Content)
	}
	if strings.Contains(second.Content, "CHILD FINDINGS") {
		t.Fatalf("second collection must NOT re-deliver the body, got %q", second.Content)
	}
}

// TestSubagentStatusPollBeforeDone pins the poll path: while the background child
// is still parked, a no-args SubagentStatus shows the roster with the child
// RUNNING — ids and labels only, no body.
func TestSubagentStatusPollBeforeDone(t *testing.T) {
	gate := newBgGateTool()
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("CHILD FINDINGS: too early"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{}`)),
		mockllm.ToolCallTurn(toolCall("p3", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	evs := drainObserving(t, r, func(ev session.Event) {
		// Release the child only AFTER the roster poll (p2) has produced its result,
		// so the roster deterministically observed a RUNNING child.
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "p2" {
			gate.releaseOnce()
		}
	})
	results := resultByCallID(evs)

	roster := results["p2"]
	if roster == nil || roster.IsError {
		t.Fatalf("roster poll must succeed, got %+v", roster)
	}
	if !strings.Contains(roster.Content, "subagent-s1-p1") || !strings.Contains(roster.Content, "running") {
		t.Fatalf("roster must list the running child by id, got %q", roster.Content)
	}
	if !strings.Contains(roster.Content, "background") {
		t.Fatalf("roster must mark the child background, got %q", roster.Content)
	}
	if strings.Contains(roster.Content, "CHILD FINDINGS") {
		t.Fatalf("a running child has no body to show; roster leaked %q", roster.Content)
	}
	// And the later collection still works.
	if collected := results["p3"]; collected == nil || !strings.Contains(collected.Content, "CHILD FINDINGS: too early") {
		t.Fatalf("post-roster collection must deliver the body, got %+v", collected)
	}
}

// TestBackgroundChildCancelledAtRunEnd is the D8 run-end contract: a clean
// parent end with a live background child cancels it, joins it (no panic, no
// leak), emits its subagent.end BEFORE the terminal EvResult (A4b ordering),
// persists the cancelled child, and the child is RESUMABLE in a brand-new run.
// Since I3b the FIRST clean-end attempt draws the background-pending nudge, so
// the script ends twice — the drain contract under test is the second end's.
func TestBackgroundChildCancelledAtRunEnd(t *testing.T) {
	store := memstore.New()
	gate := newBgGateTool() // never released: the child is still parked when the run ends
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("resumed fine"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)),
		agent.WithSubagentStore(store))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","background":true}`)),
		// Deterministic anchor: the parent's next turn parks until the child has
		// genuinely consumed its first model turn and is parked inside Gate — so
		// the clean end below ALWAYS catches a mid-flight child.
		mockllm.ToolCallTurn(toolCall("pw", "AwaitChild", `{}`)),
		mockllm.TextTurn("parent done"),
		mockllm.TextTurn("parent really done"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: gate.started})
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	// The run completed CLEANLY despite the live child (cancelled at end, not an
	// error), and the child's run-end events PRECEDE the terminal result.
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("parent run must end cleanly, got stop %q (err %q)", got.Stop, got.Error)
	}
	endIdx, resultIdx := -1, -1
	var endStop session.StopReason
	for i, ev := range evs {
		switch {
		case ev.Type == session.EvSubagentEnd && ev.Subagent != nil:
			endIdx, endStop = i, ev.Subagent.Stop
		case ev.Type == session.EvResult:
			resultIdx = i
		}
	}
	if endIdx == -1 {
		t.Fatalf("the drained child's subagent.end must be emitted")
	}
	if endStop != session.StopCancelled {
		t.Fatalf("run-end drain must cancel the child, got stop %q", endStop)
	}
	if endIdx > resultIdx {
		t.Fatalf("subagent.end (idx %d) must precede the terminal EvResult (idx %d) — A4b ordering", endIdx, resultIdx)
	}

	// Persisted cancelled — the loss mitigation.
	saved, err := store.Load(context.Background(), "subagent-s1-p1")
	if err != nil || saved == nil {
		t.Fatalf("run-end-cancelled background child must be persisted: %v", err)
	}
	if saved.State != session.StateCancelled {
		t.Fatalf("persisted child state = %q, want cancelled", saved.State)
	}

	// Resumable in a NEW run (cancelled→Interrupt recovery).
	parent2 := mockllm.New(
		mockllm.ToolCallTurn(toolCall("q1", "Subagent", `{"prompt":"continue","resume":"subagent-s1-p1"}`)),
		mockllm.TextTurn("parent 2 done"),
	)
	e2 := newEngine(agent.Deps{LLM: parent2, Catalog: cat})
	sess2 := session.New("s2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	r2 := e2.Run(context.Background(), sess2, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs2 := drainObserving(t, r2, nil)
	resumed := resultByCallID(evs2)["q1"]
	if resumed == nil || resumed.IsError || !strings.Contains(resumed.Content, "resumed fine") {
		t.Fatalf("run-end-cancelled child must be resumable in a new run, got %+v", resumed)
	}
}

// TestBackgroundChildSurfacedAskAnsweredMidRun is the D11/A8 test: a BACKGROUND
// child's permission ask surfaces to the interactive parent while the parent is
// mid-run on its own next turn (here: parked inside SubagentStatus wait_ms), the
// human's approval routes back to the child, the command runs, and the result is
// collected normally.
func TestBackgroundChildSurfacedAskAnsweredMidRun(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: bash done"),
	)
	task := agent.NewSubagentTool(bashChildEngine(childLLM, bash),
		agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run it","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var surfaced bool
	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			surfaced = true
			r.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	})
	if !surfaced {
		t.Fatalf("the background child's ask must surface as a parent EvPermissionAsk")
	}
	if got := bash.ran(); len(got) != 1 || !strings.Contains(got[0], "cat") {
		t.Fatalf("the approved command must have run in the child, got %v", got)
	}
	collected := resultByCallID(evs)["p2"]
	if collected == nil || collected.IsError || !strings.Contains(collected.Content, "child: bash done") {
		t.Fatalf("collection after the late-answered ask must deliver the body, got %+v", collected)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("parent run must end cleanly, got %q", got.Stop)
	}
}

// TestBackgroundChildCancelledWhileParkedOnAsk: CancelChild on a background child
// parked on its surfaced ask unwinds it (retract emitted, command never runs) and
// the CANCELLED-BY-USER result is still COLLECTIBLE — partial work + the
// resumable trailer, exactly the foreground rendering.
func TestBackgroundChildCancelledWhileParkedOnAsk(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("partial findings"),
			mockllm.ToolCallChunk(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("child: never reached"),
	)
	task := agent.NewSubagentTool(bashChildEngine(childLLM, bash),
		agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run it","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var retracts []string
	evs := drainObserving(t, r, func(ev session.Event) {
		switch {
		case ev.Type == session.EvPermissionAsk && ev.Ask != nil:
			r.CancelChild("subagent-s1-p1")
		case ev.Type == session.EvPermissionRetract && ev.Ask != nil:
			retracts = append(retracts, ev.Ask.AskID)
		}
	})
	if len(retracts) != 1 {
		t.Fatalf("expected exactly one permission.retract, got %v", retracts)
	}
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("the cancelled command must never run, got %v", got)
	}
	collected := resultByCallID(evs)["p2"]
	if collected == nil || collected.IsError {
		t.Fatalf("a client-cancelled background child must still be collectible as a success-with-note, got %+v", collected)
	}
	if !strings.Contains(collected.Content, "[subagent cancelled by user]") ||
		!strings.Contains(collected.Content, "partial findings") ||
		!strings.Contains(collected.Content, "agentId: subagent-s1-p1") {
		t.Fatalf("collected cancel note/partial text/trailer missing: %q", collected.Content)
	}
}

// TestRunEndDrainRetractsParkedAsk pins the child-terminal retraction chokepoint
// on the RUN-END path (bug: only Run.CancelChild used to retract — a child
// cancelled by the run-end drain left the client holding a stale modal): a
// background child parks on a surfaced ask that is NEVER answered; the run ends
// cleanly (twice — the first clean end draws the background-pending nudge); the
// drain cancels the child, whose registry terminal (markDoneResult) retracts the
// still-pending ask BEFORE the doneCh join — so exactly ONE permission.retract
// rides the parent stream at a LOWER index than the terminal EvResult. A late
// post-run approval is a no-op (the router entry was unregistered before the
// retract — fail-safe), and the cancelled child is persisted + resumable.
func TestRunEndDrainRetractsParkedAsk(t *testing.T) {
	store := memstore.New()
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child resumed fine"),
	)
	task := agent.NewSubagentTool(bashChildEngine(childLLM, bash),
		agent.WithChildForker(&recordingSubagentForker{}),
		agent.WithSubagentStore(store))

	// Deterministic anchor: the parent's second turn parks until the child's ask
	// has genuinely SURFACED on the parent stream, so the clean ends below always
	// catch a child parked on its ask.
	askSeen := make(chan struct{})
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run it","background":true}`)),
		mockllm.ToolCallTurn(toolCall("pw", "AwaitChild", `{}`)),
		mockllm.TextTurn("parent done"),
		mockllm.TextTurn("parent really done"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: askSeen})
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: cat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var askID string
	var askOnce sync.Once
	var retracts []string
	evs := drainObserving(t, r, func(ev session.Event) {
		switch {
		case ev.Type == session.EvPermissionAsk && ev.Ask != nil:
			askID = ev.Ask.AskID
			askOnce.Do(func() { close(askSeen) })
			// Deliberately never answered: the run-end drain must clean it up.
		case ev.Type == session.EvPermissionRetract && ev.Ask != nil:
			retracts = append(retracts, ev.Ask.AskID)
		}
	})

	if askID == "" {
		t.Fatalf("expected the background child's ask to surface on the parent stream")
	}
	if len(retracts) != 1 || retracts[0] != askID {
		t.Fatalf("run-end drain must retract the parked ask exactly once, got %v (want [%s])", retracts, askID)
	}
	// Ordering: the retract precedes the terminal EvResult (markDoneResult emits
	// it before the doneCh close the drain joins on; EvResult follows the drain).
	retractIdx, resultIdx := -1, -1
	for i, ev := range evs {
		switch ev.Type {
		case session.EvPermissionRetract:
			retractIdx = i
		case session.EvResult:
			resultIdx = i
		}
	}
	if retractIdx == -1 || resultIdx == -1 || retractIdx > resultIdx {
		t.Fatalf("permission.retract (idx %d) must precede the terminal EvResult (idx %d)", retractIdx, resultIdx)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("parent run must end cleanly, got stop %q (err %q)", got.Stop, got.Error)
	}

	// The late approval (the user answered after the run ended): a safe no-op —
	// the router entry is gone, the command must never run.
	r.Approve(askID, session.VerdictAllowOnce)
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("a late approval after the run-end retract must be a no-op; command ran: %v", got)
	}

	// Persisted cancelled + resumable (the loss mitigation).
	saved, err := store.Load(context.Background(), "subagent-s1-p1")
	if err != nil || saved == nil {
		t.Fatalf("run-end-cancelled child must be persisted: %v", err)
	}
	if saved.State != session.StateCancelled {
		t.Fatalf("persisted child state = %q, want cancelled", saved.State)
	}
	parent2 := mockllm.New(
		mockllm.ToolCallTurn(toolCall("q1", "Subagent", `{"prompt":"continue","resume":"subagent-s1-p1"}`)),
		mockllm.TextTurn("parent 2 done"),
	)
	e2 := newEngine(agent.Deps{LLM: parent2, Catalog: cat})
	sess2 := session.New("s2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	r2 := e2.Run(context.Background(), sess2, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs2 := drainObserving(t, r2, nil)
	resumed := resultByCallID(evs2)["q1"]
	if resumed == nil || resumed.IsError || !strings.Contains(resumed.Content, "child resumed fine") {
		t.Fatalf("run-end-cancelled child must be resumable in a new run, got %+v", resumed)
	}
}

// TestBackgroundGateFullFailFast pins D12: with the gate full, a second
// BACKGROUND start fails FAST with a model-addressable error listing the live
// background ids (ids ONLY — A9), the phantom registration is removed (A5 — the
// roster never shows it), and the run keeps going.
func TestBackgroundGateFullFailFast(t *testing.T) {
	gate := newBgGateTool()
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("first child done"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)),
		agent.WithMaxConcurrentChildren(1))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"first","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "Subagent", `{"prompt":"second","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p3", "SubagentStatus", `{}`)),
		// The first clean-end attempt draws the I3b background-pending nudge (the
		// gated child is still live); the second clean end terminates normally.
		mockllm.TextTurn("parent done"),
		mockllm.TextTurn("parent really done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)
	results := resultByCallID(evs)

	failed := results["p2"]
	if failed == nil || !failed.IsError {
		t.Fatalf("gate-full background start must be a fail-fast tool error, got %+v", failed)
	}
	if !strings.Contains(failed.Content, "concurrency limit") || !strings.Contains(failed.Content, "subagent-s1-p1") {
		t.Fatalf("gate-full error must name the limit and list the live background ids, got %q", failed.Content)
	}
	// The failing call's OWN id (subagent-p2) must be ABSENT from the error: its
	// pre-gate registration is aborted BEFORE the live-ids read, so the error
	// never claims the child that just failed to start is "currently running".
	if strings.Contains(failed.Content, "subagent-p2") {
		t.Fatalf("gate-full error must not list the failing call's own id, got %q", failed.Content)
	}
	if !strings.Contains(failed.Content, "SubagentStatus") {
		t.Fatalf("gate-full error must point at the recoverable action, got %q", failed.Content)
	}
	roster := results["p3"]
	if roster == nil || !strings.Contains(roster.Content, "subagent-s1-p1") {
		t.Fatalf("roster must list the live child, got %+v", roster)
	}
	if strings.Contains(roster.Content, "subagent-p2") {
		t.Fatalf("the failed-fast registration must be REMOVED (A5) — roster leaked the phantom: %q", roster.Content)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("parent run must end cleanly, got %q", got.Stop)
	}
}

// TestBackgroundStructuredOutput proves background composes with output_schema:
// the structured retry loop runs inside the detached goroutine and the validated
// payload is what SubagentStatus delivers.
func TestBackgroundStructuredOutput(t *testing.T) {
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "SubmitResult", `{"name":"Ada","age":36}`)),
		mockllm.TextTurn("done"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			`{"prompt":"extract","background":true,"output_schema":{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"]}}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	collected := resultByCallID(evs)["p2"]
	if collected == nil || collected.IsError {
		t.Fatalf("structured background collection must succeed, got %+v", collected)
	}
	if !strings.Contains(collected.Content, `"Ada"`) || !strings.Contains(collected.Content, "agentId: subagent-s1-p1") {
		t.Fatalf("collected result must be the validated payload + trailer, got %q", collected.Content)
	}
}

// TestCompactionDuringLiveBackgroundChild pins the invariant-audit claim: parent
// compaction and a live background child do not interact — compaction rewrites
// the PARENT conversation, the child owns its own session, and the collected
// body still arrives after compaction ran.
func TestCompactionDuringLiveBackgroundChild(t *testing.T) {
	gate := newBgGateTool()
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("CHILD FINDINGS: post-compaction"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	big := strings.Repeat("filler output ", 100) // ~1400 chars ⇒ blows past the tiny window below
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "Probe", `{}`)),
		mockllm.ToolCallTurn(toolCall("p3", "Probe", `{}`)),
		mockllm.ToolCallTurn(toolCall("p4", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{
		LLM:           parentLLM,
		Catalog:       catalogWith(t, task, agent.NewSubagentStatusTool(), &probeTool{out: big}),
		Compactor:     agent.HeuristicCompactor{KeepLastTurns: 3},
		ContextWindow: func() int { return 200 }, // threshold 160 tokens ≈ 640 chars: trips after one big probe
	})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var sawCompaction bool
	evs := drainObserving(t, r, func(ev session.Event) {
		switch {
		case ev.Type == session.EvCompaction:
			sawCompaction = true
		case ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.ID == "p4":
			gate.releaseOnce()
		}
	})
	if !sawCompaction {
		t.Fatalf("test setup: compaction must have triggered while the background child lived")
	}
	if sess.State == session.StateFailed {
		t.Fatalf("compaction during a live background child must not fail the run")
	}
	collected := resultByCallID(evs)["p4"]
	if collected == nil || collected.IsError || !strings.Contains(collected.Content, "CHILD FINDINGS: post-compaction") {
		t.Fatalf("collection after compaction must still deliver, got %+v", collected)
	}
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("parent history must stay pairing-valid: %v", err)
	}
}

// TestSubagentStatusWaitRespectsRunCancel: a SubagentStatus parked on wait_ms
// unwinds promptly when the PARENT run is cancelled (the dispatch ctx dies) —
// the park can never wedge a cancel.
func TestSubagentStatusWaitRespectsRunCancel(t *testing.T) {
	gate := newBgGateTool() // never released
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("never"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":120000}`)),
		mockllm.TextTurn("never reached"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.ID == "p2" {
			r.Cancel() // cancel the whole run while the status tool is parked
		}
	})
	// drainObserving's watchdog fails the test if the park wedges; the run must
	// end cancelled.
	if got := lastResult(t, evs); got.Stop != session.StopCancelled {
		t.Fatalf("run must end cancelled, got %q", got.Stop)
	}
}

// TestBackgroundComposesWithResume proves `background` + `resume` together: a
// finished (foreground) child is resumed IN THE BACKGROUND and its continuation
// is collected via SubagentStatus.
func TestBackgroundComposesWithResume(t *testing.T) {
	store := memstore.New()
	childLLM := mockllm.New(
		mockllm.TextTurn("first pass done"),
		mockllm.TextTurn("second pass done"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)),
		agent.WithSubagentStore(store))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"first"}`)),
		mockllm.ToolCallTurn(toolCall("p2", "Subagent", `{"prompt":"go deeper","resume":"subagent-s1-p1","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p3", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)
	results := resultByCallID(evs)

	if first := results["p1"]; first == nil || !strings.Contains(first.Content, "first pass done") {
		t.Fatalf("foreground first pass must succeed, got %+v", first)
	}
	collected := results["p3"]
	if collected == nil || collected.IsError || !strings.Contains(collected.Content, "second pass done") {
		t.Fatalf("background resume must be collectible, got %+v", collected)
	}
}

// TestSubagentBackgroundWithoutRegistryErrors: a caps-less drive (plain Execute —
// no parent run, no registry) cannot honour `background`; it must be an honest
// error, never a silent foreground fallback.
func TestSubagentBackgroundWithoutRegistryErrors(t *testing.T) {
	task := agent.NewSubagentTool(childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t)))
	res, err := task.Execute(context.Background(),
		toolCall("p1", "Subagent", `{"prompt":"x","background":true}`), agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "background") {
		t.Fatalf("background without a registry must be a model-addressable error, got %+v", res)
	}
}

// TestBackgroundSubagentFailureCarriesCause is the BACKGROUND half of issue #319, and the
// path the field exists for. A background child's failure never reaches an inline Subagent
// card — its Subagent call already returned the started-result — so subagent.end's Cause is
// the ONLY channel a client has for "why did it fail", and the SubagentStatus-collected
// body is the only channel the MODEL has. Both are asserted here: without them the
// background emit could be mutated to Cause:"" and the whole suite would stay green.
func TestBackgroundSubagentFailureCarriesCause(t *testing.T) {
	const chatter = "Now let me check the tests."
	// MULTI-LINE on purpose: the subagent.end Cause is a LINE-ORIENTED field and this is
	// emit site 2 of 3. Site 1 (the foreground terminal) was the only one whose
	// normalisation was asserted, while the delta simultaneously removed the ACP
	// projector's and mecademo's own collapses — so a regression here would reach both
	// consumers as a multi-row status line with nothing firing.
	const causeText = "upstream 503:\n  model overloaded"
	const collapsedCause = "upstream 503: model overloaded"
	// The child says something chatty, calls a tool, then breaks mid-stream — the exact
	// shape whose last chat line used to be reported AS the failure reason.
	task := agent.NewSubagentTool(childEngineWith(
		chattyThenBrokenChild(chatter, errors.New(causeText)),
		catalogWith(t, failureCauseReadTool())))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	// (a) The OBSERVABILITY channel: exactly one subagent.end, carrying the stop AND the why.
	var ends int
	var end *session.SubagentPayload
	for i := range evs {
		if evs[i].Type == session.EvSubagentEnd && evs[i].Subagent != nil {
			ends++
			end = evs[i].Subagent
		}
	}
	if ends != 1 || end == nil {
		t.Fatalf("want exactly 1 subagent.end for the background child, got %d", ends)
	}
	if end.Stop != session.StopError {
		t.Fatalf("the background child must end StopError (the precondition for a cause), got %q", end.Stop)
	}
	if !strings.Contains(end.Cause, collapsedCause) {
		t.Fatalf("a BACKGROUND child's subagent.end must carry the failure cause — it is the only channel there (issue #319); got %q", end.Cause)
	}
	if strings.ContainsAny(end.Cause, "\n\r\t") {
		t.Fatalf("the background emit site must normalise the cause to ONE line through subagentCausePayload like the foreground one, got %q", end.Cause)
	}

	// (b) The MODEL channel: the SubagentStatus-collected body leads with the cause, and
	//     never presents the child's chat line as the failure reason.
	collected := resultByCallID(evs)["p2"]
	if collected == nil {
		t.Fatalf("SubagentStatus produced no result: %v", typesOf(evs))
	}
	if !strings.Contains(collected.Content, causeText) {
		t.Fatalf("the collected background body must carry the provider cause, got:\n%s", collected.Content)
	}
	if strings.Contains(collected.Content, chatter) && !strings.Contains(collected.Content, "Last activity before the failure: "+chatter) {
		t.Fatalf("the child's last chat line may only appear as labelled context, got:\n%s", collected.Content)
	}
}

// TestBackgroundSubagentFailureAdvertisesResume is the DISCOVERABILITY half on the
// background path. A background child's failure reaches the model ONLY through the
// SubagentStatus-collected body, so if that body omits the resume hint the model never
// learns the child is recoverable — and a background child is exactly the kind that has
// already done expensive work. The store is wired deliberately: it is validateResume's
// first precondition, so this is the configuration in which the advertised action can
// actually succeed.
func TestBackgroundSubagentFailureAdvertisesResume(t *testing.T) {
	const causeText = "upstream 503: model overloaded"
	task := agent.NewSubagentTool(
		childEngineWith(mockllm.New(mockllm.ErrorTurn(errors.New(causeText), mockllm.TextChunk("x"))), catalogWith(t)),
		agent.WithSubagentStore(memstore.New()))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	collected := resultByCallID(evs)["p2"]
	if collected == nil {
		t.Fatalf("SubagentStatus produced no result: %v", typesOf(evs))
	}
	body := collected.Content
	if !strings.Contains(body, "resume it with the agentId above to continue") {
		t.Fatalf("a failed BACKGROUND delegation must advertise the resume path (issue #318), got:\n%s", body)
	}
	// The ordering the hint's own wording depends on.
	idAt := strings.Index(body, "agentId: ")
	hintAt := strings.Index(body, "resume it with the agentId above")
	if idAt < 0 {
		t.Fatalf("the collected error body must keep the agentId trailer, got:\n%s", body)
	}
	if hintAt < idAt {
		t.Fatalf("the hint says \"the agentId above\" but the agentId line comes after it:\n%s", body)
	}
}
