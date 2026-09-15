package app

import (
	"context"
	"path/filepath"
	"testing"
)

// TestProjectModelBindingsE2E is the offline end-to-end (ADR 0030 Phase 4): app.Build over
// a memfs-less tempdir workspace seeding .mecatl/settings.yaml project models, an operator
// allowlist supplied via PermissionConfigs (the explicit/CLI tier), TrustProject:true, and
// a mock provider. It asserts the build-once INFO lines show the ACCEPTED in-cap binding
// and the DROPPED out-of-cap binding — the resolved compaction/plan/default model surface.
func TestProjectModelBindingsE2E(t *testing.T) {
	ws := t.TempDir()
	// Project re-binds: plan→reasoning (allowlisted → gpt-4o), compaction→rogue (NOT
	// allowlisted → dropped), default→opus-id (allowlisted).
	writeProjectFile(t, filepath.Join(ws, ".mecatl", "settings.yaml"), `
models:
  default: opus-id
  slots:
    plan: reasoning
    compaction: rogue-unvetted-id
`)
	opPath := filepath.Join(t.TempDir(), "operator.yaml")
	writeProjectFile(t, opPath, `
models:
  allowlist:
    - reasoning
    - opus-id
  aliases:
    reasoning: gpt-4o
    cheap: gpt-4o-mini
  default: gpt-4o-mini
`)

	diag := &capturingDiag{}
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:               ws,
		Model:                   "", // let the operator default + project default decide.
		UseMock:                 true,
		Diagnostics:             diag,
		PermissionsConventional: true,
		permConfigEnv:           isolatedPermConfigEnv(t),
		TrustProject:            true,
		PermissionConfigs:       []string{opPath},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// The in-cap plan slot was ACCEPTED.
	if !diag.has("models.slots: project binding ACCEPTED") {
		t.Fatalf("expected an ACCEPTED INFO for the in-cap plan slot; lines:\n%v", diag.lines)
	}
	// The plan slot's build-once "ACTIVE" narration reflects the project value (gpt-4o).
	if !diag.has("plan-mode turns run on the plan model") {
		t.Fatalf("expected the plan-slot ACTIVE narration; lines:\n%v", diag.lines)
	}
	// The out-of-cap compaction slot was DROPPED with exactly one WARN.
	if n := diag.count("models.slots: project binding DROPPED"); n != 1 {
		t.Fatalf("expected exactly ONE DROPPED WARN for the out-of-cap compaction slot, got %d; lines:\n%v", n, diag.lines)
	}
	// The project default within cap re-bound cfg.Model (the resolved default echo is opus-id).
	rm := built.Service.ResolvedModel("")
	if rm.ModelID != "opus-id" {
		t.Fatalf("project default within cap should re-bind the resolved default model; got %q", rm.ModelID)
	}
}

// TestProjectModelBindingsE2EByteIdenticalNoAllowlist is the e2e regression guard: an
// operator models: block with NO allowlist leaves a project models: block WARN-ignored,
// and the resolved default model is the operator/registry default — NOT the project value.
func TestProjectModelBindingsE2EByteIdenticalNoAllowlist(t *testing.T) {
	ws := t.TempDir()
	writeProjectFile(t, filepath.Join(ws, ".mecatl", "settings.yaml"), `
models:
  default: opus-id
`)
	opPath := filepath.Join(t.TempDir(), "operator.yaml")
	writeProjectFile(t, opPath, `
models:
  slots:
    compaction: cheap
  aliases:
    cheap: gpt-4o-mini
`)

	diag := &capturingDiag{}
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:               ws,
		Model:                   "operator-default-model",
		UseMock:                 true,
		Diagnostics:             diag,
		PermissionsConventional: true,
		permConfigEnv:           isolatedPermConfigEnv(t),
		TrustProject:            true,
		PermissionConfigs:       []string{opPath},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if diag.has("project binding ACCEPTED") {
		t.Fatalf("with no operator allowlist, NO project binding may be accepted; lines:\n%v", diag.lines)
	}
	if !diag.has("no operator models.allowlist configured") {
		t.Fatalf("expected the opt-in WARN naming the missing allowlist; lines:\n%v", diag.lines)
	}
	if rm := built.Service.ResolvedModel(""); rm.ModelID != "operator-default-model" {
		t.Fatalf("with no allowlist the project default must be ignored; resolved default = %q", rm.ModelID)
	}
}
