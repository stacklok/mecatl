package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// noopReadTool is a trivial read-only tool a multi-turn branch engine calls so its child
// loop spans more than one turn (turn 1 calls it, turn 2 emits the final text). Read-only +
// allow-all policy ⇒ it dispatches without an ask.
type noopReadTool struct{}

func (noopReadTool) Spec() tool.ToolSpec { return tool.ToolSpec{Name: "Noop", Description: "no-op"} }
func (noopReadTool) ReadOnly() bool      { return true }
func (noopReadTool) Execute(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, "ok"), nil
}

// multiTurnMarkerEngine builds a branch child engine that runs MORE THAN ONE turn: turn 1
// calls Noop, turn 2 returns the marker text. It lets a decide-once test prove routing is
// consulted once PER BRANCH, not once per TURN (a single-turn markerEngine could not catch
// a regression that re-routed every turn).
func multiTurnMarkerEngine(marker string) *Engine {
	cat := tool.NewCatalog()
	cat.MustRegister(noopReadTool{})
	return NewEngine(Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("n1", "Noop", json.RawMessage(`{}`))),
			mockllm.TextTurn(marker),
		),
		Catalog: cat,
		Policy:  allowAllInt(),
		Model:   marker,
	})
}

// parallel_router_test.go drives the Parallel runBranch routing seam (ADR 0034) directly,
// asserting the per-branch precedence/fail-soft/decide-once contract and the gauntlet-#7
// no-leak guarantee on the routed metadata — the seams the composition end-to-end test
// cannot reach in isolation. It is the structural twin of modelrouter_internal_test.go.

// routerForker is a minimal tool.WorkspaceForker for these tests: each Fork returns a
// fresh in-memory workspace and a no-op cleanup. (Branch isolation is irrelevant here; we
// only care which engine each branch runs on.)
type routerForker struct{}

func (routerForker) Fork(_ context.Context, _ tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	return memfs.NewWorkspace("/fork/" + label), func() error { return nil }, "", nil
}

// routerParallelTool builds a Parallel tool whose shared branch child returns "DEFAULT"
// and whose engineFactory mints an engine returning "ROUTED:<model>" for any model — so a
// branch's summary reveals which engine actually drove it. factoryWired=false leaves the
// factory nil (the OFF posture).
func routerParallelTool(factoryWired bool) *ParallelTool {
	opts := []ParallelOption{}
	if factoryWired {
		opts = append(opts, WithParallelEngineFactory(func(model string) (*Engine, bool) {
			return markerEngine("ROUTED:" + model), true
		}))
	}
	return NewParallelTool(markerEngine("DEFAULT"), routerForker{}, opts...).(*ParallelTool)
}

// parallelArgsJSON builds a Parallel call with the given tasks (join=all).
func parallelArgsJSON(tasks ...string) json.RawMessage {
	b, _ := json.Marshal(parallelArgs{Tasks: tasks})
	return b
}

// A wired routeTask + factory mints each branch on the ROUTED model.
func TestParallelRoutesBranchOnClassifiedModel(t *testing.T) {
	tl := routerParallelTool(true)
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		return "large", "big-model", true
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON("explore A", "explore B")),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	// Every branch ran on the routed engine.
	if got := strings.Count(res.Content, "ROUTED:big-model"); got != 2 {
		t.Fatalf("want both branches on the routed model; ROUTED:big-model count=%d in %q", got, res.Content)
	}
	if strings.Contains(res.Content, "DEFAULT") {
		t.Fatalf("a routed branch must NOT run on the default child; got %q", res.Content)
	}
}

// FAIL-SOFT: a routeTask MISS (ok=false) falls through to the shared childEngine — the
// branch still completes, never errors.
func TestParallelRouteMissInheritsDefault(t *testing.T) {
	tl := routerParallelTool(true)
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		return "", "", false // miss
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON("explore A")),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "DEFAULT") || strings.Contains(res.Content, "ROUTED:") {
		t.Fatalf("a router miss must run on the DEFAULT branch child; got %q", res.Content)
	}
}

// OFF (byte-identical): no factory wired AND no routeTask — runBranch never consults the
// router and every branch runs on the shared childEngine. Also asserts that even WITH a
// routeTask, a nil factory does not route (the gate is factory!=nil AND routeTask!=nil).
func TestParallelOffIsDefaultEngine(t *testing.T) {
	// Case 1: neither factory nor routeTask.
	tl := routerParallelTool(false)
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON("a")),
		memfs.NewWorkspace("/ws"), nil, parentCaps{children: newChildRunRegistry()})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "DEFAULT") {
		t.Fatalf("OFF: branch must run on the default child; got %q", res.Content)
	}

	// Case 2: routeTask wired but NO factory — must still be the default (no way to mint a
	// routed engine, so classifying would be wasted spend; the gate skips it entirely).
	var calls int
	tl2 := routerParallelTool(false) // factory nil
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		calls++
		return "large", "big", true
	}}
	res2, err := tl2.ExecuteWithParent(context.Background(),
		session.NewToolCall("p2", "Parallel", parallelArgsJSON("a")),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("routeTask must NOT be consulted when no engine factory is wired; called %d times", calls)
	}
	if !strings.Contains(res2.Content, "DEFAULT") {
		t.Fatalf("no-factory: branch must run on the default child; got %q", res2.Content)
	}
}

