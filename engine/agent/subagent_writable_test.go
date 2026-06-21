package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// writableChildWriting scripts a child that calls a "Write" fakeTool (recording
// the workspace root it ran against), then emits a final summary. recordedRoot
// captures the workspace root the child's mutating tool actually saw, so a test
// can assert the child ran in the FORK, not the shared parent base.
func writableChildWriting(t *testing.T, summary string, recordedRoot *atomic.Pointer[string]) *agent.Engine {
	t.Helper()
	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, ws tool.Workspace) (session.ToolResult, error) {
			r := ws.Root()
			recordedRoot.Store(&r)
			return session.NewToolResult(in.ID, "wrote a file"), nil
		}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"x.txt","content":"hi"}`)),
		mockllm.TextTurn(summary),
	)
	return childEngineWith(llm, catalogWith(t, writeTool))
}

// newWritableSubagent assembles a Subagent tool with the full writable wiring: a
// read-only explorer engine (whose summary differs so a test can tell which engine
// ran), a writable child engine, a memForker as the writable forker, and the given
// merger. The read-only childEngine is the mandatory first arg.
func newWritableSubagent(t *testing.T, writable *agent.Engine, fk tool.WorkspaceForker, merger tool.ForkMerger, extra ...agent.SubagentOption) tool.Tool {
	t.Helper()
	readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("READ-ONLY EXPLORER RAN")), catalogWith(t))
	opts := append([]agent.SubagentOption{
		agent.WithWritableChildEngine(writable),
		agent.WithWritableChildForker(fk),
		agent.WithSubagentAutoMerge(merger),
	}, extra...)
	return agent.NewSubagentTool(readOnly, opts...)
}

// TestSubagentWritableSelectsWritableEngineAndMerges proves a mode:"read-write"
// call runs the WRITABLE child engine (not the read-only explorer), the child's
// mutating tool runs against the FORK workspace, and the injected merger's Merge is
// called EXACTLY ONCE post-run with the fork root. The result notes the merge.
func TestSubagentWritableSelectsWritableEngineAndMerges(t *testing.T) {
	var recordedRoot atomic.Pointer[string]
	writable := writableChildWriting(t, "WRITABLE CHILD DID THE WORK", &recordedRoot)
	mf := &memForker{}
	merger := &fakeMerger{}
	task := newWritableSubagent(t, writable, mf, merger)

	res := runOneSubagent(t, task, "p1", `{"prompt":"implement the fix","mode":"read-write"}`)
	if res.IsError {
		t.Fatalf("writable subagent returned an error: %q", res.Content)
	}
	// The WRITABLE engine ran (its summary), not the read-only explorer.
	if !strings.Contains(res.Content, "WRITABLE CHILD DID THE WORK") {
		t.Fatalf("result must carry the writable child's summary, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "READ-ONLY EXPLORER RAN") {
		t.Fatalf("read-write call must NOT run the read-only explorer engine, got:\n%s", res.Content)
	}
	// The merger was called exactly once, with the fork root.
	if merger.callCount() != 1 {
		t.Fatalf("merger called %d times, want exactly 1", merger.callCount())
	}
	if got := merger.calls[0].ForkRoot; !strings.HasPrefix(got, "/fork/") {
		t.Fatalf("merger called with fork root %q, want a /fork/... fork path", got)
	}
	if merger.calls[0].ParentRoot != "/ws" {
		t.Fatalf("merger called with parent root %q, want /ws (the parent workspace)", merger.calls[0].ParentRoot)
	}
	// The child's mutating tool ran in the FORK, not the shared parent base.
	if rr := recordedRoot.Load(); rr == nil || !strings.HasPrefix(*rr, "/fork/") {
		t.Fatalf("child Write ran against root %v, want a /fork/... isolated fork", rr)
	}
	// The fork was cleaned up on the success path.
	if forks, cleaned := mf.counts(); forks != 1 || cleaned != 1 {
		t.Fatalf("forks=%d cleaned=%d, want 1/1 (fork cleaned on merge success)", forks, cleaned)
	}
	// The result notes the merge so the model knows the edits landed.
	if !strings.Contains(res.Content, "merged into your workspace") {
		t.Fatalf("result must note the merge-back, got:\n%s", res.Content)
	}
}

// writableChildWritingThenError scripts a writable child that calls Write
// SUCCESSFULLY, then the next turn yields a GENUINE in-stream provider error so the
// child run terminates StopError (the loop calls session.Fail). It exercises FIX A:
// a crashed child's partial edits must NOT be merged into the parent.
func writableChildWritingThenError(t *testing.T, recordedRoot *atomic.Pointer[string]) *agent.Engine {
	t.Helper()
	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, ws tool.Workspace) (session.ToolResult, error) {
			r := ws.Root()
			recordedRoot.Store(&r)
			return session.NewToolResult(in.ID, "wrote a partial, half-finished edit"), nil
		}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"x.txt","content":"partial"}`)),
		mockllm.ErrorTurn(errors.New("provider stream blew up")),
	)
	return childEngineWith(llm, catalogWith(t, writeTool))
}

