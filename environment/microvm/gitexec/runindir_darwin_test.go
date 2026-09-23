//go:build darwin

package gitexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInDirUsesDescriptorAndPassesArgumentsOpaquely(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temporary directory: %v", err)
	}
	bin := filepath.Join(root, "bin")
	bound := filepath.Join(root, "bound")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bound, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(python, filepath.Join(bin, "python3")); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(bin, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir, err := os.Open(bound)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	injected := "; touch " + filepath.Join(root, "injected")
	output, err := RunInDir(t.Context(), dir, nil, "status", injected)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 || lines[0] != bound || lines[len(lines)-1] != injected {
		t.Fatalf("descriptor-bound argv = %q", output)
	}
	if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
		t.Fatalf("Git argument was interpreted by a shell: %v", err)
	}
}
