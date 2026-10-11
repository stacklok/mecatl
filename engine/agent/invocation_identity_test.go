package agent_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestInvocationIdentityAcrossSessionsAndDelegatedChildren(t *testing.T) {
	store := memstore.New()
	ids := make(map[session.InvocationID]bool)
	for _, name := range []session.SessionID{"parent-a", "parent-b"} {
		child := childEngineWith(mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("shared", "Read", []byte(`{}`))),
			mockllm.TextTurn("child done"),
		), catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(c.ID, "read"), nil
		}}))
		parent := newEngine(agent.Deps{
			LLM: mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("shared", "Subagent", []byte(`{"prompt":"inspect"}`))),
				mockllm.TextTurn("parent done"),
			),
			Catalog: catalogWith(t, agent.NewSubagentTool(child, agent.WithSubagentStore(store))),
		})
		s := session.New(name, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
		events := drain(parent.Run(context.Background(), s, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
		result := resultByCallID(events)["shared"]
		if result == nil || result.IsError {
			t.Fatalf("delegation %s: %+v", name, result)
		}
		childID := extractAgentID(t, result.Content)
		loaded, err := store.Load(context.Background(), session.SessionID(childID))
		if err != nil {
			t.Fatal(err)
		}
		for _, history := range [][]session.Message{s.Conversation.Messages, loaded.Conversation.Messages} {
			var call session.ToolCall
			for _, m := range history {
				if len(m.ToolCalls) != 0 && m.ToolCalls[0].ID == "shared" {
					call = m.ToolCalls[0]
				}
				if m.ToolResult != nil && m.ToolResult.CallID == "shared" && m.ToolResult.InvocationID != call.InvocationID {
					t.Fatalf("%s result = %q, call = %q", name, m.ToolResult.InvocationID, call.InvocationID)
				}
			}
			if call.InvocationID == "" || ids[call.InvocationID] {
				t.Fatalf("%s duplicate/empty accepted occurrence %q", name, call.InvocationID)
			}
			ids[call.InvocationID] = true
		}
	}
	if len(ids) != 4 {
		t.Fatalf("accepted occurrences: %v", ids)
	}
}

func TestInvocationIdentityAllowsCompletedProviderIDReuse(t *testing.T) {
	call := session.NewToolCall("reused", "Read", []byte(`{}`))
	sess := newSession(t, session.Limits{})
	engine := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.ToolCallTurn(call), mockllm.TextTurn("done")),
		Catalog: catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(c.ID, "ok"), nil
		}}),
	})
	run := engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	var ids []session.InvocationID
	var available []session.InvocationID
	var canonical []session.InvocationID
	for ev := range run.Events() {
		if ev.Type == session.EvToolResultAvailable {
			available = append(available, ev.ToolResult.InvocationID)
		}
		if ev.Type == session.EvToolCall {
			ids = append(ids, ev.ToolCall.InvocationID)
		}
		if ev.Type == session.EvToolResult {
			canonical = append(canonical, ev.ToolResult.InvocationID)
			if len(ids) == 0 || ev.ToolResult.InvocationID != ids[len(ids)-1] {
				t.Fatalf("result occurrence = %q, call = %q", ev.ToolResult.InvocationID, ids)
			}
		}
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] == ids[1] || len(available) != 2 || available[0] != ids[0] || available[1] != ids[1] || !reflect.DeepEqual(canonical, ids) {
		t.Fatalf("reused provider ID occurrences = %q, available = %q", ids, available)
	}
	if got := sess.Conversation.Messages; len(got) == 0 {
		t.Fatal("missing history")
	}
	store := memstore.New()
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Conversation.Messages, sess.Conversation.Messages) {
		t.Fatal("replay lost or changed occurrence pairing")
	}
}

func TestInvocationIdentityRejectsMalformedTurnBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		restored bool
	}{{"within turn", false}, {"restored outstanding", true}} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession(t, session.Limits{})
			call := session.NewToolCall("reused", "Read", []byte(`{}`))
			if tc.restored {
				if err := sess.BeginTurn(); err != nil {
					t.Fatal(err)
				}
				if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
					t.Fatal(err)
				}
			}
			before := session.CloneMessages(sess.Conversation.Messages)
			calls := []session.ToolCall{call}
			if !tc.restored {
				calls = append(calls, call)
			}
			turns := []mockllm.Turn{mockllm.ToolCallTurn(calls...)}
			executed := 0
			engine := newEngine(agent.Deps{LLM: mockllm.New(turns...), Catalog: catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executed++
				return session.NewToolResult(c.ID, "executed"), nil
			}})})
			run := engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
			cards := 0
			for ev := range run.Events() {
				if ev.Type == session.EvToolCall || ev.Type == session.EvHook || ev.Type == session.EvToolResultAvailable {
					cards++
				}
			}
			if cards != 0 || executed != 0 {
				t.Fatalf("malformed turn: %d cards/hooks, %d executions", cards, executed)
			}
			if len(before) > 0 && !reflect.DeepEqual(sess.Conversation.Messages[:len(before)], before) {
				t.Fatal("modified retained prehistory")
			}
			for _, m := range sess.Conversation.Messages[len(before):] {
				if m.Role == session.RoleAssistant {
					t.Fatal("accepted malformed incoming assistant turn")
				}
			}
		})
	}
}
