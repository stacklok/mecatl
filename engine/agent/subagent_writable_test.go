package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// failingForker is a tool.WorkspaceForker that FAILS the test if Fork is ever
// called. A direct-write (mode:"read-write") Subagent must NOT fork — it runs
// against the real parent workspace (ADR 0041) — so wiring this as the read-only
// childForker proves the writable path never forks.
type failingForker struct{ t *testing.T }

func (f *failingForker) Fork(_ context.Context, _ tool.Workspace, _ string) (tool.Workspace, func() error, string, error) {
	f.t.Helper()
	f.t.Fatal("a mode:\"read-write\" Subagent must NOT fork the workspace (direct-write, ADR 0041)")
	return nil, nil, "", errors.New("unreachable")
}

// writableChildWriting scripts a child that calls a "Write" fakeTool (recording the
// workspace root it ran against), then emits a final summary. recordedRoot captures
// the workspace root the child's mutating tool actually saw, so a test can assert the
// child ran against the REAL parent workspace, not a fork.
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

// newWritableSubagent assembles a Subagent tool with the direct-write wiring: a
// read-only explorer engine (whose summary differs so a test can tell which engine
// ran) plus a writable child engine. The read-only childEngine is the mandatory first
// arg. There is no forker and no merger — a writable child writes the parent tree
// directly (ADR 0041).
func newWritableSubagent(t *testing.T, writable *agent.Engine, extra ...agent.SubagentOption) tool.Tool {
	t.Helper()
	readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("READ-ONLY EXPLORER RAN")), catalogWith(t))
	opts := append([]agent.SubagentOption{
		agent.WithWritableChildEngine(writable),
	}, extra...)
	return agent.NewSubagentTool(readOnly, opts...)
}

