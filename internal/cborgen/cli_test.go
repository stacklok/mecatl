package cborgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildCLI(t *testing.T) string {
	t.Helper()
	isolateGoEnvironment(t)
	binary := filepath.Join(t.TempDir(), "cborgen")
	build := exec.Command("go", "build", "-o", binary, "./cmd/cborgen")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s %v", out, err)
	}
	return binary
}

func TestCLIGenericAliasChainReason(t *testing.T) {
	binary := buildCLI(t)
	cfg := executableFixture(t, "type Generic[T any] = []T;type Plain = Generic[bool];type Record struct { V Plain `cbor:\"v\"` }")
	cmd := exec.Command(binary, "-dir", cfg.Dir, "-types", "Record", "-out", cfg.Output)
	cmd.Dir = t.TempDir()
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "generic types unsupported") {
		t.Fatalf("CLI got %q, %v; want generic types unsupported", out, err)
	}
}

func TestCLIRegenerationAndProtection(t *testing.T) {
	binary := buildCLI(t)
	cfg := executableFixture(t, callers)
	path := filepath.Join(cfg.Dir, cfg.Output)
	run := func(want string) {
		t.Helper()
		cmd := exec.Command(binary, "-dir", cfg.Dir, "-types", "Record", "-out", cfg.Output)
		cmd.Dir = t.TempDir()
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if want == "" {
			if err != nil {
				t.Fatalf("CLI: %s %v", out, err)
			}
		} else if err == nil || !strings.Contains(string(out), want) {
			t.Fatalf("CLI got %q, %v; want %s", out, err, want)
		}
	}
	run("")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run("")
	second, err := os.ReadFile(path)
	if err != nil || string(first) != string(second) {
		t.Fatal("CLI nondeterminism", err)
	}
	for _, prior := range []string{GeneratedMarker + "\nbroken syntax", GeneratedMarker + "\npackage owner\nvar stale Undefined"} {
		writeFixture(t, path, prior)
		run("")
	}
	writeFixture(t, filepath.Join(cfg.Dir, "bad.go"), "package owner\nvar handwritten UndefinedHandwritten\n")
	prior, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run("UndefinedHandwritten")
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(prior) {
		t.Fatal("CLI changed output on failure", err)
	}
	if err := os.Remove(filepath.Join(cfg.Dir, "bad.go")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, "package owner\n// handwritten\n")
	run("unmarked output")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.go")
	writeFixture(t, target, string(first))
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	run("not a symlink")
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(first) {
		t.Fatal("CLI wrote symlink target", err)
	}
}
