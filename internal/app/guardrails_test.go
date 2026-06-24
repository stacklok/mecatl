package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// (15) fail-fast unknown guardrails model: the loud-misconfig posture, mirroring the
// ask-reviewer normalization. Empty = off, an unknown bare token / inherit-alias is a
// BUILD error, a concrete id / mapped alias passes verbatim, UseMock skips validation.
func TestNormalizeGuardrailsModelFailFast(t *testing.T) {
	if got, err := normalizeGuardrailsModel(Config{}); err != nil || got != "" {
		t.Fatalf("empty must be off: got %q err %v", got, err)
	}
	if _, err := normalizeGuardrailsModel(Config{GuardrailsModel: "bogus"}); err == nil {
		t.Fatal("an unknown bare token must fail fast")
	}
	if _, err := normalizeGuardrailsModel(Config{GuardrailsModel: "sonnet"}); err == nil {
		t.Fatal("a builtin inherit-alias must fail fast (the checker needs a concrete model)")
	}
	if got, err := normalizeGuardrailsModel(Config{GuardrailsModel: "gpt-5-mini"}); err != nil || got != "gpt-5-mini" {
		t.Fatalf("a concrete id must pass verbatim: got %q err %v", got, err)
	}
	if got, err := normalizeGuardrailsModel(Config{UseMock: true, GuardrailsModel: "bogus"}); err != nil || got != "bogus" {
		t.Fatalf("UseMock must skip validation: got %q err %v", got, err)
	}
	// The kill-switch short-circuits validation (off regardless of the value).
	if got, err := normalizeGuardrailsModel(Config{GuardrailsModel: "bogus", GuardrailsDisabled: true}); err != nil || got != "bogus" {
		t.Fatalf("a disabled guardrail must skip model validation: got %q err %v", got, err)
	}
}

// the build-once ACTIVE facts are GONE (issue #159): normalizeGuardrailsModel is now
// validate-only — it emits NOTHING. The always-one-line posture (ON|OFF, resolved model
// + provenance) is emitted by logGuardrailsPosture (TestLogGuardrailsPostureBranches).
func TestNormalizeGuardrailsModelNarratesFacts(t *testing.T) {
	active := &capturingDiag{}
	if _, err := normalizeGuardrailsModel(Config{
		GuardrailsModel: "gpt-5-mini",
		GuardrailsRules: []GuardrailRule{{Match: "WebFetch"}},
		Diagnostics:     active,
	}); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if len(active.lines) != 0 {
		t.Fatalf("normalizeGuardrailsModel must emit NOTHING now (posture moved to logGuardrailsPosture); lines=%v", active.lines)
	}

	// A model with NO explicit rules is still valid — but the validator stays silent
	// (the ON/default-advisory posture is logGuardrailsPosture's job).
	defaults := &capturingDiag{}
	if _, err := normalizeGuardrailsModel(Config{GuardrailsModel: "gpt-5-mini", Diagnostics: defaults}); err != nil {
		t.Fatalf("model-without-rules: %v", err)
	}
	if len(defaults.lines) != 0 {
		t.Fatalf("normalizeGuardrailsModel must emit NOTHING even with default rules; lines=%v", defaults.lines)
	}
}

// TestGuardrailsConfiguredSlotEnables pins the #159 gate truth table: a bound
// `guardrail` model slot now CO-ENABLES guardrails (configure = enable, ADR 0046), not
// merely routes an already-enabled checker. The kill-switch still wins.
func TestGuardrailsConfiguredSlotEnables(t *testing.T) {
	slot := func() map[string]string { return map[string]string{slotGuardrail: "cheap"} }
	aliases := map[string]string{"cheap": "slot-id"}
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"nothing", Config{}, false},
		{"gate only", Config{GuardrailsModel: "gpt-5-mini"}, true},
		{"slot only (explicit guardrail=)", Config{ModelSlots: slot(), ModelAliases: aliases}, true},
		{"slot via tier fall-through (cheap= only)", Config{
			ModelSlots:   map[string]string{slotCheap: "cheap"},
			ModelAliases: aliases,
		}, true},
		{"slot bound + --guardrails=off", Config{
			GuardrailsDisabled: true,
			ModelSlots:         slot(),
			ModelAliases:       aliases,
		}, false},
		{"slot + model", Config{
			GuardrailsModel: "gpt-5-mini",
			ModelSlots:      slot(),
			ModelAliases:    aliases,
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := guardrailsConfigured(c.cfg); got != c.want {
				t.Fatalf("guardrailsConfigured = %v, want %v", got, c.want)
			}
		})
	}
}

