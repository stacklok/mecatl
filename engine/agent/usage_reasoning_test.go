package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestReasoningTokensPropagateAndDoNotInflateBudget is the gauntlet e2e for the
// ReasoningTokens subset invariant: a provider that emits
// session.Usage{InputTokens:60, OutputTokens:40, ReasoningTokens:30} must (1)
// propagate ReasoningTokens==30 to the terminal result's Usage, and (2) NOT
// inflate the budget brake — TotalTokens() stays input+output (100), so a
// runaway provider emitting that per-turn usage trips StopBudget at the SAME
// threshold as before (reasoning is a subset of OutputTokens, already counted).
//
// It reuses the runawayProvider/budget_test.go shape: the provider never stops
// on its own, so the budget is the only brake. If reasoning were added to
// TotalTokens(), the per-turn total would be 130 instead of 100 and the budget
// would trip earlier — the threshold assertion catches that double-count.
func TestReasoningTokensPropagateAndDoNotInflateBudget(t *testing.T) {
	const budget = 350 // crossed after 4 turns of input+output==100 (4*100=400 >= 350)
	perTurn := session.Usage{InputTokens: 60, OutputTokens: 40, ReasoningTokens: 30}

	// (1) Propagation: a single turn carrying reasoning surfaces it on the result.
	propLLM := mockllm.NewWith(nil,
		mockllm.ChunksTurn(
			mockllm.TextChunk("done"),
			mockllm.UsageChunk(perTurn),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	propEngine := newEngine(agent.Deps{LLM: propLLM, Catalog: catalogWith(t, loopTool()), MaxRunTokens: 0})
	propSess := newSession(t, session.Limits{})
	propEvs := drain(propEngine.Run(context.Background(), propSess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	propRes := lastResult(t, propEvs)
	if propRes.Usage.ReasoningTokens != 30 {
		t.Errorf("propagation: ResultPayload.Usage.ReasoningTokens = %d, want 30", propRes.Usage.ReasoningTokens)
	}
	if propRes.Usage.OutputTokens != 40 {
		t.Errorf("propagation: OutputTokens = %d, want 40 (unchanged)", propRes.Usage.OutputTokens)
	}
	// Subset guard on the propagated value.
	if propRes.Usage.ReasoningTokens > propRes.Usage.OutputTokens {
		t.Errorf("propagation: ReasoningTokens %d > OutputTokens %d — must be a subset", propRes.Usage.ReasoningTokens, propRes.Usage.OutputTokens)
	}

	// (2) No budget inflation: a runaway provider emitting the reasoning-bearing
	// per-turn usage trips StopBudget on input+output only. If ReasoningTokens
	// were added to TotalTokens(), the per-turn total would be 130 and the budget
	// would trip after 3 turns (3*130=390 >= 350) instead of 4 (4*100=400 >= 350).
	// The runawayProvider emits a tool call every turn so the loop keeps going;
	// only the budget can stop it.
	runawayLLM := &runawayProvider{perTurn: perTurn}
	runawayEngine := newEngine(agent.Deps{LLM: runawayLLM, Catalog: catalogWith(t, loopTool()), MaxRunTokens: budget})
	runawaySess := newSession(t, session.Limits{})
	runawayEvs := drain(runawayEngine.Run(context.Background(), runawaySess, agent.MemEnv("/ws"), agent.RunRequest{Text: "run forever"}))
	runawayRes := lastResult(t, runawayEvs)
	if runawayRes.Stop != session.StopBudget {
		t.Fatalf("terminal stop = %q, want %q (reasoning must not inflate the budget — StopBudget fires on input+output only)",
			runawayRes.Stop, session.StopBudget)
	}
	// The cumulative TotalTokens() must reflect input+output only (NOT +reasoning):
	// the runaway ran >= 4 turns of input+output==100 each before tripping at >=350.
	// If reasoning were added, the total would be 130/turn and the run would have
	// stopped at 3 turns (total 390) — the calls count catches that.
	if got := runawayLLM.calls.Load(); got < 4 {
		t.Fatalf("runaway made %d model calls, want >= 4 (reasoning must NOT inflate TotalTokens — if it did, the budget would trip after 3 turns at 130/turn)", got)
	}
	if got := runawayRes.Usage.TotalTokens(); got < budget {
		t.Fatalf("cumulative TotalTokens = %d, want >= budget %d", got, budget)
	}
	// And the reasoning subset invariant holds on the terminal aggregate too.
	if runawayRes.Usage.ReasoningTokens > runawayRes.Usage.OutputTokens {
		t.Errorf("terminal ReasoningTokens %d > OutputTokens %d — must be a subset", runawayRes.Usage.ReasoningTokens, runawayRes.Usage.OutputTokens)
	}
}