// DECIDE-ONCE: each branch routes at most once — routeTask is consulted exactly len(tasks)
// times, never per-turn or per-fork.
func TestParallelRoutesEachBranchExactlyOnce(t *testing.T) {
	tl := routerParallelTool(true)
	var (
		mu    sync.Mutex
		calls int
	)
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		return "large", "big-model", true
	}}
	const branches = 3
	tasks := make([]string, branches)
	for i := range tasks {
		tasks[i] = fmt.Sprintf("task %d", i)
	}
	if _, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON(tasks...)),
		memfs.NewWorkspace("/ws"), nil, caps); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != branches {
		t.Fatalf("routeTask consulted %d times, want exactly %d (decide-once per branch)", calls, branches)
	}
}

// DECIDE-ONCE on a MULTI-TURN branch: a branch whose child engine runs more than one turn
// is still routed EXACTLY ONCE — routing happens once in runBranch before the child loop,
// not once per child turn. A single-turn markerEngine could not catch a regression that
// re-routed every turn; this drives a 2-turn branch (Noop call → final text) and asserts
// one consult. Mutation-verified: moving the route call into the child turn loop FAILs here.
func TestParallelRoutesMultiTurnBranchExactlyOnce(t *testing.T) {
	tl := NewParallelTool(markerEngine("DEFAULT"), routerForker{},
		WithParallelEngineFactory(func(model string) (*Engine, bool) {
			return multiTurnMarkerEngine("ROUTED:" + model), true
		})).(*ParallelTool)
	var (
		mu    sync.Mutex
		calls int
	)
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		return "large", "big-model", true
	}}
	// Pass an emit closure so the branch's intermediate tool.result is counted — proving the
	// branch genuinely ran multi-turn (a branch_tool event for the Noop call must appear),
	// so a "route once per branch, not per turn" assertion is non-vacuous.
	var (
		emu     sync.Mutex
		sawTool bool
	)
	emit := func(ev session.Event) {
		emu.Lock()
		if ev.Type == session.EvParallelBranch && ev.Parallel != nil && ev.Parallel.Kind == session.ParallelBranchTool {
			sawTool = true
		}
		emu.Unlock()
	}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON("a multi-turn task")),
		memfs.NewWorkspace("/ws"), emit, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	// Confirm the branch actually ran multi-turn on the routed engine (so the assertion is
	// not vacuous): the routed marker rides the final summary AND a tool turn was observed.
	if !strings.Contains(res.Content, "ROUTED:big-model") {
		t.Fatalf("the multi-turn branch did not complete on the routed engine; got %q", res.Content)
	}
	emu.Lock()
	if !sawTool {
		t.Fatal("the branch never emitted a branch_tool event; it did not run multi-turn, so the once-per-branch assertion is vacuous")
	}
	emu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("routeTask consulted %d times for ONE multi-turn branch, want exactly 1 (route once per branch, NOT per turn)", calls)
	}
}

// branch_start carries the routed category + model metadata when the router hits.
func TestParallelBranchStartCarriesRoutedMetadata(t *testing.T) {
	tl := routerParallelTool(true)
	var (
		mu  sync.Mutex
		evs []session.Event
	)
	emit := func(ev session.Event) {
		mu.Lock()
		evs = append(evs, ev)
		mu.Unlock()
	}
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		return "large", "big-model", true
	}}
	if _, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON("a")),
		memfs.NewWorkspace("/ws"), emit, caps); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawStart bool
	for _, ev := range evs {
		if ev.Type != session.EvParallelBranch || ev.Parallel == nil ||
			ev.Parallel.Kind != session.ParallelBranchStart {
			continue
		}
		sawStart = true
		if ev.Parallel.RoutedCategory != "large" || ev.Parallel.RoutedModel != "big-model" {
			t.Fatalf("branch_start routed metadata = (%q, %q), want (large, big-model)",
				ev.Parallel.RoutedCategory, ev.Parallel.RoutedModel)
		}
		// The generic Model field (issue #112 / ADR 0035) equals the routed branch
		// engine's resolved model — the router minted it on "ROUTED:big-model" (the
		// routerParallelTool factory's marker for the routed model), so Model must equal
		// that and equal RoutedModel's routed-engine manifestation. When routed, Model
		// carries the concrete model the branch ran on.
		if ev.Parallel.Model == "" {
			t.Fatalf("branch_start Model must be set (the routed branch engine's model), got empty")
		}
	}
	if !sawStart {
		t.Fatal("no parallel.branch{branch_start} event emitted")
	}
}

