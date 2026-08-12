package agent_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// driveWrite runs ONE prompt that issues a single Write{path:"a"} tool call,
// resolving any permission.ask with verdict. It returns how many asks were
// emitted and whether the Write tool executed. Each call builds a FRESH engine
// (each needs its own scripted mockllm cursor) but the SAME policy is passed in,
// so a rule learned on an earlier call governs a later one.
func driveWrite(t *testing.T, policy *permpolicy.Policy, sess *session.Session, verdict session.ApprovalVerdict) (asks int, executed bool) {
	t.Helper()
	var ran atomic.Bool
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			ran.Store(true)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	cat := catalogWith(t, write)
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: policy})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			asks++
			r.Approve(ev.Ask.AskID, verdict)
		}
	}
	return asks, ran.Load()
}

// VerdictAllowAlways: the FIRST identical call asks; the rule is learned; the
// SECOND identical call (same session, same policy) does NOT ask and still runs.
func TestAllowAlwaysLearnsThenNoAsk(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New()) // Write asks by default
	sess := newSession(t, session.Limits{})

	asks1, ran1 := driveWrite(t, policy, sess, session.VerdictAllowAlways)
	if asks1 != 1 || !ran1 {
		t.Fatalf("first call: expected 1 ask and execution, got asks=%d ran=%v", asks1, ran1)
	}
	// Reopen the completed session so a second prompt can run against it (the
	// learned rule lives on the shared policy/store, not the session).
	if err := sess.Reopen(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	asks2, ran2 := driveWrite(t, policy, sess, session.VerdictAllowAlways)
	if asks2 != 0 {
		t.Fatalf("second call after allow-always: expected NO ask, got %d", asks2)
	}
	if !ran2 {
		t.Fatalf("second call should still execute (learned allow)")
	}
}

// VerdictAllowOnce: every identical call STILL asks — nothing is learned.
func TestAllowOnceDoesNotLearn(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := newSession(t, session.Limits{})

	if asks1, ran1 := driveWrite(t, policy, sess, session.VerdictAllowOnce); asks1 != 1 || !ran1 {
		t.Fatalf("first call: expected 1 ask and execution, got asks=%d ran=%v", asks1, ran1)
	}
	if err := sess.Reopen(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if asks2, ran2 := driveWrite(t, policy, sess, session.VerdictAllowOnce); asks2 != 1 || !ran2 {
		t.Fatalf("second call after allow-once: expected another ask + execution, got asks=%d ran=%v", asks2, ran2)
	}
}

// VerdictDeny: the call asks, is denied, and the tool does NOT execute.
func TestDenyVerdictBlocksExecution(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := newSession(t, session.Limits{})

	asks, ran := driveWrite(t, policy, sess, session.VerdictDeny)
	if asks != 1 {
		t.Fatalf("expected 1 ask, got %d", asks)
	}
	if ran {
		t.Fatalf("denied call must NOT execute the tool")
	}
}
