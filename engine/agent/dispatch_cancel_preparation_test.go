package agent_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type cancelReadPreparationPolicy struct {
	cancel context.CancelFunc
	at     session.ToolCallID
}

func (p cancelReadPreparationPolicy) Evaluate(_ context.Context, _ session.SessionID, _ session.PermissionMode, c session.ToolCall, _ tool.WorkspaceReader) port.PermissionResult {
	if c.ID == p.at {
		p.cancel()
	}
	if c.ID == "denied" {
		return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Deny, Reason: "denied by policy"}}
	}
	return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Allow}}
}

func (cancelReadPreparationPolicy) Learn(session.SessionID, session.ToolCall) {}

func TestCancelledReadPreparationPairsEveryCall(t *testing.T) {
	for _, cancelAt := range []session.ToolCallID{"middle", "last"} {
		t.Run(string(cancelAt), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var executed []session.ToolCallID
			read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executed = append(executed, c.ID)
				return session.NewToolResult(c.ID, "executed"), nil
			}}
			write := &fakeTool{name: "Write", exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executed = append(executed, c.ID)
				return session.NewToolResult(c.ID, "executed"), nil
			}}
			calls := []session.ToolCall{
				toolCall("denied", "Read", `{}`), toolCall("middle", "Read", `{}`), toolCall("last", "Read", `{}`),
				toolCall("serial", "Write", `{}`), toolCall("later", "Read", `{}`),
			}
			sess := newSession(t, session.Limits{})
			eng := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...)), Catalog: catalogWith(t, read, write), Policy: cancelReadPreparationPolicy{cancel: cancel, at: cancelAt}})
			events := drain(eng.Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			if len(executed) != 0 || sess.State != session.StateCancelled {
				t.Fatalf("executed=%v state=%s", executed, sess.State)
			}
			var opened, canonical, available []session.ToolCallID
			for _, ev := range events {
				switch ev.Type {
				case session.EvToolCall:
					opened = append(opened, ev.ToolCall.ID)
				case session.EvToolResult:
					canonical = append(canonical, ev.ToolResult.CallID)
					if ev.ToolResult.CallID == "denied" {
						if !strings.Contains(ev.ToolResult.Content, "denied by policy") {
							t.Fatalf("denial replaced: %+v", ev.ToolResult)
						}
					} else if !ev.ToolResult.IsError || !strings.Contains(ev.ToolResult.Content, "cancelled") {
						t.Fatalf("missing cancellation result: %+v", ev.ToolResult)
					}
				case session.EvToolResultAvailable:
					available = append(available, ev.ToolResult.CallID)
				}
			}
			var want []session.ToolCallID
			for _, c := range calls {
				want = append(want, c.ID)
			}
			if !reflect.DeepEqual(opened, want) || !reflect.DeepEqual(canonical, want) || !reflect.DeepEqual(available, want) || sess.Counters.ToolCalls != len(calls) {
				t.Fatalf("opened=%v canonical=%v available=%v count=%d, want %v", opened, canonical, available, sess.Counters.ToolCalls, want)
			}
			if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
				t.Fatal(err)
			}
		})
	}
}
