package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrepareExtractsCommittedDirectories(t *testing.T) {
	requireGit(t)
	source := newRepository(t)
	writeTestFile(t, filepath.Join(source, "nested", "fact.txt"), []byte("nested\n"), 0o644)
	gitTest(t, source, nil, "add", "nested/fact.txt")
	gitTest(t, source, nil, "commit", "-qm", "add nested file")
	root := canonicalTestTempDir(t)
	prepared, err := New().Prepare(context.Background(), Request{
		Source: source, WorktreePath: filepath.Join(root, "worktree"),
		MetadataPath: filepath.Join(root, "metadata"), Branch: "mecatl/nested",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Cleanup(context.Background()) })
	got, err := os.ReadFile(filepath.Join(prepared.WorktreePath, "nested", "fact.txt"))
	if err != nil || string(got) != "nested\n" {
		t.Fatalf("nested committed file = %q, %v", got, err)
	}
}

func TestCleanupRejectsLogicalAncestorSymlinkSubstitution(t *testing.T) {
	requireGit(t)
	attackerSource := newRepository(t)
	victimSource := newRepository(t)
	root := canonicalTestTempDir(t)
	attackerRoot := filepath.Join(root, "attacker")
	victimRoot := filepath.Join(root, "victim")
	if err := os.MkdirAll(attackerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(victimRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	attacker, err := New().Prepare(context.Background(), Request{
		Source: attackerSource, WorktreePath: filepath.Join(attackerRoot, "worktree"),
		MetadataPath: filepath.Join(attackerRoot, "metadata"), Branch: "mecatl/attacker",
	})
	if err != nil {
		t.Fatalf("prepare attacker: %v", err)
	}
	victim, err := New().Prepare(context.Background(), Request{
		Source: victimSource, WorktreePath: filepath.Join(victimRoot, "worktree"),
		MetadataPath: filepath.Join(victimRoot, "metadata"), Branch: "mecatl/victim",
	})
	if err != nil {
		t.Fatalf("prepare victim: %v", err)
	}
	t.Cleanup(func() { _ = victim.Cleanup(context.Background()) })

	attackerOriginal := attackerRoot + ".original"
	if err := os.Rename(attackerRoot, attackerOriginal); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimRoot, attackerRoot); err != nil {
		t.Fatal(err)
	}

	if err := attacker.Cleanup(context.Background()); err == nil {
		t.Fatal("Cleanup accepted a symlink-substituted logical ancestor")
	}
	for _, path := range []string{victim.WorktreePath, victim.MetadataPath} {
		if info, statErr := os.Lstat(path); statErr != nil || !info.IsDir() {
			t.Fatalf("victim path %q was removed: %v", path, statErr)
		}
	}
	if got := gitOutput(t, victim.WorktreePath, "status", "--porcelain"); got != "" {
		t.Fatalf("victim worktree became dirty: %q", got)
	}
}

func TestCleanupBindsRemovalBeforeAncestorSwap(t *testing.T) {
	requireGit(t)
	source := newRepository(t)
	root := canonicalTestTempDir(t)
	owned := filepath.Join(root, "owned")
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(owned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(victim, "survives")
	writeTestFile(t, marker, []byte("victim\n"), 0o600)
	prepared, err := New().Prepare(context.Background(), Request{
		Source: source, WorktreePath: filepath.Join(owned, "worktree"),
		MetadataPath: filepath.Join(owned, "metadata"), Branch: "mecatl/anchored-cleanup",
	})
	if err != nil {
		t.Fatal(err)
	}
	original := owned + ".original"
	prepared.beforeCleanupRemoval = func() {
		if err := os.Rename(owned, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, owned); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepared.Cleanup(context.Background()); err != nil {
		t.Fatalf("descriptor-bound cleanup: %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "victim\n" {
		t.Fatalf("victim changed after ancestor swap: %q, %v", got, err)
	}
}

func TestPrepareRollbackUsesBoundRemovalAfterAncestorSwap(t *testing.T) {
	requireGit(t)
	source := newRepository(t)
	root := canonicalTestTempDir(t)
	owned := filepath.Join(root, "owned")
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(owned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(victim, "survives")
	writeTestFile(t, marker, []byte("victim\n"), 0o600)
	p := New()
	p.afterWorktree = func(string) error {
		if err := os.Rename(owned, owned+".original"); err != nil {
			return err
		}
		if err := os.Symlink(victim, owned); err != nil {
			return err
		}
		return errors.New("injected preparation failure")
	}
	_, err := p.Prepare(context.Background(), Request{
		Source: source, WorktreePath: filepath.Join(owned, "worktree"),
		MetadataPath: filepath.Join(owned, "metadata"), Branch: "mecatl/anchored-rollback",
	})
	if err == nil {
		t.Fatal("Prepare accepted injected failure")
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "victim\n" {
		t.Fatalf("rollback changed victim: %q, %v", got, readErr)
	}
	for _, path := range []string{filepath.Join(owned+".original", "worktree"), filepath.Join(owned+".original", "metadata")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("rollback retained owned path %q: %v", path, statErr)
		}
	}
	entries, readErr := os.ReadDir(filepath.Join(source, ".git", "worktrees"))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read linked-worktree administration: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("rollback retained linked-worktree administration: %v", entries)
	}
	command := exec.CommandContext(t.Context(), "git", "show-ref", "--verify", "--quiet", "refs/heads/mecatl/anchored-rollback")
	command.Dir = source
	if branchErr := command.Run(); branchErr == nil {
		t.Fatal("rollback retained temporary branch")
	}
}

func TestMicroVMEnvironments_Scenario3_SourceStateCaptureIsExactOrFails(t *testing.T) {
	requireGit(t)
	source := newRepository(t)
	writeTestFile(t, filepath.Join(source, "both.txt"), []byte("staged\n"), 0o644)
	writeTestFile(t, filepath.Join(source, "staged.txt"), []byte("staged only\n"), 0o755)
	gitTest(t, source, nil, "add", "both.txt", "staged.txt")
	writeTestFile(t, filepath.Join(source, "both.txt"), []byte("unstaged after staged\n"), 0o644)
	writeTestFile(t, filepath.Join(source, "untracked.bin"), []byte{0, 1, 0xff, 2}, 0o600)

	prepared, err := New().Prepare(context.Background(), Request{
		Source:       source,
		WorktreePath: filepath.Join(canonicalTestTempDir(t), "session-worktree"),
		MetadataPath: filepath.Join(canonicalTestTempDir(t), "guest-git"),
		Branch:       "mecatl/session-test",
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Cleanup(context.Background()) })

	sourceState, err := captureState(context.Background(), source)
	if err != nil {
		t.Fatalf("capture source: %v", err)
	}
	preparedState, err := captureState(context.Background(), prepared.WorktreePath)
	if err != nil {
		t.Fatalf("capture prepared worktree: %v", err)
	}
	if !bytes.Equal(sourceState.digest, preparedState.digest) {
		t.Fatalf("prepared state differs from source\nsource: %x\nprepared: %x", sourceState.digest, preparedState.digest)
	}
	if got := gitOutput(t, prepared.WorktreePath, "diff", "--cached", "--", "both.txt"); !strings.Contains(got, "+staged") {
		t.Fatalf("staged state was not retained: %q", got)
	}
	if got := gitOutput(t, prepared.WorktreePath, "diff", "--", "both.txt"); !strings.Contains(got, "+unstaged after staged") {
		t.Fatalf("unstaged state was not retained: %q", got)
	}

	racy := newRepository(t)
	p := New()
	p.afterCapture = func() error {
		return os.WriteFile(filepath.Join(racy, "tracked.txt"), []byte("changed during capture\n"), 0o644)
	}
	racyWorktree := filepath.Join(canonicalTestTempDir(t), "racy-worktree")
	racyMetadata := filepath.Join(canonicalTestTempDir(t), "racy-git")
	_, err = p.Prepare(context.Background(), Request{
		Source:       racy,
		WorktreePath: racyWorktree,
		MetadataPath: racyMetadata,
		Branch:       "mecatl/racy",
	})
	if err == nil || !strings.Contains(err.Error(), "source changed during capture") {
		t.Fatalf("racing source error = %v, want exact-capture failure", err)
	}
	for _, path := range []string{racyWorktree, racyMetadata} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed capture left provisional path %q: %v", path, statErr)
		}
	}
}

func TestSourceCaptureLimitsAndCleanup(t *testing.T) {
	requireGit(t)
	tests := []struct {
		name       string
		byteLimit  int64
		entryLimit int
		prepare    func(*testing.T, string)
		wantStage  string
	}{
		{
			name: "oversized archive", byteLimit: 100 << 10, entryLimit: captureEntryLimit,
			prepare: func(t *testing.T, source string) {
				writeTestFile(t, filepath.Join(source, "archive.bin"), bytes.Repeat([]byte("a"), 64<<10), 0o644)
				gitTest(t, source, nil, "add", "archive.bin")
				gitTest(t, source, nil, "commit", "-qm", "large archive")
			},
			wantStage: "capture committed tree",
		},
		{
			name: "oversized diff", byteLimit: 32 << 10, entryLimit: captureEntryLimit,
			prepare: func(t *testing.T, source string) {
				writeTestFile(t, filepath.Join(source, "deleted.bin"), bytes.Repeat([]byte{0xa5}, 64<<10), 0o644)
				gitTest(t, source, nil, "add", "deleted.bin")
				gitTest(t, source, nil, "commit", "-qm", "large deleted file")
				if err := os.Remove(filepath.Join(source, "deleted.bin")); err != nil {
					t.Fatal(err)
				}
				gitTest(t, source, nil, "add", "-u")
			},
			wantStage: "capture staged changes",
		},
		{
			name: "oversized untracked file", byteLimit: 8 << 10, entryLimit: captureEntryLimit,
			prepare: func(t *testing.T, source string) {
				writeTestFile(t, filepath.Join(source, "untracked.bin"), bytes.Repeat([]byte{1}, 16<<10), 0o600)
			},
			wantStage: "capture source state",
		},
		{
			name: "oversized tracked file", byteLimit: 8 << 10, entryLimit: captureEntryLimit,
			prepare: func(t *testing.T, source string) {
				writeTestFile(t, filepath.Join(source, "tracked.txt"), bytes.Repeat([]byte{2}, 16<<10), 0o644)
			},
			wantStage: "capture source state",
		},
		{
			name: "entry limit", byteLimit: captureByteLimit, entryLimit: 1,
			prepare:   func(*testing.T, string) {},
			wantStage: "extract committed tree",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source := newRepository(t)
			tc.prepare(t, source)
			root := canonicalTestTempDir(t)
			p := New()
			p.byteLimit = tc.byteLimit
			p.entryLimit = tc.entryLimit
			worktreePath := filepath.Join(root, "worktree")
			metadataPath := filepath.Join(root, "metadata")
			_, err := p.Prepare(t.Context(), Request{
				Source: source, WorktreePath: worktreePath, MetadataPath: metadataPath,
				Branch: "mecatl/limit-" + strings.ReplaceAll(tc.name, " ", "-"),
			})
			if err == nil || !strings.Contains(err.Error(), errCaptureLimit.Error()) || !strings.Contains(err.Error(), tc.wantStage) {
				t.Fatalf("Prepare error = %v, want %q at %q", err, errCaptureLimit, tc.wantStage)
			}
			assertCaptureClean(t, root, worktreePath, metadataPath)
		})
	}
}

func TestSourceCaptureVerificationLimitCleansProvisionalWorktree(t *testing.T) {
	requireGit(t)
	source := newRepository(t)
	root := canonicalTestTempDir(t)
	p := New()
	p.byteLimit = 24 << 10
	p.afterCapture = func() error {
		return os.WriteFile(filepath.Join(source, "tracked.txt"), bytes.Repeat([]byte{3}, 32<<10), 0o644)
	}
	worktreePath := filepath.Join(root, "worktree")
	metadataPath := filepath.Join(root, "metadata")
	_, err := p.Prepare(t.Context(), Request{
		Source: source, WorktreePath: worktreePath, MetadataPath: metadataPath,
		Branch: "mecatl/verification-limit",
	})
	if err == nil || !strings.Contains(err.Error(), "recheck source state") || !strings.Contains(err.Error(), errCaptureLimit.Error()) {
		t.Fatalf("Prepare error = %v, want bounded verification failure", err)
	}
	assertCaptureClean(t, root, worktreePath, metadataPath)
}

func TestSourceCaptureCancellationCleansTemporaryState(t *testing.T) {
	requireGit(t)
	source := newRepository(t)
	root := canonicalTestTempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	p := New()
	p.afterCapture = func() error {
		cancel()
		return nil
	}
	worktreePath := filepath.Join(root, "worktree")
	metadataPath := filepath.Join(root, "metadata")
	_, err := p.Prepare(ctx, Request{
		Source: source, WorktreePath: worktreePath, MetadataPath: metadataPath,
		Branch: "mecatl/cancelled-capture",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare error = %v, want context cancellation", err)
	}
	assertCaptureClean(t, root, worktreePath, metadataPath)
}

func TestSourceCaptureConcurrencyIsProcessBounded(t *testing.T) {
	requireGit(t)
	started := make(chan struct{}, captureConcurrency+1)
	release := make(chan struct{})
	p := New()
	var active atomic.Int32
	var peak atomic.Int32
	p.afterCapture = func() error {
		now := active.Add(1)
		for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return nil
	}
	type result struct {
		prepared *Prepared
		err      error
	}
	results := make(chan result, captureConcurrency+1)
	for i := 0; i < captureConcurrency+1; i++ {
		source := newRepository(t)
		root := canonicalTestTempDir(t)
		go func(i int) {
			prepared, err := p.Prepare(context.Background(), Request{
				Source: source, WorktreePath: filepath.Join(root, "worktree"),
				MetadataPath: filepath.Join(root, "metadata"), Branch: fmt.Sprintf("mecatl/concurrency-%d", i),
			})
			results <- result{prepared: prepared, err: err}
		}(i)
	}
	for i := 0; i < captureConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("capture did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("capture concurrency exceeded process-wide bound")
	case <-time.After(100 * time.Millisecond):
	}
	for i := 0; i < captureConcurrency; i++ {
		release <- struct{}{}
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("queued capture did not start")
	}
	release <- struct{}{}
	for i := 0; i < captureConcurrency+1; i++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("Prepare: %v", got.err)
		}
		if err := got.prepared.Cleanup(context.Background()); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
	}
	if got := peak.Load(); got > captureConcurrency {
		t.Fatalf("peak captures = %d, want <= %d", got, captureConcurrency)
	}
}

