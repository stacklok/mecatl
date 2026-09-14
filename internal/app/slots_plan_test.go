package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestPlanSlotDefaultTierIsReasoning pins the one DELIBERATE divergence in
// slotDefaultTier (ADR 0030 Layer 3): the `plan` slot falls through to the `reasoning`
// tier, NOT `cheap` like the three internal-call slots — a plan-mode model is a
// strong-reasoning model. A regression that points plan at cheap would silently demote
// planning turns to the cheapest model.
func TestPlanSlotDefaultTierIsReasoning(t *testing.T) {
	if got := slotDefaultTier[slotPlan]; got != slotReasoning {
		t.Fatalf("slotDefaultTier[plan] = %q, want %q (a plan model is a strong-reasoning model, NOT cheap)", got, slotReasoning)
	}
	// And the three internal-call slots still default to cheap (no accidental flip).
	for _, s := range []string{slotCompaction, slotAskReviewer, slotGuardrail} {
		if got := slotDefaultTier[s]; got != slotCheap {
			t.Fatalf("slotDefaultTier[%s] = %q, want %q (internal-call slots stay cheap)", s, got, slotCheap)
		}
	}
}

// TestPlanSlotResolves pins the resolution paths for the `plan` slot (ADR 0030 Layer 3):
// an explicit binding, the reasoning-tier default fallthrough, and an alias. It reuses
// resolveSlotModel UNCHANGED — the grammar is identical to the call-slots.
func TestPlanSlotResolves(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		wantModel string
		wantOK    bool
	}{
		{
			name:      "explicit plan binding (literal id)",
			cfg:       Config{ModelSlots: map[string]string{slotPlan: "opus-id"}},
			wantModel: "opus-id", wantOK: true,
		},
		{
			name:      "plan via the reasoning tier default fallthrough",
			cfg:       Config{ModelSlots: map[string]string{slotReasoning: "reason-id"}},
			wantModel: "reason-id", wantOK: true,
		},
		{
			name:      "plan via an alias",
			cfg:       Config{ModelSlots: map[string]string{slotPlan: "reasoning"}, ModelAliases: map[string]string{"reasoning": "reason-id"}},
			wantModel: "reason-id", wantOK: true,
		},
		{
			name:   "no plan slot, no reasoning tier ⇒ unset (byte-identical default)",
			cfg:    Config{ModelSlots: map[string]string{slotCheap: "cheap-id"}}, // cheap is unrelated to plan
			wantOK: false,
		},
		{
			name:   "no slots at all ⇒ unset",
			cfg:    Config{},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diag := &capturingDiag{}
			cfg := tc.cfg
			cfg.Diagnostics = diag
			model, ok := resolveSlotModel(cfg, slotPlan, "session-model")
			if ok != tc.wantOK || model != tc.wantModel {
				t.Fatalf("resolveSlotModel(plan) = (%q, %v), want (%q, %v)", model, ok, tc.wantModel, tc.wantOK)
			}
			// resolveSlotModel is SILENT (the no-per-engine-duplication trap) — the plan
			// slot is no exception.
			if len(diag.lines) != 0 {
				t.Fatalf("resolveSlotModel(plan) must not log; got %v", diag.lines)
			}
		})
	}
}

// TestPlanSlotByteIdenticalWhenUnconfigured is the regression guard: with NO plan slot
// configured, resolveSlotModel(plan) returns ("", false) AND logSlotConfigFacts emits
// NOTHING — byte-identical to pre-Phase-3. A mode flip changes no model.
func TestPlanSlotByteIdenticalWhenUnconfigured(t *testing.T) {
	cfg := Config{Model: "session-model"} // no ModelSlots at all
	if model, ok := resolveSlotModel(cfg, slotPlan, cfg.Model); ok || model != "" {
		t.Fatalf("unconfigured plan slot resolveSlotModel = (%q, %v), want (\"\", false)", model, ok)
	}
	// modeNeedsEngine returns nil (never promote) when no plan slot is active.
	if fn := modeNeedsEngine(cfg); fn != nil {
		t.Fatalf("modeNeedsEngine must be nil with no plan slot (byte-identical default), got non-nil")
	}
	// logSlotConfigFacts logs nothing when ModelSlots is empty.
	diag := &capturingDiag{}
	cfg.Diagnostics = diag
	logSlotConfigFacts(cfg)
	if len(diag.lines) != 0 {
		t.Fatalf("logSlotConfigFacts must be silent with no slots; got %v", diag.lines)
	}
}

