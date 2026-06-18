package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// branchProvider is a STATELESS scripted LLM safe for the CONCURRENT child runs a
// Fork fans out: it decides its reply from the request's own history, not a shared
// cursor (unlike mockllm.Provider whose cursor would interleave across parallel
// branches). If the conversation already carries a tool result, it returns the
// summary turn; otherwise it asks for the read tool once. This makes every branch
// deterministic regardless of interleaving.
type branchProvider struct {
	summary string
}

func (*branchProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (p *branchProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	hasToolResult := false
	for _, m := range req.Messages {
		if m.ToolResult != nil {
			hasToolResult = true
			break
		}
	}
	var chunks []port.Chunk
	if hasToolResult {
		chunks = []port.Chunk{
			{Kind: port.ChunkText, Text: p.summary},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}
	} else {
		call := toolCall("k", "Read", `{"path":"x"}`)
		chunks = []port.Chunk{
			{Kind: port.ChunkToolCall, ToolCall: &call},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// memForker is a test tool.WorkspaceForker: each Fork returns a fresh, distinct
// in-memory workspace (isolated by construction — memfs stores are independent)
// and a cleanup that records it ran. It tracks how many forks were created and
// how many were cleaned up.
type memForker struct {
	mu          sync.Mutex
	forks       int
	cleaned     int
	failOnLabel string // fork whose label equals this fails; "" = never
	forkSeq     int
}

func (m *memForker) Fork(_ context.Context, _ tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	m.mu.Lock()
	m.forkSeq++
	seq := m.forkSeq
	m.forks++
	m.mu.Unlock()
	if m.failOnLabel != "" && label == m.failOnLabel {
		return nil, nil, "", fmt.Errorf("memForker: scripted failure on %s", label)
	}
	ws := memfs.NewWorkspace(fmt.Sprintf("/fork/%s/%d", label, seq))
	cleanup := func() error {
		m.mu.Lock()
		m.cleaned++
		m.mu.Unlock()
		return nil
	}
	return ws, cleanup, "", nil
}

func (m *memForker) counts() (forks, cleaned int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.forks, m.cleaned
}

// TestParallelJoinsAllBranches runs 3 scripted child branches in parallel and asserts
// the parent receives ONE ToolResult joining all branch summaries, with no child
// intermediate events leaking, and every fork cleaned up.
func TestParallelJoinsAllBranches(t *testing.T) {
	// Each child branch reads then summarizes; the summary text is unique per branch
	// so we can assert all three landed in the joined result.
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read something"), nil
		}}
	// 3 branches run CONCURRENTLY over one child Engine, so the child LLM must be
	// stateless (a shared mockllm cursor would interleave across branches).
	childEngine := childEngineWith(&branchProvider{summary: "branch summary X"}, catalogWith(t, childRead))

	mf := &memForker{}
	fork := agent.NewParallelTool(childEngine, mf)
	parentCat := catalogWith(t, fork)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel",
			`{"tasks":["explore A","explore B","explore C"],"shared":"common"}`)),
		mockllm.TextTurn("parent joined the branches"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	var parentResults []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult {
			parentResults = append(parentResults, ev.ToolResult)
		}
	}
	if len(parentResults) != 1 {
		t.Fatalf("parent saw %d tool results, want exactly 1: %v", len(parentResults), typesOf(evs))
	}
	joined := parentResults[0]
	if joined.IsError {
		t.Fatalf("joined result is an error: %q", joined.Content)
	}
	// All three branch delimiters present.
	for _, want := range []string{"branch-1", "branch-2", "branch-3"} {
		if !strings.Contains(joined.Content, want) {
			t.Fatalf("joined result missing %q:\n%s", want, joined.Content)
		}
	}
	if strings.Count(joined.Content, "branch summary X") != 3 {
		t.Fatalf("expected 3 branch summaries in joined result:\n%s", joined.Content)
	}
	if !strings.Contains(joined.Content, "3 succeeded, 0 failed") {
		t.Fatalf("join header wrong:\n%s", joined.Content)
	}

	// No child intermediate signals reached the parent.
	for _, ev := range evs {
		if ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, "child read something") {
			t.Fatalf("parent observed a child's intermediate tool.result")
		}
	}

	// Every fork was created and cleaned up.
	forks, cleaned := mf.counts()
	if forks != 3 || cleaned != 3 {
		t.Fatalf("forks=%d cleaned=%d, want 3/3", forks, cleaned)
	}
}

