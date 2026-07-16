package permconfig

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

const operatorModelsYAML = `
models:
  slots:
    compaction: cheap
    guardrail: fast
  aliases:
    cheap: gpt-4o-mini
    fast: gpt-4o
`

// TestOperatorModelsFromCLIHonoured pins that an OPERATOR-TIER (CLI explicit)
// models: block is honoured and parsed faithfully (ADR 0030).
func TestOperatorModelsFromCLIHonoured(t *testing.T) {
	env := envWithExplicit("/etc/mecatl/models.yaml", operatorModelsYAML)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/models.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	m := r.OperatorModelSlots()
	if m == nil {
		t.Fatal("operator-tier models must be honoured from the CLI/explicit tier")
		return
	}
	if m.Slots["compaction"] != "cheap" || m.Slots["guardrail"] != "fast" {
		t.Fatalf("slots not parsed faithfully: %+v", m.Slots)
	}
	if m.Aliases["cheap"] != "gpt-4o-mini" || m.Aliases["fast"] != "gpt-4o" {
		t.Fatalf("aliases not parsed faithfully: %+v", m.Aliases)
	}
}

// TestProjectModelsIgnoredWithWarn pins the operator-tier gate (ADR 0030): a
// project-tier models: block is IGNORED with a WARN (re-pointing a slot from a
// project repo is deferred to the allowlist-capped Layer-3 work).
func TestProjectModelsIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, operatorModelsYAML)

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	if r.OperatorModelSlots() != nil {
		t.Fatal("a PROJECT-tier models: block must NOT become operator models")
	}
	if log := buf.String(); !strings.Contains(log, "IGNORING a project-tier models") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}

const operatorRouterYAML = `
models:
  router:
    classifier-slot: cheap
    default-category: small
    categories:
      - name: small
        description: trivial mechanical tasks
        model: gpt-4o-mini
      - name: large
        description: deep reasoning
        model: gpt-5
`

// TestOperatorRouterParsed pins that an operator-tier models.router: subtree is parsed
// faithfully (ADR 0031): the classifier slot, default category, and category list.
func TestOperatorRouterParsed(t *testing.T) {
	env := envWithExplicit("/etc/mecatl/router.yaml", operatorRouterYAML)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/router.yaml"}}, env)
	m := r.OperatorModelPolicy()
	if m == nil || m.Router == nil {
		t.Fatal("operator-tier models.router must be honoured")
	}
	if m.Router.ClassifierSlot != "cheap" || m.Router.DefaultCategory != "small" {
		t.Fatalf("router header not parsed: %+v", m.Router)
	}
	if len(m.Router.Categories) != 2 {
		t.Fatalf("want 2 categories, got %d: %+v", len(m.Router.Categories), m.Router.Categories)
	}
	if m.Router.Categories[1].Name != "large" || m.Router.Categories[1].Model != "gpt-5" {
		t.Fatalf("category not parsed faithfully: %+v", m.Router.Categories[1])
	}
	// The taxonomy above sets no kill-switch, so Disabled defaults false.
	if m.Router.Disabled {
		t.Fatal("router.Disabled must default false when the disabled: key is absent")
	}
}

// TestOperatorRouterDisabledParsed pins the ADR 0042 YAML kill-switch: `disabled: true`
// inside models.router: parses to RouterSection.Disabled (mirroring guardrails: disabled).
func TestOperatorRouterDisabledParsed(t *testing.T) {
	const yamlCfg = `
models:
  router:
    disabled: true
    categories:
      - name: small
        description: trivial tasks
        model: gpt-4o-mini
`
	env := envWithExplicit("/etc/mecatl/router-off.yaml", yamlCfg)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/router-off.yaml"}}, env)
	m := r.OperatorModelPolicy()
	if m == nil || m.Router == nil {
		t.Fatal("operator-tier models.router must be honoured")
	}
	if !m.Router.Disabled {
		t.Fatal("models.router.disabled: true must parse to RouterSection.Disabled")
	}
	if len(m.Router.Categories) != 1 {
		t.Fatalf("the taxonomy must still parse when disabled (disabled is the kill-switch, not a parse drop); got %d", len(m.Router.Categories))
	}
}

