package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// writeProjectFile writes content to an absolute path (creating parent dirs). Distinct
// from subprovider_test.go's dir/name writeFile.
func writeProjectFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// projectFoldHarness builds a Config whose permResolver is a REAL permconfig.Resolver over
// an explicit operator file (operatorYAML) and a project .mecatl/settings.yaml
// (projectYAML) under a tempdir workspace. trust sets cfg.TrustProject. The returned cfg
// also carries a capturingDiag so a test can count WARN/INFO lines.
func projectFoldHarness(t *testing.T, operatorYAML, projectYAML string, trust bool) (Config, *capturingDiag) {
	t.Helper()
	ws := t.TempDir()
	opPath := filepath.Join(t.TempDir(), "operator.yaml")
	writeProjectFile(t, opPath, operatorYAML)
	if projectYAML != "" {
		writeProjectFile(t, filepath.Join(ws, ".mecatl", "settings.yaml"), projectYAML)
	}
	diag := &capturingDiag{}
	res := permconfig.New(permconfig.Options{
		Conventional:  true,
		TrustProject:  trust,
		ExplicitFiles: []string{opPath},
		Diagnostics:   diag,
	})
	cfg := Config{
		Workspace:    ws,
		TrustProject: trust,
		Diagnostics:  diag,
		permResolver: res,
	}
	return cfg, diag
}

const opAllowlistYAML = `
models:
  allowlist:
    - reasoning
    - opus-id
  aliases:
    reasoning: gpt-4o
    cheap: gpt-4o-mini
  slots:
    plan: cheap
  default: gpt-4o-mini
`

// TestFoldProjectModelBindingsWithinCap pins the Phase-4 happy path (ADR 0030): a TRUSTED
// project re-binds default/slot/alias to ALLOWLISTED values and they are APPLIED.
func TestFoldProjectModelBindingsWithinCap(t *testing.T) {
	const projectYAML = `
models:
  default: opus-id
  slots:
    plan: reasoning
  aliases:
    myalias: opus-id
`
	cfg, _ := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	// foldOperatorModelSlots runs first in Build; mirror it so the alias/slot maps hold
	// the operator-YAML layer the cap canonicalizes through.
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini" // the operator default (post reg.ResolvedDefaultModel()).
	cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))

	if cfg.Model != "opus-id" {
		t.Fatalf("project default within cap should re-bind cfg.Model; got %q", cfg.Model)
	}
	if cfg.ModelSlots[slotPlan] != "gpt-4o" { // "reasoning" alias → gpt-4o (allowlisted)
		t.Fatalf("project plan slot within cap should apply; got %q", cfg.ModelSlots[slotPlan])
	}
	if cfg.ModelAliases["myalias"] != "opus-id" {
		t.Fatalf("project alias within cap should apply; got %q", cfg.ModelAliases["myalias"])
	}
}

// TestFoldProjectModelBindingsDroppedOutsideCap pins the cap (resolve-then-check): a
// project binding resolving OUTSIDE the allowlist is DROPPED with exactly ONE build-once
// WARN, and the operator/default value is kept.
func TestFoldProjectModelBindingsDroppedOutsideCap(t *testing.T) {
	const projectYAML = `
models:
  slots:
    compaction: not-allowlisted-id
`
	cfg, diag := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini"
	before := cfg.ModelSlots[slotCompaction]
	cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))

	if cfg.ModelSlots[slotCompaction] != before {
		t.Fatalf("an out-of-cap project slot must be dropped (kept %q), got %q", before, cfg.ModelSlots[slotCompaction])
	}
	if n := diag.count("models.slots: project binding DROPPED"); n != 1 {
		t.Fatalf("expected exactly ONE build-once dropped WARN, got %d (lines: %v)", n, diag.lines)
	}
}

// TestFoldProjectDefaultRebindAndDrop pins the default axis both ways: a default within
// the cap re-binds cfg.Model; a default outside the cap is dropped (cfg.Model unchanged).
func TestFoldProjectDefaultRebindAndDrop(t *testing.T) {
	t.Run("within cap re-binds cfg.Model", func(t *testing.T) {
		cfg, _ := projectFoldHarness(t, opAllowlistYAML, "models:\n  default: opus-id\n", true)
		cfg = foldOperatorModelSlots(cfg)
		cfg.Model = "gpt-4o-mini"
		cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))
		if cfg.Model != "opus-id" {
			t.Fatalf("project default within cap must re-bind cfg.Model; got %q", cfg.Model)
		}
	})
	t.Run("outside cap keeps the operator default", func(t *testing.T) {
		cfg, _ := projectFoldHarness(t, opAllowlistYAML, "models:\n  default: rogue-id\n", true)
		cfg = foldOperatorModelSlots(cfg)
		cfg.Model = "gpt-4o-mini"
		cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))
		if cfg.Model != "gpt-4o-mini" {
			t.Fatalf("project default outside cap must be dropped; cfg.Model = %q", cfg.Model)
		}
	})
}

