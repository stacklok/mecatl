package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// promptCapturingChecker records the assembled CheckRequest.Prompt for the LAST call,
// so a routing test can assert WHICH rubric the Runner built for a given (tool, phase).
// It returns a safe verdict so the call passes through unchanged.
type promptCapturingChecker struct{ lastPrompt string }

func (c *promptCapturingChecker) Check(_ context.Context, req modelhook.CheckRequest) (modelhook.Verdict, error) {
	c.lastPrompt = req.Prompt
	safe := true
	return modelhook.Verdict{Safe: &safe}, nil
}

// TestDefaultShellRuleRoutesToShellRubric is the KEY wiring test for the false-positive
// fix (ADR 0060): the DEFAULT Shell rule must route a Pre Shell check to
// modelhook.DefaultShellPrePrompt (the local-writes-are-safe rubric), while Web/MCP Pre
// checks keep the generic exfiltration rubric. It drives the REAL compiled default
// rule set through a Runner with a prompt-capturing checker — so it proves the
// false-positive class is structurally addressed, not just that a const exists.
func TestDefaultShellRuleRoutesToShellRubric(t *testing.T) {
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model"}
	specs, usedDefaults := effectiveGuardrailSpecs(cfg)
	if !usedDefaults {
		t.Fatal("model-only must use the default set")
	}
	rules, ok := compileGuardrailRules(cfg, specs)
	if !ok {
		t.Fatal("default rules must compile")
	}
	chk := &promptCapturingChecker{}
	runner := modelhook.New(hookexec.New(nil), modelhook.Options{Rules: rules, Checker: chk})

	// A MUTATING Shell command (read-only would be skipped by the pre-filter and never
	// reach the checker). Assert the captured rubric is the Shell one.
	bashArgs, _ := json.Marshal(map[string]string{"command": "cat hello > /other/repo/file"})
	if _, err := runner.Run(context.Background(), governance.HookEvent{
		Phase: governance.PhasePreToolUse, Tool: "Shell", Input: bashArgs,
	}); err != nil {
		t.Fatalf("Shell pre run: %v", err)
	}
	if chk.lastPrompt == "" {
		t.Fatal("the mutating Shell command must have reached the checker")
	}
	if !strings.Contains(chk.lastPrompt, "is NOT exfiltration") ||
		!strings.Contains(chk.lastPrompt, "data that stays") {
		t.Fatalf("the Shell Pre check must use the Shell rubric (local writes are safe); got:\n%s", chk.lastPrompt)
	}
	if strings.Contains(chk.lastPrompt, "If you are uncertain, judge unsafe") {
		t.Fatal("the Shell rubric must NOT carry the generic 'if uncertain, judge unsafe' clause")
	}

	// A WebSearch Pre check must still use the GENERIC exfiltration rubric.
	chk.lastPrompt = ""
	if _, err := runner.Run(context.Background(), governance.HookEvent{
		Phase: governance.PhasePreToolUse, Tool: "WebSearch", Input: json.RawMessage(`{"query":"x"}`),
	}); err != nil {
		t.Fatalf("WebSearch pre run: %v", err)
	}
	if !strings.Contains(chk.lastPrompt, "If you are uncertain, judge unsafe") {
		t.Fatalf("the WebSearch Pre check must keep the generic exfiltration rubric; got:\n%s", chk.lastPrompt)
	}
	if strings.Contains(chk.lastPrompt, "is NOT exfiltration") {
		t.Fatal("the WebSearch rubric must NOT be the Shell rubric")
	}

	// An mcp__* Pre check must also use the GENERIC rubric.
	chk.lastPrompt = ""
	if _, err := runner.Run(context.Background(), governance.HookEvent{
		Phase: governance.PhasePreToolUse, Tool: "mcp__github__create_pr", Input: json.RawMessage(`{"x":"y"}`),
	}); err != nil {
		t.Fatalf("mcp pre run: %v", err)
	}
	if !strings.Contains(chk.lastPrompt, "If you are uncertain, judge unsafe") {
		t.Fatalf("an mcp__* Pre check must keep the generic exfiltration rubric; got:\n%s", chk.lastPrompt)
	}
}