// TestSubagentWritableSelectsWritableEngineAndWritesParentWorkspace proves a
// mode:"read-write" call runs the WRITABLE child engine (not the read-only explorer),
// the child's mutating tool runs against the REAL parent workspace (no fork), and the
// result carries the honest direct-write note.
func TestSubagentWritableSelectsWritableEngineAndWritesParentWorkspace(t *testing.T) {
	var recordedRoot atomic.Pointer[string]
	writable := writableChildWriting(t, "WRITABLE CHILD DID THE WORK", &recordedRoot)
	// Wire a failing read-only forker: the writable path must never reach it.
	task := newWritableSubagent(t, writable, agent.WithChildForker(&failingForker{t}))

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
	// The child's mutating tool ran against the REAL parent workspace (/ws), NOT a fork.
	if rr := recordedRoot.Load(); rr == nil || *rr != "/ws" {
		t.Fatalf("child Write ran against root %v, want the real parent workspace /ws (direct-write)", rr)
	}
	// The result honestly tells the model its edits landed directly in the workspace.
	if !strings.Contains(res.Content, "edited your workspace directly") {
		t.Fatalf("result must note the edits were applied directly, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "merge") || strings.Contains(res.Content, "fork") {
		t.Fatalf("direct-write result must NOT mention merge/fork, got:\n%s", res.Content)
	}
}

// TestSubagentWritableDirectWriteE2E drives a mode:"read-write" Subagent through the
// real harness (a parent Engine.Run whose turn calls Subagent) over a memfs workspace,
// and asserts (a) the file the child writes lands in the REAL parent workspace, (b) NO
// fork happened (the read-only childForker is a failingForker that fails the test if
// Fork is ever called on the writable path), and (c) gauntlet #7 holds (the parent
// never sees the child's intermediate tool events/content).
func TestSubagentWritableDirectWriteE2E(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")

	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, w tool.Workspace) (session.ToolResult, error) {
			if err := w.Write(context.Background(), "child-output.txt", []byte("written by the writable subagent\n")); err != nil {
				return session.NewToolError(in.ID, "write failed: "+err.Error()), nil
			}
			return session.NewToolResult(in.ID, "wrote child-output.txt"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"child-output.txt"}`)),
		mockllm.TextTurn("CHILD SECRET INTERMEDIATE"),
	)
	writable := childEngineWith(childLLM, catalogWith(t, writeTool))
	task := newWritableSubagent(t, writable, agent.WithChildForker(&failingForker{t}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"implement","mode":"read-write"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), ws, agent.RunRequest{Text: "go"})

	var sawChildContent bool
	for _, ev := range drain(r) {
		// gauntlet #7: the parent's own event stream must never carry the child's
		// intermediate tool result text or message content.
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, "wrote child-output.txt") {
			sawChildContent = true
		}
		if ev.Type == session.EvMessageDelta && strings.Contains(ev.Text, "CHILD SECRET INTERMEDIATE") {
			sawChildContent = true
		}
	}
	if sawChildContent {
		t.Fatal("parent run leaked the child's intermediate tool result/content (gauntlet #7)")
	}

	// (a) The file landed in the REAL parent workspace (proving direct-write — the
	// child wrote the very workspace the parent owns, not a fork copy).
	if got, err := ws.Read(context.Background(), "child-output.txt"); err != nil {
		t.Fatalf("the writable subagent's edit did not land in the real workspace: %v", err)
	} else if !strings.Contains(string(got), "written by the writable subagent") {
		t.Fatalf("workspace file has unexpected content: %q", got)
	}
	// (b) No fork occurred: the failingForker would have failed the test if the
	// writable path forked.
}

// TestSubagentWritablePartialEditSurvivesStopError is the adversarial direct-write
// guard: a writable child that does a successful Write to the REAL tree but then
// terminates StopError leaves that first edit IN the tree (NOT rolled back / NOT
// quarantined — there is no fork to roll back), no fork was created, and the result
// text honestly reports the failure + that edits may have PARTIALLY landed.
func TestSubagentWritablePartialEditSurvivesStopError(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")

	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, w tool.Workspace) (session.ToolResult, error) {
			if err := w.Write(context.Background(), "partial.txt", []byte("first edit landed\n")); err != nil {
				return session.NewToolError(in.ID, "write failed: "+err.Error()), nil
			}
			return session.NewToolResult(in.ID, "wrote partial.txt"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"partial.txt"}`)),
		mockllm.ErrorTurn(errors.New("provider stream blew up")),
	)
	writable := childEngineWith(childLLM, catalogWith(t, writeTool))
	// The failingForker proves no fork was created (the writable path must not fork).
	task := newWritableSubagent(t, writable, agent.WithChildForker(&failingForker{t}))

	res, err := task.Execute(context.Background(),
		session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"implement the fix","mode":"read-write"}`)), ws)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}

	// (a) The first edit IS present in the real tree (not rolled back / not quarantined).
	if got, rerr := ws.Read(context.Background(), "partial.txt"); rerr != nil {
		t.Fatalf("the writable child's first edit must survive a StopError (no rollback), but it is gone: %v", rerr)
	} else if !strings.Contains(string(got), "first edit landed") {
		t.Fatalf("partial edit content unexpected: %q", got)
	}
	// (b) No fork was created (the failingForker would have failed the test).
	// (c) The result honestly reports the failure + that edits may have partially landed.
	if !res.IsError {
		t.Fatalf("a StopError child must surface a tool error, got success:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "may be PARTIAL") {
		t.Fatalf("result must warn that the edits may be partial, got:\n%s", res.Content)
	}
}

// TestSubagentWritablePartialEditSurvivesStopCancelled closes the untested half of the
// StopError||StopCancelled OR: a writable child lands ONE innocuous edit in the REAL
// tree, then blocks; the PARENT ctx is cancelled mid-run. The edit must survive (no
// rollback / no quarantine — direct-write) and the result must honestly warn the model
// the edits may be PARTIAL.
func TestSubagentWritablePartialEditSurvivesStopCancelled(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, w tool.Workspace) (session.ToolResult, error) {
			if err := w.Write(context.Background(), "cancelled.txt", []byte("first edit landed\n")); err != nil {
				return session.NewToolError(in.ID, "write failed: "+err.Error()), nil
			}
			return session.NewToolResult(in.ID, "wrote cancelled.txt"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"cancelled.txt"}`)),
		mockllm.ToolCallTurn(toolCall("b1", "Block", `{}`)),
		mockllm.TextTurn("never reached"),
	)
	writable := childEngineWith(childLLM, catalogWith(t, writeTool, block))
	task := newWritableSubagent(t, writable)

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type out struct {
		res session.ToolResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := task.Execute(parentCtx,
			session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"implement the fix","mode":"read-write"}`)), ws)
		done <- out{res, err}
	}()

	<-block.entered // the child has landed its edit and is now in-flight in Block
	cancel()        // cancel the PARENT mid-run

	got := <-done
	if got.err != nil {
		t.Fatalf("transport error: %v", got.err)
	}
	// (a) The first edit survives in the real tree (no rollback).
	if data, rerr := ws.Read(context.Background(), "cancelled.txt"); rerr != nil {
		t.Fatalf("the writable child's edit must survive a cancel (no rollback): %v", rerr)
	} else if !strings.Contains(string(data), "first edit landed") {
		t.Fatalf("partial edit content unexpected: %q", data)
	}
	// (b) The result honestly warns the edits may be partial.
	if !strings.Contains(got.res.Content, "may be PARTIAL") {
		t.Fatalf("a cancelled writable child must warn the edits may be partial, got:\n%s", got.res.Content)
	}
}

// TestSubagentWritableTimeoutSurfacesTimeBudgetError is the time-budget guard: a
// writable child that lands ONE real edit and then blows its timeout_ms is stopped with
// the time-budget error AND warned that its edits may be PARTIAL. Under direct-write (ADR
// 0041) the edit DID land in the real tree, and a mid-task timeout kill can leave
// half-finished work — so the timeout terminal must carry the same git-recovery thread
// the StopError/cancel path gets, never the benign "edited directly" note.
func TestSubagentWritableTimeoutSurfacesTimeBudgetError(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, w tool.Workspace) (session.ToolResult, error) {
			if err := w.Write(context.Background(), "timed-out.txt", []byte("first edit landed\n")); err != nil {
				return session.NewToolError(in.ID, "write failed: "+err.Error()), nil
			}
			return session.NewToolResult(in.ID, "wrote timed-out.txt"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"timed-out.txt"}`)),
		mockllm.ToolCallTurn(toolCall("b1", "Block", `{}`)),
		mockllm.TextTurn("never reached"),
	)
	writable := childEngineWith(childLLM, catalogWith(t, writeTool, block))
	task := newWritableSubagent(t, writable)

	res, err := task.Execute(context.Background(),
		session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"loop forever","mode":"read-write","timeout_ms":120}`)), ws)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "time budget") {
		t.Fatalf("a timed-out writable child must surface the time-budget error, got %+v", res)
	}
	// The first edit DID land in the real tree (direct-write) and the model is warned
	// the work may be PARTIAL — a timeout is a mid-task kill, not a clean end.
	if got, rerr := ws.Read(context.Background(), "timed-out.txt"); rerr != nil {
		t.Fatalf("the writable child's edit must survive a timeout (no rollback): %v", rerr)
	} else if !strings.Contains(string(got), "first edit landed") {
		t.Fatalf("partial edit content unexpected: %q", got)
	}
	if !strings.Contains(res.Content, "may be PARTIAL") {
		t.Fatalf("a writable timeout must warn the edits may be partial, got:\n%s", res.Content)
	}
}

// TestSubagentWritableWithOutputSchemaComposes is the positive composition guard for
// read-write + output_schema (the schema description advertises this; only +fork was
// exercised before): a writable child calls SubmitResult with a VALID payload, and the
// Subagent result carries the validated JSON. It runs the WRITABLE engine (not the
// read-only explorer) and the result still carries the direct-write note.
func TestSubagentWritableWithOutputSchemaComposes(t *testing.T) {
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "SubmitResult", `{"name":"Ada","age":36}`)),
		mockllm.TextTurn("done"),
	)
	writable := childEngineWith(childLLM, catalogWith(t))
	task := newWritableSubagent(t, writable, agent.WithChildForker(&failingForker{t}))

	res := runOneSubagent(t, task, "p1",
		`{"prompt":"profile Ada","mode":"read-write","output_schema":`+personSchema+`}`)
	if res.IsError {
		t.Fatalf("read-write + output_schema with a valid payload must succeed, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, `"name":"Ada"`) || !strings.Contains(res.Content, `"age":36`) {
		t.Fatalf("result must carry the validated payload, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "READ-ONLY EXPLORER RAN") {
		t.Fatalf("read-write call must run the writable engine, not the explorer, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "edited your workspace directly") {
		t.Fatalf("read-write result must still carry the direct-write note, got:\n%s", res.Content)
	}
}

// TestSubagentWritableNotIsolatedSkipsA2 is the BEHAVIORAL isolated:false guard,
// driving the REAL run() posture (not a hand-built childPosture): a writable child
// issuing an isolation-APPROVABLE Bash substitution (`go test $(echo ./...)`) under a
// HEADLESS parent must AUTO-DENY it — because a direct-write child is isolated:false
// (ADR 0041), so the A2 isolation auto-approve (which only fires when isolated) does
// NOT apply. If run()'s posture were `isolated: true || ...` (the pre-0041 bug) the A2
// path would auto-APPROVE and the Bash would RUN — so this test FAILS under that
// mutation. The read-only forker is a failingForker to also prove no fork happens.
func TestSubagentWritableNotIsolatedSkipsA2(t *testing.T) {
	bash := &fakeBash{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":"go test $(echo ./...)"}`)),
		mockllm.TextTurn("child: adapted after the denied command"),
	)
	writable := bashChildEngine(childLLM, bash)
	task := newWritableSubagent(t, writable, agent.WithChildForker(&failingForker{t}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"implement","mode":"read-write"}`)),
		mockllm.TextTurn("parent: done"),
	)
	// newEngine ⇒ Interactive=false (headless): an unresolved ask auto-denies.
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("a NON-isolated (direct-write) writable child's isolation-approvable Bash must NOT be "+
			"A2 auto-approved under a headless parent — it must auto-deny; but the command ran: %v", got)
	}
}

// TestSubagentWritableReadOnlyStaysTrue PINS the invariant: configuring a writable
// Subagent tool does NOT flip ReadOnly() — read-only fan-out stays parallel-safe; a
// read-write CALL is excluded from the concurrent batch via MutatesParent instead.
func TestSubagentWritableReadOnlyStaysTrue(t *testing.T) {
	writable := childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t))
	task := newWritableSubagent(t, writable)
	if !task.ReadOnly() {
		t.Fatal("a writable-configured SubagentTool must still report ReadOnly()==true")
	}
}

// TestSubagentModeCombinationGuards is the combination table: an unknown mode,
// read-write+background, read-write+agent+model, and read-write+agent-without-factory are
// all rejected; read-write+agent SUCCEEDS when the writable-specialist factory is wired
// (ADR 0058); read-write with no writable engine wired is "not supported"; read-write+fork
// is ALLOWED (composes).
func TestSubagentModeCombinationGuards(t *testing.T) {
	makeWritable := func() tool.Tool {
		var rr atomic.Pointer[string]
		return newWritableSubagent(t, writableChildWriting(t, "ok", &rr))
	}

	t.Run("unknown mode rejected", func(t *testing.T) {
		res := runOneSubagent(t, makeWritable(), "p1", `{"prompt":"go","mode":"sideways"}`)
		if !res.IsError || !strings.Contains(res.Content, "unknown mode") {
			t.Fatalf("unknown mode must be rejected, got %+v", res)
		}
	})

	t.Run("explicit read-only accepted (read-only explorer runs)", func(t *testing.T) {
		// The explicit "read-only" literal is accepted exactly like "" — it runs the
		// read-only explorer (TEST GAP: pin the literal, not just "").
		var rr atomic.Pointer[string]
		task := newWritableSubagent(t, writableChildWriting(t, "should NOT run", &rr))
		res := runOneSubagent(t, task, "p1", `{"prompt":"look around","mode":"read-only"}`)
		if res.IsError {
			t.Fatalf("explicit mode:\"read-only\" must be accepted, got error: %q", res.Content)
		}
		if !strings.Contains(res.Content, "READ-ONLY EXPLORER RAN") {
			t.Fatalf("explicit read-only must run the read-only explorer, got:\n%s", res.Content)
		}
	})

	t.Run("read-write + background rejected", func(t *testing.T) {
		res := runOneSubagent(t, makeWritable(), "p1", `{"prompt":"go","mode":"read-write","background":true}`)
		if !res.IsError || !strings.Contains(res.Content, "background") {
			t.Fatalf("read-write+background must be rejected, got %+v", res)
		}
	})

	t.Run("read-write + agent succeeds via factory", func(t *testing.T) {
		// ADR 0058: read-write+agent is ALLOWED when the deployment wires the writable-
		// specialist factory (WithAgentWritableEngineFactory). The factory returns a
		// writable specialist engine (carrying a Write fakeTool + a marker summary); the
		// call SUCCEEDS, the writable specialist ran (marker), the pre-built read-only
		// specialist did NOT run (no map reuse), the direct-write note appears, and no
		// fork happened (failingForker).
		var rr atomic.Pointer[string]
		writableSpec := writableChildWriting(t, "WRITABLE SPECIALIST RAN", &rr)
		readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t))
		prebuiltReviewer := childEngineWith(mockllm.New(mockllm.TextTurn("PREBUILT-REVIEWER")), catalogWith(t))
		task := agent.NewSubagentTool(readOnly,
			agent.WithWritableChildEngine(writableChildWriting(t, "should NOT run", &rr)),
			agent.WithAgentEngines(
				map[string]*agent.Engine{"reviewer": prebuiltReviewer},
				[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}},
			),
			agent.WithAgentWritableEngineFactory(func(agentName string) (*agent.Engine, bool) {
				if agentName == "reviewer" {
					return writableSpec, true
				}
				return nil, false
			}),
			agent.WithChildForker(&failingForker{t}))
		res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write","agent":"reviewer"}`)
		if res.IsError {
			t.Fatalf("read-write+agent with a wired factory must succeed, got error: %q", res.Content)
		}
		if !strings.Contains(res.Content, "WRITABLE SPECIALIST RAN") {
			t.Fatalf("read-write+agent must run the writable specialist (factory engine), got:\n%s", res.Content)
		}
		if strings.Contains(res.Content, "PREBUILT-REVIEWER") {
			t.Fatalf("read-write+agent must NOT run the pre-built read-only specialist (no map reuse), got:\n%s", res.Content)
		}
		if strings.Contains(res.Content, "should NOT run") {
			t.Fatalf("read-write+agent must NOT run the generic writable explorer, got:\n%s", res.Content)
		}
		if !strings.Contains(res.Content, "edited your workspace directly") {
			t.Fatalf("read-write+agent result must carry the direct-write note, got:\n%s", res.Content)
		}
	})

	t.Run("read-write + agent + model rejected", func(t *testing.T) {
		// validateMode's read-write+agent+model arm fires FIRST (before selectChildEngine),
		// so read-write+agent+model is rejected at the v1-scope guard — never reaching the
		// agent+model support path. A writable specialist runs on its own resolved model.
		var rr atomic.Pointer[string]
		readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t))
		overrideEngine := childEngineWithModel("fast", mockllm.New(mockllm.TextTurn("fast")), catalogWith(t))
		task := agent.NewSubagentTool(readOnly,
			agent.WithWritableChildEngine(writableChildWriting(t, "ok", &rr)),
			agent.WithAgentEngines(
				map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
				[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}},
			),
			agent.WithAgentModelEngineFactory(func(string, string) (*agent.Engine, bool) { return overrideEngine, true }),
			agent.WithAgentWritableEngineFactory(func(string) (*agent.Engine, bool) { return overrideEngine, true }))
		res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write","agent":"reviewer","model":"fast"}`)
		if !res.IsError || !strings.Contains(res.Content, "cannot be combined with both") {
			t.Fatalf("read-write+agent+model must be rejected with the v1-scope message, got %+v", res)
		}
		if strings.Contains(res.Content, "not supported in this deployment") {
			t.Fatalf("the v1-scope guard must fire (not the factory-nil unsupported guard), got %q", res.Content)
		}
	})

	t.Run("read-write unsupported when unwired (no agent)", func(t *testing.T) {
		// A plain read-only Subagent tool (no writable wiring) rejects read-write (the
		// no-agent unwired case — the writable explorer path).
		task := agent.NewSubagentTool(childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t)))
		res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write"}`)
		if !res.IsError || !strings.Contains(res.Content, "not supported") {
			t.Fatalf("read-write with no writable engine must be 'not supported', got %+v", res)
		}
	})

	t.Run("read-write + agent unsupported without factory", func(t *testing.T) {
		// read-write+agent with WithWritableChildEngine wired BUT no
		// WithAgentWritableEngineFactory: the factory-nil arm rejects it as "not supported
		// in this deployment" (a writable specialist needs the writable-specialist factory,
		// not the generic writable explorer engine).
		var rr atomic.Pointer[string]
		readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t))
		task := agent.NewSubagentTool(readOnly,
			agent.WithWritableChildEngine(writableChildWriting(t, "ok", &rr)),
			agent.WithAgentEngines(
				map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
				[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}},
			))
		res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write","agent":"reviewer"}`)
		if !res.IsError || !strings.Contains(res.Content, "not supported in this deployment") {
			t.Fatalf("read-write+agent with no writable-specialist factory must be 'not supported in this deployment', got %+v", res)
		}
	})

	t.Run("read-write + fork allowed", func(t *testing.T) {
		var rr atomic.Pointer[string]
		writable := writableChildWriting(t, "forked writable", &rr)
		readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t))
		task := agent.NewSubagentTool(readOnly, agent.WithWritableChildEngine(writable))
		// fork:true needs a parent fork-history seam; drive through a parent run so
		// caps.forkHistory is wired (mode:"read-write" + fork composes).
		res := runWritableForkWithParent(t, task)
		if res.IsError {
			t.Fatalf("read-write+fork must be allowed, got error: %q", res.Content)
		}
		if !strings.Contains(res.Content, "forked writable") {
			t.Fatalf("read-write+fork must run the writable child, got:\n%s", res.Content)
		}
	})
}

