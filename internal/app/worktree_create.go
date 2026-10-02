package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// Server-created session worktrees (ADR 0374 Decision 1). The provider owns
// location and naming: every worktree lives directly under the managed root on
// its own mecatl/<name> branch, created from the configured root's HEAD.
const (
	managedWorktreeNamePrefix   = "mecatl-"
	managedWorktreeBranchPrefix = "mecatl/"
	maxManagedWorktreeAttempts  = 5
	managedWorktreeGitTimeout   = 30 * time.Second
)

// worktreeCreateLocks serializes worktree creation per configured repository
// root across every provider in this process. Entries are never removed; the
// map is bounded by the number of distinct repository roots.
var worktreeCreateLocks sync.Map // string -> *sync.Mutex

func lockWorktreeCreate(root string) func() {
	value, _ := worktreeCreateLocks.LoadOrStore(root, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// worktreeCreator creates server-created worktrees of one configured
// repository. newName and git are injectable for tests.
type worktreeCreator struct {
	root        string
	managedRoot string
	newName     func() (string, error)
	git         func(ctx context.Context, dir string, args ...string) (string, error)
}

// newWorktreeCreator returns nil unless the deployment passes the same gate as
// worktree listing (configured workspace, shell, trusted project; ADR 0095) and
// a server state directory resolves.
func newWorktreeCreator(cfg Config) *worktreeCreator {
	if buildWorktreeLister(cfg) == nil {
		return nil
	}
	state := xdgconfig.UserStateDir(xdgconfig.OSEnv)
	if state == "" {
		return nil
	}
	return &worktreeCreator{
		root: cfg.Workspace, managedRoot: managedWorktreeRoot(state, cfg.Workspace),
		newName: randomManagedWorktreeName, git: runHardenedGit,
	}
}

// managedWorktreeRoot is $XDG_STATE_HOME/mecatl/worktrees/<repo>-<hash8>, where
// <repo> is the configured root's directory name and <hash8> the first eight hex
// characters of the SHA-256 of the configured root. It is outside the
// repository so recursive tools in the main checkout never descend into it.
func managedWorktreeRoot(stateBase, root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(stateBase, "mecatl", "worktrees", filepath.Base(root)+"-"+hex.EncodeToString(sum[:])[:8])
}

func randomManagedWorktreeName() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return managedWorktreeNamePrefix + hex.EncodeToString(b[:]), nil
}

func validManagedWorktreeName(name string) bool {
	suffix, ok := strings.CutPrefix(name, managedWorktreeNamePrefix)
	if !ok || len(suffix) != 8 {
		return false
	}
	for _, c := range suffix {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// runHardenedGit runs one git subcommand in dir with the forker's scrubbed,
// hook-free environment (envscrub then gitenv: no harness credentials, no repo
// hooks, pager, fsmonitor, or external diff). Arguments are passed as argv,
// never through a shell.
func runHardenedGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, managedWorktreeGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed git subcommands over server-generated arguments.
	cmd.Env = gitSafeEnvironment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// create makes one new worktree and returns its path plus a discard function
// that removes the worktree and its new branch. Creates are serialized per
// repository; a name whose path or branch already exists is retried.
func (c *worktreeCreator) create(ctx context.Context) (string, func() error, error) {
	unlock := lockWorktreeCreate(c.root)
	defer unlock()
	if _, err := c.git(ctx, c.root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return "", nil, fmt.Errorf("%w: repository HEAD has no commit", server.ErrWorktreeCreationUnavailable)
	}
	if err := os.MkdirAll(c.managedRoot, 0o700); err != nil {
		return "", nil, fmt.Errorf("%w: create managed worktree root: %v", server.ErrPlacementUnavailable, err)
	}
	for range maxManagedWorktreeAttempts {
		name, err := c.newName()
		if err != nil || !validManagedWorktreeName(name) {
			return "", nil, fmt.Errorf("%w: generate worktree name", server.ErrPlacementUnavailable)
		}
		path := filepath.Join(c.managedRoot, name)
		branch := managedWorktreeBranchPrefix + name
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if _, err := c.git(ctx, c.root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			continue
		}
		discard := func() error { return c.discard(path, branch) }
		if _, err := c.git(ctx, c.root, "worktree", "add", "-b", branch, path, "HEAD"); err != nil {
			_ = discard()
			return "", nil, fmt.Errorf("%w: %v", server.ErrPlacementUnavailable, err)
		}
		return path, discard, nil
	}
	return "", nil, fmt.Errorf("%w: no free worktree name after %d attempts", server.ErrPlacementUnavailable, maxManagedWorktreeAttempts)
}

// discard removes a just-created worktree and its new branch. It runs detached
// from the request context so a cancelled create still cleans up.
func (c *worktreeCreator) discard(path, branch string) error {
	ctx := context.Background()
	_, removeErr := c.git(ctx, c.root, "worktree", "remove", "--force", path)
	if err := os.RemoveAll(path); err != nil && removeErr == nil {
		removeErr = err
	}
	if _, err := c.git(ctx, c.root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return removeErr
	}
	_, branchErr := c.git(ctx, c.root, "branch", "-D", branch)
	return errors.Join(removeErr, branchErr)
}

// CanCreateWorktrees implements server.PlacementWorktreeCreator.
func (p *localPlacementProvider) CanCreateWorktrees() bool {
	return p.root != "" && p.worktrees != nil && p.creator != nil
}

// bindNewWorktree creates a fresh worktree and binds it through the ordinary
// worktree path. Any failure after creation discards it; the returned binding's
// Rollback discards it if the session is never published.
func (p *localPlacementProvider) bindNewWorktree(ctx context.Context) (server.PlacementBinding, error) {
	if !p.CanCreateWorktrees() {
		return server.PlacementBinding{}, server.ErrWorktreeCreationUnavailable
	}
	path, discard, err := p.creator.create(ctx)
	if err != nil {
		return server.PlacementBinding{}, err
	}
	choice, ok := p.listedWorktree(ctx, path)
	if !ok {
		_ = discard()
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	binding, err := p.bindWorktree(choice)
	if err != nil {
		_ = discard()
		return server.PlacementBinding{}, err
	}
	// Display metadata names the new worktree and its branch, never its path. The
	// server's display sanitizer drops slash-bearing values, so the slash-free
	// worktree name is the label that reaches clients.
	name := filepath.Base(path)
	binding.Metadata.Label = name
	binding.Metadata.Branch = managedWorktreeBranchPrefix + name
	binding.Rollback = discard
	return binding, nil
}

// listedWorktree returns git's listing of path, compared symlink-resolved.
func (p *localPlacementProvider) listedWorktree(ctx context.Context, path string) (server.Worktree, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || p.worktrees == nil {
		return server.Worktree{}, false
	}
	current, err := p.worktrees.List(ctx, p.root)
	if err != nil {
		return server.Worktree{}, false
	}
	for _, candidate := range current {
		if listed, err := filepath.EvalSymlinks(candidate.Path); err == nil && listed == resolved {
			return candidate, true
		}
	}
	return server.Worktree{}, false
}

// serverCreatedWorktree reports whether ref names a worktree this provider
// created (ADR 0374 Decision 3): the ref's path is not a symlink, its resolved
// form is a direct child of the resolved managed root, and git lists it as a
// worktree of the configured repository. Ownership is derived, never persisted.
func (p *localPlacementProvider) serverCreatedWorktree(ctx context.Context, ref session.EnvironmentRef) bool {
	if p.creator == nil || ref.Kind != session.EnvKindLocal || ref.ID == "" || ref.ID == p.root {
		return false
	}
	managed, err := filepath.EvalSymlinks(p.creator.managedRoot)
	if err != nil {
		return false
	}
	info, err := os.Lstat(ref.ID)
	if err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	resolved, err := filepath.EvalSymlinks(ref.ID)
	if err != nil || filepath.Dir(resolved) != managed {
		return false
	}
	_, listed := p.listedWorktree(ctx, resolved)
	return listed
}
