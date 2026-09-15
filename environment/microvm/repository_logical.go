package microvm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/gitexec"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/environment/microvm/workspace"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

// RepositoryGuestMount describes the host export and the distinct path visible
// to the guest. Only GuestPath may enter an authenticated Binding.
type RepositoryGuestMount struct {
	HostPath  string
	GuestPath string
}

// RepositoryGuestRegistrar authenticates and attaches one logical root to an
// already-running repository guest. It must not provide a host fallback.
type RepositoryGuestRegistrar interface {
	Register(context.Context, RepositoryVMRecord, control.Binding, RepositoryGuestMount) (*guestagent.Services, error)
	Unregister(context.Context, RepositoryVMRecord, control.Binding) error
}

// LogicalEnvironmentRequest selects a repository singleton and asks for one
// distinct logical Git worktree in it.
type LogicalEnvironmentRequest struct {
	Owner        string
	Checkout     string
	Verified     VerifiedArtifacts
	BaseRevision string
}

// LogicalEnvironment is one authenticated Workspace/runner pair in a shared repository VM.
type LogicalEnvironment struct {
	Repository   RepositoryVMRecord
	Binding      control.Binding
	Ref          EnvironmentRef
	WorktreePath string
	SourceRoot   string
	GuestRoot    string
	MetadataPath string
	IndexPath    string
	Branch       string
	Workspace    *workspace.Workspace
	Runner       *guestexec.Runner

	services   *guestagent.Services
	guest      RepositoryGuestRegistrar
	prepared   *worktree.Prepared
	preparer   *worktree.Preparer
	detachOnce sync.Once
	detachErr  error
	closeOnce  sync.Once
	closeErr   error
}

// Detach closes this process's guest registration and data-plane handles while
// preserving the logical worktree for exact session reattachment.
func (e *LogicalEnvironment) Detach() error {
	if e == nil {
		return nil
	}
	e.detachOnce.Do(func() {
		if e.guest != nil {
			e.detachErr = e.guest.Unregister(context.Background(), e.Repository, e.Binding)
		}
		if e.services != nil {
			e.detachErr = errors.Join(e.detachErr, e.services.Close())
		}
	})
	return e.detachErr
}

// DeletePreservingDirty detaches the logical environment and removes only a clean
// worktree. Dirty state remains at the reported worktree path for operator recovery.
func (e *LogicalEnvironment) DeletePreservingDirty(ctx context.Context) (bool, error) {
	if e == nil {
		return false, nil
	}
	if err := e.Detach(); err != nil {
		return false, err
	}
	status, err := gitexec.Run(ctx, e.WorktreePath, nil, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, fmt.Errorf("inspect repository logical worktree before delete: %w", err)
	}
	if len(status) != 0 {
		return true, nil
	}
	prepared := e.prepared
	if prepared == nil {
		prepared = &worktree.Prepared{
			SourceRoot: e.SourceRoot, WorktreePath: e.WorktreePath,
			MetadataPath: e.MetadataPath, Branch: e.Branch,
		}
	}
	if err := e.preparer.Cleanup(ctx, prepared); err != nil {
		return false, err
	}
	return false, nil
}

// Close detaches process-local protocol handles and removes only this logical
// worktree. It never stops the repository VM or removes its rootfs.
func (e *LogicalEnvironment) Close() error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		e.closeErr = e.Detach()
		if e.preparer != nil && e.prepared != nil {
			e.closeErr = errors.Join(e.closeErr, e.preparer.Cleanup(context.Background(), e.prepared))
		}
	})
	return e.closeErr
}

type repositoryLogicalStage string

const (
	repositoryLogicalStageEnsure   repositoryLogicalStage = "ensure"
	repositoryLogicalStageIdentity repositoryLogicalStage = "identity"
	repositoryLogicalStageAllocate repositoryLogicalStage = "allocate"
	repositoryLogicalStageReserve  repositoryLogicalStage = "reserve"
	repositoryLogicalStagePrepare  repositoryLogicalStage = "prepare"
	repositoryLogicalStageRegister repositoryLogicalStage = "register"
)

type repositoryLogicalStageError struct {
	stage repositoryLogicalStage
	cause error
}

