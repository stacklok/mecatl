package microvm

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const defaultMaxActiveForks = 8

var (
	// ErrForkQuotaExceeded is returned before driver work starts when the active
	// child-environment quota is full.
	ErrForkQuotaExceeded = errors.New("microvm child environment fork quota exceeded")
	// ErrInvalidFork rejects an incomplete, aliased, or generation-inconsistent
	// child returned by the driver.
	ErrInvalidFork = errors.New("invalid microvm child environment")
	// ErrMergeConflict means the parent changed from the child's immutable fork
	// base on at least one path the child changed. The parent is left untouched.
	ErrMergeConflict = errors.New("microvm child environment merge conflict")
)

// ForkRequest asks the driver to capture an isolated child from one exact parent
// environment generation. Label is descriptive only and never an identity.
type ForkRequest struct {
	Parent session.EnvironmentRef
	Label  string
}

// ForkedEnvironment is the complete child capability plus the driver-owned
// identities needed to prove concurrent children do not share mutable resources.
type ForkedEnvironment struct {
	Environment  tool.Environment
	WorktreePath string
	MetadataPath string
	Endpoint     string
	Generation   uint32
	// BaseRevision identifies the immutable tree captured before the child can
	// mutate. The daemon persists it with the child generation.
	BaseRevision string
}

// MergeRequest asks the daemon to atomically apply one child's additions,
// replacements, and deletions relative to its durable immutable fork base.
// BaseRevision is supplied by the creating harness when available; after a
// harness restart it may be empty and the daemon resolves it from the durable
// child record. Parent and Child are always generation-fenced.
type MergeRequest struct {
	Parent       session.EnvironmentRef
	Child        session.EnvironmentRef
	BaseRevision string
}

// ForkDriver is the daemon-side child lifecycle seam. Fork must capture Parent
// exactly and durably retain its immutable base before returning a complete
// Workspace+runner Environment. Merge must validate parentage and the durable
// base, detect every conflict before applying any path, then atomically apply
// additions, replacements, and deletions. Destroy is idempotent for the returned
// generation.
type ForkDriver interface {
	Fork(context.Context, ForkRequest) (ForkedEnvironment, error)
	Merge(context.Context, MergeRequest) error
	Destroy(context.Context, session.EnvironmentRef) error
}

type activeFork struct {
	forked ForkedEnvironment
	parent session.EnvironmentRef
}

const mergeLockStripes = 64

// EnvironmentForker adapts the microVM driver to the engine's complete-child
// EnvironmentForker and EnvironmentMerger seams. Its quota is held for the whole
// child lifetime, not merely while the create RPC is in flight. Fixed lock
// stripes serialize every merge for the same parent without retaining an
// unbounded parent-key map.
type EnvironmentForker struct {
	driver ForkDriver
	quota  chan struct{}

	mu          sync.Mutex
	active      map[session.EnvironmentRef]activeFork
	roots       map[string]struct{}
	metadata    map[string]struct{}
	endpoints   map[string]struct{}
	generations map[uint32]struct{}
	mergeLocks  [mergeLockStripes]sync.Mutex
}

// NewEnvironmentForker constructs a fail-fast, active-child-bounded driver
// forker. Non-positive limits use a conservative default.
func NewEnvironmentForker(driver ForkDriver, maxActive int) *EnvironmentForker {
	if maxActive <= 0 {
		maxActive = defaultMaxActiveForks
	}
	return &EnvironmentForker{
		driver:      driver,
		quota:       make(chan struct{}, maxActive),
		active:      make(map[session.EnvironmentRef]activeFork),
		roots:       make(map[string]struct{}),
		metadata:    make(map[string]struct{}),
		endpoints:   make(map[string]struct{}),
		generations: make(map[uint32]struct{}),
	}
}

var (
	_ tool.EnvironmentForker = (*EnvironmentForker)(nil)
	_ tool.EnvironmentMerger = (*EnvironmentForker)(nil)
)