// TestFoldProjectPlanSlotEnablesModeNeedsEngine pins that a project plan-slot binding
// within the cap flows into modeNeedsEngine (non-nil) AND the resolved plan model is the
// project value — the allowlist applies to the Phase-3 plan slot too.
func TestFoldProjectPlanSlotEnablesModeNeedsEngine(t *testing.T) {
	const projectYAML = `
models:
  slots:
    plan: opus-id
`
	cfg, _ := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini"
	cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))

	planModel, ok := resolveSlotModel(cfg, slotPlan, cfg.Model)
	if !ok || planModel != "opus-id" {
		t.Fatalf("project plan slot should resolve to opus-id; got (%q, %v)", planModel, ok)
	}
	if fn := modeNeedsEngine(cfg); fn == nil {
		t.Fatal("a project plan slot differing from cfg.Model must make modeNeedsEngine non-nil")
	}
}

// TestFoldProjectModelBindingsByteIdenticalNoAllowlist is the regression guard: with NO
// operator allowlist, foldProjectModelBindings is a NO-OP — cfg deep-equals its input and
// the diag is silent (byte-identical to pre-Phase-4).
func TestFoldProjectModelBindingsByteIdenticalNoAllowlist(t *testing.T) {
	const operatorNoAllowlist = `
models:
  slots:
    compaction: cheap
  aliases:
    cheap: gpt-4o-mini
`
	const projectYAML = `
models:
  default: opus-id
  slots:
    plan: opus-id
`
	cfg, diag := projectFoldHarness(t, operatorNoAllowlist, projectYAML, true)
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini"
	diag.lines = nil // ignore fold/resolve narration; measure ONLY the project fold.

	in := cfg
	inSlots := cloneStrMap(cfg.ModelSlots)
	inAliases := cloneStrMap(cfg.ModelAliases)
	out := foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))

	if out.Model != in.Model || !reflect.DeepEqual(out.ModelSlots, inSlots) || !reflect.DeepEqual(out.ModelAliases, inAliases) {
		t.Fatalf("no-allowlist fold must be a no-op: Model %q→%q slots %v→%v aliases %v→%v",
			in.Model, out.Model, inSlots, out.ModelSlots, inAliases, out.ModelAliases)
	}
	if len(diag.lines) != 0 {
		t.Fatalf("no-allowlist fold must be silent; got %v", diag.lines)
	}
}

// TestFoldProjectModelBindingsByteIdenticalNoResolver pins the nil-permResolver fast path.
func TestFoldProjectModelBindingsByteIdenticalNoResolver(t *testing.T) {
	diag := &capturingDiag{}
	cfg := Config{Model: "gpt-5", TrustProject: true, Diagnostics: diag} // no permResolver
	out := foldProjectModelBindings(cfg, cliModelKeys{})
	if out.Model != "gpt-5" || len(diag.lines) != 0 {
		t.Fatalf("nil-permResolver fold must be a silent no-op; Model=%q lines=%v", out.Model, diag.lines)
	}
}

// TestPrecedenceProjectOverridesOperatorYAML pins CLI > project > operator-YAML: a project
// slot binding within the cap OVERRIDES the operator-YAML binding for the same key.
func TestPrecedenceProjectOverridesOperatorYAML(t *testing.T) {
	// Operator YAML binds plan=cheap (→gpt-4o-mini); project binds plan=reasoning (→gpt-4o).
	const projectYAML = `
models:
  slots:
    plan: reasoning
`
	cfg, _ := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	cfg = foldOperatorModelSlots(cfg)
	if cfg.ModelSlots[slotPlan] != "cheap" {
		t.Fatalf("operator-YAML should set plan=cheap first; got %q", cfg.ModelSlots[slotPlan])
	}
	cfg.Model = "gpt-4o-mini"
	cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))
	if cfg.ModelSlots[slotPlan] != "gpt-4o" { // reasoning → gpt-4o
		t.Fatalf("project plan binding must OVERRIDE operator-YAML; got %q", cfg.ModelSlots[slotPlan])
	}
}