// runWritableForkWithParent drives a single mode:"read-write",fork:true Subagent call
// through a parent engine loop so the parent fork-history seam (caps.forkHistory) is
// wired (a plain Execute has none). It returns the parent's single Subagent ToolResult.
func runWritableForkWithParent(t *testing.T, task tool.Tool) session.ToolResult {
	t.Helper()
	parentCat := catalogWith(t, task)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"continue this","mode":"read-write","fork":true}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	for _, ev := range drain(r) {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			return *ev.ToolResult
		}
	}
	t.Fatal("no Subagent tool result observed in the parent run")
	return session.ToolResult{}
}

// subagentGenericResumeHintFragment is a fragment unique to renderSubagentResult's GENERIC
// subagentErrorResumeHint ("…resume it with the agentId above to continue from its
// transcript…"). The writable arm's combined note says "the agentId BELOW … to finish on
// top of them" and never this, so matching on this fragment distinguishes "the generic
// hint leaked in" from "the combined decision is present".
//
// This copy of a production string is the exact thing that went stale once already: it
// used to read "continue where it left off", which the resume-honesty fix removed from the
// hint, leaving the absence check below unfalsifiable — it would have passed with the
// generic hint fully re-leaked. So the check is now paired with a POSITIVE control in the
// same test (a read-only failure MUST carry this fragment). If the production wording
// moves again, the positive control fails loudly instead of the guard going quiet.
const subagentGenericResumeHintFragment = "continue from its transcript"