// TestSubagentWritableStopErrorDoesNotMerge is the FIX A ship-blocker guard: a
// writable child that does a successful Write but then terminates StopError (provider
// stream error / MaxConsecutiveFailures / hook rejection) must NOT have its partial,
// internally-inconsistent edits merged into the parent — and the fork is cleaned up
// normally (nothing intentional is landing), NOT preserved.
func TestSubagentWritableStopErrorDoesNotMerge(t *testing.T) {
	var recordedRoot atomic.Pointer[string]
	writable := writableChildWritingThenError(t, &recordedRoot)
	mf := &memForker{}
	merger := &fakeMerger{}
	task := newWritableSubagent(t, writable, mf, merger)

	runOneSubagent(t, task, "p1", `{"prompt":"implement the fix","mode":"read-write"}`)

	// The child wrote into the fork (so there WAS a diff that could have merged).
	if rr := recordedRoot.Load(); rr == nil || !strings.HasPrefix(*rr, "/fork/") {
		t.Fatalf("child Write should have run in the fork, got root %v", rr)
	}
	// FIX A: a StopError terminal must NEVER merge.
	if merger.callCount() != 0 {
		t.Fatalf("a StopError child must NOT merge, but merger was called %d times", merger.callCount())
	}
	// The fork is cleaned up normally on a non-merged terminal (not preserved).
	if forks, cleaned := mf.counts(); forks != 1 || cleaned != 1 {
		t.Fatalf("forks=%d cleaned=%d, want 1/1 (fork cleaned, not preserved, on a non-merged StopError)", forks, cleaned)
	}
}

