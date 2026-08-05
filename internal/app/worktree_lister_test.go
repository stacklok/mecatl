package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// worktree_lister_test.go exercises the osfs-backed buildWorktreeLister (issue
// #102) against a real temp git repo. It mirrors the forker_test.go git helpers
// locally (it cannot cross-import a `_test` package).

func wtWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func wtRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func wtInitRepo(t *testing.T, dir string) {
	t.Helper()
	wtRunGit(t, dir, "init")
	wtRunGit(t, dir, "config", "user.email", "test@example.com")
	wtRunGit(t, dir, "config", "user.name", "Test")
}

func wtCommit(t *testing.T, dir string) {
	t.Helper()
	wtRunGit(t, dir, "add", "-A")
	wtRunGit(t, dir, "commit", "-m", "init")
}

// wtHead returns the trimmed HEAD SHA of the repo at dir.
func wtHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestBuildWorktreeListerParsesPorcelain creates a real git repo with a sibling
// worktree and asserts the lister returns both, with correct Path/Head/Branch.
func TestBuildWorktreeListerParsesPorcelain(t *testing.T) {
	base := t.TempDir()
	wtInitRepo(t, base)
	wtWriteFile(t, filepath.Join(base, "README"), "base\n")
	wtCommit(t, base)
	head := wtHead(t, base)

	wtB := filepath.Join(base, "..", "wtB")
	wtRunGit(t, base, "worktree", "add", "-b", "feature", wtB)

	cfg := Config{Workspace: base, Shell: "/bin/sh", TrustProject: true, Diagnostics: port.NopDiagnostics{}}
	lister := buildWorktreeLister(cfg)
	if lister == nil {
		t.Fatal("buildWorktreeLister returned nil for a trusted workspace with a shell")
	}
	wts, err := lister.List(context.Background(), base)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(wts) != 2 {
		t.Fatalf("worktrees = %d, want 2 (base + wtB): %+v", len(wts), wts)
	}
	// The main worktree is first; the sibling second. git reports paths in
	// their canonical (EvalSymlinks) form, so canonicalize the expected values
	// the same way to avoid a symlinked-temp-root mismatch (macOS /var ->
	// /private/var).
	wantBase, err := osfs.ResolveRoot(base)
	if err != nil {
		t.Fatalf("resolve base %q: %v", base, err)
	}
	if wts[0].Path != wantBase {
		t.Errorf("worktree[0].Path = %q, want %q", wts[0].Path, wantBase)
	}
	if wts[0].Head != head {
		t.Errorf("worktree[0].Head = %q, want %q", wts[0].Head, head)
	}
	if wts[0].Branch == "" {
		t.Errorf("worktree[0].Branch empty, want a ref (main or master)")
	}
	// wtB path resolves through filepath.Join(base, "..", "wtB") — canonicalize.
	wtBAbs, _ := filepath.Abs(wtB)
	wantWtB, err := osfs.ResolveRoot(wtBAbs)
	if err != nil {
		t.Fatalf("resolve wtB %q: %v", wtBAbs, err)
	}
	if wts[1].Path != wantWtB {
		t.Errorf("worktree[1].Path = %q, want %q", wts[1].Path, wantWtB)
	}
	if wts[1].Branch != "refs/heads/feature" {
		t.Errorf("worktree[1].Branch = %q, want refs/heads/feature", wts[1].Branch)
	}
}

// TestBuildWorktreeListerNilForEmptyWorkspace: a child/member/cloud service with
// no launch workspace gets a nil lister (the feature is honestly absent there).
func TestBuildWorktreeListerNilForEmptyWorkspace(t *testing.T) {
	cfg := Config{Workspace: "", Shell: "/bin/sh", TrustProject: true, Diagnostics: port.NopDiagnostics{}}
	if l := buildWorktreeLister(cfg); l != nil {
		t.Errorf("buildWorktreeLister(empty workspace) = %v, want nil", l)
	}
}

// TestBuildWorktreeListerNilWhenUntrusted: an untrusted workspace (no
// --trust-project) gets a nil lister — no git ever runs (the gitSnapshot
// discipline: the shell gate is cfg.TrustProject).
func TestBuildWorktreeListerNilWhenUntrusted(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Shell: "/bin/sh", TrustProject: false, Diagnostics: port.NopDiagnostics{}}
	if l := buildWorktreeLister(cfg); l != nil {
		t.Errorf("buildWorktreeLister(untrusted) = %v, want nil", l)
	}
}

// TestBuildWorktreeListerFailSoftOnNonRepo: a non-repo root yields an empty list,
// NOT an error — discovery must never block the overlay.
func TestBuildWorktreeListerFailSoftOnNonRepo(t *testing.T) {
	nonRepo := t.TempDir()
	cfg := Config{Workspace: nonRepo, Shell: "/bin/sh", TrustProject: true, Diagnostics: port.NopDiagnostics{}}
	lister := buildWorktreeLister(cfg)
	if lister == nil {
		t.Fatal("buildWorktreeLister returned nil for a trusted non-repo workspace")
	}
	wts, err := lister.List(context.Background(), nonRepo)
	if err != nil {
		t.Fatalf("List on a non-repo should be fail-soft (nil, nil), got err = %v", err)
	}
	if wts != nil {
		t.Fatalf("List on a non-repo should be fail-soft (nil, nil), got wts = %+v", wts)
	}
}

// TestParseWorktreePorcelain is a pure unit test over the porcelain parser.
func TestParseWorktreePorcelain(t *testing.T) {
	out := "worktree /repo\nHEAD abcdef1234567890\nbranch refs/heads/main\n\n" +
		"worktree /repo-wt\nHEAD 1234567890abcdef\nbranch refs/heads/feature\n\n" +
		"worktree /repo-bare\nbare\n"
	wts := parseWorktreePorcelain(out)
	if len(wts) != 3 {
		t.Fatalf("parsed %d worktrees, want 3: %+v", len(wts), wts)
	}
	if wts[0].Path != "/repo" || wts[0].Head != "abcdef1234567890" || wts[0].Branch != "refs/heads/main" || wts[0].Bare {
		t.Errorf("wts[0] = %+v", wts[0])
	}
	if wts[1].Path != "/repo-wt" || wts[1].Branch != "refs/heads/feature" || wts[1].Bare {
		t.Errorf("wts[1] = %+v", wts[1])
	}
	if wts[2].Path != "/repo-bare" || !wts[2].Bare {
		t.Errorf("wts[2] = %+v (want bare)", wts[2])
	}
}

// TestParseWorktreePorcelainDetached: a detached-HEAD worktree has no branch line.
func TestParseWorktreePorcelainDetached(t *testing.T) {
	out := "worktree /repo-detached\ndetached\nHEAD deadbeef\n"
	wts := parseWorktreePorcelain(out)
	if len(wts) != 1 {
		t.Fatalf("parsed %d worktrees, want 1: %+v", len(wts), wts)
	}
	if wts[0].Path != "/repo-detached" || wts[0].Head != "deadbeef" || wts[0].Branch != "" {
		t.Errorf("detached worktree = %+v", wts[0])
	}
}

// Compile-time: ensure the lister type satisfies the interface.
var _ server.WorktreeLister = gitWorktreeLister{}