// TestModeNeedsEngine pins the composition predicate wired into
// server.Config.ModeNeedsEngine (ADR 0030 Layer 3): true ONLY for ModePlan when the
// plan slot resolves to a model DIFFERING from the shared engine model; nil otherwise.
func TestModeNeedsEngine(t *testing.T) {
	t.Run("active plan slot ⇒ true only for plan", func(t *testing.T) {
		cfg := Config{Model: "session-model", ModelSlots: map[string]string{slotPlan: "plan-id"}}
		fn := modeNeedsEngine(cfg)
		if fn == nil {
			t.Fatal("modeNeedsEngine must be non-nil with an active plan slot")
		}
		if !fn(session.ModePlan) {
			t.Fatal("ModePlan must need an engine (the plan model differs)")
		}
		if fn(session.ModeDefault) || fn(session.ModeAccept) {
			t.Fatal("only ModePlan needs an engine; default/acceptEdits keep the session model")
		}
	})
	t.Run("plan slot resolves to the shared-engine model ⇒ nil (no change)", func(t *testing.T) {
		cfg := Config{Model: "same-id", ModelSlots: map[string]string{slotPlan: "same-id"}}
		if fn := modeNeedsEngine(cfg); fn != nil {
			t.Fatal("a plan slot resolving to cfg.Model changes nothing — modeNeedsEngine must be nil")
		}
	})
}

// planFactory builds a real sessionEngineFactory over a single mock provider with a
// `plan` slot bound to planModel and a session default of cfg.Model.
func planFactory(t *testing.T, sessionModel, planModel string) server.SessionEngineFactory {
	t.Helper()
	cfg := Config{
		Model:      sessionModel,
		ModelSlots: map[string]string{slotPlan: planModel},
	}
	provider := mockllm.New(mockllm.TextTurn("X"))
	reg := regForTest(provider, providerOpenAI, sessionModel)
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	return sessionEngineFactory(cfg, reg, provider, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
}

// TestSessionEngineFactoryPlanVsExecute is the FACTORY-level Phase 3 guard (ADR 0030
// Layer 3): the SAME factory, the SAME zero selector, called with mode=ModePlan vs
// mode=ModeDefault, resolves the engine to the PLAN model vs the SESSION model — and
// stamps BuiltForMode from the one source. The provider is unchanged (fixed per
// session); only the model differs.
func TestSessionEngineFactoryPlanVsExecute(t *testing.T) {
	const sessionModel, planModel = "gpt-5", "opus-plan-id"
	factory := planFactory(t, sessionModel, planModel)
	ctx := context.Background()

	plan, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModePlan)
	if err != nil {
		t.Fatalf("factory(plan): %v", err)
	}
	defer func() { _ = plan.Close() }()
	exec, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(default): %v", err)
	}
	defer func() { _ = exec.Close() }()

	if plan.ModelID != planModel {
		t.Fatalf("plan-mode engine ModelID = %q, want the plan slot model %q", plan.ModelID, planModel)
	}
	if exec.ModelID != sessionModel {
		t.Fatalf("default-mode engine ModelID = %q, want the session model %q (no plan re-resolution)", exec.ModelID, sessionModel)
	}
	if plan.BuiltForMode != session.ModePlan {
		t.Fatalf("plan-mode BuiltForMode = %q, want %q", plan.BuiltForMode, session.ModePlan)
	}
	if exec.BuiltForMode != session.ModeDefault {
		t.Fatalf("default-mode BuiltForMode = %q, want %q", exec.BuiltForMode, session.ModeDefault)
	}
	// Provider is FIXED per session — the plan re-resolution swaps the MODEL only.
	if plan.ProviderID != exec.ProviderID {
		t.Fatalf("plan vs default ProviderID diverged (%q vs %q) — the plan slot must NOT switch provider", plan.ProviderID, exec.ProviderID)
	}
}