// readOnlySubagentFailureBody renders a READ-ONLY child's StopError result in the same
// store-wired configuration the writable test uses. It exists as the POSITIVE CONTROL for
// subagentGenericResumeHintFragment: the arm that DOES carry the generic hint, so an
// absence check against that fragment is provably falsifiable.
func readOnlySubagentFailureBody(t *testing.T) string {
	t.Helper()
	childEngine := childEngineWith(mockllm.New(mockllm.EmptyTurnWithStop(session.StopError)), catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(memstore.New()))
	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("positive control: want 1 errored read-only tool result, got %+v", results)
	}
	return results[0].Content
}

// TestWritableSubagentFailureRendersOneCombinedNextAction pins the render #318's
// MOTIVATING scenario produces: a long-running direct-write child that died mid-task with
// edits already applied to the real tree. Three independently-owned pieces collide in that
// one body and none of them was asserted together:
//
//   - subagentErrorBody's cause-leads composition (issue #319),
//   - the partial-edits honesty ADR 0041 owes ("its edits may be PARTIAL"),
//   - the resume affordance (issue #318).
//
// The load-bearing property is that the last two arrive as ONE decision. Stated as two
// independent imperatives ("undo with git checkout" and "resume to continue where it left
// off"), a model can do both — discard the edits and then resume a child that
// resumeWritableNote immediately greets with "the file edits you already made are STILL IN
// PLACE", which the discard just made false. So the writable arm carries
// writableSubagentFailedNote and the generic subagentErrorResumeHint must NOT also appear.
//
// The ordering is asserted rather than trusted: the note says "the agentId BELOW", and it
// is true only because renderSubagentResult puts the trailer LAST on the StopError arm
// while the writable note is prepended at the top. That is a shape change away from being
// wrong.
func TestWritableSubagentFailureRendersOneCombinedNextAction(t *testing.T) {
	const chatter = "Now let me wire the second half."
	const causeText = "context deadline exceeded: stream idle for 180s"
	// A store is wired: it is validateResume's first precondition, so it is the
	// configuration in which the advertised resume can actually succeed.
	writable := childEngineWith(
		chattyThenBrokenChild(chatter, errors.New(causeText)),
		catalogWith(t, failureCauseReadTool()))
	task := newWritableSubagent(t, writable,
		agent.WithSubagentStore(memstore.New()),
		agent.WithChildForker(&failingForker{t}))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"implement the fix","mode":"read-write"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	body := results[0].Content

	// (a) The actionable half leads — not the child's last chat line.
	if !strings.Contains(body, causeText) {
		t.Fatalf("a failed WRITABLE delegation must carry the provider cause (issue #319), got:\n%s", body)
	}
	if strings.Contains(body, chatter) && !strings.Contains(body, "Last activity before the failure: "+chatter) {
		t.Fatalf("the child's last chat line may only appear as labelled context, got:\n%s", body)
	}
	// (b) The direct-write honesty: edits may be sitting half-finished in the real tree.
	if !strings.Contains(body, "may be PARTIAL") {
		t.Fatalf("a mid-task killed writable child must warn that its edits may be PARTIAL (ADR 0041), got:\n%s", body)
	}
	// (c) ONE decision, with the two options named as mutually exclusive.
	if !strings.Contains(body, "Either resume it with the agentId below") || !strings.Contains(body, "Do not do both") {
		t.Fatalf("the writable failure must state resume-or-discard as ONE exclusive decision, got:\n%s", body)
	}
	// (d) …and NOT also the generic hint, which would re-create the two-imperatives trap.
	//
	// POSITIVE CONTROL FIRST — without it this is an absence check against a string
	// literal that can silently stop matching the production hint (it did exactly that
	// once). A READ-ONLY failure in the same store-wired configuration MUST carry the
	// fragment, which proves the absence check below can actually fire.
	if roBody := readOnlySubagentFailureBody(t); !strings.Contains(roBody, subagentGenericResumeHintFragment) {
		t.Fatalf("subagentGenericResumeHintFragment %q no longer appears in the GENERIC resume hint, so the suppression check below is vacuous — re-point it at the live wording:\n%s",
			subagentGenericResumeHintFragment, roBody)
	}
	if strings.Contains(body, subagentGenericResumeHintFragment) {
		t.Fatalf("the writable arm must suppress the generic resume hint (it owns a combined note), got:\n%s", body)
	}
	// The writable note must not smuggle the generic hint's OTHER identifying clause
	// either — "agentId above" is the read-only layout, "agentId below" is this one.
	if strings.Contains(body, "resume it with the agentId above") {
		t.Fatalf("the writable arm must not carry the read-only hint's \"agentId above\" clause, got:\n%s", body)
	}
	// (e) The ordering the note's own wording depends on: the agentId really is BELOW it.
	noteAt := strings.Index(body, "Either resume it with the agentId below")
	idAt := strings.Index(body, "agentId: ")
	if idAt < 0 {
		t.Fatalf("the error result must keep the agentId trailer, got:\n%s", body)
	}
	if idAt < noteAt {
		t.Fatalf("the note says \"the agentId below\" but the agentId line comes above it:\n%s", body)
	}
}

// TestStorelessWritableSubagentFailureKeepsPlainPartialNote is the negative of (c)/(d)
// above on the OTHER precondition: with no session store wired, validateResume refuses
// every resume, so the writable failure must fall back to the plain review-or-undo note
// and offer no resume at all — neither the combined decision nor the generic hint.
func TestStorelessWritableSubagentFailureKeepsPlainPartialNote(t *testing.T) {
	writable := childEngineWith(
		mockllm.New(mockllm.ErrorTurn(errors.New("upstream 503"), mockllm.TextChunk("x"))),
		catalogWith(t))
	task := newWritableSubagent(t, writable, agent.WithChildForker(&failingForker{t}))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"implement","mode":"read-write"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	body := results[0].Content
	if !strings.Contains(body, "may be PARTIAL") {
		t.Fatalf("the partial-edits warning is unconditional on a mid-task kill, got:\n%s", body)
	}
	if strings.Contains(body, "resume it with the agentId") {
		t.Fatalf("a store-less deployment must offer no resume at all, got:\n%s", body)
	}
	if strings.Contains(body, "Do not do both") {
		t.Fatalf("the combined resume-or-discard decision must not appear where resume is unsupported, got:\n%s", body)
	}
}