func (e *repositoryLogicalStageError) Error() string {
	return "microvm repository logical environment failed during " + string(e.stage)
}

func (e *repositoryLogicalStageError) Unwrap() error { return e.cause }

func repositoryLogicalFailure(stage repositoryLogicalStage, err error) error {
	return &repositoryLogicalStageError{stage: stage, cause: err}
}

// RepositoryLogicalManager creates logical worktrees over the task-62 singleton registry.
type RepositoryLogicalManager struct {
	registry *RepositoryVMRegistry
	preparer *worktree.Preparer
	guest    RepositoryGuestRegistrar
}

// NewRepositoryLogicalManager constructs the daemon-side logical routing use case.
func NewRepositoryLogicalManager(registry *RepositoryVMRegistry, preparer *worktree.Preparer, guest RepositoryGuestRegistrar) (*RepositoryLogicalManager, error) {
	if registry == nil || preparer == nil || guest == nil {
		return nil, errors.New("microvm repository logical manager is not fully configured")
	}
	return &RepositoryLogicalManager{registry: registry, preparer: preparer, guest: guest}, nil
}

// Create reuses one healthy repository VM while allocating a fresh ref, branch,
// index, worktree, and assigned guest root.
func (m *RepositoryLogicalManager) Create(ctx context.Context, request LogicalEnvironmentRequest) (_ *LogicalEnvironment, retErr error) {
	result, err := m.registry.Ensure(ctx, RepositoryVMRequest{
		Owner: request.Owner, Checkout: request.Checkout, Verified: request.Verified,
	})
	if err != nil {
		return nil, repositoryLogicalFailure(repositoryLogicalStageEnsure, err)
	}
	identity, err := ResolveRepositoryIdentity(ctx, request.Owner, request.Checkout, m.registry.stateRoot)
	if err != nil {
		return nil, repositoryLogicalFailure(repositoryLogicalStageIdentity, err)
	}
	logicalID, err := randomLogicalID()
	if err != nil {
		return nil, repositoryLogicalFailure(repositoryLogicalStageAllocate, err)
	}
	logicalRoot, err := m.createLogicalRoot(identity, logicalID)
	if err != nil {
		return nil, repositoryLogicalFailure(repositoryLogicalStageReserve, err)
	}
	prepared, err := m.preparer.Prepare(ctx, worktree.Request{
		Source: request.Checkout, WorktreePath: filepath.Join(logicalRoot, "worktree"),
		MetadataPath: filepath.Join(logicalRoot, "metadata"), Branch: "mecatl/" + logicalID,
		BaseRevision: request.BaseRevision,
	})
	if err != nil {
		return nil, repositoryLogicalFailure(repositoryLogicalStagePrepare, err)
	}
	defer func() {
		if retErr != nil {
			_ = m.preparer.Cleanup(context.Background(), prepared)
		}
	}()
	ref := EnvironmentRef{Kind: Kind, ID: "logical-" + logicalID + "@" + fmt.Sprint(result.Record.Generation)}
	guestRoot := path.Join("/run/mecatl/repositories", logicalID, "worktree")
	binding := control.Binding{
		Owner: request.Owner, SessionID: logicalID, EnvironmentID: logicalID,
		Ref: ref.ID, Generation: result.Record.Generation, AssignedRoot: guestRoot,
	}
	services, err := m.guest.Register(ctx, result.Record, binding, RepositoryGuestMount{HostPath: prepared.WorktreePath, GuestPath: guestRoot})
	if err != nil {
		return nil, repositoryLogicalFailure(repositoryLogicalStageRegister, ErrRepositoryLogicalRootUnavailable)
	}
	return &LogicalEnvironment{
		Repository: result.Record, Binding: binding, Ref: ref,
		WorktreePath: prepared.WorktreePath, SourceRoot: prepared.SourceRoot, GuestRoot: binding.AssignedRoot,
		MetadataPath: prepared.MetadataPath, IndexPath: filepath.Join(prepared.MetadataPath, "index"), Branch: prepared.Branch,
		Workspace: services.Workspace, Runner: services.Runner,
		services: services, guest: m.guest, prepared: prepared, preparer: m.preparer,
	}, nil
}

