package agent_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestADR_0370_Scenario1_UniformAvailability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		calls      []session.ToolCall
		deniedTool string
	}{
		{"serial", []session.ToolCall{toolCall("serial", "Write", `{}`)}, ""},
		{"read batch", []session.ToolCall{toolCall("first", "Read", `{}`), toolCall("second", "Read", `{}`)}, ""},
		{"batch preparation denied", []session.ToolCall{toolCall("denied-1", "Read", `{}`), toolCall("denied-2", "Read", `{}`)}, "Read"},
		{"permission denied", []session.ToolCall{toolCall("denied", "Write", `{}`)}, "Write"},
		{"unknown tool", []session.ToolCall{toolCall("unknown", "Missing", `{}`)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResultWithParts(c.ID, "read", []session.Content{session.NewTextBlock("typed"), session.NewStructuredContentBlock(`{"ok":true}`)}), nil
			}}
			write := &fakeTool{name: "Write", exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResultWithParts(c.ID, "written", []session.Content{session.NewTextBlock("typed")}), nil
			}}
			recorder := &recordingLogger{}
			deps := agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(tc.calls...), mockllm.TextTurn("done")), Catalog: catalogWith(t, read, write), ToolCallRecorder: recorder}
			if tc.deniedTool != "" {
				deps.Policy = permpolicy.NewPolicy([]governance.Rule{{Scope: governance.ScopeBuiltinDefault, Tool: tc.deniedTool, Effect: governance.Deny}}, nil)
			}
			sess := newSession(t, session.Limits{})
			events := drain(newEngine(deps).Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			available := map[session.ToolCallID][]session.ToolResult{}
			canonical := map[session.ToolCallID]int{}
			for _, ev := range events {
				if ev.ToolResult == nil {
					continue
				}
				id := ev.ToolResult.CallID
				switch ev.Type {
				case session.EvToolResultAvailable:
					if canonical[id] != 0 {
						t.Errorf("availability after canonical for %s", id)
					}
					available[id] = append(available[id], *ev.ToolResult)
				case session.EvToolResult:
					canonical[id]++
					if len(available[id]) != 1 || !reflect.DeepEqual(available[id][0], *ev.ToolResult) {
						t.Errorf("%s availability=%+v canonical=%+v", id, available[id], *ev.ToolResult)
					}
				}
			}
			for _, call := range tc.calls {
				if len(available[call.ID]) != 1 || canonical[call.ID] != 1 {
					t.Errorf("%s: availability=%d canonical=%d; events=%v", call.ID, len(available[call.ID]), canonical[call.ID], typesOf(events))
				}
			}
			if len(available) != len(tc.calls) || len(canonical) != len(tc.calls) || sess.Counters.ToolCalls != len(tc.calls) {
				t.Errorf("unexpected results or accounting: available=%v canonical=%v counters=%+v", available, canonical, sess.Counters)
			}
			wantRecorded := len(tc.calls)
			if tc.deniedTool != "" || tc.name == "unknown tool" {
				wantRecorded = 0 // the tool never reached execution
			}
			if recorder.calls != wantRecorded {
				t.Errorf("recorder calls=%d, want %d canonical executions", recorder.calls, wantRecorded)
			}
			modelResults := 0
			for _, message := range sess.Conversation.Messages {
				if message.ToolResult != nil {
					modelResults++
				}
			}
			if modelResults != len(tc.calls) {
				t.Errorf("model results=%d, want %d canonical results", modelResults, len(tc.calls))
			}
		})
	}

	t.Run("resumed denial and interrupted sibling closeout", func(t *testing.T) {
		write := &fakeTool{name: "Write", exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(c.ID, "written"), nil
		}}
		policy := permpolicy.NewPolicy(nil, permstore.New())
		calls := []session.ToolCall{toolCall("pending", "Write", `{}`), toolCall("deferred", "Write", `{}`)}
		sess := newSession(t, session.Limits{})
		first := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...)), Catalog: catalogWith(t, write), Policy: policy})
		askID, restored := driveToAwaiting(t, first, sess, agent.MemEnv("/ws"), "go")
		resume := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t, write), Policy: policy})
		events := drain(resume.ResumeApproval(context.Background(), restored, agent.MemEnv("/ws"), askID, session.VerdictDeny))
		for _, call := range calls {
			var available, canonical []session.ToolResult
			for _, ev := range events {
				if ev.ToolResult == nil || ev.ToolResult.CallID != call.ID {
					continue
				}
				switch ev.Type {
				case session.EvToolResultAvailable:
					if len(canonical) != 0 {
						t.Errorf("%s available after canonical", call.ID)
					}
					available = append(available, *ev.ToolResult)
				case session.EvToolResult:
					canonical = append(canonical, *ev.ToolResult)
				}
			}
			if len(available) != 1 || len(canonical) != 1 || !reflect.DeepEqual(available, canonical) || !canonical[0].IsError {
				t.Errorf("%s: availability=%+v canonical=%+v", call.ID, available, canonical)
			}
		}
	})
}
