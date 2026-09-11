package apicheck

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestADR_0224_MicroVMDependenciesStayOutOfEngineAndRoot keeps the libkrun-backed
// runtime in its opt-in nested module. The production engine and default binaries
// must remain usable without resolving or linking the microVM runtime.
func TestADR_0224_MicroVMDependenciesStayOutOfEngineAndRoot(t *testing.T) {
	repo := repoRoot(t)
	microvmMod := filepath.Join(repo, "environment", "microvm", "go.mod")
	mustContain(t, microvmMod, "module github.com/stacklok/mecatl/environment/microvm")
	mustContain(t, microvmMod, "github.com/stacklok/go-microvm v0.0.40")
	mustContain(t, filepath.Join(repo, "go.work"), "use ./environment/microvm")

	for _, path := range []string{
		filepath.Join(repo, "go.mod"),
		filepath.Join(repo, "engine", "go.mod"),
	} {
		mustNotContainAny(t, path, "go-microvm", "libkrun")
	}

	for _, dir := range []string{
		filepath.Join(repo, "engine"),
		repo,
	} {
		assertProductionImportsExclude(t, dir, filepath.Join(repo, "environment", "microvm"))
	}

	assertGoListExcludes(t, repo, "./cmd/mecated", "./cmd/mecademo", "./cmd/mecatequi", "./cmd/mecak8s", "./cmd/mecatui")
	assertGoListExcludes(t, filepath.Join(repo, "engine"), "./...")
}

func TestMicroVMRuntime_StandardBinariesDoNotLinkLibkrun(t *testing.T) {
	repo := repoRoot(t)
	microvm := filepath.Join(repo, "environment", "microvm")
	for _, target := range []string{"./cmd/mecatl-microvmd", "./cmd/mecatl-guest-agent"} {
		outputPath := filepath.Join(t.TempDir(), filepath.Base(target))
		cmd := exec.Command("go", "build", "-o", outputPath, target)
		cmd.Dir = microvm
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build microVM target %s: %v\n%s", target, err, output)
		}
	}
	assertGoListExcludes(t, repo, "./cmd/mecated", "./cmd/mecademo", "./cmd/mecatequi", "./cmd/mecak8s", "./cmd/mecatui")
	assertGoListExcludes(t, filepath.Join(repo, "engine"), "./...")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func mustContain(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Contains(contents, []byte(want)) {
		t.Fatalf("%s does not contain %q", path, want)
	}
}

func mustNotContainAny(t *testing.T, path string, forbidden ...string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, value := range forbidden {
		if bytes.Contains(contents, []byte(value)) {
			t.Fatalf("%s unexpectedly contains %q", path, value)
		}
	}
}

func assertProductionImportsExclude(t *testing.T, root, excluded string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == excluded || path == filepath.Join(repoRoot(t), ".scratch") || (root != filepath.Join(repoRoot(t), "engine") && path == filepath.Join(root, "engine")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		mustNotContainAny(t, path, `"github.com/stacklok/go-microvm`, "libkrun")
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func assertGoListExcludes(t *testing.T, dir string, patterns ...string) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list", "-deps"}, patterns...)...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", strings.Join(patterns, " "), err, output)
	}
	for _, forbidden := range []string{"go-microvm", "libkrun"} {
		if bytes.Contains(output, []byte(forbidden)) {
			t.Fatalf("go list -deps %s unexpectedly includes %q", strings.Join(patterns, " "), forbidden)
		}
	}
}