// Reattach restores process-local handles for one exact retained logical ref.
// It first authenticates the recorded repository generation through Ensure and
// never creates a replacement when that health check fails.
func (m *RepositoryLogicalManager) Reattach(ctx context.Context, request LogicalEnvironmentRequest, ref EnvironmentRef) (*LogicalEnvironment, error) {
	result, err := m.registry.Ensure(ctx, RepositoryVMRequest{Owner: request.Owner, Checkout: request.Checkout})
	if err != nil {
		return nil, err
	}
	environmentID, generation, err := parseEnvironmentRef(ref)
	if err != nil || generation != result.Record.Generation || !strings.HasPrefix(environmentID, "logical-") {
		return nil, ErrRepositoryVMInconsistent
	}
	logicalID := strings.TrimPrefix(environmentID, "logical-")
	decoded, err := hex.DecodeString(logicalID)
	if err != nil || len(decoded) != 16 {
		return nil, ErrRepositoryVMInconsistent
	}
	identity, err := ResolveRepositoryIdentity(ctx, request.Owner, request.Checkout, m.registry.stateRoot)
	if err != nil {
		return nil, err
	}
	logicalRoot := filepath.Join(identity.StateDirectory, "logical", logicalID)
	worktreePath := filepath.Join(logicalRoot, "worktree")
	info, err := os.Lstat(worktreePath)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: retained logical worktree is unavailable", ErrRepositoryVMInconsistent)
	}
	guestRoot := path.Join(RepositoryGuestMountRoot, logicalID, "worktree")
	binding := control.Binding{
		Owner: request.Owner, SessionID: logicalID, EnvironmentID: logicalID,
		Ref: ref.ID, Generation: generation, AssignedRoot: guestRoot,
	}
	services, err := m.guest.Register(ctx, result.Record, binding, RepositoryGuestMount{HostPath: worktreePath, GuestPath: guestRoot})
	if err != nil {
		return nil, fmt.Errorf("reattach repository logical root: %w", ErrRepositoryLogicalRootUnavailable)
	}
	return &LogicalEnvironment{
		Repository: result.Record, Binding: binding, Ref: ref,
		WorktreePath: worktreePath, SourceRoot: request.Checkout, GuestRoot: guestRoot,
		MetadataPath: filepath.Join(logicalRoot, "metadata"), IndexPath: filepath.Join(logicalRoot, "metadata", "index"), Branch: "mecatl/" + logicalID,
		Workspace: services.Workspace, Runner: services.Runner,
		services: services, guest: m.guest, preparer: m.preparer,
	}, nil
}

func (m *RepositoryLogicalManager) createLogicalRoot(identity RepositoryIdentity, logicalID string) (string, error) {
	validated, err := m.registry.validateIdentity(identity)
	if err != nil {
		return "", err
	}
	repository, err := m.registry.openIdentityDirectory(validated, false)
	if err != nil {
		return "", err
	}
	defer func() { _ = repository.Close() }()
	if err := unix.Mkdirat(int(repository.file.Fd()), "logical", 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return "", fmt.Errorf("create repository logical namespace: %w", err)
	}
	logicalFD, err := openatOpaque(int(repository.file.Fd()), "logical", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open repository logical namespace: %w", err)
	}
	logical := os.NewFile(uintptr(logicalFD), filepath.Join(identity.StateDirectory, "logical"))
	defer func() { _ = logical.Close() }()
	info, err := logical.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("repository logical namespace is not private")
	}
	if err := unix.Mkdirat(logicalFD, logicalID, 0o700); err != nil {
		return "", fmt.Errorf("reserve repository logical identity: %w", err)
	}
	rootFD, err := openatOpaque(logicalFD, logicalID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open repository logical identity: %w", err)
	}
	root := os.NewFile(uintptr(rootFD), filepath.Join(identity.StateDirectory, "logical", logicalID))
	defer func() { _ = root.Close() }()
	info, err = root.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("repository logical identity is not private")
	}
	return filepath.Join(identity.StateDirectory, "logical", logicalID), nil
}

func randomLogicalID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("allocate repository logical environment: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
