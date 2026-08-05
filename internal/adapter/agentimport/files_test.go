package agentimport

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCopyWorkspaceCopiesFilesSkipsGitAndSymlinks(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "target")
	writeTestFile(t, filepath.Join(src, "README.md"), "hello")
	writeTestFile(t, filepath.Join(src, "nested", "main.go"), "package main")
	writeTestFile(t, filepath.Join(src, ".git", "config"), "secret")
	if err := os.Symlink(filepath.Join(src, "README.md"), filepath.Join(src, "linked")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatalf("Symlink: %v", err)
	}

	stats, err := CopyWorkspace(src, dst)
	if err != nil {
		t.Fatalf("CopyWorkspace: %v", err)
	}
	if stats.Files != 2 || stats.SkippedSymlinks != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "nested", "main.go")); err != nil || string(got) != "package main" {
		t.Fatalf("copied file = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git copied, stat error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "linked")); !os.IsNotExist(err) {
		t.Fatalf("symlink copied, lstat error = %v", err)
	}
}

func TestCopyWorkspaceRefusesCollisionBeforeWriting(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(src, "a.txt"), "new")
	writeTestFile(t, filepath.Join(src, "z.txt"), "new")
	writeTestFile(t, filepath.Join(dst, "z.txt"), "existing")

	if _, err := CopyWorkspace(src, dst); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("CopyWorkspace error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("preflight wrote a.txt, stat error = %v", err)
	}
}

func TestCopySkillsCopiesOnlyBundlesAndRefusesReplacement(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(src, "review", "SKILL.md"), "---\nname: review\n---")
	writeTestFile(t, filepath.Join(src, "review", "references", "guide.md"), "guide")
	writeTestFile(t, filepath.Join(src, "not-a-skill", "README.md"), "ignore")

	stats, err := CopySkills(src, dst)
	if err != nil {
		t.Fatalf("CopySkills: %v", err)
	}
	if stats.Skills != 1 || stats.Files != 2 {
		t.Fatalf("stats = %#v", stats)
	}
	if _, err := os.Stat(filepath.Join(dst, "not-a-skill")); !os.IsNotExist(err) {
		t.Fatalf("non-skill copied, stat error = %v", err)
	}
	if _, err := CopySkills(src, dst); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second CopySkills error = %v", err)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