// A router MISS leaves the routed metadata empty on branch_start (a miss and a never-routed
// branch are indistinguishable on the wire — both carry no category/model).
func TestParallelBranchStartEmptyRoutedOnMiss(t *testing.T) {
	tl := routerParallelTool(true)
	var (
		mu  sync.Mutex
		evs []session.Event
	)
	emit := func(ev session.Event) { mu.Lock(); evs = append(evs, ev); mu.Unlock() }
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		return "", "", false
	}}
	if _, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON("a")),
		memfs.NewWorkspace("/ws"), emit, caps); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range evs {
		if ev.Type == session.EvParallelBranch && ev.Parallel != nil &&
			ev.Parallel.Kind == session.ParallelBranchStart {
			if ev.Parallel.RoutedCategory != "" || ev.Parallel.RoutedModel != "" {
				t.Fatalf("a router miss must leave routed metadata empty; got (%q, %q)",
					ev.Parallel.RoutedCategory, ev.Parallel.RoutedModel)
			}
		}
	}
}

// TestParallelFanOutSharesBreakerRace drives a real Parallel fan-out of N branches through
// the PRODUCTION breaker (Engine.parentCaps over a Run carrying a modelRouterBreaker), so
// the N concurrent branch classifications + the unsynchronized parent-usage fold all run
// under the one breaker mutex. Under -race it proves the cross-branch concurrent routing is
// data-race-clean; functionally, an always-miss router opens the breaker after exactly
// `max` consultations regardless of branch count.
func TestParallelFanOutSharesBreakerRace(t *testing.T) {
	var (
		mu        sync.Mutex
		callCount int
	)
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: func(context.Context, string) (string, string, session.Usage, bool) {
			mu.Lock()
			callCount++
			mu.Unlock()
			return "", "", session.Usage{}, false // always miss → breaker opens after `max`
		},
	})
	// A parent session so the classifier-usage fold path runs under the breaker mutex.
	sess := session.New("p-sess", session.ModeDefault, "/ws", session.Limits{}, time.Now())
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, sess, 0)

	tl := routerParallelTool(true)
	const branches = 8
	tasks := make([]string, branches)
	for i := range tasks {
		tasks[i] = fmt.Sprintf("branch %d", i)
	}
	if _, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON(tasks...)),
		memfs.NewWorkspace("/ws"), nil, caps); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if callCount != defaultModelRouterMaxMisses {
		t.Fatalf("underlying router consulted %d times across %d branches, want exactly %d (one shared breaker opens once)",
			callCount, branches, defaultModelRouterMaxMisses)
	}
}

// GAUNTLET #7: no branch-AUTHORED content (the child's summary/output) rides ANY
// parallel.* event when the router classifies. The routed branch's child returns a secret
// SUMMARY; that child output must never surface on the observability stream — only the
// bare routed category/model metadata. (The parent-authored task prompt legitimately rides
// the existing Goal metadata field and is benign here, so the secret lives ONLY in the
// child's summary — the content gauntlet #7 protects.)
func TestParallelRoutedEventsNoContentLeak(t *testing.T) {
	const secret = "SECRET-BRANCH-SUMMARY-OUTPUT"
	// The ROUTED branch child returns the secret as its summary text. Its Model is a
	// benign marker id (issue #112 surfaces Model as bare metadata on branch_start —
	// it must NOT carry the secret summary); only the LLM turn text holds the secret.
	routed := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.TextTurn(secret)),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "routed-model",
	})
	tl := NewParallelTool(markerEngine("DEFAULT"), routerForker{},
		WithParallelEngineFactory(func(string) (*Engine, bool) {
			return routed, true
		})).(*ParallelTool)
	var (
		mu  sync.Mutex
		evs []session.Event
	)
	emit := func(ev session.Event) { mu.Lock(); evs = append(evs, ev); mu.Unlock() }
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		return "large", "big", true
	}}
	if _, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Parallel", parallelArgsJSON("benign task prompt")),
		memfs.NewWorkspace("/ws"), emit, caps); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawRouted bool
	for _, ev := range evs {
		if ev.Parallel != nil && ev.Parallel.RoutedModel == "big" {
			sawRouted = true
		}
		blob, _ := json.Marshal(ev.Parallel)
		if strings.Contains(string(blob), secret) {
			t.Fatalf("a parallel.* event leaked branch SUMMARY content (%q) in payload: %s", secret, blob)
		}
	}
	if !sawRouted {
		t.Fatal("the routed branch never emitted its metadata; the assertion did not exercise")
	}
}