// TestSessionEngineFactoryNoPlanSlotByteIdentical pins the factory byte-identical
// guarantee: with NO plan slot configured, calling the factory with ModePlan yields the
// SAME model as ModeDefault (the session model) — a mode flip changes nothing.
func TestSessionEngineFactoryNoPlanSlotByteIdentical(t *testing.T) {
	const sessionModel = "gpt-5"
	cfg := Config{Model: sessionModel} // no plan slot
	provider := mockllm.New(mockllm.TextTurn("X"))
	reg := regForTest(provider, providerOpenAI, sessionModel)
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	ctx := context.Background()

	plan, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModePlan)
	if err != nil {
		t.Fatalf("factory(plan): %v", err)
	}
	defer func() { _ = plan.Close() }()
	if plan.ModelID != sessionModel {
		t.Fatalf("plan-mode engine ModelID = %q with NO plan slot, want the unchanged session model %q (byte-identical)", plan.ModelID, sessionModel)
	}
}

// TestApplyPlanModePostureAppendsNote proves the helper appends the plan-approval
// workflow contract (the PresentPlan gate + inline-is-NOT-approval) to the Role
// and falls back to DefaultRole when Role is empty — the applyNoFSPosture idiom.
func TestApplyPlanModePostureAppendsNote(t *testing.T) {
	pc := applyPlanModePosture(prompt.Config{}, session.ModePlan)
	if pc.Role == "" {
		t.Fatal("applyPlanModePosture on an empty Config must fall back to DefaultRole")
	}
	for _, clause := range []string{
		"PLAN MODE",
		"call the PresentPlan tool EXACTLY ONCE PER CURRENT PRESENTATION",
		"and STOP",
		"denied for iteration",
		"pending run is cancelled",
		"wait for new user input",
		"revised or unchanged plan",
		"NEW PresentPlan call",
		"Later chat assent requests a fresh gated review and is never execution approval. Only the harness proceed message that follows approval through the current PresentPlan gate starts execution.",
		"Pass the FULL plan text in the PresentPlan `plan` argument",
	} {
		if !strings.Contains(pc.Role, clause) {
			t.Errorf("Role missing plan-approval contract clause %q\ngot=%q", clause, pc.Role)
		}
	}

	// An explicit Role is preserved; the note is appended.
	custom := applyPlanModePosture(prompt.Config{Role: "custom-role"}, session.ModePlan)
	if !strings.HasPrefix(custom.Role, "custom-role") {
		t.Fatalf("explicit Role was not preserved; got %q", custom.Role)
	}
	if !strings.Contains(custom.Role, "PresentPlan") {
		t.Fatal("plan note was not appended to the explicit Role")
	}

	// Non-plan mode must be a no-op: the Config is returned unchanged.
	nonPlan := applyPlanModePosture(prompt.Config{Role: "just-role"}, session.ModeDefault)
	if nonPlan.Role != "just-role" {
		t.Fatalf("non-plan applyPlanModePosture must be a no-op; got Role=%q", nonPlan.Role)
	}
}

