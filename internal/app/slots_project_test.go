package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func writeProjectFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func projectFoldHarness(t *testing.T, operatorYAML, projectYAML string, trust bool) (Config, *capturingDiag) {
	t.Helper()
	ws := t.TempDir()
	opPath := filepath.Join(t.TempDir(), "operator.yaml")
	writeProjectFile(t, opPath, operatorYAML)
	if projectYAML != "" {
		writeProjectFile(t, filepath.Join(ws, ".mecatl", "settings.yaml"), projectYAML)
	}
	diag := &capturingDiag{}
	res := permconfig.New(permconfig.Options{Conventional: true, TrustProject: trust, ExplicitFiles: []string{opPath}, Diagnostics: diag})
	return Config{Workspace: ws, TrustProject: trust, Diagnostics: diag, permResolver: res}, diag
}

// Project model folding was removed by ADR 0369; project models are discarded
// in permconfig before nested model-schema decoding.