// TestResolveGuardrailsCheckerModelProvenance pins the shared resolver's precedence +
// provenance, the single source of truth both buildGuardrailsChecker and the posture
// line read. Slot supersedes gate; same-id gate stays srcSlot; UseMock gate-only passes
// the literal through.
func TestResolveGuardrailsCheckerModelProvenance(t *testing.T) {
	cases := []struct {
		name      string
		cfg       Config
		wantModel string
		wantSrc   guardrailSource
		wantConf  bool
	}{
		{
			name:      "gate only",
			cfg:       Config{GuardrailsModel: "gpt-5-mini"},
			wantModel: "gpt-5-mini",
			wantSrc:   srcGate,
			wantConf:  true,
		},
		{
			name:      "slot only",
			cfg:       Config{ModelSlots: map[string]string{slotGuardrail: "cheap"}, ModelAliases: map[string]string{"cheap": "slot-id"}},
			wantModel: "slot-id",
			wantSrc:   srcSlot,
			wantConf:  true,
		},
		{
			name:      "slot+gate differing (slot wins, supersedes gate)",
			cfg:       Config{GuardrailsModel: "gate-id", ModelSlots: map[string]string{slotGuardrail: "cheap"}, ModelAliases: map[string]string{"cheap": "slot-id"}},
			wantModel: "slot-id",
			wantSrc:   srcSlotSupersedingGate,
			wantConf:  true,
		},
		{
			name:      "slot+gate same resolved id (no superseding)",
			cfg:       Config{GuardrailsModel: "slot-id", ModelSlots: map[string]string{slotGuardrail: "cheap"}, ModelAliases: map[string]string{"cheap": "slot-id"}},
			wantModel: "slot-id",
			wantSrc:   srcSlot,
			wantConf:  true,
		},
		{
			name:     "nothing",
			cfg:      Config{},
			wantSrc:  srcNone,
			wantConf: false,
		},
		{
			name:      "UseMock gate-only (literal passthrough)",
			cfg:       Config{UseMock: true, GuardrailsModel: "bogus-literal"},
			wantModel: "bogus-literal",
			wantSrc:   srcGate,
			wantConf:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotModel, gotSrc, gotConf := resolveGuardrailsCheckerModel(c.cfg)
			if gotModel != c.wantModel || gotSrc != c.wantSrc || gotConf != c.wantConf {
				t.Fatalf("resolveGuardrailsCheckerModel = (%q, %v, %v), want (%q, %v, %v)",
					gotModel, gotSrc, gotConf, c.wantModel, c.wantSrc, c.wantConf)
			}
		})
	}
}

// (7) OFF-by-default: buildGuardrailsHooks returns inner UNCHANGED when guardrails are
// unconfigured (byte-identical to no guardrails — the same inner pointer).
func TestGuardrailsOffReturnsInnerUnchanged(t *testing.T) {
	inner := hookexec.New(nil)
	llm := mockllm.New()

	// No model at all → OFF (byte-identical to no guardrails).
	if got := buildGuardrailsHooks(Config{UseMock: true}, nil, llm, "mock", "m", inner); got != port.HookRunner(inner) {
		t.Fatal("unconfigured guardrails (no model) must return inner unchanged (OFF byte-identical)")
	}
	// The master kill-switch wins over a full config.
	cfg := Config{UseMock: true, GuardrailsModel: "x", GuardrailsRules: []GuardrailRule{{Match: "*"}}, GuardrailsDisabled: true}
	if got := buildGuardrailsHooks(cfg, nil, llm, "mock", "m", inner); got != port.HookRunner(inner) {
		t.Fatal("--guardrails=off must return inner unchanged")
	}
}

// A model with NO explicit rules WRAPS inner with the DEFAULT advisory rule set (the
// headline default: ON advisory for WebSearch/WebFetch/mcp__*).
func TestGuardrailsModelOnlyShipsDefaultBlock(t *testing.T) {
	inner := hookexec.New(nil)
	llm := mockllm.New()
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model"}
	got := buildGuardrailsHooks(cfg, nil, llm, "mock", "m", inner)
	if got == port.HookRunner(inner) {
		t.Fatal("a guardrails model with no explicit rules must ship the DEFAULT block rules, not stay inert")
	}
	// The default set is exactly WebSearch/WebFetch/mcp__*; assert it compiles to 3.
	specs, usedDefaults := effectiveGuardrailSpecs(cfg)
	if !usedDefaults || len(specs) != 3 {
		t.Fatalf("model-only must use the 3-rule default set; usedDefaults=%v n=%d", usedDefaults, len(specs))
	}
	for _, s := range specs {
		if s.Mode != string(modelhook.ModeBlock) {
			t.Fatalf("default rules must be block (enforcement); got %q for %q", s.Mode, s.Match)
		}
	}
}

