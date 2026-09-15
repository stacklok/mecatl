package microvm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/environment/microvm/gitexec"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

// IdentitySequence allocates random generation-fenced daemon identities.
type IdentitySequence struct{ EndpointDir string }

// Allocate implements IdentityAllocator.
func (a IdentitySequence) Allocate(sessionID string) (EnvironmentIdentity, error) {
	if sessionID == "" || !filepath.IsAbs(a.EndpointDir) {
		return EnvironmentIdentity{}, errors.New("microvm identity allocator is not configured")
	}
	if err := os.MkdirAll(a.EndpointDir, 0o700); err != nil {
		return EnvironmentIdentity{}, fmt.Errorf("create microvm endpoint directory: %w", err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return EnvironmentIdentity{}, err
	}
	id := hex.EncodeToString(random[:])
	return EnvironmentIdentity{EnvironmentID: id, VMID: "mecatl-" + id, Endpoint: filepath.Join(a.EndpointDir, id+".sock"), Generation: 1}, nil
}

// GitWorktrees adapts the concrete worktree preparer to lifecycle and retention.
type GitWorktrees struct{ preparer *worktree.Preparer }

// NewGitWorktrees constructs the production Git worktree lifecycle.
func NewGitWorktrees() *GitWorktrees { return &GitWorktrees{preparer: worktree.New()} }

// Prepare implements WorktreeLifecycle.
func (g *GitWorktrees) Prepare(ctx context.Context, request worktree.Request) (*worktree.Prepared, error) {
	return g.preparer.Prepare(ctx, request)
}

// Cleanup rolls back a prepared worktree using Git's own linked-worktree operation.
func (*GitWorktrees) Cleanup(ctx context.Context, prepared *worktree.Prepared) error {
	if prepared == nil {
		return nil
	}
	return prepared.Cleanup(ctx)
}

// Dirty reports whether the prepared worktree has any tracked or untracked change.
func (*GitWorktrees) Dirty(ctx context.Context, record EnvironmentRecord) (bool, error) {
	output, err := gitexec.Run(ctx, record.WorktreePath, nil, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return false, fmt.Errorf("inspect microvm worktree: %w", err)
	}
	return len(output) != 0, nil
}

// CleanupRecord removes the exact clean worktree retained in the durable record.
func (*GitWorktrees) CleanupRecord(ctx context.Context, record EnvironmentRecord) error {
	return cleanupGitWorktree(ctx, record.SourceCheckout, record.WorktreePath, record.MetadataPath)
}

func cleanupGitWorktree(ctx context.Context, source, path, metadata string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(path) || !filepath.IsAbs(metadata) || source == path {
		return errors.New("refusing unsafe microvm worktree cleanup")
	}
	head, err := os.ReadFile(filepath.Join(metadata, "HEAD"))
	if err != nil {
		return fmt.Errorf("read microvm guest metadata identity: %w", err)
	}
	const prefix = "ref: refs/heads/"
	branch := strings.TrimSuffix(strings.TrimPrefix(string(head), prefix), "\n")
	if !strings.HasPrefix(string(head), prefix) || branch == "" || string(head) != prefix+branch+"\n" {
		return errors.New("refusing microvm worktree cleanup with invalid metadata identity")
	}
	if err := (&worktree.Prepared{SourceRoot: source, WorktreePath: path, MetadataPath: metadata, Branch: branch}).Cleanup(ctx); err != nil {
		return fmt.Errorf("remove microvm Git worktree: %w", err)
	}
	return nil
}

// RetentionAdapter exposes GitWorktrees through WorktreeRetention without
// overloading its two lifecycle Cleanup signatures.
type RetentionAdapter struct{ Worktrees *GitWorktrees }

// Dirty implements WorktreeRetention.Dirty.
func (a RetentionAdapter) Dirty(ctx context.Context, record EnvironmentRecord) (bool, error) {
	return a.Worktrees.Dirty(ctx, record)
}

// Cleanup implements WorktreeRetention.Cleanup.
func (a RetentionAdapter) Cleanup(ctx context.Context, record EnvironmentRecord) error {
	return a.Worktrees.CleanupRecord(ctx, record)
}

var placementProcessLocks sync.Map

// ChildRequestBuilder derives a child create request from an authoritative parent.
type ChildRequestBuilder func(context.Context, EnvironmentRecord, string) (CreateRequest, error)

// LifecycleChildren creates child generations through the same Lifecycle that owns
// VM/worktree admission, then performs conflict-aware Git merge-back.
type LifecycleChildren struct {
	creator   EnvironmentCreator
	registry  ReconcileRegistry
	admission *AdmissionController
	observer  *OperationsObserver
	build     ChildRequestBuilder
}

// NewLifecycleChildren constructs the daemon's production delegation lifecycle.
func NewLifecycleChildren(creator EnvironmentCreator, registry ReconcileRegistry, admission *AdmissionController, build ChildRequestBuilder, observers ...*OperationsObserver) *LifecycleChildren {
	var observer *OperationsObserver
	if len(observers) != 0 {
		observer = observers[0]
	}
	return &LifecycleChildren{creator: creator, registry: registry, admission: admission, observer: observer, build: build}
}

// Fork captures the parent revision before provisioning and holds only the transient
// fork quota here; Lifecycle remains the sole owner of VM/worktree reservations.
func (c *LifecycleChildren) Fork(ctx context.Context, parent EnvironmentRecord, label string) (EnvironmentRecord, error) {
	if c == nil || c.creator == nil || c.registry == nil || c.build == nil || parent.State != EnvironmentReady || label == "" {
		return EnvironmentRecord{}, ErrInvalidFork
	}
	var forkLease *AdmissionLease
	var err error
	if c.admission != nil {
		forkLease, err = c.admission.AcquireContext(ctx, parent.Owner, ResourceUsage{Forks: 1})
		if err != nil {
			if c.observer != nil {
				c.observer.QuotaRejected(QuotaForks)
			}
			return EnvironmentRecord{}, err
		}
		defer forkLease.Release()
	}
	base, err := captureForkBase(ctx, parent.WorktreePath)
	if err != nil {
		return EnvironmentRecord{}, fmt.Errorf("capture microvm fork base: %w", err)
	}
	request, err := c.build(ctx, parent, label)
	if err != nil {
		return EnvironmentRecord{}, err
	}
	request.Owner = parent.Owner
	request.Profile = parent.Profile
	request.ParentRef = parent.Ref
	request.ForkBase = base
	request.Worktree.BaseRevision = base
	created, err := c.creator.Create(ctx, request)
	if err != nil {
		return EnvironmentRecord{}, err
	}
	environmentID, _, err := parseEnvironmentRef(created.Ref)
	if err != nil {
		return EnvironmentRecord{}, err
	}
	child, err := c.registry.Lookup(ctx, environmentID)
	if err != nil {
		return EnvironmentRecord{}, err
	}
	if child.ParentRef != parent.Ref || child.ForkBase != request.ForkBase || child.State != EnvironmentReady {
		return EnvironmentRecord{}, ErrInvalidFork
	}
	return child, nil
}

func captureForkBase(ctx context.Context, parent string) (string, error) {
	first, err := captureForkBaseOnce(ctx, parent)
	if err != nil {
		return "", err
	}
	second, err := captureForkBaseOnce(ctx, parent)
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("parent worktree changed while capturing microvm fork base")
	}
	return first, nil
}