func assertCaptureClean(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed capture retained %q: %v", path, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".mecatl-source-capture-") {
			t.Fatalf("failed capture retained temporary directory %q", entry.Name())
		}
	}
}

func TestMicroVMEnvironments_Scenario3_WorktreeGitMetadataIsConfined(t *testing.T) {
	requireGit(t)
	t.Run("reconstructed metadata and read-only alternates mount", func(t *testing.T) {
		source := newRepository(t)
		hookSentinel := filepath.Join(canonicalTestTempDir(t), "hook-fired")
		hooks := filepath.Join(source, ".git", "hooks")
		if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\ntouch \""+hookSentinel+"\"\n"), 0o755); err != nil {
			t.Fatalf("write hostile hook: %v", err)
		}
		gitTest(t, source, nil, "config", "core.pager", "false")
		prepared, err := New().Prepare(context.Background(), Request{
			Source:       source,
			WorktreePath: filepath.Join(canonicalTestTempDir(t), "session-worktree"),
			MetadataPath: filepath.Join(canonicalTestTempDir(t), "guest-git"),
			Branch:       "mecatl/confined",
		})
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		t.Cleanup(func() { _ = prepared.Cleanup(context.Background()) })
		if _, statErr := os.Stat(hookSentinel); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("repository-controlled post-checkout hook ran: %v", statErr)
		}
		guestConfig, err := os.ReadFile(filepath.Join(prepared.MetadataPath, "config"))
		if err != nil {
			t.Fatalf("read reconstructed config: %v", err)
		}
		if bytes.Contains(guestConfig, []byte("pager")) {
			t.Fatalf("repository-controlled config leaked into guest metadata: %s", guestConfig)
		}

		wantMount := Mount{HostPath: prepared.CommonObjectStore, GuestPath: GuestObjectStore, ReadOnly: true}
		if !slices.Contains(prepared.Mounts, wantMount) {
			t.Fatalf("mounts = %#v, want host-enforced read-only object-store mount %#v", prepared.Mounts, wantMount)
		}
		alternates, err := os.ReadFile(filepath.Join(prepared.MetadataPath, "objects", "info", "alternates"))
		if err != nil {
			t.Fatalf("read alternates: %v", err)
		}
		if string(alternates) != GuestObjectStore+"\n" {
			t.Fatalf("alternates = %q, want guest-only path", alternates)
		}
		cmd := exec.Command("git", "status", "--porcelain")
		cmd.Dir = prepared.WorktreePath
		cmd.Env = append(os.Environ(),
			"GIT_DIR="+prepared.MetadataPath,
			"GIT_WORK_TREE="+prepared.WorktreePath,
			"GIT_ALTERNATE_OBJECT_DIRECTORIES="+prepared.CommonObjectStore,
		)
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("git with reconstructed metadata: %v: %s", runErr, out)
		}
	})

	t.Run("source object-store alternates cannot name a host path", func(t *testing.T) {
		source := newRepository(t)
		alternates := filepath.Join(source, ".git", "objects", "info", "alternates")
		if err := os.MkdirAll(filepath.Dir(alternates), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(alternates, []byte("/etc\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := New().Prepare(context.Background(), Request{
			Source:       source,
			WorktreePath: filepath.Join(canonicalTestTempDir(t), "session-worktree"),
			MetadataPath: filepath.Join(canonicalTestTempDir(t), "guest-git"),
			Branch:       "mecatl/external-alternate",
		})
		if err == nil || !strings.Contains(err.Error(), "external object-store alternates") {
			t.Fatalf("external alternates error = %v, want confined failure", err)
		}
	})

	for _, tc := range []struct {
		name   string
		tamper func(string) error
	}{
		{
			name: "dot-git symlink",
			tamper: func(worktree string) error {
				if err := os.Remove(filepath.Join(worktree, ".git")); err != nil {
					return err
				}
				return os.Symlink("/etc", filepath.Join(worktree, ".git"))
			},
		},
		{
			name: "commondir escape",
			tamper: func(worktree string) error {
				gitFile, err := os.ReadFile(filepath.Join(worktree, ".git"))
				if err != nil {
					return err
				}
				gitDir := strings.TrimSpace(strings.TrimPrefix(string(gitFile), "gitdir:"))
				return os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../../../../../../etc\n"), 0o644)
			},
		},
		{
			name: "object-store symlink",
			tamper: func(worktree string) error {
				gitFile, err := os.ReadFile(filepath.Join(worktree, ".git"))
				if err != nil {
					return err
				}
				gitDir := strings.TrimSpace(strings.TrimPrefix(string(gitFile), "gitdir:"))
				commondir, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
				if err != nil {
					return err
				}
				objects := filepath.Join(gitDir, strings.TrimSpace(string(commondir)), "objects")
				if err := os.Rename(objects, objects+".real"); err != nil {
					return err
				}
				return os.Symlink("/etc", objects)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := newRepository(t)
			p := New()
			p.afterWorktree = tc.tamper
			prepared, err := p.Prepare(context.Background(), Request{
				Source:       source,
				WorktreePath: filepath.Join(canonicalTestTempDir(t), "session-worktree"),
				MetadataPath: filepath.Join(canonicalTestTempDir(t), "guest-git"),
				Branch:       "mecatl/hostile-" + strings.ReplaceAll(tc.name, " ", "-"),
			})
			if prepared != nil {
				_ = prepared.Cleanup(context.Background())
			}
			if err == nil {
				t.Fatal("Prepare accepted hostile linked-worktree metadata")
			}
		})
	}
}

func canonicalTestTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temporary directory: %v", err)
	}
	return dir
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	dir := canonicalTestTempDir(t)
	gitTest(t, dir, nil, "init", "-q")
	gitTest(t, dir, nil, "config", "user.email", "test@example.invalid")
	gitTest(t, dir, nil, "config", "user.name", "Test")
	writeTestFile(t, filepath.Join(dir, "tracked.txt"), []byte("committed\n"), 0o644)
	gitTest(t, dir, nil, "add", "tracked.txt")
	gitTest(t, dir, nil, "commit", "-qm", "initial")
	return dir
}

func writeTestFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, dir string, stdin []byte, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