// TestPrecedenceCLIBeatsProject pins CLI > project: an operator --model-slot binding for a
// key SURVIVES a project override (the CLI-set key is SKIPPED by the project fold).
func TestPrecedenceCLIBeatsProject(t *testing.T) {
	const projectYAML = `
models:
  slots:
    plan: reasoning
`
	cfg, _ := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	// Simulate a CLI --model-slot plan=opus-id BEFORE the operator-YAML fold.
	cfg.ModelSlots = map[string]string{slotPlan: "opus-id"}
	cliKeys := captureCLIModelKeys(cfg) // capture BEFORE foldOperatorModelSlots, as Build does
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini"
	cfg = foldProjectModelBindings(cfg, cliKeys)
	if cfg.ModelSlots[slotPlan] != "opus-id" {
		t.Fatalf("a CLI-set plan slot must SURVIVE the project override; got %q", cfg.ModelSlots[slotPlan])
	}
}

// TestPrecedenceCLIModelBeatsProjectDefault pins CLI --model > project default.
func TestPrecedenceCLIModelBeatsProjectDefault(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opAllowlistYAML, "models:\n  default: opus-id\n", true)
	cfg.Model = "cli-model" // a CLI --model value, present BEFORE the captures.
	cliKeys := captureCLIModelKeys(cfg)
	cfg = foldOperatorModelSlots(cfg)
	cfg = foldProjectModelBindings(cfg, cliKeys)
	if cfg.Model != "cli-model" {
		t.Fatalf("a CLI --model must beat a project default; got %q", cfg.Model)
	}
}

func cloneStrMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// --- Fix #1: operator-YAML models.default precedence ---

// opDefaultYAML is an operator-tier models: block whose `default:` is an UNCAPPED
// operator binding (note default-model is NOT in the allowlist — the operator's own
// default must apply regardless, the allowlist caps PROJECT only).
const opDefaultYAML = `
models:
  allowlist:
    - reasoning
    - opus-id
  aliases:
    reasoning: gpt-4o
  default: operator-default-model
`

// applyDefaultPrecedence mirrors the Build default-precedence ordering: capture CLI keys,
// fold operator slots/aliases, apply the registry default ONLY when cfg.Model is empty,
// then the operator-YAML default rung, then the capped project bindings. registryDefault
// is what reg.ResolvedDefaultModel() would yield.
func applyDefaultPrecedence(cfg Config, registryDefault string) Config {
	cliKeys := captureCLIModelKeys(cfg)
	cfg = foldOperatorModelSlots(cfg)
	if cfg.Model == "" {
		cfg.Model = registryDefault
	}
	cfg = foldOperatorModelDefault(cfg, cliKeys)
	cfg = foldProjectModelBindings(cfg, cliKeys)
	return cfg
}

// TestOperatorYAMLDefaultApplied pins fix #1: an operator settings.yaml models.default
// re-binds cfg.Model OVER the registry default when no --model/project default is set.
func TestOperatorYAMLDefaultApplied(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opDefaultYAML, "", true) // no project file
	cfg = applyDefaultPrecedence(cfg, "registry-default")
	if cfg.Model != "operator-default-model" {
		t.Fatalf("operator-YAML default should re-bind cfg.Model over the registry default; got %q", cfg.Model)
	}
}

// TestOperatorYAMLDefaultUncapped pins fix #1: the operator's OWN default is NOT capped by
// the allowlist (operator-default-model is absent from the allowlist, yet applies).
func TestOperatorYAMLDefaultUncapped(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opDefaultYAML, "", true)
	cfg = applyDefaultPrecedence(cfg, "registry-default")
	if cfg.Model != "operator-default-model" {
		t.Fatalf("the operator's OWN default must be UNCAPPED (allowlist caps PROJECT only); got %q", cfg.Model)
	}
}

// TestProjectDefaultOverridesOperatorYAMLDefaultWithinCap pins fix #1: a capped project
// default overrides the operator-YAML default.
func TestProjectDefaultOverridesOperatorYAMLDefaultWithinCap(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opDefaultYAML, "models:\n  default: opus-id\n", true)
	cfg = applyDefaultPrecedence(cfg, "registry-default")
	if cfg.Model != "opus-id" { // opus-id is allowlisted ⇒ accepted, overriding operator default.
		t.Fatalf("a capped project default must override the operator-YAML default; got %q", cfg.Model)
	}
}

