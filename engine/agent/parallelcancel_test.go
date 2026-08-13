package agent_test

// Per-branch cancel for the Parallel fork-join tool (BACKGROUND-SUBAGENTS I2).
// These tests drive a REAL parent engine run (so parentCaps/the child-run registry
// are wired exactly as in production), discover each branch's child id from the
// parallel.branch{branch_start} observability event (the D16 wire field — never a
// derived id), and cancel branches via Run.CancelChild — the same entry the
// Converse-stream CancelChild frame routes through. Per-join-mode semantics:
//
//	all   → the cancelled branch lands [FAILED] "cancelled by user"; peers finish.
//	first → a cancelled branch can never win; the next success wins.
//	judge → a cancelled branch is excluded from the candidates; cancelling the
//	        only success degrades to the all-failed report (judge never called).
//
// Every cancelled branch still gets its bracketing branch_start/branch_end events.

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// namedParkTool is a parkingTool with a configurable name, so two branches can park
// on DISTINCT tools and the test knows which branch is genuinely mid-drive.
type namedParkTool struct {
	name    string
	started chan struct{}
	once    sync.Once
	// cancelled closes once the tool's ctx dies — the observable proxy that a
	// per-child cancel genuinely reached the PARKED drive, so a wrong-target
	// mutation fails by a loud assertion rather than by wedging the run.
	cancelled chan struct{}
	cancOnce  sync.Once
}

func newNamedParkTool(name string) *namedParkTool {
	return &namedParkTool{name: name, started: make(chan struct{}), cancelled: make(chan struct{})}
}

func (p *namedParkTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: p.name, Description: p.name + ": parks until cancelled", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*namedParkTool) ReadOnly() bool { return true }
func (p *namedParkTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	p.cancOnce.Do(func() { close(p.cancelled) })
	return session.NewToolResult(in.ID, "interrupted"), nil
}

// gateTool blocks until the test releases it (or ctx dies — the watchdog escape),
// so a branch's completion can be SEQUENCED after another branch's cancel.
type gateTool struct {
	name    string
	release chan struct{}
}

func newGateTool(name string) *gateTool { return &gateTool{name: name, release: make(chan struct{})} }

func (g *gateTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: g.name, Description: g.name + ": waits for the test gate", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*gateTool) ReadOnly() bool { return true }
func (g *gateTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	select {
	case <-g.release:
		return session.NewToolResult(in.ID, "gate released"), nil
	case <-ctx.Done():
		return session.NewToolResult(in.ID, "interrupted"), nil
	}
}

// branchRoute scripts one branch's behaviour for cancelRoutingProvider, keyed on a
// substring of the branch prompt.
type branchRoute struct {
	toolName string // "" = no tool call; reply with summary immediately
	summary  string
}

// cancelRoutingProvider is a STATELESS scripted LLM safe for CONCURRENT branches
// (like routingBranchProvider): each branch's behaviour is keyed on a marker in its
// own prompt, not a shared cursor. A routed branch first calls its route's tool,
// then summarises; a route with no tool summarises immediately.
type cancelRoutingProvider struct {
	routes map[string]branchRoute
}

func (*cancelRoutingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *cancelRoutingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	route := branchRoute{summary: "generic branch summary"}
	prompt := lastUserText(req)
	for key, rt := range p.routes {
		if strings.Contains(prompt, key) {
			route = rt
			break
		}
	}
	hasToolResult := false
	for _, m := range req.Messages {
		if m.ToolResult != nil {
			hasToolResult = true
			break
		}
	}
	var chunks []port.Chunk
	if route.toolName == "" || hasToolResult {
		chunks = []port.Chunk{
			{Kind: port.ChunkText, Text: route.summary},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}
	} else {
		call := toolCall("t1", route.toolName, `{}`)
		chunks = []port.Chunk{
			{Kind: port.ChunkToolCall, ToolCall: &call},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}
	}
	return streamChunks(ctx, chunks), nil
}

// parallelEngineFor wires a parent engine whose catalog carries ONE Parallel tool
// over the given child engine, scripted to fan out the given tasks under join.
func parallelEngineFor(t *testing.T, childEngine *agent.Engine, mf tool.EnvironmentForker, join string, tasks []string, opts ...agent.ParallelOption) *agent.Engine {
	t.Helper()
	fork := agent.NewParallelTool(childEngine, mf, opts...)
	args, err := json.Marshal(map[string]any{"tasks": tasks, "join": join})
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", string(args))),
		mockllm.TextTurn("parent: done"),
	)
	return newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, fork)})
}

// branchPayloads collects the parallel.branch payloads of one kind.
func branchPayloads(evs []session.Event, kind session.ParallelEventKind) []*session.ParallelPayload {
	var out []*session.ParallelPayload
	for _, ev := range evs {
		if ev.Type == session.EvParallelBranch && ev.Parallel != nil && ev.Parallel.Kind == kind {
			out = append(out, ev.Parallel)
		}
	}
	return out
}