func captureForkBaseOnce(ctx context.Context, parent string) (string, error) {
	gitDirOut, err := gitexec.Run(ctx, parent, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(gitDirOut))
	index, err := os.CreateTemp(gitDir, ".mecatl-fork-index-*")
	if err != nil {
		return "", err
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		_ = os.Remove(indexPath)
		return "", err
	}
	if err := os.Remove(indexPath); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(indexPath) }()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := gitexec.RunWithEnv(ctx, parent, nil, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := gitexec.RunWithEnv(ctx, parent, nil, env, "add", "-A", "--"); err != nil {
		return "", err
	}
	base, err := gitexec.RunWithEnv(ctx, parent, nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(base))
	if len(value) != 40 && len(value) != 64 {
		return "", errors.New("git returned an invalid fork-base tree id")
	}
	return value, nil
}

// Merge rejects every parent-side overlap before applying the complete child patch.
func (*LifecycleChildren) Merge(ctx context.Context, parent, child EnvironmentRecord) error {
	if parent.State != EnvironmentReady || child.State != EnvironmentReady || child.ParentRef != parent.Ref || child.ForkBase == "" {
		return ErrInvalidFork
	}
	if _, err := gitexec.Run(ctx, child.WorktreePath, nil, "add", "-N", "--all"); err != nil {
		return fmt.Errorf("index microvm child additions: %w", err)
	}
	names, err := gitexec.Run(ctx, child.WorktreePath, nil, "diff", "--name-only", "-z", child.ForkBase, "--")
	if err != nil {
		return fmt.Errorf("enumerate microvm child changes: %w", err)
	}
	paths := splitNUL(names)
	if len(paths) == 0 {
		return nil
	}
	if err := checkParentMergeBase(ctx, parent.WorktreePath, child.ForkBase, paths); err != nil {
		return err
	}
	patchArgs := append([]string{"diff", "--binary", child.ForkBase, "--"}, paths...)
	patch, err := gitexec.Run(ctx, child.WorktreePath, nil, patchArgs...)
	if err != nil {
		return fmt.Errorf("build microvm child patch: %w", err)
	}
	if _, err := gitexec.Run(ctx, parent.WorktreePath, patch, "apply", "--check", "--binary", "-"); err != nil {
		return fmt.Errorf("%w: %v", ErrMergeConflict, err)
	}
	// The daemon parent lock covers every client merge. Re-read the immutable
	// base comparison after patch construction/check and directly before apply.
	if err := checkParentMergeBase(ctx, parent.WorktreePath, child.ForkBase, paths); err != nil {
		return err
	}
	if _, err := gitexec.Run(ctx, parent.WorktreePath, patch, "apply", "--binary", "-"); err != nil {
		return fmt.Errorf("apply microvm child patch: %w", err)
	}
	return nil
}

