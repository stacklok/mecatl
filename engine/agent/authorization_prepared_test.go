package agent_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestPreparedAuthorizationContinuationGatesExecutionAndStartsOnce(t *testing.T) {
	var executed atomic.Int32
	protected := &fakeTool{name: "protected", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed.Add(1)
		return session.NewToolResult(call.ID, "ok"), nil
	}}
	engine := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t, protected)})
	sess := newSession(t, session.Limits{})
	call := session.NewToolCall("call", protected.Spec().Name, nil)
	authorization := session.ExternalAuthorization{ID: "authorization", DisplayName: "Calendar", Binding: "binding", ExpiresAt: time.Now().Add(time.Hour)}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	if err := sess.PauseForAuthorization(session.PendingAuthorization{Authorization: authorization, Call: call}); err != nil {
		t.Fatal(err)
	}
	pending, err := sess.ClaimAuthorization()
	if err != nil {
		t.Fatal(err)
	}

	prepared := engine.PrepareAuthorizationContinuation(context.Background(), sess, agent.MemEnv("/ws"), pending, session.AuthorizationGranted)
	if got := executed.Load(); got != 0 {
		t.Fatalf("execution before Start = %d", got)
	}
	run := prepared.Run()
	started, transition := prepared.Start()
	if started != run || transition != agent.PreparedRunStarted {
		t.Fatalf("first Start = %p, %q", started, transition)
	}
	duplicate, transition := prepared.Start()
	if duplicate != run || transition != agent.PreparedRunDuplicateStart {
		t.Fatalf("duplicate Start = %p, %q", duplicate, transition)
	}
	if transition := prepared.Abort(); transition != agent.PreparedRunAbortAfterStart {
		t.Fatalf("Abort after Start = %q", transition)
	}
	events := drain(run)
	resultAt, resolvedAt := -1, -1
	for i, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == call.ID {
			resultAt = i
		}
		if ev.Type == session.EvAuthorizationResolved {
			if ev.Authorization == nil || ev.Authorization.DisplayName != "Calendar" {
				t.Fatalf("resolved authorization = %+v", ev.Authorization)
			}
			resolvedAt = i
		}
	}
	if resultAt < 0 || resolvedAt <= resultAt {
		t.Fatalf("authorization event order: result=%d resolved=%d", resultAt, resolvedAt)
	}
	if got := executed.Load(); got != 1 {
		t.Fatalf("execution after duplicate Start = %d", got)
	}
}

func TestPreparedAuthorizationContinuationAbortWinsWithoutExecution(t *testing.T) {
	var executed atomic.Int32
	protected := &fakeTool{name: "protected", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed.Add(1)
		return session.NewToolResult(call.ID, "ok"), nil
	}}
	engine := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t, protected)})
	sess := newSession(t, session.Limits{})
	call := session.NewToolCall("call", protected.Spec().Name, nil)
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	authorization := session.ExternalAuthorization{ID: "authorization", DisplayName: "Calendar", Binding: "binding", ExpiresAt: time.Now().Add(time.Hour)}
	if err := sess.PauseForAuthorization(session.PendingAuthorization{Authorization: authorization, Call: call}); err != nil {
		t.Fatal(err)
	}
	pending, err := sess.ClaimAuthorization()
	if err != nil {
		t.Fatal(err)
	}

	prepared := engine.PrepareAuthorizationContinuation(context.Background(), sess, agent.MemEnv("/ws"), pending, session.AuthorizationGranted)
	if transition := prepared.Abort(); transition != agent.PreparedRunAborted {
		t.Fatalf("first Abort = %q", transition)
	}
	if transition := prepared.Abort(); transition != agent.PreparedRunDuplicateAbort {
		t.Fatalf("duplicate Abort = %q", transition)
	}
	if run, transition := prepared.Start(); run != prepared.Run() || transition != agent.PreparedRunStartAfterAbort {
		t.Fatalf("Start after Abort = %p, %q", run, transition)
	}
	drain(prepared.Run())
	if got := executed.Load(); got != 0 {
		t.Fatalf("execution after Abort = %d", got)
	}
}

func TestPreparedAfterAuthorizationGatesLoop(t *testing.T) {
	engine := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("continued")), Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	authorization := session.ExternalAuthorization{ID: "authorization", DisplayName: "Calendar", Binding: "binding", ExpiresAt: time.Now().Add(time.Hour)}
	call := session.NewToolCall("call", "protected", nil)
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	if err := sess.PauseForAuthorization(session.PendingAuthorization{Authorization: authorization, Call: call}); err != nil {
		t.Fatal(err)
	}
	results, err := sess.AbortAuthorization(string(session.AuthorizationCancelled))
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordToolResults(results); err != nil {
		t.Fatal(err)
	}
	prepared := engine.PrepareAfterAuthorization(context.Background(), sess, agent.MemEnv("/ws"), authorization, call.ID, results, session.AuthorizationCancelled)
	select {
	case <-prepared.Run().Events():
		t.Fatal("continuation emitted before Start")
	default:
	}
	run, transition := prepared.Start()
	if transition != agent.PreparedRunStarted {
		t.Fatalf("Start = %q", transition)
	}
	events := drain(run)
	resultAt, resolvedAt := -1, -1
	for i, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == call.ID {
			resultAt = i
		}
		if ev.Type == session.EvAuthorizationResolved {
			if ev.Authorization == nil || ev.Authorization.DisplayName != "Calendar" {
				t.Fatalf("resolved authorization = %+v", ev.Authorization)
			}
			resolvedAt = i
		}
	}
	if resultAt < 0 || resolvedAt <= resultAt {
		t.Fatalf("authorization event order: result=%d resolved=%d", resultAt, resolvedAt)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("state = %q", sess.State)
	}
}
