package gitexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRunDisablesRepositoryAndAmbientExecutionHooks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	rawGit(t, repo, "init", "-q")
	rawGit(t, repo, "config", "user.name", "Test")
	rawGit(t, repo, "config", "user.email", "test@example.invalid")
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, repo, "add", "tracked.txt")
	rawGit(t, repo, "commit", "-qm", "base")

	sentinel := filepath.Join(root, "executed")
	executable := filepath.Join(root, "hostile")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf ran >>"+strconv.Quote(sentinel)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexec "+strconv.Quote(executable)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, repo, "config", "core.fsmonitor", executable)
	rawGit(t, repo, "config", "core.hooksPath", hooks)
	rawGit(t, repo, "config", "diff.external", executable)
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\tpager = "+executable+"\n[diff]\n\texternal = "+executable+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"status", "--porcelain"}, {"diff", "--"}, {"commit", "--allow-empty", "-m", "safe"}, {"log", "-1", "--oneline"}} {
		if _, err := Run(context.Background(), repo, nil, args...); err != nil {
			t.Fatalf("Run(%v): %v", args, err)
		}
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe Git extension executed: %v", err)
	}
}

func TestRunKeepsMachineStdoutSeparateFromStderr(t *testing.T) {
	root := t.TempDir()
	git := filepath.Join(root, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf 'machine-output\\n'\nprintf 'diagnostic-noise\\n' >&2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	output, err := Run(t.Context(), root, nil, "status")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "machine-output\n" {
		t.Fatalf("stdout = %q, want machine output only", output)
	}
}

func rawGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