func checkParentMergeBase(ctx context.Context, parent, base string, paths []string) error {
	quietArgs := append([]string{"diff", "--quiet", base, "--"}, paths...)
	if _, err := gitexec.Run(ctx, parent, nil, quietArgs...); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return fmt.Errorf("%w: parent changed a child-modified path", ErrMergeConflict)
		}
		return fmt.Errorf("inspect microvm parent merge conflicts: %w", err)
	}
	return nil
}

func splitNUL(data []byte) []string {
	var result []string
	for _, value := range strings.Split(string(data), "\x00") {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

// FileSessionPersister writes daemon-side placement commits atomically. The host
// adapter consumes these records to stamp its own session aggregate.
type FileSessionPersister struct {
	path        string
	lock        *flock.Flock
	processLock *sync.Mutex
}

// NewFileSessionPersister constructs a placement journal projection.
func NewFileSessionPersister(path string) (*FileSessionPersister, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("microvm placement path must be absolute and clean")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	processLock, _ := placementProcessLocks.LoadOrStore(path, &sync.Mutex{})
	return &FileSessionPersister{path: path, lock: flock.New(path + ".lock"), processLock: processLock.(*sync.Mutex)}, nil
}

// Persist atomically commits the latest placement for each session.
func (p *FileSessionPersister) Persist(ctx context.Context, placement SessionPlacement) error {
	if placement.SessionID == "" || placement.Ref.Kind != Kind {
		return errors.New("invalid microvm session placement")
	}
	p.processLock.Lock()
	defer p.processLock.Unlock()
	locked, err := p.lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock microvm placements: %w", err)
	}
	if !locked {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		return errors.New("microvm placements lock was not acquired")
	}
	defer func() { _ = p.lock.Unlock() }()
	placements := make(map[string]SessionPlacement)
	data, err := os.ReadFile(p.path)
	if err == nil {
		if err := json.Unmarshal(data, &placements); err != nil {
			return fmt.Errorf("decode microvm placements: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	placements[placement.SessionID] = placement
	data, err = json.Marshal(placements)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".placements-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, p.path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(p.path))
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