// TestDefaultShellSpecCarriesShellRubric pins the spec-level wiring: the default Shell
// entry carries modelhook.DefaultShellPrePrompt as its Prompt (and Web/MCP do not).
func TestDefaultShellSpecCarriesShellRubric(t *testing.T) {
	specs, _ := effectiveGuardrailSpecs(Config{UseMock: true, GuardrailsModel: "m"})
	for _, s := range specs {
		switch s.Match {
		case "Shell":
			if s.Prompt != modelhook.DefaultShellPrePrompt {
				t.Fatalf("the default Shell spec must carry DefaultShellPrePrompt; got %q", s.Prompt)
			}
		default:
			if s.Prompt != "" {
				t.Fatalf("the default %s spec must carry NO prompt (uses the built-in default); got %q", s.Match, s.Prompt)
			}
		}
	}
}

// TestExplicitShellRuleDoesNotInheritShellRubric: an OPERATOR explicit Shell rule with no
// prompt does NOT inherit the default Shell rubric (least-surprising — an explicit rule
// opts out of the default-set conveniences). It falls back to the built-in
// defaultPrePrompt (asserted via the generic rubric's distinguishing text).
func TestExplicitShellRuleDoesNotInheritShellRubric(t *testing.T) {
	cfg := Config{UseMock: true, GuardrailsModel: "m",
		GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}}}
	specs, usedDefaults := effectiveGuardrailSpecs(cfg)
	if usedDefaults {
		t.Fatal("explicit rules must not use defaults")
	}
	if len(specs) != 1 || specs[0].Prompt != "" {
		t.Fatalf("an explicit Shell rule must carry no prompt (falls back to defaultPrePrompt); got %+v", specs)
	}
	rules, _ := compileGuardrailRules(cfg, specs)
	chk := &promptCapturingChecker{}
	runner := modelhook.New(hookexec.New(nil), modelhook.Options{Rules: rules, Checker: chk})
	bashArgs, _ := json.Marshal(map[string]string{"command": "cp a b"})
	if _, err := runner.Run(context.Background(), governance.HookEvent{
		Phase: governance.PhasePreToolUse, Tool: "Shell", Input: bashArgs,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(chk.lastPrompt, "If you are uncertain, judge unsafe") {
		t.Fatalf("an explicit Shell rule (no prompt) must use the generic defaultPrePrompt; got:\n%s", chk.lastPrompt)
	}
}

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
	if got := buildGuardrailsHooks(Config{UseMock: true}, nil, llm, "mock", "m", inner, nil); got != port.HookRunner(inner) {
		t.Fatal("unconfigured guardrails (no model) must return inner unchanged (OFF byte-identical)")
	}
	// The master kill-switch wins over a full config.
	cfg := Config{UseMock: true, GuardrailsModel: "x", GuardrailsRules: []GuardrailRule{{Match: "*"}}, GuardrailsDisabled: true}
	if got := buildGuardrailsHooks(cfg, nil, llm, "mock", "m", inner, nil); got != port.HookRunner(inner) {
		t.Fatal("--guardrails=off must return inner unchanged")
	}
}

