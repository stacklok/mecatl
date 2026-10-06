package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// TestResolveSlotModelTable pins resolveSlotModel's precedence and fail-soft posture
// (ADR 0030): literal id / operator alias / built-in alias / default-tier fallthrough
// / unset / inherit-or-unknown. resolveSlotModel is SILENT (the misconfig WARN lives in
// the build-once logSlotConfigFacts, not here): this test asserts it NEVER logs, so the
// per-engine duplication trap can't regress.
func TestResolveSlotModelTable(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		slot      string
		wantModel string
		wantOK    bool
	}{
		{
			name:      "literal id",
			cfg:       Config{ModelSlots: map[string]string{slotCompaction: "gpt-4o-mini"}},
			slot:      slotCompaction,
			wantModel: "gpt-4o-mini", wantOK: true,
		},
		{
			name:      "operator alias",
			cfg:       Config{ModelSlots: map[string]string{slotGuardrail: "cheap"}, ModelAliases: map[string]string{"cheap": "gpt-4o-mini"}},
			slot:      slotGuardrail,
			wantModel: "gpt-4o-mini", wantOK: true,
		},
		{
			name:      "default-tier fallthrough",
			cfg:       Config{ModelSlots: map[string]string{slotCheap: "cheap-id"}},
			slot:      slotCompaction, // no explicit compaction binding ⇒ falls to cheap
			wantModel: "cheap-id", wantOK: true,
		},
		{
			name:      "explicit slot beats its default tier",
			cfg:       Config{ModelSlots: map[string]string{slotCheap: "cheap-id", slotCompaction: "explicit-id"}},
			slot:      slotCompaction,
			wantModel: "explicit-id", wantOK: true,
		},
		{
			name:   "unset slot, no tier",
			cfg:    Config{ModelSlots: map[string]string{slotFast: "fast-id"}},
			slot:   slotCompaction, // compaction defaults to cheap (unset) — fast is unrelated
			wantOK: false,
		},
		{
			name:   "inherit alias degrades silently",
			cfg:    Config{ModelSlots: map[string]string{slotCompaction: "sonnet"}}, // built-in sonnet ⇒ inherit ("")
			slot:   slotCompaction,
			wantOK: false,
		},
		{
			name:   "unknown bare token degrades silently",
			cfg:    Config{ModelSlots: map[string]string{slotCompaction: "bogus"}},
			slot:   slotCompaction,
			wantOK: false,
		},
		{
			name:   "no slots at all",
			cfg:    Config{},
			slot:   slotGuardrail,
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diag := &capturingDiag{}
			cfg := tc.cfg
			cfg.Diagnostics = diag
			model, ok := resolveSlotModel(cfg, tc.slot, "session-model")
			if ok != tc.wantOK || model != tc.wantModel {
				t.Fatalf("resolveSlotModel = (%q, %v), want (%q, %v)", model, ok, tc.wantModel, tc.wantOK)
			}
			// resolveSlotModel must be SILENT — no per-engine diagnostics ever (the
			// duplication trap). The misconfig WARN is the build-once path's job.
			if len(diag.lines) != 0 {
				t.Fatalf("resolveSlotModel must not log; got lines=%v", diag.lines)
			}
		})
	}
}

// TestCompactionSlotRoutesSummaryOnly pins E1 (ADR 0030): a compaction slot routes
// ONLY the CascadeCompactor's summary model; the engine's own Model stays the session
// model.
func TestCompactionSlotRoutesSummaryOnly(t *testing.T) {
	const sessionModel = "gpt-4o"
	cfg := Config{
		Model:        sessionModel,
		Compaction:   "cascade",
		ModelSlots:   map[string]string{slotCompaction: "cheap"},
		ModelAliases: map[string]string{"cheap": "cheap-model-id"},
	}
	provider, store, policy, hooks, mcpP, instr := depsTestFixture(t)
	deps := engineDepsForProvider(cfg, provider, testProviderModel(sessionModel), func() int { return defaultContextWindowTokens }, store, policy, hooks, mcpP, instr)
	if deps.Model != sessionModel {
		t.Fatalf("deps.Model = %q, want the session model %q (the slot must NOT move the engine model)", deps.Model, sessionModel)
	}
	cc, ok := deps.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("Compactor type = %T, want CascadeCompactor", deps.Compactor)
	}
	if cc.Model != "cheap-model-id" {
		t.Fatalf("CascadeCompactor.Model = %q, want the slot model %q", cc.Model, "cheap-model-id")
	}
}

