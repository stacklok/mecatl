package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type taskWindowReviewer struct{ requests []agent.ToolReviewRequest }

func (r *taskWindowReviewer) Review(_ context.Context, req agent.ToolReviewRequest, _ agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
	r.requests = append(r.requests, req)
	return agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, nil
}

func TestChildPromptIsPersistedAndEmittedNonPrincipal(t *testing.T) {
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Role: "subagent"})
	sess := newSession(t, session.Limits{})
	run := engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "worker goal"})
	var provenance session.UserPromptProvenance
	for ev := range run.Events() {
		if ev.Type == session.EvUserPrompt && ev.UserPrompt != nil && ev.UserPrompt.Text == "worker goal" {
			provenance = ev.UserPrompt.Provenance
		}
	}
	if got := sess.Conversation.Messages[0].UserPromptProvenance; got != session.UserPromptProvenanceUnknown {
		t.Fatalf("child persisted provenance = %q, want unknown", got)
	}
	if provenance != session.UserPromptProvenanceUnknown {
		t.Fatalf("child event provenance = %q, want unknown", provenance)
	}
}

func TestRootPromptIsPersistedAndEmittedPrincipal(t *testing.T) {
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()})
	sess := newSession(t, session.Limits{})
	run := engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "root task"})
	var provenance session.UserPromptProvenance
	for ev := range run.Events() {
		if ev.Type == session.EvUserPrompt && ev.UserPrompt != nil && ev.UserPrompt.Text == "root task" {
			provenance = ev.UserPrompt.Provenance
		}
	}
	if got := sess.Conversation.Messages[0].UserPromptProvenance; got != session.UserPromptProvenancePrincipal {
		t.Fatalf("root persisted provenance = %q, want principal", got)
	}
	if provenance != session.UserPromptProvenancePrincipal {
		t.Fatalf("root event provenance = %q, want principal", provenance)
	}
}

func TestTaskWindowFlowsThroughActualEngineAcrossRuns(t *testing.T) {
	reviewer := &taskWindowReviewer{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(&fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(call.ID, "ok"), nil
	}})
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("one", "Act", json.RawMessage(`{}`))), mockllm.TextTurn("done one"),
			mockllm.ToolCallTurn(session.NewToolCall("two", "Act", json.RawMessage(`{}`))), mockllm.TextTurn("done two"),
		),
		Catalog:          catalog,
		Policy:           permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		ToolReviewer:     reviewer,
		ReviewTaskWindow: 2,
	})
	sess := newSession(t, session.Limits{})
	env := agent.MemEnv("/ws")
	drain(engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "first authenticated task"}))
	if err := sess.Reopen(); err != nil {
		t.Fatal(err)
	}
	drain(engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "second authenticated task"}))

	if len(reviewer.requests) != 2 {
		t.Fatalf("review requests = %d, want 2", len(reviewer.requests))
	}
	want := [][]string{{"first authenticated task"}, {"first authenticated task", "second authenticated task"}}
	for i, req := range reviewer.requests {
		var tasks []string
		for _, fact := range req.PrincipalFacts {
			if fact.Kind == "genuine_user_task" {
				tasks = append(tasks, fact.Statement)
			}
		}
		if len(tasks) != len(want[i]) {
			t.Fatalf("request %d tasks = %v, want %v", i, tasks, want[i])
		}
		for j := range tasks {
			if tasks[j] != want[i][j] {
				t.Fatalf("request %d tasks = %v, want %v", i, tasks, want[i])
			}
		}
	}
}