// assertBranchBracketing asserts every one of n branches got BOTH its branch_start
// and branch_end event — cancelled branches included (no missing event).
func assertBranchBracketing(t *testing.T, evs []session.Event, n int) {
	t.Helper()
	for _, kind := range []session.ParallelEventKind{session.ParallelBranchStart, session.ParallelBranchEnd} {
		seen := map[int]bool{}
		for _, p := range branchPayloads(evs, kind) {
			seen[p.BranchIndex] = true
		}
		for i := 0; i < n; i++ {
			if !seen[i] {
				t.Errorf("branch %d missing its %s event", i, kind)
			}
		}
	}
}

// TestCancelParallelBranchJoinAll cancels one mid-drive branch of a join=all run:
// the cancelled branch lands [FAILED] with the "cancelled by user" reason, the
// other branches complete normally, the cancelled branch still gets its bracketing
// branch_end, branch_start/branch_end carry the D16 child id, and the PARENT run
// completes cleanly.
func TestCancelParallelBranchJoinAll(t *testing.T) {
	park := newNamedParkTool("Wait")
	prov := &cancelRoutingProvider{routes: map[string]branchRoute{
		"PARK": {toolName: "Wait"},
		"fast": {summary: "fast branch summary"},
	}}
	childEngine := childEngineWith(prov, catalogWith(t, park))
	e := parallelEngineFor(t, childEngine, &memForker{}, "all", []string{"fast one", "PARK two", "fast three"})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	gotChild := make(chan string, 1)
	var cancelOK bool
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		childID := <-gotChild
		<-park.started // the branch is genuinely mid-drive (parked in its tool)
		cancelOK = r.CancelChild(childID)
	}()

	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvParallelBranch && ev.Parallel != nil &&
			ev.Parallel.Kind == session.ParallelBranchStart && ev.Parallel.BranchIndex == 1 {
			select {
			case gotChild <- ev.Parallel.ChildID:
			default:
			}
		}
	})
	cancelDone.Wait()

	if !cancelOK {
		t.Fatalf("CancelChild must return true for a live branch")
	}
	res := firstToolResult(t, evs)
	if res.IsError {
		t.Fatalf("joined result must not be a harness error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "2 succeeded, 1 failed") {
		t.Fatalf("expected 2 ok / 1 failed:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "branch-2 [FAILED]") || !strings.Contains(res.Content, "cancelled by user") {
		t.Fatalf("the cancelled branch must read FAILED with the cancelled-by-user reason:\n%s", res.Content)
	}
	assertBranchBracketing(t, evs, 3)
	// The D16 child id rides branch_start AND branch_end ("parallel-<callID>-<i>").
	for _, kind := range []session.ParallelEventKind{session.ParallelBranchStart, session.ParallelBranchEnd} {
		for _, p := range branchPayloads(evs, kind) {
			want := fmt.Sprintf("parallel-s1-p1-%d", p.BranchIndex)
			if p.ChildID != want {
				t.Errorf("%s branch %d ChildID = %q, want %q", kind, p.BranchIndex, p.ChildID, want)
			}
		}
	}
	// The cancelled branch's terminal event reads failed + cancelled.
	for _, p := range branchPayloads(evs, session.ParallelBranchEnd) {
		if p.BranchIndex == 1 && (!p.Failed || p.Stop != session.StopCancelled) {
			t.Errorf("cancelled branch end = failed:%v stop:%q, want failed cancelled", p.Failed, p.Stop)
		}
	}
	if got := lastResult(t, evs); got.Stop == session.StopError || got.Stop == session.StopCancelled {
		t.Fatalf("the PARENT run must complete cleanly after a branch cancel, got stop %q", got.Stop)
	}
}

// TestCancelParallelBranchJoinFirstWinnerNeverCancelled cancels the would-be winner
// of a join=first run: the cancelled branch can never win, and the NEXT branch to
// succeed becomes the winner.
func TestCancelParallelBranchJoinFirstWinnerNeverCancelled(t *testing.T) {
	park := newNamedParkTool("Wait")
	gate := newGateTool("Gate")
	prov := &cancelRoutingProvider{routes: map[string]branchRoute{
		"PARK": {toolName: "Wait"},
		"GATE": {toolName: "Gate", summary: "gated branch summary"},
	}}
	childEngine := childEngineWith(prov, catalogWith(t, park, gate))
	e := parallelEngineFor(t, childEngine, &memForker{}, "first", []string{"PARK one", "GATE two"})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	gotChild := make(chan string, 1)
	branch0Ended := make(chan struct{})
	var endOnce sync.Once
	var cancelOK bool
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		childID := <-gotChild
		<-park.started // branch-1 is mid-drive
		cancelOK = r.CancelChild(childID)
		// Only AFTER the cancelled branch has terminally ended does branch-2 get to
		// succeed — so the "first" winner decision sees the cancelled branch FIRST
		// and must skip it.
		<-branch0Ended
		close(gate.release)
	}()

	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type != session.EvParallelBranch || ev.Parallel == nil {
			return
		}
		if ev.Parallel.Kind == session.ParallelBranchStart && ev.Parallel.BranchIndex == 0 {
			select {
			case gotChild <- ev.Parallel.ChildID:
			default:
			}
		}
		if ev.Parallel.Kind == session.ParallelBranchEnd && ev.Parallel.BranchIndex == 0 {
			endOnce.Do(func() { close(branch0Ended) })
		}
	})
	cancelDone.Wait()

	if !cancelOK {
		t.Fatalf("CancelChild must return true for a live branch")
	}
	res := firstToolResult(t, evs)
	if !strings.Contains(res.Content, "branch-2 succeeded first") || !strings.Contains(res.Content, "branch-2 [WINNER]") {
		t.Fatalf("the next success must win after the would-be winner was cancelled:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "branch-1 [WINNER]") {
		t.Fatalf("a cancelled branch must never win:\n%s", res.Content)
	}
	assertBranchBracketing(t, evs, 2)
	if got := lastResult(t, evs); got.Stop == session.StopError || got.Stop == session.StopCancelled {
		t.Fatalf("the PARENT run must complete cleanly, got stop %q", got.Stop)
	}
}