// TestAskReviewerSlotSupersedesFlag pins E2 (ADR 0030): a configured ask-reviewer slot
// SUPERSEDES the flag's model, but a slot ALONE does not enable the reviewer (the flag
// stays the on/off gate).
func TestAskReviewerSlotSupersedesFlag(t *testing.T) {
	provider, _, _, _, _, _ := depsTestFixture(t)

	// Slot supersedes the flag's model.
	cfg := Config{
		Model:                    "session-model",
		SubagentAskReviewerModel: "flag-model-id",
		ModelSlots:               map[string]string{slotAskReviewer: "cheap"},
		ModelAliases:             map[string]string{"cheap": "slot-model-id"},
	}
	deps, ok := askAdjudicatorDeps(cfg, nil, provider, "openai", "session-model")
	if !ok {
		t.Fatalf("a flag-enabled reviewer must yield deps")
	}
	if deps.Model != "slot-model-id" {
		t.Fatalf("reviewer Model = %q, want the slot model (it supersedes the flag's %q)", deps.Model, "flag-model-id")
	}

	// Flag empty + slot set ⇒ reviewer NIL (the slot does not enable it).
	noFlag := Config{
		Model:        "session-model",
		ModelSlots:   map[string]string{slotAskReviewer: "cheap"},
		ModelAliases: map[string]string{"cheap": "slot-model-id"},
	}
	if _, on := askAdjudicatorDeps(noFlag, nil, provider, "openai", "session-model"); on {
		t.Fatalf("a slot WITHOUT the --subagent-ask-reviewer flag must NOT enable the reviewer")
	}
}

// TestGuardrailSlotRoutesChecker pins E3 (ADR 0030) + the #159 enable widening: a
// configured guardrail slot routes the checker model AND (alone) ENABLES guardrails
// (configure = enable, ADR 0046). The slot supersedes the --guardrails-model value
// when both are set.
func TestGuardrailSlotRoutesChecker(t *testing.T) {
	const sessionModel = "gpt-4o"
	cfg := Config{
		Model:           sessionModel,
		UseMock:         true,
		GuardrailsModel: "guard-flag-id",
		ModelSlots:      map[string]string{slotGuardrail: "cheap"},
		ModelAliases:    map[string]string{"cheap": "slot-guard-id"},
	}
	if m := checkerModel(t, cfg, sessionModel); m != "slot-guard-id" {
		t.Fatalf("guardrail checker model = %q, want the slot model %q", m, "slot-guard-id")
	}

	// A slot WITHOUT a GuardrailsModel now ENABLES the checker (the slot alone enables,
	// #159): buildGuardrailsChecker returns non-nil and routes the slot model.
	noModel := Config{
		Model:        sessionModel,
		UseMock:      true,
		ModelSlots:   map[string]string{slotGuardrail: "cheap"},
		ModelAliases: map[string]string{"cheap": "slot-guard-id"},
	}
	if checker := buildGuardrailsChecker(noModel, nil, mockllm.New(mockllm.TextTurn("x")), "openai", sessionModel); checker == nil {
		t.Fatalf("a guardrail slot WITHOUT --guardrails-model must now ENABLE the checker (ADR 0046)")
	}
	if m := checkerModel(t, noModel, sessionModel); m != "slot-guard-id" {
		t.Fatalf("slot-only guardrail checker model = %q, want the slot model %q", m, "slot-guard-id")
	}
}

// TestFoldOperatorModelSlotsNoResolverNoOp pins the nil-resolver / no-block no-op
// (mirroring TestFoldOperatorGuardrailsNoResolverNoOp).
func TestFoldOperatorModelSlotsNoResolverNoOp(t *testing.T) {
	if got := foldOperatorModelSlots(Config{Model: "m"}); len(got.ModelSlots) != 0 || len(got.ModelAliases) != 0 {
		t.Fatal("with no resolver the fold must be a no-op")
	}
	// A real resolver but NO models: block is also a no-op.
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(path, []byte("permissions:\n  allow: []\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if got := foldOperatorModelSlots(Config{Model: "m", permResolver: res}); len(got.ModelSlots) != 0 || len(got.ModelAliases) != 0 {
		t.Fatal("a resolver with no models: block must fold nothing")
	}
}

// TestFoldOperatorModelSlotsFromYAML pins the operator-tier fold (ADR 0030): the YAML
// models.slots/models.aliases merge onto cfg, CLI wins PER KEY for both maps, and an
// unknown INNER slot key is dropped fail-soft with a Build-once WARN (composition-layer
// validation, distinct from the YAML strict-parse of unknown TOP keys).
func TestFoldOperatorModelSlotsFromYAML(t *testing.T) {
	const yamlCfg = `
models:
  slots:
    compaction: cheap
    guardrail: yaml-guard
    slotz: bogus           # unknown INNER key ⇒ dropped fail-soft + WARN
  aliases:
    cheap: gpt-4o-mini
    fast: yaml-fast
`
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil")
	}

	diag := &capturingDiag{}
	// CLI presets: a slot AND an alias the YAML also names — CLI must win per key.
	cfg := foldOperatorModelSlots(Config{
		Diagnostics:  diag,
		permResolver: res,
		ModelSlots:   map[string]string{"compaction": "cli-slot-model"},
		ModelAliases: map[string]string{"cheap": "cli-cheap-id"},
	})

	// (a) CLI-wins per key.
	if cfg.ModelSlots["compaction"] != "cli-slot-model" {
		t.Fatalf("CLI --model-slot must win per key; compaction=%q", cfg.ModelSlots["compaction"])
	}
	if cfg.ModelAliases["cheap"] != "cli-cheap-id" {
		t.Fatalf("CLI --model-alias must win per key; cheap=%q", cfg.ModelAliases["cheap"])
	}
	// (b) YAML fills the keys the CLI did not set.
	if cfg.ModelSlots["guardrail"] != "yaml-guard" {
		t.Fatalf("YAML slot must fold where CLI is silent; guardrail=%q", cfg.ModelSlots["guardrail"])
	}
	if cfg.ModelAliases["fast"] != "yaml-fast" {
		t.Fatalf("YAML alias must fold where CLI is silent; fast=%q", cfg.ModelAliases["fast"])
	}
	// (c) The unknown INNER slot key is DROPPED and WARNed (fail-soft, distinct from the
	// YAML strict-parse of unknown TOP keys).
	if _, present := cfg.ModelSlots["slotz"]; present {
		t.Fatalf("an unknown inner slot key must be dropped; ModelSlots=%v", cfg.ModelSlots)
	}
	if !diag.has("unknown slot key IGNORED") {
		t.Fatalf("an unknown inner slot key must WARN once at Build; lines=%v", diag.lines)
	}
}