// TestProjectDefaultOutsideCapKeepsOperatorYAMLDefault pins fix #1: a project default
// OUTSIDE the cap is dropped, leaving the operator-YAML default in place.
func TestProjectDefaultOutsideCapKeepsOperatorYAMLDefault(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opDefaultYAML, "models:\n  default: rogue-id\n", true)
	cfg = applyDefaultPrecedence(cfg, "registry-default")
	if cfg.Model != "operator-default-model" {
		t.Fatalf("an out-of-cap project default must be dropped, keeping the operator-YAML default; got %q", cfg.Model)
	}
}

// TestCLIModelBeatsOperatorYAMLDefault pins fix #1: a CLI --model wins over the
// operator-YAML default.
func TestCLIModelBeatsOperatorYAMLDefault(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opDefaultYAML, "", true)
	cfg.Model = "cli-model" // a CLI --model present BEFORE the captures.
	cfg = applyDefaultPrecedence(cfg, "registry-default")
	if cfg.Model != "cli-model" {
		t.Fatalf("a CLI --model must beat the operator-YAML default; got %q", cfg.Model)
	}
}

// TestOperatorYAMLDefaultByteIdenticalWhenAbsent pins that with no operator models.default
// the registry default survives (no operator-default rung fires).
func TestOperatorYAMLDefaultByteIdenticalWhenAbsent(t *testing.T) {
	const opNoDefault = `
models:
  allowlist:
    - opus-id
`
	cfg, _ := projectFoldHarness(t, opNoDefault, "", true)
	cfg = applyDefaultPrecedence(cfg, "registry-default")
	if cfg.Model != "registry-default" {
		t.Fatalf("with no operator models.default the registry default must survive; got %q", cfg.Model)
	}
}

// --- Fix #2: alias-laundering defense ---

// TestProjectAliasLaunderingDropped is the security-critical guard (QA M1): a TRUSTED
// project tries to launder a NON-allowlisted id by defining its OWN alias to it and binding
// a slot through that alias. BOTH the alias and the slot must be DROPPED, the non-allowlisted
// id must reach NEITHER cfg.ModelAliases NOR cfg.ModelSlots, and two build-once WARNs fire.
// This pins the ordering: the allowlist is canonicalized through the OPERATOR-merged alias
// map BEFORE project aliases are merged, and slots are checked against that same set — so a
// project alias can never widen the cap. A refactor that canonicalized after merging project
// aliases, or merged slots before aliases, would silently reopen the laundering path.
func TestProjectAliasLaunderingDropped(t *testing.T) {
	const projectYAML = `
models:
  aliases:
    sneaky: rogue-unvetted-id
  slots:
    compaction: sneaky
`
	cfg, diag := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini"
	cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))

	if cfg.ModelAliases["sneaky"] == "rogue-unvetted-id" {
		t.Fatalf("the laundering alias must be DROPPED, not merged: %v", cfg.ModelAliases)
	}
	if cfg.ModelSlots[slotCompaction] == "rogue-unvetted-id" || cfg.ModelSlots[slotCompaction] == "sneaky" {
		t.Fatalf("the laundering slot must be DROPPED, not bound: %v", cfg.ModelSlots)
	}
	// The non-allowlisted id must appear NOWHERE on cfg.
	for k, v := range cfg.ModelAliases {
		if v == "rogue-unvetted-id" {
			t.Fatalf("rogue id leaked into cfg.ModelAliases[%q]", k)
		}
	}
	for k, v := range cfg.ModelSlots {
		if v == "rogue-unvetted-id" {
			t.Fatalf("rogue id leaked into cfg.ModelSlots[%q]", k)
		}
	}
	if n := diag.count("project binding DROPPED"); n != 2 {
		t.Fatalf("expected exactly TWO DROPPED WARNs (the alias + the slot), got %d; lines: %v", n, diag.lines)
	}
}

// --- Fix #4: CLI > project > operator-YAML ordering guards (all three tiers, same key) ---

