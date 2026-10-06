package permconfig

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// operatorAllowlistYAML is an operator-tier models: block carrying an allowlist (the
// Phase-4 opt-in) plus the operator's own bindings.
const operatorAllowlistYAML = `
models:
  allowlist:
    - fast
    - opus-id
  aliases:
    fast: gpt-4o
  default: gpt-4o
`

// projectModelsYAML is a project-tier .mecatl/settings.yaml models: block re-binding the
// session default + a slot + an alias.
const projectModelsYAML = `
models:
  default: opus-id
  slots:
    plan: opus-id
  aliases:
    myalias: gpt-4o
`

// explicitEnvWith returns a fake env that serves BOTH an explicit operator file and no
// user-global/home (the project file is read through the workspace, not this env).
func explicitEnvWith(path, content string) xdgconfig.ResolveEnv {
	return envWithExplicit(path, content)
}

// newCapturedResolver builds a resolver over an explicit operator file + conventional
// project discovery, with a captured diagnostics buffer.
func newCapturedResolver(t *testing.T, operatorPath, operatorYAML string, trust bool) (*Resolver, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)
	r := newWithEnv(Options{
		Conventional:  true,
		TrustProject:  trust,
		ExplicitFiles: []string{operatorPath},
		Diagnostics:   diag,
	}, explicitEnvWith(operatorPath, operatorYAML))
	if r == nil {
		t.Fatal("resolver should be non-nil")
	}
	return r, &buf
}

// TestProjectModelsHonouredWithinAllowlist pins the Phase-4 happy path (ADR 0030): a
// TRUSTED project's models: bindings are CAPTURED (slots/aliases/default) when an operator
// allowlist exists. The allowlist-MEMBERSHIP cap is enforced in composition; permconfig
// captures the raw bindings within the trust/opt-in gate.
func TestProjectModelsHonouredWithinAllowlist(t *testing.T) {
	r, _ := newCapturedResolver(t, "/etc/mecatl/op.yaml", operatorAllowlistYAML, true)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, projectModelsYAML)

	got := r.ProjectModelBindings(ws)
	if got == nil {
		t.Fatal("a trusted project models: block must be captured when an operator allowlist exists")
		return
	}
	if got.Default != "opus-id" {
		t.Fatalf("project default not captured: %q", got.Default)
	}
	if got.Slots["plan"].Model != "opus-id" {
		t.Fatalf("project slot not captured: %+v", got.Slots)
	}
	if got.Aliases["myalias"] != "gpt-4o" {
		t.Fatalf("project alias not captured: %+v", got.Aliases)
	}
	// The captured block must NEVER carry an allowlist (non-wideable; stripped at capture).
	if len(got.Allowlist) != 0 {
		t.Fatalf("captured project block must not carry an allowlist: %+v", got.Allowlist)
	}
}

// TestProjectModelsByteIdenticalNoAllowlist pins the OPT-IN (ADR 0030 Phase 4): with NO
// operator allowlist, a project models: block stays WARN-ignored (byte-identical to
// pre-Phase-4) — ProjectModelBindings returns nil and the ignore-WARN fires.
func TestProjectModelsByteIdenticalNoAllowlist(t *testing.T) {
	// Operator models: block WITHOUT an allowlist (just slots) — the opt-in is OFF.
	r, buf := newCapturedResolver(t, "/etc/mecatl/op.yaml", operatorModelsYAML, true)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, projectModelsYAML)

	if got := r.ProjectModelBindings(ws); got != nil {
		t.Fatalf("with no operator allowlist, project models must be ignored; got %+v", got)
	}
	if log := buf.String(); !strings.Contains(log, "no operator models.allowlist configured") {
		t.Fatalf("expected the opt-in WARN naming the missing allowlist; got:\n%s", log)
	}
}

// TestProjectModelsIgnoredUntrusted pins the trust gate (ADR 0030 Phase 4): an operator
// allowlist EXISTS but the workspace is UNTRUSTED, so the project models: block is ignored
// with the untrusted-workspace WARN (the SAME trust gate as project allow rules).
func TestProjectModelsIgnoredUntrusted(t *testing.T) {
	r, buf := newCapturedResolver(t, "/etc/mecatl/op.yaml", operatorAllowlistYAML, false)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, projectModelsYAML)

	if got := r.ProjectModelBindings(ws); got != nil {
		t.Fatalf("an untrusted workspace's project models must be ignored; got %+v", got)
	}
	if log := buf.String(); !strings.Contains(log, "untrusted workspace") {
		t.Fatalf("expected the untrusted-workspace WARN; got:\n%s", log)
	}
}

// TestProjectAllowlistKeyStripped pins the non-wideable cap (ADR 0030 Phase 4): a project
// models.allowlist: key is STRIPPED with a WARN — a project cannot widen its own cap. The
// rest of the project block is still honoured (within the operator allowlist).
func TestProjectAllowlistKeyStripped(t *testing.T) {
	const projectWithAllowlist = `
models:
  allowlist:
    - evil-unvetted-model
  default: opus-id
`
	r, buf := newCapturedResolver(t, "/etc/mecatl/op.yaml", operatorAllowlistYAML, true)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, projectWithAllowlist)

	got := r.ProjectModelBindings(ws)
	if got == nil {
		t.Fatal("the rest of the project block must still be honoured after stripping allowlist")
		return
	}
	if len(got.Allowlist) != 0 {
		t.Fatalf("the project allowlist: key must be stripped; got %+v", got.Allowlist)
	}
	if got.Default != "opus-id" {
		t.Fatalf("the non-allowlist bindings must survive the strip; default=%q", got.Default)
	}
	if log := buf.String(); !strings.Contains(log, "IGNORING project-tier models.allowlist") {
		t.Fatalf("expected the allowlist-strip WARN; got:\n%s", log)
	}
}

// TestModelsStrictParseRejectsTypoWithNewKeys pins that the strict parser still rejects a
// typo inside models: now that `default`/`allowlist` are recognised (ADR 0030 Phase 4) —
// a bogus key must still error so a binding map can't be silently dropped.
func TestModelsStrictParseRejectsTypoWithNewKeys(t *testing.T) {
	const bad = `
models:
  default: opus-id
  allowlistt:
    - fast
`
	if _, err := parseYAML([]byte(bad)); err == nil {
		t.Fatal("a typo'd key inside models: (allowlistt) must be a strict parse error even with the new keys")
	} else if !strings.Contains(err.Error(), "models") {
		t.Fatalf("error should name the models subtree; got %v", err)
	}
}

// TestProjectModelBindingsNilWorkspace pins the nil-safe read.
func TestProjectModelBindingsNilWorkspace(t *testing.T) {
	r, _ := newCapturedResolver(t, "/etc/mecatl/op.yaml", operatorAllowlistYAML, true)
	if got := r.ProjectModelBindings(nil); got != nil {
		t.Fatalf("nil workspace must yield nil bindings; got %+v", got)
	}
	var nilResolver *Resolver
	if got := nilResolver.ProjectModelBindings(&countingWS{Workspace: memfs.NewWorkspace("/x")}); got != nil {
		t.Fatalf("nil resolver must yield nil bindings; got %+v", got)
	}
}

// compile-time: countingWS satisfies tool.WorkspaceReader (used above).
var _ tool.WorkspaceReader = (*countingWS)(nil)