// TestProjectRouterStrippedWithWarn pins ADR 0031: a project-tier models.router: is
// OPERATOR-TIER ONLY — stripped with a WARN, never honoured. The operator allowlist is
// present (so the project block is otherwise opt-in eligible and trusted), proving the
// router strip is its OWN gate, not a side effect of the opt-in.
func TestProjectRouterStrippedWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	const operatorAllowlist = `
models:
  allowlist:
    - gpt-4o-mini
`
	const projectRouter = `
models:
  router:
    categories:
      - name: small
        description: x
        model: gpt-4o-mini
`
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, projectRouter)

	env := envWithExplicit("/etc/mecatl/op.yaml", operatorAllowlist)
	r := newWithEnv(Options{Conventional: true, TrustProject: true, ExplicitFiles: []string{"/etc/mecatl/op.yaml"}, Diagnostics: diag}, env)
	_ = r.Resolve(context.Background(), ws)

	proj := r.ProjectModelBindings(ws)
	if proj != nil && proj.Router != nil {
		t.Fatal("a project-tier models.router must NEVER be honoured (operator-tier only)")
	}
	if log := buf.String(); !strings.Contains(log, "IGNORING project-tier models.router") {
		t.Fatalf("expected a router-strip WARN; got:\n%s", log)
	}
}

// TestRouterStrictUnknownKeyRejected pins the strict parse of the router subtree.
func TestRouterStrictUnknownKeyRejected(t *testing.T) {
	const bad = `
models:
  router:
    categoriez:
      - name: x
`
	if _, err := parseYAML([]byte(bad)); err == nil {
		t.Fatal("an unknown key inside models.router: must be a strict parse error")
	}
}

// TestModelsStrictUnknownKeyRejected pins the strict parse (ADR 0030): a typo'd key
// inside the models: subtree is a parse error (so a binding map can't be silently
// dropped), surfaced through the per-file skip.
func TestModelsStrictUnknownKeyRejected(t *testing.T) {
	const bad = `
models:
  slotz:
    compaction: cheap
`
	if _, err := parseYAML([]byte(bad)); err == nil {
		t.Fatal("an unknown key inside models: must be a strict parse error")
	} else if !strings.Contains(err.Error(), "models") {
		t.Fatalf("error should name the models subtree; got %v", err)
	}
}

// TestModelsDefaultProviderParsed pins that models.default_provider (Wave 2b) parses
// faithfully from the operator tier and rides the SAME ModelsSection as models.default,
// exposed via OperatorModelPolicy().DefaultProvider. An unknown key inside models:
// (e.g. default_providr) is a strict-parse error, so a typo cannot silently disable
// the operator's default-provider override.
func TestModelsDefaultProviderParsed(t *testing.T) {
	const yamlCfg = `
models:
  default_provider: toolhive
`
	env := envWithExplicit("/etc/mecatl/dp.yaml", yamlCfg)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/dp.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	m := r.OperatorModelPolicy()
	if m == nil {
		t.Fatal("operator-tier models must be honoured from the CLI/explicit tier")
	}
	if m.DefaultProvider != "toolhive" {
		t.Fatalf("models.default_provider not parsed faithfully: got %q, want %q", m.DefaultProvider, "toolhive")
	}

	// A typo'd key inside models: is a strict-parse error (the strictFields guard).
	const bad = `
models:
  default_providr: toolhive
`
	if _, err := parseYAML([]byte(bad)); err == nil {
		t.Fatal("an unknown key (default_providr) inside models: must be a strict parse error")
	}
}

// TestProjectDefaultProviderStrippedWithWarn pins that a project-tier
// models.default_provider: is OPERATOR-TIER ONLY — stripped with a WARN, never honoured
// (the same operator-only captureModels discipline as the allowlist/router). The operator
// allowlist is present (so the project block is otherwise opt-in eligible and trusted),
// proving the default_provider strip is its OWN gate, not a side effect of the opt-in.
// The captured project bindings must NOT carry DefaultProvider (it is dropped by omission).
func TestProjectDefaultProviderStrippedWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	const operatorAllowlist = `
models:
  allowlist:
    - gpt-4o-mini
`
	const projectDefaultProvider = `
models:
  default_provider: toolhive
  default: gpt-4o-mini
`
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, projectDefaultProvider)

	env := envWithExplicit("/etc/mecatl/op.yaml", operatorAllowlist)
	r := newWithEnv(Options{Conventional: true, TrustProject: true, ExplicitFiles: []string{"/etc/mecatl/op.yaml"}, Diagnostics: diag}, env)
	_ = r.Resolve(context.Background(), ws)

	proj := r.ProjectModelBindings(ws)
	if proj != nil && proj.DefaultProvider != "" {
		t.Fatalf("a project-tier models.default_provider must NEVER be honoured (operator-tier only); got %q", proj.DefaultProvider)
	}
	if log := buf.String(); !strings.Contains(log, "IGNORING project-tier models.default_provider") {
		t.Fatalf("expected a default_provider-strip WARN; got:\n%s", log)
	}
}
