package agent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestSerialToolResultAvailabilitySmoke(t *testing.T) {
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "raw result"), nil
		}}
	hooks := &mutatingHooks{mutate: map[governance.HookPhase]json.RawMessage{
		governance.PhasePostToolUse: json.RawMessage(`{"content":"safe result","is_error":true}`),
	}}
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Write", `{}`)), mockllm.TextTurn("done"))
	sess := newSession(t, session.Limits{})
	recorder := &recordingLogger{}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, write), Hooks: hooks, ToolCallRecorder: recorder})
	events := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	var availability, canonical *session.Event
	callIndex, hookIndex, availableIndex, resultIndex := -1, -1, -1, -1
	for i := range events {
		ev := &events[i]
		switch ev.Type {
		case session.EvToolCall:
			callIndex = i
		case session.EvHook:
			if ev.Hook != nil && ev.Hook.Phase == string(governance.PhasePostToolUse) {
				hookIndex = i
			}
		case session.EvToolResultAvailable:
			if availability != nil {
				t.Fatal("serial call emitted availability more than once")
			}
			availability, availableIndex = ev, i
		case session.EvToolResult:
			canonical, resultIndex = ev, i
		}
	}
	if callIndex < 0 || hookIndex <= callIndex || availableIndex <= hookIndex || resultIndex <= availableIndex {
		t.Fatalf("tool call / post hook / availability / canonical order = %d / %d / %d / %d", callIndex, hookIndex, availableIndex, resultIndex)
	}
	if availability.ToolResult == nil || canonical.ToolResult == nil || !reflect.DeepEqual(availability.ToolResult, canonical.ToolResult) || availability.ToolResult.Content != "safe result" || !availability.ToolResult.IsError {
		t.Fatalf("available result = %+v, canonical result = %+v", availability.ToolResult, canonical.ToolResult)
	}
	if recorded := recordedToolResult(sess); recorded == nil || !reflect.DeepEqual(recorded, canonical.ToolResult) {
		t.Fatalf("model result = %+v, canonical = %+v", recorded, canonical.ToolResult)
	}
	if recorder.calls != 1 {
		t.Fatalf("recorder calls = %d, want 1", recorder.calls)
	}
}
