package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/noopauthority"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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

func TestResumedApprovalOfGrantedMissingToolIsPermanentlyUnavailable(t *testing.T) {
	const (
		name = "mcp__gone__tool"
		want = "tool is currently unavailable; do not retry unless the catalog changes"
	)
	policy := permpolicy.NewPolicy(nil, permstore.New()) // mutating tools ask by default
	sess := authoritySession(t, name)

	present := &fakeTool{name: name, readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ran"), nil
		}}
	e1 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("g1", name, `{}`))),
		Catalog: catalogWith(t, present),
		Policy:  policy,
	})
	askID, restored := driveToAwaiting(t, e1, sess, agent.MemEnv("/ws"), "call it")

	e2 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done")),
		Catalog: catalogWith(t),
		Policy:  policy,
	})
	evs := resumeEvents(e2.ResumeApproval(context.Background(), restored, agent.MemEnv("/ws"), askID, session.VerdictAllowOnce))

	if nonError, total := resultsFor(evs, "g1"); nonError != 0 || total != 1 {
		t.Fatalf("results for g1: non-error=%d total=%d, want exactly one error result", nonError, total)
	}
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "g1" {
			if ev.ToolResult.Content != want {
				t.Fatalf("resumed result = %q, want %q", ev.ToolResult.Content, want)
			}
			return
		}
	}
	t.Fatal("no tool result for g1")
}
