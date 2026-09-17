package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/noopauthority"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
)

func TestGrantedMissingToolIsPermanentlyUnavailableWithoutDispatch(t *testing.T) {
	const (
		name = "mcp__gone__tool"
		want = "tool is currently unavailable; do not retry unless the catalog changes"
	)
	var request port.LLMRequest
	sentinel := &authorityTool{name: name}
	evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
	eng := newEngine(agent.Deps{
		LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) {
			request = got
		})}, mockllm.ToolCallTurn(toolCall("gone", name, `{}`)), mockllm.TextTurn("done")),
		Catalog:            catalogWith(t),
		AuthorityEvaluator: evaluator,
	})

	events := drain(eng.Run(context.Background(), authoritySession(t, name), agent.MemEnv("/ws"), agent.RunRequest{Text: "call it"}))
	if got := sentinel.ran.Load(); got != 0 {
		t.Fatalf("absent granted tool executions = %d, want 0", got)
	}
	if _, ok := specByName(request.Tools, name); ok {
		t.Fatalf("absent granted tool %q was advertised", name)
	}
	if got := evaluator.calls(); got != 0 {
		t.Fatalf("authority evaluator calls = %d, want 0 because no dispatch gate may run", got)
	}
	for _, event := range events {
		if event.ToolResult != nil && event.ToolResult.CallID == "gone" {
			if !event.ToolResult.IsError || event.ToolResult.Content != want {
				t.Fatalf("missing granted result = {error:%v content:%q}, want exact permanent unavailable error", event.ToolResult.IsError, event.ToolResult.Content)
			}
			return
		}
	}
	t.Fatal("missing unavailable tool result")
}

func TestUngrantedMissingToolRetainsUnknownBehavior(t *testing.T) {
	const name = "mcp__never_granted__tool"
	eng := newEngine(agent.Deps{
		LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("unknown", name, `{}`)), mockllm.TextTurn("done")),
		Catalog:            catalogWith(t),
		AuthorityEvaluator: noopauthority.New(),
	})

	events := drain(eng.Run(context.Background(), authoritySession(t, "Read"), agent.MemEnv("/ws"), agent.RunRequest{Text: "call it"}))
	want := `unknown tool "` + name + `"`
	for _, event := range events {
		if event.ToolResult != nil && event.ToolResult.CallID == "unknown" {
			if !event.ToolResult.IsError || event.ToolResult.Content != want {
				t.Fatalf("ungranted missing result = {error:%v content:%q}, want %q", event.ToolResult.IsError, event.ToolResult.Content, want)
			}
			return
		}
	}
	t.Fatal("missing unknown tool result")
}