// TestPrecedenceCombinedTiersSameSlotCLIWins exercises ALL THREE tiers for the SAME slot
// key simultaneously (CLI sets plan, operator-YAML sets plan, project tries to override
// within cap) and asserts the CLI value wins — the structural guard at the precedence seam.
func TestPrecedenceCombinedTiersSameSlotCLIWins(t *testing.T) {
	// Operator-YAML opAllowlistYAML binds plan=cheap; project binds plan=reasoning; CLI sets
	// plan=opus-id. All three resolve to allowlisted/known ids, so only precedence decides.
	const projectYAML = `
models:
  slots:
    plan: reasoning
`
	cfg, _ := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	cfg.ModelSlots = map[string]string{slotPlan: "opus-id"} // CLI --model-slot plan=opus-id
	cliKeys := captureCLIModelKeys(cfg)                     // captured BEFORE the operator-YAML fold, as Build does
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini"
	cfg = foldProjectModelBindings(cfg, cliKeys)
	if cfg.ModelSlots[slotPlan] != "opus-id" {
		t.Fatalf("CLI must win over BOTH operator-YAML and project for the same slot; got %q", cfg.ModelSlots[slotPlan])
	}
}

// TestPrecedenceCombinedTiersOperatorYAMLAndProject is the sibling: only operator-YAML +
// project set the SAME slot key (no CLI) → project (capped) wins over operator-YAML.
func TestPrecedenceCombinedTiersOperatorYAMLAndProject(t *testing.T) {
	const projectYAML = `
models:
  slots:
    plan: reasoning
`
	cfg, _ := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	cliKeys := captureCLIModelKeys(cfg) // no CLI slot ⇒ empty CLI set
	cfg = foldOperatorModelSlots(cfg)
	if cfg.ModelSlots[slotPlan] != "cheap" {
		t.Fatalf("operator-YAML should set plan=cheap first; got %q", cfg.ModelSlots[slotPlan])
	}
	cfg.Model = "gpt-4o-mini"
	cfg = foldProjectModelBindings(cfg, cliKeys)
	if cfg.ModelSlots[slotPlan] != "gpt-4o" { // reasoning → gpt-4o (allowlisted)
		t.Fatalf("project (capped) must win over operator-YAML when no CLI key set; got %q", cfg.ModelSlots[slotPlan])
	}
}

// --- Fix #5: composition trust-gate (S1) + multi-accept (S2) ---

// TestFoldProjectModelBindingsTrustGateIndependent pins fix #5 S1: foldProjectModelBindings'
// OWN !cfg.TrustProject guard fires (cfg deep-equals input, diag silent) — distinct from the
// permconfig-layer nil-return. An operator allowlist EXISTS and a project block exists, but
// the composition fold is fed cfg.TrustProject=false, so it must short-circuit before any merge.
func TestFoldProjectModelBindingsTrustGateIndependent(t *testing.T) {
	const projectYAML = `
models:
  default: opus-id
  slots:
    plan: reasoning
`
	// Build the resolver TRUSTED (so the permconfig layer DOES capture the project block),
	// then flip cfg.TrustProject=false so ONLY the composition guard can stop it.
	cfg, diag := projectFoldHarness(t, opAllowlistYAML, projectYAML, true)
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini"
	cfg.TrustProject = false // exercise the composition fold's own gate
	diag.lines = nil

	inModel := cfg.Model
	inSlots := cloneStrMap(cfg.ModelSlots)
	inAliases := cloneStrMap(cfg.ModelAliases)
	out := foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))

	if out.Model != inModel || !reflect.DeepEqual(out.ModelSlots, inSlots) || !reflect.DeepEqual(out.ModelAliases, inAliases) {
		t.Fatalf("the composition !TrustProject guard must short-circuit (no merge): Model %q→%q slots %v→%v aliases %v→%v",
			inModel, out.Model, inSlots, out.ModelSlots, inAliases, out.ModelAliases)
	}
	if len(diag.lines) != 0 {
		t.Fatalf("the trust-gated fold must be silent; got %v", diag.lines)
	}
}

