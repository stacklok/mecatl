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

func TestCopySkillSourcesCopiesOnlyBundlesAndRefusesReplacement(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(src, "review", "SKILL.md"), "---\nname: review\n---")
	writeTestFile(t, filepath.Join(src, "review", "references", "guide.md"), "guide")
	writeTestFile(t, filepath.Join(src, "not-a-skill", "README.md"), "ignore")

	stats, err := CopySkillSources([]string{src}, dst)
	if err != nil {
		t.Fatalf("CopySkillSources: %v", err)
	}
	if stats.Skills != 1 || stats.Files != 2 {
		t.Fatalf("stats = %#v", stats)
	}
	if _, err := os.Stat(filepath.Join(dst, "not-a-skill")); !os.IsNotExist(err) {
		t.Fatalf("non-skill copied, stat error = %v", err)
	}
	if _, err := CopySkillSources([]string{src}, dst); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second CopySkillSources error = %v", err)
	}
}

func TestCopySkillSourcesRejectsCrossSourceCollisionBeforeWriting(t *testing.T) {
	srcA, srcB, dst := t.TempDir(), t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(srcA, "review", "SKILL.md"), "---\nname: review\n---")
	writeTestFile(t, filepath.Join(srcA, "review", "guide.md"), "from A")
	writeTestFile(t, filepath.Join(srcB, "review", "SKILL.md"), "---\nname: review\n---")

	_, err := CopySkillSources([]string{srcA, srcB}, dst)
	if err == nil || !strings.Contains(err.Error(), "exists in both") {
		t.Fatalf("CopySkillSources error = %v", err)
	}
	// A cross-source collision fails before any bundle is written.
	if _, err := os.Stat(filepath.Join(dst, "review")); !os.IsNotExist(err) {
		t.Fatalf("collision wrote a bundle, stat error = %v", err)
	}
}

func TestCopySkillSourcesSkipsTraversalSkillName(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	// A malicious skill directory named ".." with a SKILL.md would escape the
	// destination tree if joined naively; the guard must skip it.
	writeTestFile(t, filepath.Join(src, "review", "SKILL.md"), "---\nname: review\n---")
	writeTestFile(t, filepath.Join(src, "..", "escape.md"), "should-not-be-imported")

	stats, err := CopySkillSources([]string{src}, dst)
	if err != nil {
		t.Fatalf("CopySkillSources: %v", err)
	}
	if stats.Skills != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	// The only written bundle is the legitimate "review" skill; no file escapes.
	if _, err := os.Stat(filepath.Join(dst, "escape.md")); !os.IsNotExist(err) {
		t.Fatalf("traversal skill wrote outside dest, stat error = %v", err)
	}
}

func TestCopyWorkspaceRefusesDestinationInsideSource(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "file.txt"), "content")
	dst := filepath.Join(src, "nested", "target")

	_, err := CopyWorkspace(src, dst)
	if err == nil || !strings.Contains(err.Error(), "must not be inside source") {
		t.Fatalf("CopyWorkspace error = %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination created inside source, stat error = %v", err)
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
