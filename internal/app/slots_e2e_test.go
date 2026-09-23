package app

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// modelRecorder captures the model id of every request that reached the provider,
// in order. It is the mockllm observer that proves WHICH model a routed call used.
type modelRecorder struct {
	mu     sync.Mutex
	models []string
}

func (r *modelRecorder) observe(req port.LLMRequest) {
	r.mu.Lock()
	r.models = append(r.models, req.Model)
	r.mu.Unlock()
}

func (r *modelRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.models...)
}

func (r *modelRecorder) contains(model string) bool {
	for _, m := range r.snapshot() {
		if m == model {
			return true
		}
	}
	return false
}

// TestCompactionSlotE2ESummaryModel is the G4 offline e2e for the compaction slot:
// it drives the REAL CascadeCompactor that engineDepsForProvider builds for a
// `compaction=cheap` slot, over a mockllm that RECORDS each request's Model, and
// asserts the tier-4 SUMMARY call carried the slot model (mock-cheap-model) — not the
// session model. mockllm DOES record per-request Model (WithRequestObserver), so the
// faithful assertion is available without the fallback.
func TestCompactionSlotE2ESummaryModel(t *testing.T) {
	const (
		sessionModel = "session-model"
		cheapModel   = "mock-cheap-model"
	)
	rec := &modelRecorder{}
	// The summariser turn returns a non-empty summary so tier-4 succeeds.
	summaryLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(rec.observe)},
		mockllm.TextTurn("## Goal\nslot test\n"),
	)
	cfg := Config{
		Model:        sessionModel,
		Compaction:   "cascade",
		ModelSlots:   map[string]string{slotCompaction: slotCheap},
		ModelAliases: map[string]string{slotCheap: cheapModel},
	}
	deps := engineDepsForProvider(cfg, summaryLLM, sessionModel, func() int { return defaultContextWindowTokens }, nil, childPermPolicy(cfg), nil, nil, nil)
	cc, ok := deps.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("Compactor type = %T, want CascadeCompactor", deps.Compactor)
	}
	if cc.Model != cheapModel {
		t.Fatalf("CascadeCompactor.Model = %q, want the slot model %q", cc.Model, cheapModel)
	}

	// Build a conversation large enough to exceed the cascade BudgetTokens
	// (defaultContextWindowTokens × ratio ≈ 76.8k tokens; the heuristic counter is
	// ~len/4) so tiers 1-3 cannot fit and tier-4 (the LLM summary) fires.
	big := strings.Repeat("alpha beta gamma delta ", 20000) // ~460KB ⇒ well over budget
	conv := &session.Conversation{Messages: []session.Message{
		session.NewSystemMessage("system"),
		session.NewUserMessage("the original task"),
		session.NewAssistantMessage(big, "", nil),
		session.NewUserMessage("follow-up one"),
		session.NewAssistantMessage(big, "", nil),
		session.NewUserMessage("follow-up two"),
		session.NewAssistantMessage("recent answer", "", nil),
	}}

	if _, _, _, err := cc.Compact(context.Background(), conv); err != nil {
		t.Fatalf("Compact (tier-4): %v", err)
	}
	if !rec.contains(cheapModel) {
		t.Fatalf("the tier-4 summary call did NOT carry the slot model %q; saw %v", cheapModel, rec.snapshot())
	}
	if rec.contains(sessionModel) {
		t.Fatalf("the summary call must NOT carry the session model %q; saw %v", sessionModel, rec.snapshot())
	}
}

// TestGuardrailSlotE2ECheckerModel is the G4 guardrail analogue: the built checker,
// driven once, sends the slot model to the provider while a no-slot baseline sends
// the configured guardrails model — proving the slot routes ONLY the checker call.
// The #159 sub-case pins that a slot ALONE (no GuardrailsModel) builds a checker that
// runs the slot model (configure = enable, ADR 0046).
func TestGuardrailSlotE2ECheckerModel(t *testing.T) {
	const sessionModel = "session-model"

	// Slot routes the checker to the cheap model.
	slotted := Config{
		Model:           sessionModel,
		UseMock:         true,
		GuardrailsModel: "guard-flag-id",
		ModelSlots:      map[string]string{slotGuardrail: slotCheap},
		ModelAliases:    map[string]string{slotCheap: "mock-cheap-model"},
	}
	if m := checkerModel(t, slotted, sessionModel); m != "mock-cheap-model" {
		t.Fatalf("slotted guardrail checker model = %q, want %q", m, "mock-cheap-model")
	}

	// No slot: the checker uses the configured guardrails model (byte-identical).
	plain := Config{Model: sessionModel, UseMock: true, GuardrailsModel: "guard-flag-id"}
	if m := checkerModel(t, plain, sessionModel); m != "guard-flag-id" {
		t.Fatalf("unslotted guardrail checker model = %q, want %q", m, "guard-flag-id")
	}

	// #159: slot ONLY (no GuardrailsModel) builds a checker that runs the slot model.
	slotOnly := Config{
		Model:        sessionModel,
		UseMock:      true,
		ModelSlots:   map[string]string{slotGuardrail: slotCheap},
		ModelAliases: map[string]string{slotCheap: "mock-cheap-model"},
	}
	if m := checkerModel(t, slotOnly, sessionModel); m != "mock-cheap-model" {
		t.Fatalf("slot-only guardrail checker model = %q, want %q (the slot must enable + route, ADR 0046)", m, "mock-cheap-model")
	}
}
