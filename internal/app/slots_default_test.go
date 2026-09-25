package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
)

// checkerModel drives a built guardrail checker once over a mock provider and
// returns the model id that reached the provider — the faithful way to read which
// model a slot routed the checker to (the checker's engine model is private).
func checkerModel(t *testing.T, cfg Config, parentModel string) string {
	t.Helper()
	var got string
	obs := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { got = r.Model })},
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	checker := buildGuardrailsChecker(cfg, nil, obs, "openai", parentModel)
	if checker == nil {
		t.Fatalf("a configured guardrails model must build a checker")
	}
	_, _ = checker.Check(context.Background(), modelhook.CheckRequest{Prompt: "inspect: ok"})
	return got
}

func TestGuardrailCompositionReturnsExactProviderModelIdentity(t *testing.T) {
	usage := session.Usage{InputTokens: 9, OutputTokens: 3}
	provider := mockllm.New(mockllm.ChunksTurn(
		mockllm.TextChunk(`{"safe":true}`),
		mockllm.UsageChunk(usage),
		mockllm.DoneChunk(session.StopEndTurn),
	))
	checker := buildGuardrailsChecker(Config{Model: "parent", UseMock: true, GuardrailsModel: "guard-id"}, nil, provider, "openai", "parent")
	if checker == nil {
		t.Fatal("configured guardrail checker was not built")
	}
	result, err := checker.Check(t.Context(), modelhook.CheckRequest{Prompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	bucket := result.Usage.Buckets[session.UsageKindGuardrail]
	if bucket.Total != usage || bucket.Models["openai/guard-id"] != usage {
		t.Fatalf("guardrail composition attribution = %#v, want exact openai/guard-id=%+v", bucket, usage)
	}
}

// TestSlotsByteIdenticalDefault is the G1 pin (ADR 0030): with NO slot configured
// (cfg.ModelSlots == nil), every routed call site keeps its EXACT pre-feature
// behaviour — the session model — and resolveSlotModel returns ("", false) for every
// slot. This is the byte-identical guarantee the feature commits to.
func TestSlotsByteIdenticalDefault(t *testing.T) {
	const sessionModel = "gpt-4o"

	// resolveSlotModel: every slot (call-slots AND tiers) returns ("", false) when
	// nothing is configured. A non-empty parentModel must NEVER leak out as the model.
	for _, slot := range []string{slotCompaction, slotAskReviewer, slotGuardrail, slotSynthesis, slotCheap, slotFast, slotReasoning, "totally-made-up"} {
		if model, ok := resolveSlotModel(Config{Model: sessionModel}, slot, "parent-model"); ok || model != "" {
			t.Fatalf("resolveSlotModel(%q) with no slots = (%q, %v), want (\"\", false)", slot, model, ok)
		}
	}

	// E1 Compaction: the CascadeCompactor's Model + the engine's Model both stay on
	// the session model when no compaction slot is set.
	cfg := Config{Model: sessionModel, Compaction: "cascade"}
	provider, store, policy, hooks, mcpP, instr := depsTestFixture(t)
	deps := engineDepsForProvider(cfg, provider, testProviderModel(sessionModel), func() int { return defaultContextWindowTokens }, store, policy, hooks, mcpP, instr)
	if deps.Model != sessionModel {
		t.Fatalf("deps.Model = %q, want the session model %q", deps.Model, sessionModel)
	}
	cc, ok := deps.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("Compactor type = %T, want CascadeCompactor", deps.Compactor)
	}
	if cc.Model != sessionModel {
		t.Fatalf("CascadeCompactor.Model = %q, want the session model %q (no slot ⇒ byte-identical)", cc.Model, sessionModel)
	}

	// E2 Ask-reviewer: with no slot, the reviewer model is exactly today's
	// lookupModelAlias(cfg, SubagentAskReviewerModel).
	revCfg := Config{
		Model:                    sessionModel,
		SubagentAskReviewerModel: "cheap",
		ModelAliases:             map[string]string{"cheap": "cheap-id"},
	}
	wantReviewer, _ := lookupModelAlias(revCfg, revCfg.SubagentAskReviewerModel)
	revDeps, on := askAdjudicatorDeps(revCfg, nil, provider, "openai", sessionModel)
	if !on {
		t.Fatalf("a configured reviewer must yield deps")
	}
	if revDeps.Model != wantReviewer {
		t.Fatalf("reviewer Model = %q, want today's resolved %q (no slot ⇒ byte-identical)", revDeps.Model, wantReviewer)
	}

	// E3 Guardrail: with no slot, the checker model is today's resolved value
	// (UseMock passes the literal through). Driving the checker over a mock observer
	// shows the model that actually reached the provider.
	gCfg := Config{Model: sessionModel, UseMock: true, GuardrailsModel: "guard-id"}
	if m := checkerModel(t, gCfg, sessionModel); m != "guard-id" {
		t.Fatalf("guardrail checker model = %q, want today's resolved %q (no slot ⇒ byte-identical)", m, "guard-id")
	}
}
