package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestLearnedSkillListFailureDiagnosticsReachConfiguredSink(t *testing.T) {
	diag := newCapturingDiagnostics()
	userModelDir := t.TempDir()
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: t.TempDir(), UserModelDir: userModelDir, UseMock: true, Diagnostics: diag,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	request := &mecatlv1.ListLearnedSkillsRequest{}
	if _, err := built.Service.ListLearnedSkills(t.Context(), request); err != nil {
		t.Fatalf("empty repository: %v", err)
	}

	// Corrupt only this test's repository after the real factory has opened it.
	storeDir := filepath.Join(userModelDir, "learned-skills")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(storeDir, "manifest.json")
	const privateBody = "private skill body must not escape"
	if err := os.WriteFile(manifest, []byte(privateBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := built.Service.ListLearnedSkills(t.Context(), request); !errors.Is(err, server.ErrInternal) || err.Error() != "server: internal error: learned skill operation failed" {
		t.Fatalf("repository failure: %v", err)
	}
	var failures []string
	for _, record := range diag.capturedStrings() {
		if strings.Contains(record, "learned-skill list failed") {
			failures = append(failures, record)
		}
	}
	if len(failures) != 1 || !strings.Contains(failures[0], "publish") || !strings.Contains(failures[0], "unclassified") {
		t.Fatalf("configured sink did not receive publication failure: %v", failures)
	}
	if strings.Contains(failures[0], userModelDir) || strings.Contains(failures[0], privateBody) || strings.Contains(failures[0], "manifest.json") {
		t.Fatalf("diagnostic leaked repository detail: %v", failures)
	}

	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := built.Service.ListLearnedSkills(t.Context(), request); err != nil {
		t.Fatalf("recovered repository: %v", err)
	}
	if diag.countContaining("learned-skill list failed") != 1 {
		t.Fatalf("successful calls emitted failure diagnostics: %v", diag.capturedStrings())
	}
}