// TestGuardrailsDefaultModeAdvisory tests the defaultMode override: setting
// defaultMode:advisory downgrades the built-in defaults to observe-only.
func TestGuardrailsDefaultModeAdvisory(t *testing.T) {
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model", GuardrailsDefaultMode: "advisory"}
	specs, usedDefaults := effectiveGuardrailSpecs(cfg)
	if !usedDefaults || len(specs) != 3 {
		t.Fatalf("model-only with defaultMode must still use the 3-rule default set; usedDefaults=%v n=%d", usedDefaults, len(specs))
	}
	for _, s := range specs {
		if s.Mode != string(modelhook.ModeAdvisory) {
			t.Fatalf("defaultMode:advisory must downgrade defaults to advisory; got %q for %q", s.Mode, s.Match)
		}
	}
}

// a configured guardrail wraps inner (no longer the same pointer).
func TestGuardrailsConfiguredWrapsInner(t *testing.T) {
	inner := hookexec.New(nil)
	llm := mockllm.New()
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model", GuardrailsRules: []GuardrailRule{{Match: "WebFetch", Phases: []string{"post"}}}}
	got := buildGuardrailsHooks(cfg, nil, llm, "mock", "m", inner)
	if got == port.HookRunner(inner) {
		t.Fatal("a configured guardrail must WRAP inner, not return it unchanged")
	}
}

// (8) quarantine / no-recursion: the checker engine is built via the CHILD deps path,
// which forces inert Hooks + nil ChildAskReviewer + Interactive false + a tool-less
// catalog — so a checker call fires no hooks and can never re-trigger the runner.
func TestGuardrailsCheckerEngineQuarantined(t *testing.T) {
	cfg := Config{UseMock: true, Model: "m"}
	deps := childEngineDepsForProvider(cfg, "guardrail-checker", mockllm.New(), "checker-model", func() int { return defaultContextWindowTokens },
		tool.NewCatalog(), promptConfig(modelCfgFor(cfg, "checker-model"), cfg.gitStatus), nil)

	if deps.ChildAskReviewer != nil {
		t.Fatal("the checker engine must carry NO ask reviewer (no nesting)")
	}
	if deps.Interactive {
		t.Fatal("the checker engine must be non-interactive")
	}
	if deps.Hooks == nil {
		t.Fatal("the checker engine must carry inert (non-nil hookexec) Hooks, never the guardrail runner")
	}
	// The hooks must NOT be a modelhook.Runner (that would recurse): a plain hookexec
	// allows every phase, so a checker tool phase fires no guardrail.
	out, err := deps.Hooks.Run(context.Background(), governance.HookEvent{Phase: governance.PhasePreToolUse, Tool: "WebFetch"})
	if err != nil || out.Block || len(out.Mutated) != 0 {
		t.Fatalf("the checker engine's hooks must be inert (allow-all), got %+v err %v", out, err)
	}
	if deps.Catalog == nil || len(deps.Catalog.Tools()) != 0 {
		t.Fatal("the checker engine must be tool-less")
	}
}

// foldOperatorGuardrails: a nil resolver / no operator block is a no-op; a YAML block
// is folded (rules + model + cost knobs); CLI flags out-rank YAML for model + disable.
func TestFoldOperatorGuardrailsNoResolverNoOp(t *testing.T) {
	cfg := Config{Model: "m"}
	if got := foldOperatorGuardrails(cfg); len(got.GuardrailsRules) != 0 || got.GuardrailsModel != "" {
		t.Fatal("with no resolver the fold must be a no-op")
	}
}

// foldOperatorGuardrails folds the OPERATOR-TIER YAML (model + minContentBytes +
// rules) onto Config; a CLI --guardrails-model wins over the YAML model. This also
// proves MinContentBytes is LIVE config (folded end-to-end), not dead.
func TestFoldOperatorGuardrailsFromYAML(t *testing.T) {
	const yamlCfg = `
guardrails:
  model: "yaml-model"
  minContentBytes: 24
  rules:
    - match: "WebFetch"
      phases: ["post"]
      mode: "block"
`
	path := filepath.Join(t.TempDir(), "guardrails.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil")
	}

	// No CLI model: adopt the YAML model + cost knobs + rules.
	cfg := foldOperatorGuardrails(Config{permResolver: res})
	if cfg.GuardrailsModel != "yaml-model" {
		t.Fatalf("YAML model must fold when no CLI model; got %q", cfg.GuardrailsModel)
	}
	if cfg.GuardrailsMinContentBytes != 24 {
		t.Fatalf("cost knob must fold (minContentBytes=%d)", cfg.GuardrailsMinContentBytes)
	}
	if len(cfg.GuardrailsRules) != 1 || cfg.GuardrailsRules[0].Match != "WebFetch" {
		t.Fatalf("rules must fold from YAML; got %+v", cfg.GuardrailsRules)
	}

	// A CLI --guardrails-model out-ranks the YAML model.
	cliCfg := foldOperatorGuardrails(Config{permResolver: res, GuardrailsModel: "cli-model"})
	if cliCfg.GuardrailsModel != "cli-model" {
		t.Fatalf("CLI model must out-rank the YAML model; got %q", cliCfg.GuardrailsModel)
	}
}
