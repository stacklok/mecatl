package worktree

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPrepareExtractsCommittedDirectories(t *testing.T) {
	requireGit(t)
	source := newRepository(t)
	writeTestFile(t, filepath.Join(source, "nested", "fact.txt"), []byte("nested\n"), 0o644)
	gitTest(t, source, nil, "add", "nested/fact.txt")
	gitTest(t, source, nil, "commit", "-qm", "add nested file")
	root := t.TempDir()
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
	root := t.TempDir()
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
	root := t.TempDir()
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
	root := t.TempDir()
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
		WorktreePath: filepath.Join(t.TempDir(), "session-worktree"),
		MetadataPath: filepath.Join(t.TempDir(), "guest-git"),
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
	racyWorktree := filepath.Join(t.TempDir(), "racy-worktree")
	racyMetadata := filepath.Join(t.TempDir(), "racy-git")
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

func TestMicroVMEnvironments_Scenario3_WorktreeGitMetadataIsConfined(t *testing.T) {
	requireGit(t)
	t.Run("reconstructed metadata and read-only alternates mount", func(t *testing.T) {
		source := newRepository(t)
		hookSentinel := filepath.Join(t.TempDir(), "hook-fired")
		hooks := filepath.Join(source, ".git", "hooks")
		if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\ntouch \""+hookSentinel+"\"\n"), 0o755); err != nil {
			t.Fatalf("write hostile hook: %v", err)
		}
		gitTest(t, source, nil, "config", "core.pager", "false")
		prepared, err := New().Prepare(context.Background(), Request{
			Source:       source,
			WorktreePath: filepath.Join(t.TempDir(), "session-worktree"),
			MetadataPath: filepath.Join(t.TempDir(), "guest-git"),
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
			WorktreePath: filepath.Join(t.TempDir(), "session-worktree"),
			MetadataPath: filepath.Join(t.TempDir(), "guest-git"),
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
				WorktreePath: filepath.Join(t.TempDir(), "session-worktree"),
				MetadataPath: filepath.Join(t.TempDir(), "guest-git"),
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

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
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
