package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// read_readroots_test.go is the tool-level e2e of the activated-skill read-root
// carve-out: over a REAL osfs workspace constructed with WithReadRoots (exactly
// how the composition root builds every production workspace), the Read tool
// serves a skill's bundled file by ABSOLUTE path — line numbers and all — while
// Edit on the same absolute path stays refused (the carve-out is read-only).

// newSkillReadRootWorkspace builds an osfs workspace plus an out-of-workspace
// skill dir registered as a read root, returning the workspace and the absolute
// path of a bundled reference file inside the skill dir.
func newSkillReadRootWorkspace(t *testing.T) (*osfs.Workspace, string) {
	t.Helper()
	skillDir := filepath.Join(t.TempDir(), "skills", "demo", "references")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	bundled := filepath.Join(skillDir, "guide.md")
	if err := os.WriteFile(bundled, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write bundled file: %v", err)
	}
	ws, err := osfs.NewWorkspace(t.TempDir(), osfs.WithReadRoots(filepath.Dir(skillDir)))
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	canonical, err := osfs.ResolveRoot(bundled)
	if err != nil {
		t.Fatalf("resolve bundled path: %v", err)
	}
	return ws, canonical
}

func TestReadToolAbsoluteSkillPath(t *testing.T) {
	ws, bundled := newSkillReadRootWorkspace(t)

	res := exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": bundled}), ws)
	if res.IsError {
		t.Fatalf("Read(absolute skill path) errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "1\tline one") || !strings.Contains(res.Content, "2\tline two") {
		t.Errorf("Read output missing line-numbered content, got %q", res.Content)
	}
}

func TestEditToolAbsoluteSkillPathRefused(t *testing.T) {
	ws, bundled := newSkillReadRootWorkspace(t)

	// Read first so the refusal below is the WRITE-side escape, not the
	// read-before-edit invariant.
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": bundled}), ws)

	// Execute directly: the refusal comes from the WORKSPACE Write (osfs never
	// consults the read-root allowlist on the mutate path), which Edit surfaces
	// as an error — either way the mutation must not happen.
	res, err := EditTool{}.Execute(t.Context(), call(t, "Edit", map[string]any{
		"path": bundled, "old_string": "line one", "new_string": "mutated",
	}), tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root()}, ws, nil))
	switch {
	case err != nil:
		if !strings.Contains(err.Error(), "escapes workspace root") {
			t.Errorf("Edit refusal should surface the escape error, got %v", err)
		}
	case res.IsError:
		if !strings.Contains(res.Content, "escapes workspace root") {
			t.Errorf("Edit refusal should surface the escape error, got %q", res.Content)
		}
	default:
		t.Fatal("Edit on an absolute skill path must be refused (the carve-out is read-only)")
	}
	// The bundled file is untouched.
	data, err := os.ReadFile(bundled)
	if err != nil || string(data) != "line one\nline two\n" {
		t.Fatalf("bundled file changed after refused Edit: %q, %v", data, err)
	}
}