// Fork creates one complete isolated child. It rejects a driver that aliases any
// active child's ref, writable worktree, Git metadata, endpoint, or generation.
func (f *EnvironmentForker) Fork(ctx context.Context, base tool.Environment, label string) (tool.Environment, func() error, string, error) {
	if f == nil || f.driver == nil || base.Ref().Kind != Kind {
		return tool.Environment{}, nil, "", fmt.Errorf("%w: unsupported parent environment", ErrInvalidFork)
	}
	select {
	case f.quota <- struct{}{}:
	case <-ctx.Done():
		return tool.Environment{}, nil, "", context.Cause(ctx)
	default:
		return tool.Environment{}, nil, "", ErrForkQuotaExceeded
	}
	releaseQuota := true
	defer func() {
		if releaseQuota {
			<-f.quota
		}
	}()

	forked, err := f.driver.Fork(ctx, ForkRequest{Parent: base.Ref(), Label: label})
	if err != nil {
		return tool.Environment{}, nil, "", err
	}
	if err := validateForkedEnvironment(base, forked); err != nil {
		_ = f.driver.Destroy(context.WithoutCancel(ctx), forked.Environment.Ref())
		return tool.Environment{}, nil, "", err
	}
	if !f.reserve(base.Ref(), forked) {
		_ = f.driver.Destroy(context.WithoutCancel(ctx), forked.Environment.Ref())
		return tool.Environment{}, nil, "", fmt.Errorf("%w: driver returned an identity already held by another child", ErrInvalidFork)
	}

	releaseQuota = false
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() {
			cleanupErr = f.driver.Destroy(context.WithoutCancel(ctx), forked.Environment.Ref())
			f.release(forked)
			<-f.quota
		})
		return cleanupErr
	}
	return forked.Environment, cleanup, "", nil
}

// Merge applies one child generation through the daemon's durable fork-base
// transaction. Every merge targeting the same parent takes the same lock stripe;
// the driver performs conflict discovery before its atomic apply. No cleanup is
// attempted here, so a conflicting child remains inspectable and caller-owned.
func (f *EnvironmentForker) Merge(ctx context.Context, child, parent tool.Environment) error {
	if f == nil || f.driver == nil || child.Ref().Kind != Kind || parent.Ref().Kind != Kind || child.Ref() == parent.Ref() {
		return fmt.Errorf("%w: invalid merge environments", ErrInvalidFork)
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}

	request := MergeRequest{Parent: parent.Ref(), Child: child.Ref()}
	f.mu.Lock()
	if active, ok := f.active[child.Ref()]; ok {
		if active.parent != parent.Ref() {
			f.mu.Unlock()
			return fmt.Errorf("%w: child belongs to another parent", ErrInvalidFork)
		}
		request.BaseRevision = active.forked.BaseRevision
	}
	f.mu.Unlock()

	lock := &f.mergeLocks[parentMergeLock(parent.Ref())]
	lock.Lock()
	defer lock.Unlock()
	return f.driver.Merge(ctx, request)
}

func parentMergeLock(ref session.EnvironmentRef) uint64 {
	// FNV-1a, kept inline to avoid a hash allocation on this off-hot-path lock.
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	hash := offset
	for _, value := range []string{string(ref.Kind), ref.ID} {
		for i := range len(value) {
			hash ^= uint64(value[i])
			hash *= prime
		}
		hash ^= 0xff
		hash *= prime
	}
	return hash % mergeLockStripes
}

func validateForkedEnvironment(base tool.Environment, forked ForkedEnvironment) error {
	env := forked.Environment
	ref := env.Ref()
	_, generation, err := parseEnvironmentRef(EnvironmentRef{Kind: string(ref.Kind), ID: ref.ID})
	if err != nil || ref == base.Ref() || env.Workspace() == nil || env.CommandRunner() == nil ||
		forked.WorktreePath == "" || forked.MetadataPath == "" || forked.Endpoint == "" ||
		forked.Generation == 0 || generation != forked.Generation || forked.BaseRevision == "" {
		return fmt.Errorf("%w: incomplete or generation-inconsistent driver result", ErrInvalidFork)
	}
	return nil
}

func (f *EnvironmentForker) reserve(parent session.EnvironmentRef, child ForkedEnvironment) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := child.Environment.Ref()
	if _, ok := f.active[ref]; ok {
		return false
	}
	if _, ok := f.roots[child.WorktreePath]; ok {
		return false
	}
	if _, ok := f.metadata[child.MetadataPath]; ok {
		return false
	}
	if _, ok := f.endpoints[child.Endpoint]; ok {
		return false
	}
	if _, ok := f.generations[child.Generation]; ok {
		return false
	}
	f.active[ref] = activeFork{forked: child, parent: parent}
	f.roots[child.WorktreePath] = struct{}{}
	f.metadata[child.MetadataPath] = struct{}{}
	f.endpoints[child.Endpoint] = struct{}{}
	f.generations[child.Generation] = struct{}{}
	return true
}

func (f *EnvironmentForker) release(child ForkedEnvironment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.active, child.Environment.Ref())
	delete(f.roots, child.WorktreePath)
	delete(f.metadata, child.MetadataPath)
	delete(f.endpoints, child.Endpoint)
	delete(f.generations, child.Generation)
}
