package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0363_ContextualGuardrails_Scenario1_ApprovalOriginReplay(t *testing.T) {
	log := memstore.NewEventLog()
	spy := &spyPolicy{}
	replay := replayApprovals(log, spy, port.NopDiagnostics{})
	sess := session.New("origin-replay", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
	call := session.NewToolCall("c1", "Write", json.RawMessage(`{"path":"x","content":"y"}`))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []session.ApprovalOrigin{session.ApprovalOriginUnknown, "future", session.ApprovalOriginHookGuardrail, session.ApprovalOriginPlan, session.ApprovalOriginPermission} {
		ev := session.Event{Type: session.EvApproval, Approval: &session.ApprovalPayload{AskID: "a", Verdict: session.VerdictStringAllowAlways, Tool: "Write", Call: call.ID, AllowAlways: true, Origin: origin}}
		if err := log.Append(context.Background(), sess.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	replay(context.Background(), sess)
	if len(spy.learned) != 1 || spy.learned[0].ID != call.ID {
		t.Fatalf("learned = %+v, want only explicit permission-origin call", spy.learned)
	}
}

func TestGuardrailsDisabledSuppressesCheckerBuildersWithConfiguredSlot(t *testing.T) {
	cfg := Config{
		UseMock:            true,
		GuardrailsDisabled: true,
		GuardrailsEscape:   true,
		GuardrailsModel:    "other-checker",
		ModelSlots:         map[string]string{slotGuardrail: "checker"},
		ModelAliases:       map[string]string{"checker": "checker-model"},
	}
	provider := mockllm.New()
	reg := &providerRegistry{defaultID: "mock", entries: map[string]providerEntry{"mock": {provider: provider}}}
	if _, _, _, configured, err := resolveGuardrailBinding(cfg, reg); err != nil || configured {
		t.Fatalf("disabled guardrail binding: configured=%v, err=%v", configured, err)
	}

	if reviewer := buildGuardrailsActionReviewer(cfg, reg, provider, "mock", nil); reviewer != nil {
		t.Fatalf("ToolReviewer = %T, want nil when guardrails are disabled", reviewer)
	}
	if checker := buildGuardrailsEscapeChecker(cfg, reg, provider); checker != nil {
		t.Fatalf("escape checker = %T, want nil when guardrails are disabled", checker)
	}
	if coverage := guardrailCoverageFor(cfg, nil); coverage.Enabled {
		t.Fatalf("guardrail coverage = %+v, want disabled", coverage)
	}
	diag := &capturingDiag{}
	cfg.Diagnostics = diag
	logGuardrailsPosture(cfg)
	if !diag.has("guardrails: OFF") || !diag.has("kill-switch") {
		t.Fatalf("posture lines = %v, want kill-switch OFF", diag.lines)
	}
}

func TestADR_0363_ContextualGuardrails_Scenario4_ExactRoute(t *testing.T) {
	reg := &providerRegistry{defaultID: "default", entries: map[string]providerEntry{"default": {}, "review": {}}}
	cases := []struct {
		name            string
		cfg             Config
		provider, model string
		src             guardrailSource
	}{
		{"scalar slot uses captured default", Config{ModelSlots: map[string]string{slotGuardrail: "guard"}, ModelAliases: map[string]string{"guard": "guard-model"}}, "default", "guard-model", srcSlot},
		{"cheap fallback uses captured default", Config{ModelSlots: map[string]string{slotCheap: "cheap"}, ModelAliases: map[string]string{"cheap": "cheap-model"}}, "default", "cheap-model", srcSlot},
		{"explicit route", Config{GuardrailSlot: &ModelTargetSelector{ProviderID: "review", Model: "guard"}, ModelAliases: map[string]string{"guard": "review-model"}}, "review", "review-model", srcSlot},
		{"slot supersedes gate", Config{ModelSlots: map[string]string{slotGuardrail: "guard"}, ModelAliases: map[string]string{"guard": "guard-model"}, GuardrailsModel: "legacy-model"}, "default", "guard-model", srcSlotSupersedingGate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, model, src, configured, err := resolveGuardrailBinding(tc.cfg, reg)
			if err != nil {
				t.Fatal(err)
			}
			if !configured || provider != tc.provider || model != tc.model || src != tc.src {
				t.Fatalf("binding=(%q,%q,%v,%v), want (%q,%q,%v,true)", provider, model, src, configured, tc.provider, tc.model, tc.src)
			}
		})
	}
	if _, _, _, _, err := resolveGuardrailBinding(Config{GuardrailSlot: &ModelTargetSelector{ProviderID: "missing", Model: "guard"}, ModelAliases: map[string]string{"guard": "review-model"}}, reg); err == nil {
		t.Fatal("unknown explicit provider must fail startup")
	}
}