// A model with NO explicit rules WRAPS inner with the DEFAULT block rule set (the
// headline contextual default: web/MCP, Shell, local mutations, and delegation).
func TestGuardrailsModelOnlyShipsDefaultBlock(t *testing.T) {
	inner := hookexec.New(nil)
	llm := mockllm.New()
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model"}
	got := buildGuardrailsHooks(cfg, nil, llm, "mock", "m", inner, nil)
	if got == port.HookRunner(inner) {
		t.Fatal("a guardrails model with no explicit rules must ship the DEFAULT block rules, not stay inert")
	}
	// The inbound default adds four local read/search tools; Shell now covers both phases.
	specs, usedDefaults := effectiveGuardrailSpecs(cfg)
	if !usedDefaults || len(specs) != 18 {
		t.Fatalf("model-only must use the 18-rule contextual default set; usedDefaults=%v n=%d", usedDefaults, len(specs))
	}
	for _, s := range specs {
		if s.Mode != string(modelhook.ModeBlock) {
			t.Fatalf("default rules must be block (enforcement); got %q for %q", s.Mode, s.Match)
		}
	}
	// The Shell spec is present for action and inbound review, with the read-only pre-filter on.
	var bash *modelhook.RuleSpec
	for i := range specs {
		if specs[i].Match == "Shell" {
			bash = &specs[i]
		}
	}
	if bash == nil {
		t.Fatal("the default set must include a Shell rule (ADR 0060)")
	}
	if len(bash.Phases) != 2 || bash.Phases[0] != "pre" || bash.Phases[1] != "post" {
		t.Fatalf("the default Shell rule must cover pre and post; phases=%v", bash.Phases)
	}
	if bash.Mode != string(modelhook.ModeBlock) {
		t.Fatalf("the default Shell rule must be block; got %q", bash.Mode)
	}
	if !bash.SkipReadOnlyShell {
		t.Fatal("the default Shell rule must carry SkipReadOnlyShell (the read-only pre-filter)")
	}
}

// TestGuardrailsSharedWaiverReachesRunner: buildGuardrailsHooks threads the SHARED
// waiver holder into the Runner it builds, so a verdict-armed waiver (ADR 0062, armed
// by the engine via the Runner's HookApprovalLearner) is the SAME holder a later block
// consults. A nil waiver is byte-identical to no waiver path (every block surfaces).
// This is the composition-side proof that the waiver holder is shared, not per-Runner.
func TestGuardrailsSharedWaiverReachesRunner(t *testing.T) {
	inner := hookexec.New(nil)
	llm := mockllm.New()
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model",
		GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}}}

	waiver := modelhook.NewWaiverHolder()
	waiver.ArmFromApproval("s-shared", "Shell", "gh pr merge 7")

	// The checker is engine-backed (UseMock); verify the WIRING: the Runner built with
	// the shared waiver is distinct from inner (it wrapped), and the shared waiver still
	// authorizes its EXACT command (the holder is shared, not copied/cleared at
	// construction). Matching is exact-equality (ADR 0062 / CWE-863), so the same key.
	got := buildGuardrailsHooks(cfg, nil, llm, "mock", "m", inner, waiver)
	if got == port.HookRunner(inner) {
		t.Fatal("a configured guardrail must wrap inner")
	}
	if !waiver.Allows("s-shared", "Shell", "gh pr merge 7") {
		t.Fatal("the shared waiver must remain armed after wiring (the holder is shared)")
	}

	// nil waiver: still wraps (guardrail is configured) but the waiver path is off —
	// byte-identical to the pre-waiver posture (no panic, no behaviour change).
	gotNil := buildGuardrailsHooks(cfg, nil, llm, "mock", "m", hookexec.New(nil), nil)
	if gotNil == nil {
		t.Fatal("a configured guardrail with a nil waiver must still build a Runner")
	}
}

// TestGuardrailsPostureCouplingDemotesOnlyYolo (ADR 0062, sub-decision B): posture
// yolo demotes every guardrail rule to advisory (observe-only); strict/trusted/auto
// keep the configured enforcing mode (under auto the approve-once ask IS the
// enforcement, gated on interactivity, not posture).
func TestGuardrailsPostureCouplingDemotesOnlyYolo(t *testing.T) {
	rules := []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}}
	for _, tc := range []struct {
		posture  Posture
		wantMode string
	}{
		{PostureStrict, "block"},
		{PostureTrusted, "block"},
		{PostureAuto, "block"},
		{PostureYolo, string(modelhook.ModeAdvisory)},
	} {
		cfg := Config{UseMock: true, GuardrailsModel: "m", GuardrailsRules: rules, Posture: tc.posture}
		specs, _ := effectiveGuardrailSpecs(cfg)
		if len(specs) != 1 {
			t.Fatalf("%s: want 1 spec, got %d", tc.posture, len(specs))
		}
		if specs[0].Mode != tc.wantMode {
			t.Fatalf("posture %s: want mode %q, got %q", tc.posture, tc.wantMode, specs[0].Mode)
		}
	}
}

