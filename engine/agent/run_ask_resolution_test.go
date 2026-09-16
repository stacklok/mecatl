package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestResolveOrdinaryAsk_LivePlanAskStaysPending proves the live ask spine
// carries plan provenance into the atomic resolver. The ordinary control may
// classify the plan ask repeatedly, after which the compatibility Approve path
// still resolves that exact ask.
func TestResolveOrdinaryAsk_LivePlanAskStaysPending(t *testing.T) {
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"step 1"}`)))
	engine := newEngine(agent.Deps{LLM: llm, Catalog: planCatalog(t), Interactive: true})
	run := engine.Run(context.Background(), newPlanSession(t), agent.MemEnv("/ws"), agent.RunRequest{Text: "plan a thing"})

	var (
		events   []session.Event
		outcomes []agent.AskResolution
		sawAsk   bool
	)
	for event := range run.Events() {
		events = append(events, event)
		if event.Type != session.EvPermissionAsk || event.Ask == nil {
			continue
		}
		sawAsk = true
		for i := 0; i < 2; i++ {
			outcomes = append(outcomes, run.ResolveOrdinaryAsk(event.Ask.AskID, session.VerdictAllowOnce))
		}
		run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
	}

	if !sawAsk {
		t.Fatal("live plan run emitted no permission ask")
	}
	for i, got := range outcomes {
		if got != agent.AskResolutionPlanOriginated {
			t.Fatalf("ordinary attempt %d outcome = %v, want %v", i+1, got, agent.AskResolutionPlanOriginated)
		}
	}
	if got := lastResult(t, events).Stop; got != session.StopPlanApproved {
		t.Fatalf("stop = %q, want %q", got, session.StopPlanApproved)
	}
}
