package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestCurrentSessionTool(t *testing.T) {
	t.Parallel()

	current := agent.NewCurrentSessionTool()
	spec := current.Spec()
	if spec.Name != agent.CurrentSessionToolName {
		t.Fatalf("name = %q, want %q", spec.Name, agent.CurrentSessionToolName)
	}
	for _, want := range []string{"exact session ID", "correlation", "debug", "opaque", "no authority"} {
		if !strings.Contains(spec.Description, want) {
			t.Errorf("description %q does not contain %q", spec.Description, want)
		}
	}
	if !current.ReadOnly() {
		t.Fatal("CurrentSession must be read-only")
	}

	call := session.NewToolCall("current", agent.CurrentSessionToolName, []byte(`{}`))
	const want session.SessionID = "session/exact:42"
	got, err := current.Execute(port.WithSessionID(context.Background(), want), call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.IsError || got.Content != string(want) {
		t.Fatalf("result = %+v, want exact successful ID %q", got, want)
	}

	for name, ctx := range map[string]context.Context{
		"missing":       context.Background(),
		"empty":         port.WithSessionID(context.Background(), ""),
		"invalid UTF-8": port.WithSessionID(context.Background(), session.SessionID("invalid\xff")),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := current.Execute(ctx, call, tool.Environment{})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !result.IsError || result.Content == "" {
				t.Fatalf("result = %+v, want non-empty model-visible tool error", result)
			}
		})
	}
}

type currentSessionRecorder struct {
	ids     []session.SessionID
	results []session.ToolResult
}

func (r *currentSessionRecorder) ToolCall(id session.SessionID, _ session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	r.ids = append(r.ids, id)
	r.results = append(r.results, result)
}

func TestDelegatedCurrentSessionReportsChildID(t *testing.T) {
	childCat := catalogWith(t, agent.NewCurrentSessionTool())
	recorder := &currentSessionRecorder{}
	child := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(toolCall("current", agent.CurrentSessionToolName, `{}`)),
			mockllm.TextTurn("child done"),
		),
		Catalog: childCat, Policy: allowAll(), Model: "child-model", ToolCallRecorder: recorder,
	})

	parent := newEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(toolCall("delegate", "Subagent", `{"prompt":"identify yourself"}`)),
			mockllm.TextTurn("done"),
		),
		Catalog: catalogWith(t, agent.NewSubagentTool(child)),
	})
	drain(parent.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "delegate"}))

	const want session.SessionID = "subagent-s1-delegate"
	if len(recorder.ids) != 1 || recorder.ids[0] != want {
		t.Fatalf("recorded child session IDs = %v, want [%q]", recorder.ids, want)
	}
	if len(recorder.results) != 1 || recorder.results[0].Content != string(want) {
		t.Fatalf("child CurrentSession results = %+v, want exact child ID %q", recorder.results, want)
	}
	if recorder.results[0].Content == "s1" {
		t.Fatal("child CurrentSession returned the parent session ID")
	}
}

func TestCurrentSessionRunContextReachesToolResult(t *testing.T) {
	const want session.SessionID = "running-session/exact:99"
	cat := catalogWith(t, agent.NewCurrentSessionTool())
	eng := newEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(toolCall("current", agent.CurrentSessionToolName, `{}`)),
			mockllm.TextTurn("done"),
		),
		Catalog: cat,
	})
	sess := session.New(want, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))

	events := drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "identify this session"}))
	for _, event := range events {
		if event.ToolResult != nil && event.ToolResult.CallID == "current" {
			if event.ToolResult.IsError || event.ToolResult.Content != string(want) {
				t.Fatalf("tool result = %+v, want exact running session ID %q", event.ToolResult, want)
			}
			return
		}
	}
	t.Fatalf("CurrentSession result missing from events: %v", typesOf(events))
}
