package apicheck

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMicroVMDependenciesStayOutOfEngineAndRoot keeps the libkrun-backed
// runtime in its opt-in nested module. The production engine and default binaries
// must remain usable without resolving or linking the microVM runtime.
func TestMicroVMDependenciesStayOutOfEngineAndRoot(t *testing.T) {
	repo := repoRoot(t)
	microvmMod := filepath.Join(repo, "environment", "microvm", "go.mod")
	mustContain(t, microvmMod, "module github.com/stacklok/mecatl/environment/microvm")
	mustContain(t, microvmMod, "github.com/stacklok/go-microvm v0.0.41")
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
		cmd := checkoutGoCommand(repo, microvm, "build", "-o", outputPath, target)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build microVM target %s: %v\n%s", target, err, output)
		}
	}
	assertGoListExcludes(t, repo, "./cmd/mecated", "./cmd/mecademo", "./cmd/mecatequi", "./cmd/mecak8s", "./cmd/mecatui")
	assertGoListExcludes(t, filepath.Join(repo, "engine"), "./...")
}

// These cross-module contracts inspect current checkout binaries, including when
// the outer test uses a per-module candidate manifest with GOWORK=off. That
// manifest belongs only to the outer module, not to the nested build targets.
func checkoutGoCommand(repo, dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK="+filepath.Join(repo, "go.work"))
	return cmd
}

func TestCheckoutGoCommandUsesCurrentSiblingSources(t *testing.T) {
	if os.Getenv("MECATL_APICHECK_FIXTURE_CHILD") == "1" {
		assertGoListExcludes(t, repoRoot(t), ".")
		return
	}
	repo := t.TempDir()
	for name, contents := range map[string]string{
		"go.work":                    "go 1.23.0\nuse (\n .\n ./sibling\n)\n",
		"go.mod":                     "module fixture.invalid/root\ngo 1.23.0\nrequire fixture.invalid/sibling v1.0.0\nreplace fixture.invalid/sibling => ./published\n",
		"main.go":                    "package main\nimport \"fixture.invalid/sibling\"\nfunc main() { _ = sibling.CheckoutOnly }\n",
		"published/go.mod":           "module fixture.invalid/sibling\ngo 1.23.0\n",
		"published/api.go":           "package sibling\nconst PublishedOnly = true\n",
		"sibling/go.mod":             "module fixture.invalid/sibling\ngo 1.23.0\n",
		"sibling/api.go":             "package sibling\nimport _ \"fixture.invalid/sibling/libkrun\"\nconst CheckoutOnly = true\n",
		"sibling/libkrun/runtime.go": "package libkrun\n",
	} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checkDir := filepath.Join(repo, "internal", "apicheck")
	if err := os.MkdirAll(checkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MECATL_APICHECK_FIXTURE_CHILD", "1")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	for _, ambient := range []string{"off", filepath.Join(t.TempDir(), "foreign.work")} {
		t.Run(ambient, func(t *testing.T) {
			t.Setenv("GOWORK", ambient)
			cmd := checkoutGoCommand(repo, repo, "build", "-o", filepath.Join(t.TempDir(), "fixture"), ".")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build checkout-only API: %v\n%s", err, output)
			}
			// Run the real assertion in a child so its expected Fatalf can be checked.
			// Its cwd gives repoRoot the disposable checkout, as in production callers.
			for _, forbidden := range []bool{true, false} {
				source := "package sibling\nconst CheckoutOnly = true\n"
				if forbidden {
					source = "package sibling\nimport _ \"fixture.invalid/sibling/libkrun\"\nconst CheckoutOnly = true\n"
				}
				if err := os.WriteFile(filepath.Join(repo, "sibling", "api.go"), []byte(source), 0o600); err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command(testBinary, "-test.run=^TestCheckoutGoCommandUsesCurrentSiblingSources$")
				cmd.Dir = checkDir
				output, err := cmd.CombinedOutput()
				if forbidden {
					if err == nil || !bytes.Contains(output, []byte(`unexpectedly includes "libkrun"`)) {
						t.Fatalf("dependency assertion must reject current-sibling libkrun: %v\n%s", err, output)
					}
				} else if err != nil {
					t.Fatalf("dependency assertion positive control: %v\n%s", err, output)
				}
			}
		})
	}
	// Without the checkout workspace, the same source must reject the old API.
	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "fixture"), ".")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("undefined: sibling.CheckoutOnly")) {
		t.Fatalf("standalone negative control: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(repo, "sibling", "api.go"), []byte("package sibling\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = checkoutGoCommand(repo, repo, "build", "-o", filepath.Join(t.TempDir(), "fixture"), ".")
	output, err = cmd.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("undefined: sibling.CheckoutOnly")) {
		t.Fatalf("broken checkout source must still fail: %v\n%s", err, output)
	}
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
	cmd := checkoutGoCommand(repoRoot(t), dir, append([]string{"list", "-deps"}, patterns...)...)
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