// TestGuardrailsPostureCouplingDemotesDefaults: the yolo demotion also applies to the
// built-in default rule set (not just operator-authored rules).
func TestGuardrailsPostureCouplingDemotesDefaults(t *testing.T) {
	cfg := Config{UseMock: true, GuardrailsModel: "m", Posture: PostureYolo}
	specs, usedDefaults := effectiveGuardrailSpecs(cfg)
	if !usedDefaults {
		t.Fatal("no explicit rules: must use the default set")
	}
	for _, s := range specs {
		if s.Mode != string(modelhook.ModeAdvisory) {
			t.Fatalf("under yolo every default rule must be advisory; got %q for %q", s.Mode, s.Match)
		}
	}
}

// TestGuardrailsDefaultModeAdvisory tests the defaultMode override: setting
// defaultMode:advisory downgrades the built-in defaults to observe-only WHILE
// preserving the Shell read-only pre-filter flag (the mode flip must not strip it).
func TestGuardrailsDefaultModeAdvisory(t *testing.T) {
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model", GuardrailsDefaultMode: "advisory"}
	specs, usedDefaults := effectiveGuardrailSpecs(cfg)
	if !usedDefaults || len(specs) != 18 {
		t.Fatalf("model-only with defaultMode must still use the 18-rule contextual default set; usedDefaults=%v n=%d", usedDefaults, len(specs))
	}
	for _, s := range specs {
		if s.Mode != string(modelhook.ModeAdvisory) {
			t.Fatalf("defaultMode:advisory must downgrade defaults to advisory; got %q for %q", s.Mode, s.Match)
		}
		if s.Match == "Shell" && !s.SkipReadOnlyShell {
			t.Fatal("the defaultMode flip must PRESERVE the Shell read-only pre-filter flag")
		}
	}
}

// TestGuardrailsExplicitRulesReplaceDefaults: an explicit rules list replaces the
// built-in defaults entirely — the default Shell rule is gone, and an explicit Shell
// rule does NOT inherit SkipReadOnlyShell (operator Shell rules inspect EVERYTHING).
func TestGuardrailsExplicitRulesReplaceDefaults(t *testing.T) {
	cfg := Config{
		UseMock:         true,
		GuardrailsModel: "checker-model",
		GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}},
	}
	specs, usedDefaults := effectiveGuardrailSpecs(cfg)
	if usedDefaults {
		t.Fatal("explicit rules must REPLACE the defaults (usedDefaults=false)")
	}
	if len(specs) != 1 || specs[0].Match != "Shell" {
		t.Fatalf("explicit rules must be the sole rules; got %+v", specs)
	}
	// The default WebSearch/WebFetch/mcp__* rules are gone (replaced).
	if specs[0].SkipReadOnlyShell {
		t.Fatal("an explicit Shell rule must NOT carry SkipReadOnlyShell — operator Shell rules inspect every command")
	}
}

// a configured guardrail wraps inner (no longer the same pointer).
func TestGuardrailsConfiguredWrapsInner(t *testing.T) {
	inner := hookexec.New(nil)
	llm := mockllm.New()
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model", GuardrailsRules: []GuardrailRule{{Match: "WebFetch", Phases: []string{"post"}}}}
	got := buildGuardrailsHooks(cfg, nil, llm, "mock", "m", inner, nil)
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