// TestParallelFailingBranchDoesNotKillOthers asserts that one branch whose fork fails
// is reported as FAILED in the joined summary while the other branches still run
// and succeed.
func TestParallelFailingBranchDoesNotKillOthers(t *testing.T) {
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	childLLM := mockllm.New(
		mockllm.TextTurn("ok branch summary"),
		mockllm.TextTurn("ok branch summary"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, childRead))

	// Fail the fork for branch-2 deterministically by LABEL (labels encode the
	// 1-based branch index regardless of which goroutine forks first).
	mf := &memForker{failOnLabel: "branch-2"}
	fork := agent.NewParallelTool(childEngine, mf)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel",
			`{"tasks":["A","B","C"]}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, fork)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	res := firstToolResult(t, evs)
	if !strings.Contains(res.Content, "2 succeeded, 1 failed") {
		t.Fatalf("expected 2 ok / 1 failed:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "branch-2 [FAILED]") {
		t.Fatalf("expected branch-2 marked FAILED:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "branch-1 [OK]") || !strings.Contains(res.Content, "branch-3 [OK]") {
		t.Fatalf("expected branch-1 and branch-3 OK:\n%s", res.Content)
	}
}

// TestParallelRunsInParallel asserts the branches actually overlap in time (bounded by
// the worker limit), not run serially.
func TestParallelRunsInParallel(t *testing.T) {
	var (
		mu      sync.Mutex
		running int
		maxObs  int
	)
	// A child tool that blocks long enough for sibling branches to enter, recording
	// the max observed concurrency.
	gate := make(chan struct{})
	var entered atomic.Int32
	childTool := &fakeTool{name: "Read", readOnly: true,
		exec: func(ctx context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			mu.Lock()
			running++
			if running > maxObs {
				maxObs = running
			}
			mu.Unlock()
			// Release once all 3 branches have entered concurrently.
			if entered.Add(1) == 3 {
				close(gate)
			}
			select {
			case <-gate:
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			mu.Lock()
			running--
			mu.Unlock()
			return session.NewToolResult(in.ID, "done"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "branch done"}, catalogWith(t, childTool))

	mf := &memForker{}
	fork := agent.NewParallelTool(childEngine, mf, agent.WithParallelConcurrency(3))
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["A","B","C"]}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, fork)})

	done := make(chan struct{})
	go func() {
		r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
		drain(r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Fork did not finish — branches likely did not run in parallel")
	}

	mu.Lock()
	got := maxObs
	mu.Unlock()
	if got < 2 {
		t.Fatalf("max observed concurrency = %d, want >= 2 (branches did not overlap)", got)
	}
}

// TestParallelConcurrencyCapBounded asserts the worker limit caps simultaneous
// branches: with concurrency 1, branches never overlap.
func TestParallelConcurrencyCapBounded(t *testing.T) {
	var (
		mu      sync.Mutex
		running int
		maxObs  int
	)
	childTool := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			mu.Lock()
			running++
			if running > maxObs {
				maxObs = running
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return session.NewToolResult(in.ID, "done"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "branch done"}, catalogWith(t, childTool))
	mf := &memForker{}
	fork := agent.NewParallelTool(childEngine, mf, agent.WithParallelConcurrency(1))
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["A","B","C"]}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, fork)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	drain(r)

	mu.Lock()
	got := maxObs
	mu.Unlock()
	if got != 1 {
		t.Fatalf("max observed concurrency = %d, want 1 (worker limit not enforced)", got)
	}
}

// TestParallelFanOutCapEnforced asserts an over-cap fan-out is rejected with an error
// result and NO forks are created.
func TestParallelFanOutCapEnforced(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(), tool.NewCatalog())
	mf := &memForker{}
	fork := agent.NewParallelTool(childEngine, mf, agent.WithMaxBranches(2))

	tasks, _ := json.Marshal(map[string]any{"tasks": []string{"a", "b", "c"}})
	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", tasks), memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("unexpected harness error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "maximum fan-out") {
		t.Fatalf("expected a cap-exceeded error result, got %+v", res)
	}
	if forks, _ := mf.counts(); forks != 0 {
		t.Fatalf("forks created despite cap rejection: %d", forks)
	}
}

// TestParallelRejectsEmptyTasks asserts empty/blank tasks yield an error result.
func TestParallelRejectsEmptyTasks(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(), tool.NewCatalog())
	fork := agent.NewParallelTool(childEngine, &memForker{})

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["  ",""]}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("unexpected harness error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "tasks") {
		t.Fatalf("expected an error about missing tasks, got %+v", res)
	}
}

// TestParallelParentCancelPropagates asserts cancelling the parent ctx cancels the
// in-flight branches: Execute returns without hanging.
func TestParallelParentCancelPropagates(t *testing.T) {
	started := make(chan struct{}, 8)
	var once sync.Once
	// A child tool that signals it started then blocks until ctx is cancelled.
	childTool := &fakeTool{name: "Read", readOnly: true,
		exec: func(ctx context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			started <- struct{}{}
			<-ctx.Done()
			return session.NewToolResult(in.ID, "interrupted"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "branch done"}, catalogWith(t, childTool))
	mf := &memForker{}
	fork := agent.NewParallelTool(childEngine, mf, agent.WithParallelConcurrency(2))
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["A","B"]}`)),
		mockllm.TextTurn("recovered"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, fork)})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan []session.Event, 1)
	go func() {
		r := e.Run(ctx, newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
		done <- drain(r)
	}()

	// Wait for a branch to start, then cancel.
	<-started
	once.Do(cancel)

	select {
	case evs := <-done:
		res := lastResult(t, evs)
		if res.Stop != session.StopCancelled {
			t.Fatalf("parent stop = %q, want cancelled", res.Stop)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Fork did not return after parent cancel — branches ignored ctx")
	}
}

// TestNewParallelToolNilArgsPanic asserts the composition-root contracts.
func TestNewParallelToolNilArgsPanic(t *testing.T) {
	t.Run("nil engine", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("NewParallelTool(nil engine) did not panic")
			}
		}()
		_ = agent.NewParallelTool(nil, &memForker{})
	})
	t.Run("nil forker", func(t *testing.T) {
		childEngine := childEngineWith(mockllm.New(), tool.NewCatalog())
		defer func() {
			if recover() == nil {
				t.Fatalf("NewParallelTool(nil forker) did not panic")
			}
		}()
		_ = agent.NewParallelTool(childEngine, nil)
	})
}

// firstToolResult returns the first EvToolResult payload in evs.
func firstToolResult(t *testing.T, evs []session.Event) *session.ToolResult {
	t.Helper()
	for _, ev := range evs {
		if ev.Type == session.EvToolResult {
			return ev.ToolResult
		}
	}
	t.Fatalf("no tool result event in %v", typesOf(evs))
	return nil
}