// TestSubagentWritableClientCancelDoesNotMerge is the FIX A ship-blocker guard for a
// client cancel (CancelChild): the user withdrew the delegation, so its partial edits
// must NOT be landed in the parent. Driven through a parent run so the child is
// genuinely client-cancelled mid-drive.
func TestSubagentWritableClientCancelDoesNotMerge(t *testing.T) {
	park := newParkingTool()
	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote a partial edit"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("w1", "Write", `{"path":"x.txt","content":"hi"}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("about to park"),
			mockllm.ToolCallChunk(toolCall("w2", "Wait", `{}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("never reached"),
	)
	writable := childEngineWith(childLLM, catalogWith(t, writeTool, park))
	mf := &memForker{}
	merger := &fakeMerger{}
	task := newWritableSubagent(t, writable, mf, merger)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"implement","mode":"read-write"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	gotChild := make(chan string, 1)
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		childID := <-gotChild
		<-park.started
		r.CancelChild(childID)
	}()

	drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			select {
			case gotChild <- ev.Subagent.ChildID:
			default:
			}
		}
	})
	cancelDone.Wait()

	if merger.callCount() != 0 {
		t.Fatalf("a client-cancelled writable child must NOT merge, but merger was called %d times", merger.callCount())
	}
}

// TestSubagentWritableTimeoutDoesNotMerge is the FIX A ship-blocker guard for a
// time-budget terminal: a writable child that blows its timeout_ms is stopped with the
// time-budget error and must NOT merge its half-finished fork.
func TestSubagentWritableTimeoutDoesNotMerge(t *testing.T) {
	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("b1", "Block", `{}`)),
		mockllm.TextTurn("never reached"),
	)
	writable := childEngineWith(childLLM, catalogWith(t, block))
	mf := &memForker{}
	merger := &fakeMerger{}
	task := newWritableSubagent(t, writable, mf, merger)

	res := runOneSubagent(t, task, "p1", `{"prompt":"loop forever","mode":"read-write","timeout_ms":120}`)
	if !res.IsError || !strings.Contains(res.Content, "time budget") {
		t.Fatalf("a timed-out writable child must surface the time-budget error, got %+v", res)
	}
	if merger.callCount() != 0 {
		t.Fatalf("a timed-out writable child must NOT merge, but merger was called %d times", merger.callCount())
	}
}

// TestSubagentWritableConflictPreservesFork proves a merge CONFLICT surfaces a
// model-addressable tool error naming the PRESERVED fork, and the fork is NOT
// cleaned up (so the operator can resolve it by hand).
func TestSubagentWritableConflictPreservesFork(t *testing.T) {
	var recordedRoot atomic.Pointer[string]
	writable := writableChildWriting(t, "did work", &recordedRoot)
	mf := &memForker{}
	merger := &fakeMerger{errOnCall: 1}
	task := newWritableSubagent(t, writable, mf, merger)

	res := runOneSubagent(t, task, "p1", `{"prompt":"implement the fix","mode":"read-write"}`)
	if !res.IsError {
		t.Fatalf("a merge conflict must surface a tool error, got a success:\n%s", res.Content)
	}
	if merger.callCount() != 1 {
		t.Fatalf("merger must be called once even on conflict, got %d", merger.callCount())
	}
	if !strings.Contains(res.Content, "preserved") {
		t.Fatalf("conflict error must say the fork is preserved, got:\n%s", res.Content)
	}
	// The message must be RECOVERABLE for a model, not just name a path: it states
	// the cause + that nothing was applied, prescribes recovery with its own tools,
	// and warns against a blind retry.
	for _, want := range []string{"NOTHING was applied", "Read", "Edit/Write", "re-delegate", "Do NOT simply retry"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("conflict error must carry actionable recovery guidance %q, got:\n%s", want, res.Content)
		}
	}
	forkRoot := merger.calls[0].ForkRoot
	if !strings.Contains(res.Content, forkRoot) {
		t.Fatalf("conflict error must name the preserved fork %q, got:\n%s", forkRoot, res.Content)
	}
	// The fork is NOT cleaned up on conflict (preserved for manual resolution).
	if forks, cleaned := mf.counts(); forks != 1 || cleaned != 0 {
		t.Fatalf("forks=%d cleaned=%d, want 1 forked / 0 cleaned (fork preserved on conflict)", forks, cleaned)
	}
}

// TestSubagentWritableNoMergerWhenNothingToMerge proves that with a nil-returning
// merger (no diff) the result honestly reports no changes were merged.
func TestSubagentWritableNoChangesNote(t *testing.T) {
	// A writable child that produces NO mutating tool call still merges via the
	// merger; the fakeMerger returns nil (success) so the note is "merged".
	// To exercise the "no changes" note we wire a nil merger so mergeWritableChild
	// short-circuits (merged=false) — modelling a deployment with no merger.
	writable := childEngineWith(mockllm.New(mockllm.TextTurn("explored, no edits")), catalogWith(t))
	mf := &memForker{}
	task := agent.NewSubagentTool(
		childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t)),
		agent.WithWritableChildEngine(writable),
		agent.WithWritableChildForker(mf),
		// No WithSubagentAutoMerge: mergeWritableChild short-circuits, merged=false.
	)
	res := runOneSubagent(t, task, "p1", `{"prompt":"look around","mode":"read-write"}`)
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "no file changes to merge") {
		t.Fatalf("result must note no changes were merged, got:\n%s", res.Content)
	}
}

// TestSubagentWritableReadOnlyStaysTrue PINS the D2 invariant: configuring a
// writable Subagent tool does NOT flip ReadOnly() — the merge is a post-run step,
// not a dispatch-time mutation, so read-only fan-out stays parallel-safe.
func TestSubagentWritableReadOnlyStaysTrue(t *testing.T) {
	writable := childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t))
	task := newWritableSubagent(t, writable, &memForker{}, &fakeMerger{})
	if !task.ReadOnly() {
		t.Fatal("a writable-configured SubagentTool must still report ReadOnly()==true")
	}
}

// TestSubagentModeCombinationGuards is the D2/D3/D4 combination table: an unknown
// mode, read-write+background, and read-write+agent are all rejected; read-write
// with no writable engine wired is "not supported"; read-write+fork is ALLOWED (the
// merge fires). read-write+model is also allowed (composes).
func TestSubagentModeCombinationGuards(t *testing.T) {
	makeWritable := func() tool.Tool {
		var rr atomic.Pointer[string]
		return newWritableSubagent(t, writableChildWriting(t, "ok", &rr), &memForker{}, &fakeMerger{})
	}

	t.Run("unknown mode rejected", func(t *testing.T) {
		res := runOneSubagent(t, makeWritable(), "p1", `{"prompt":"go","mode":"sideways"}`)
		if !res.IsError || !strings.Contains(res.Content, "unknown mode") {
			t.Fatalf("unknown mode must be rejected, got %+v", res)
		}
	})

	t.Run("explicit read-only accepted (read-only explorer runs)", func(t *testing.T) {
		// The explicit "read-only" literal is accepted exactly like "" — it runs the
		// read-only explorer and never merges (TEST GAP: pin the literal, not just "").
		var rr atomic.Pointer[string]
		merger := &fakeMerger{}
		task := newWritableSubagent(t, writableChildWriting(t, "should NOT run", &rr), &memForker{}, merger)
		res := runOneSubagent(t, task, "p1", `{"prompt":"look around","mode":"read-only"}`)
		if res.IsError {
			t.Fatalf("explicit mode:\"read-only\" must be accepted, got error: %q", res.Content)
		}
		if !strings.Contains(res.Content, "READ-ONLY EXPLORER RAN") {
			t.Fatalf("explicit read-only must run the read-only explorer, got:\n%s", res.Content)
		}
		if merger.callCount() != 0 {
			t.Fatalf("a read-only call must never merge, got %d merge calls", merger.callCount())
		}
	})

	t.Run("read-write + background rejected", func(t *testing.T) {
		res := runOneSubagent(t, makeWritable(), "p1", `{"prompt":"go","mode":"read-write","background":true}`)
		if !res.IsError || !strings.Contains(res.Content, "background") {
			t.Fatalf("read-write+background must be rejected, got %+v", res)
		}
	})

	t.Run("read-write + agent rejected", func(t *testing.T) {
		var rr atomic.Pointer[string]
		readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t))
		task := agent.NewSubagentTool(readOnly,
			agent.WithWritableChildEngine(writableChildWriting(t, "ok", &rr)),
			agent.WithWritableChildForker(&memForker{}),
			agent.WithSubagentAutoMerge(&fakeMerger{}),
			agent.WithAgentEngines(
				map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
				[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}},
			))
		res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write","agent":"reviewer"}`)
		if !res.IsError || !strings.Contains(res.Content, "agent") {
			t.Fatalf("read-write+agent must be rejected, got %+v", res)
		}
	})

	t.Run("read-write unsupported when unwired", func(t *testing.T) {
		// A plain read-only Subagent tool (no writable wiring) rejects read-write.
		task := agent.NewSubagentTool(childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t)))
		res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write"}`)
		if !res.IsError || !strings.Contains(res.Content, "not supported") {
			t.Fatalf("read-write with no writable engine must be 'not supported', got %+v", res)
		}
	})

	t.Run("read-write + fork allowed and merges", func(t *testing.T) {
		var rr atomic.Pointer[string]
		writable := writableChildWriting(t, "forked writable", &rr)
		merger := &fakeMerger{}
		readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t))
		task := agent.NewSubagentTool(readOnly,
			agent.WithWritableChildEngine(writable),
			agent.WithWritableChildForker(&memForker{}),
			agent.WithSubagentAutoMerge(merger),
		)
		// fork:true needs a parent fork-history seam; drive through a parent run so
		// caps.forkHistory is wired (mode:"read-write" + fork composes).
		res := runWritableForkWithParent(t, task)
		if res.IsError {
			t.Fatalf("read-write+fork must be allowed, got error: %q", res.Content)
		}
		if merger.callCount() != 1 {
			t.Fatalf("read-write+fork must still merge, merger called %d times, want 1", merger.callCount())
		}
	})
}

// runWritableForkWithParent drives a single mode:"read-write",fork:true Subagent
// call through a parent engine loop so the parent fork-history seam (caps.forkHistory)
// is wired (a plain Execute has none). It returns the parent's single Subagent
// ToolResult.
func runWritableForkWithParent(t *testing.T, task tool.Tool) session.ToolResult {
	t.Helper()
	parentCat := catalogWith(t, task)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"continue this","mode":"read-write","fork":true}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	for _, ev := range drain(r) {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			return *ev.ToolResult
		}
	}
	t.Fatal("no Subagent tool result observed in the parent run")
	return session.ToolResult{}
}