// TestLogSlotConfigFacts pins the build-once narration (S1, the live-e2e's oracle): an
// INFO "model slot ACTIVE" per RESOLVED routed slot, the one-time misconfig WARN per
// CONFIGURED-but-unresolvable routed slot, and SILENCE when no slot is configured.
func TestLogSlotConfigFacts(t *testing.T) {
	// Nothing configured ⇒ no facts.
	emptyDiag := &capturingDiag{}
	logSlotConfigFacts(Config{Model: "m", Diagnostics: emptyDiag})
	if len(emptyDiag.lines) != 0 {
		t.Fatalf("an unconfigured slot map must narrate nothing; lines=%v", emptyDiag.lines)
	}

	// A resolved slot ⇒ exactly one ACTIVE line; a configured-but-unresolvable slot ⇒
	// exactly one misconfig WARN.
	diag := &capturingDiag{}
	logSlotConfigFacts(Config{
		Model:        "m",
		Diagnostics:  diag,
		ModelSlots:   map[string]string{slotCompaction: "cheap", slotGuardrail: "bogus"},
		ModelAliases: map[string]string{"cheap": "gpt-4o-mini"},
	})
	if got := diag.count("model slot ACTIVE"); got != 1 {
		t.Fatalf("ACTIVE lines = %d, want 1 (only the compaction slot resolves); lines=%v", got, diag.lines)
	}
	if got := diag.count("unknown alias or one meaning inherit"); got != 1 {
		t.Fatalf("misconfig WARN lines = %d, want 1 (only the guardrail slot is unresolvable); lines=%v", got, diag.lines)
	}
}

// TestCompactionBudgetIsCounterIndependent is the O5 TRIPWIRE (Architecture Low):
// CascadeCompactor.BudgetTokens is WINDOW-derived and independent of the Counter, so
// the compaction reroute can key the Counter to the slot model without mis-sizing the
// budget. Building the compactor with two DIFFERENT counters/models must yield the SAME
// BudgetTokens — a future "make BudgetTokens counter-derived" refactor trips this red.
func TestCompactionBudgetIsCounterIndependent(t *testing.T) {
	provider := mockllm.New(mockllm.TextTurn("x"))
	a := buildCompactor(Config{Model: "gpt-4o", Compaction: "cascade", Tokenizer: "tiktoken"}, provider, testProviderModel("gpt-4o"), buildTokenCounter(Config{Model: "gpt-4o", Tokenizer: "tiktoken"}))
	b := buildCompactor(Config{Model: "gpt-4", Compaction: "cascade", Tokenizer: "tiktoken"}, provider, testProviderModel("gpt-4"), buildTokenCounter(Config{Model: "gpt-4", Tokenizer: "tiktoken"}))
	ca, ok := a.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("compactor a type = %T, want CascadeCompactor", a)
	}
	cb, ok := b.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("compactor b type = %T, want CascadeCompactor", b)
	}
	if ca.Model == cb.Model {
		t.Fatalf("test setup: the two compactors must use DIFFERENT models, got %q twice", ca.Model)
	}
	if ca.BudgetTokens != cb.BudgetTokens {
		t.Fatalf("BudgetTokens must be COUNTER-INDEPENDENT (window-derived): %d != %d — "+
			"a refactor made the budget counter-derived; the compaction-slot reroute would mis-count", ca.BudgetTokens, cb.BudgetTokens)
	}
	if ca.BudgetTokens <= 0 {
		t.Fatalf("BudgetTokens must be a positive window-derived value, got %d", ca.BudgetTokens)
	}
}