// TestFoldProjectModelBindingsMultiAccept pins fix #5 S2: multiple in-cap bindings (2 slots
// + 1 alias) ALL land on cfg, and the ACCEPTED count == N (not just one accept line exists).
func TestFoldProjectModelBindingsMultiAccept(t *testing.T) {
	// Allowlist all three target ids so each binding is in-cap.
	const opMulti = `
models:
  allowlist:
    - reasoning
    - opus-id
    - gpt-4o-mini
  aliases:
    reasoning: gpt-4o
`
	const projectYAML = `
models:
  slots:
    plan: reasoning
    compaction: gpt-4o-mini
  aliases:
    myalias: opus-id
`
	cfg, diag := projectFoldHarness(t, opMulti, projectYAML, true)
	cfg = foldOperatorModelSlots(cfg)
	cfg.Model = "gpt-4o-mini"
	cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))

	if cfg.ModelSlots[slotPlan] != "gpt-4o" {
		t.Fatalf("plan slot not applied: %v", cfg.ModelSlots)
	}
	if cfg.ModelSlots[slotCompaction] != "gpt-4o-mini" {
		t.Fatalf("compaction slot not applied: %v", cfg.ModelSlots)
	}
	if cfg.ModelAliases["myalias"] != "opus-id" {
		t.Fatalf("alias not applied: %v", cfg.ModelAliases)
	}
	if n := diag.count("project binding ACCEPTED"); n != 3 {
		t.Fatalf("expected exactly THREE ACCEPTED lines (2 slots + 1 alias), got %d; lines: %v", n, diag.lines)
	}
}

// --- Fix #7 (optional): canonical-allowlist fail-closed entry ---

// TestCanonicalAllowlistFailClosedEntry pins QA O1: an allowlist with an unresolvable bare
// token + a real id forms a cap of ONLY the real id (the bogus entry never widens it).
func TestCanonicalAllowlistFailClosedEntry(t *testing.T) {
	cfg := Config{ModelAliases: map[string]string{}}
	// "bogustoken" has no separator and is not a known alias ⇒ lookupModelAlias returns
	// known=false (genuinely unresolvable); "real-id-here" contains separators ⇒ a literal id.
	set := canonicalAllowlist(cfg, []string{"bogustoken", "real-id-here"})
	if _, ok := set["real-id-here"]; !ok {
		t.Fatalf("a resolvable id must form the cap; got %v", set)
	}
	if len(set) != 1 {
		t.Fatalf("an unresolvable bare token must NOT widen the cap; set = %v", set)
	}
}

// --- Wave 2b: operator-YAML models.default_provider fold ---

// opDefaultProviderYAML is an operator-tier models: block carrying default_provider
// (the --default-provider YAML twin) so foldOperatorDefaultProvider can be exercised
// against a real permconfig.Resolver (the SAME path Build reads it through).
const opDefaultProviderYAML = `
models:
  default_provider: toolhive
`

// TestFoldOperatorDefaultProviderCLIWins pins the CLI-wins precedence: when
// DefaultProviderFlagSet is true the YAML value is NOT read (the explicit flag wins).
func TestFoldOperatorDefaultProviderCLIWins(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opDefaultProviderYAML, "", true)
	cfg.DefaultProvider = "openrouter"
	cfg.DefaultProviderFlagSet = true
	got := foldOperatorDefaultProvider(cfg)
	if got.DefaultProvider != "openrouter" {
		t.Fatalf("CLI --default-provider must win over the YAML value; got %q, want %q", got.DefaultProvider, "openrouter")
	}
}

// TestFoldOperatorDefaultProviderAppliesYAML pins the YAML-applies path: with no
// explicit flag, the operator-YAML models.default_provider: folds onto
// cfg.DefaultProvider.
func TestFoldOperatorDefaultProviderAppliesYAML(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opDefaultProviderYAML, "", true)
	got := foldOperatorDefaultProvider(cfg)
	if got.DefaultProvider != "toolhive" {
		t.Fatalf("operator-YAML models.default_provider must fold onto cfg.DefaultProvider; got %q, want %q", got.DefaultProvider, "toolhive")
	}
}

// TestFoldOperatorDefaultProviderNoOpWhenAbsent pins the no-op: with no
// models.default_provider key (and no flag), the fold is byte-identical (the
// zero-value default, "" — the ladder's preferred default wins).
func TestFoldOperatorDefaultProviderNoOpWhenAbsent(t *testing.T) {
	cfg, _ := projectFoldHarness(t, opAllowlistYAML, "", true) // no default_provider key
	got := foldOperatorDefaultProvider(cfg)
	if got.DefaultProvider != "" {
		t.Fatalf("with no YAML default_provider and no flag, the fold must be a no-op; got %q, want empty", got.DefaultProvider)
	}
}

// TestFoldOperatorDefaultProviderNoResolverNoOp pins the nil-resolver fast path.
func TestFoldOperatorDefaultProviderNoResolverNoOp(t *testing.T) {
	got := foldOperatorDefaultProvider(Config{DefaultProvider: "openai"})
	if got.DefaultProvider != "openai" {
		t.Fatalf("nil-resolver fold must be a no-op; got %q, want %q", got.DefaultProvider, "openai")
	}
}