// TestCancelParallelBranchJudgeExcluded cancels one branch of a join=judge run while
// the other succeeds: the cancelled branch is EXCLUDED from the judge's candidates —
// with a single success left the judge is skipped entirely and the success wins.
func TestCancelParallelBranchJudgeExcluded(t *testing.T) {
	park := newNamedParkTool("Wait")
	prov := &cancelRoutingProvider{routes: map[string]branchRoute{
		"PARK": {toolName: "Wait"},
		"fast": {summary: "fast branch summary"},
	}}
	judge := &fakeJudge{pick: "fast"}
	childEngine := childEngineWith(prov, catalogWith(t, park))
	e := parallelEngineFor(t, childEngine, &memForker{}, "judge",
		[]string{"PARK one", "fast two"}, agent.WithParallelJudge(judge))
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	gotChild := make(chan string, 1)
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		childID := <-gotChild
		<-park.started
		r.CancelChild(childID)
	}()
	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvParallelBranch && ev.Parallel != nil &&
			ev.Parallel.Kind == session.ParallelBranchStart && ev.Parallel.BranchIndex == 0 {
			select {
			case gotChild <- ev.Parallel.ChildID:
			default:
			}
		}
	})
	cancelDone.Wait()

	res := firstToolResult(t, evs)
	if !strings.Contains(res.Content, "selected branch-2") {
		t.Fatalf("the surviving success must win:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "only one branch succeeded; selected without judging") {
		t.Fatalf("with the cancelled branch excluded, a single success skips the judge:\n%s", res.Content)
	}
	if judge.called() {
		t.Fatalf("the judge must never see a cancelled branch as a candidate")
	}
	// The scoreboard now also carries each branch's discoverable id (issue #30), so the
	// FAILED line is "branch-1 [FAILED] (branch id: parallel-…): cancelled by user". Assert
	// the CONTIGUOUS shape including the id note (joinJudgeResult's exact rendering), so a
	// mis-rendered/dropped id note can't pass two independent substring checks from
	// different lines.
	if !strings.Contains(res.Content, "branch-1 [FAILED] (branch id: parallel-") {
		t.Fatalf("the cancelled branch must render the contiguous FAILED+branch-id note:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "cancelled by user") {
		t.Fatalf("the cancelled branch must attribute the kill:\n%s", res.Content)
	}
	assertBranchBracketing(t, evs, 2)
}

// TestCancelParallelBranchJudgeOnlySuccessCancelled cancels the ONLY viable branch
// of a join=judge run (the other branch's fork fails): the run degrades to the
// existing all-failed report and the judge is never called.
func TestCancelParallelBranchJudgeOnlySuccessCancelled(t *testing.T) {
	park := newNamedParkTool("Wait")
	prov := &cancelRoutingProvider{routes: map[string]branchRoute{
		"PARK": {toolName: "Wait"},
	}}
	judge := &fakeJudge{pick: "anything"}
	childEngine := childEngineWith(prov, catalogWith(t, park))
	// branch-2's fork fails deterministically; branch-1 (the only viable branch) parks.
	mf := &memForker{failOnLabel: "branch-2"}
	e := parallelEngineFor(t, childEngine, mf, "judge",
		[]string{"PARK one", "doomed two"}, agent.WithParallelJudge(judge))
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	gotChild := make(chan string, 1)
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		childID := <-gotChild
		<-park.started
		r.CancelChild(childID)
	}()
	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvParallelBranch && ev.Parallel != nil &&
			ev.Parallel.Kind == session.ParallelBranchStart && ev.Parallel.BranchIndex == 0 {
			select {
			case gotChild <- ev.Parallel.ChildID:
			default:
			}
		}
	})
	cancelDone.Wait()

	res := firstToolResult(t, evs)
	if !strings.Contains(res.Content, "0 succeeded, 2 failed") {
		t.Fatalf("cancelling the only success must degrade to the all-failed report:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "cancelled by user") {
		t.Fatalf("the report must attribute the kill:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "[WINNER]") {
		t.Fatalf("an all-failed judge run must select no winner:\n%s", res.Content)
	}
	if judge.called() {
		t.Fatalf("the judge must not be called with zero successful candidates")
	}
	assertBranchBracketing(t, evs, 2)
	if got := lastResult(t, evs); got.Stop == session.StopError || got.Stop == session.StopCancelled {
		t.Fatalf("the PARENT run must complete cleanly, got stop %q", got.Stop)
	}
}

// TestCancelParallelBranchWhileQueued cancels a branch still QUEUED on the worker
// semaphore (concurrency 1): the queued branch is already registered — and so
// cancellable — before it ever acquires a slot, and its result reads
// "cancelled by user before start" while its bracketing events still fire.
func TestCancelParallelBranchWhileQueued(t *testing.T) {
	park1 := newNamedParkTool("Wait1")
	park2 := newNamedParkTool("Wait2")
	prov := &cancelRoutingProvider{routes: map[string]branchRoute{
		"PARK-A": {toolName: "Wait1"},
		"PARK-B": {toolName: "Wait2"},
	}}
	childEngine := childEngineWith(prov, catalogWith(t, park1, park2))
	e := parallelEngineFor(t, childEngine, &memForker{}, "all",
		[]string{"PARK-A one", "PARK-B two"}, agent.WithParallelConcurrency(1))
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	var queuedOK, runningOK bool
	go func() {
		defer cancelDone.Done()
		// Whichever branch parks first holds the single worker slot; the OTHER is
		// queued. Branch ids are deterministic ("parallel-<callID>-<i>"), so the
		// queued one is addressable before its branch_start ever fires.
		var runningIdx int
		select {
		case <-park1.started:
			runningIdx = 0
		case <-park2.started:
			runningIdx = 1
		}
		queuedIdx := 1 - runningIdx
		// The queued branch registers inside its OWN goroutine, which is not
		// sequenced against the sibling's park — under scheduler load its
		// registration can lag past park.started, and CancelChild on a not-yet-
		// registered id is a documented false no-op (the branch would then run
		// uncancelled and park forever → watchdog wedge). Retry (bounded) until
		// the registration lands: the branch cannot leave the queue meanwhile,
		// because the single worker slot is held by the parked running branch,
		// which is only cancelled after this succeeds.
		deadline := time.After(10 * time.Second)
		for !queuedOK {
			if queuedOK = r.CancelChild(fmt.Sprintf("parallel-s1-p1-%d", queuedIdx)); queuedOK {
				break
			}
			select {
			case <-deadline:
				// Fail CRISPLY: without a run cancel the parked running branch
				// (and so the whole run) would only unwind via drainObserving's
				// watchdog, masking this as a generic wedge. Errorf is
				// goroutine-safe; the main goroutine still reports !queuedOK.
				t.Errorf("queued branch parallel-s1-p1-%d never became cancellable within the deadline", queuedIdx)
				r.Cancel()
				return
			case <-time.After(time.Millisecond):
			}
		}
		runningOK = r.CancelChild(fmt.Sprintf("parallel-s1-p1-%d", runningIdx))
	}()

	evs := drainObserving(t, r, nil)
	cancelDone.Wait()

	if !queuedOK {
		t.Fatalf("a QUEUED branch must already be cancellable (registered before the gate)")
	}
	if !runningOK {
		t.Fatalf("the running branch must be cancellable")
	}
	res := firstToolResult(t, evs)
	if !strings.Contains(res.Content, "0 succeeded, 2 failed") {
		t.Fatalf("both branches must read failed:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "cancelled by user before start") {
		t.Fatalf("the queued branch must read cancelled-by-user-before-start:\n%s", res.Content)
	}
	assertBranchBracketing(t, evs, 2)
	if got := lastResult(t, evs); got.Stop == session.StopError || got.Stop == session.StopCancelled {
		t.Fatalf("the PARENT run must complete cleanly, got stop %q", got.Stop)
	}
}