// TestPlanModeEngineSystemPromptContainsPlanApprovalContract is the
// model-visible-discoverability gate for the plan-approval affordance (ADR 0070): a
// gate whose correct operation depends on the model CALLING PresentPlan MUST ship with
// a model-visible prompt instruction telling the model so, AND a test proving that
// instruction lands in the built engine's system prompt via the REAL factory path — so
// deleting the `applyPlanModePosture` wiring in `sessionEngineFactory` fails CI.
//
// It drives a one-turn run through the factory-built engine and captures the LLM
// request. It asserts the plan-approval contract lands on the StablePrefix (the Role
// layer `applyPlanModePosture` owns), NOT merely on the combined Render() — because the
// same contract is ALSO carried in `engine/prompt/builder.go`'s volatile suffix, so a
// Render() oracle would stay green if the factory wiring were silently dropped (proven by
// mutation: commenting out `deps.PromptConfig = applyPlanModePosture(...)` leaves the
// volatile-suffix clauses intact, so only the StablePrefix-owned "PLAN MODE" sentinel
// distinguishes the two layers; asserting the full clause set against StablePrefix makes
// the oracle robust to a future "harmonization" of that sentinel).
func TestPlanModeEngineSystemPromptContainsPlanApprovalContract(t *testing.T) {
	const sessionModel = "gpt-5"
	cfg := Config{Model: sessionModel}
	var captured prompt.Layered
	var invoked bool
	observer := func(req port.LLMRequest) {
		captured = req.System
		invoked = true
	}
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(observer),
	}, mockllm.TextTurn("ok"))
	reg := regForTest(provider, providerOpenAI, sessionModel)
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
	ctx := context.Background()

	plan, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModePlan)
	if err != nil {
		t.Fatalf("factory(plan): %v", err)
	}
	defer func() { _ = plan.Close() }()

	// Drive a one-turn run to trigger buildRequest → prompt.Build → captured system.
	sess := session.New("s1", session.ModePlan, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	run := plan.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "plan a task", Parts: nil})
	for range run.Events() {
	}
	if !invoked {
		t.Fatal("the LLM was not invoked; the mock script may be insufficient")
	}
	// The load-bearing clauses live on the StablePrefix — the Role layer
	// `applyPlanModePosture` appends to in sessionEngineFactory. Asserting here (not
	// against Render()) is what makes removing the factory wiring fail this test: the
	// volatile suffix in engine/prompt/builder.go carries the same prose, so a
	// combined Render() oracle is vacuous against the wiring removal.
	for _, clause := range []string{
		"PLAN MODE",
		"call the PresentPlan tool EXACTLY ONCE PER CURRENT PRESENTATION",
		"and STOP",
		"denied for iteration",
		"pending run is cancelled",
		"wait for new user input",
		"revised or unchanged plan",
		"NEW PresentPlan call",
		"Later chat assent requests a fresh gated review and is never execution approval. Only the harness proceed message that follows approval through the current PresentPlan gate starts execution.",
		"Pass the FULL plan text in the PresentPlan `plan` argument",
	} {
		if !strings.Contains(captured.StablePrefix, clause) {
			t.Errorf("plan-mode StablePrefix missing clause %q\ngot StablePrefix (first 500):\n%s",
				clause, firstN(captured.StablePrefix, 500))
		}
	}
}

// TestDefaultModeEngineSystemPromptLacksPlanApprovalContract proves the factory-built
// DEFAULT-mode engine's system prompt does NOT contain the plan-approval workflow
// contract — the note is plan-mode only.
func TestDefaultModeEngineSystemPromptLacksPlanApprovalContract(t *testing.T) {
	const sessionModel = "gpt-5"
	cfg := Config{Model: sessionModel}
	var capturedSystem string
	observer := func(req port.LLMRequest) {
		capturedSystem = req.System.Render()
	}
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(observer),
	}, mockllm.TextTurn("ok"))
	reg := regForTest(provider, providerOpenAI, sessionModel)
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
	ctx := context.Background()

	defEng, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(default): %v", err)
	}
	defer func() { _ = defEng.Close() }()

	sess := session.New("s2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	run := defEng.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "do something", Parts: nil})
	for range run.Events() {
	}
	if capturedSystem == "" {
		t.Fatal("the LLM was not invoked")
	}
	// The factory-applied plan note must NOT be present — default mode is not plan.
	if strings.Contains(capturedSystem, "call the PresentPlan tool EXACTLY ONCE") {
		t.Error("default-mode system prompt must NOT contain the plan-approval contract\n" +
			"got system prompt (first 500):\n" + firstN(capturedSystem, 500))
	}
	// The volatile plan reminder (from prompt.Build) must also NOT be present.
	if strings.Contains(capturedSystem, "Plan mode is active") {
		t.Error("default-mode system prompt must NOT contain the volatile plan reminder")
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
