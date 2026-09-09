package microvm

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

// RepositoryAttachment is one session or isolated-child handle on a logical
// worktree in a repository-scoped VM.
type RepositoryAttachment struct {
	Logical     *LogicalEnvironment
	Environment tool.Environment

	manager *RepositoryAttachmentManager
	once    sync.Once
	err     error
}

// Close detaches only this logical environment and its process-local handles.
func (a *RepositoryAttachment) Close() error {
	if a == nil {
		return nil
	}
	a.once.Do(func() {
		var record *repositoryAttachmentRecord
		if a.manager != nil {
			a.manager.mu.Lock()
			delete(a.manager.active, a.Environment.Ref())
			record = a.manager.records[a.Environment.Ref().ID]
			a.manager.mu.Unlock()
		}
		a.err = a.Logical.Close()
		if a.manager != nil {
			a.manager.mu.Lock()
			if record != nil {
				a.err = errors.Join(a.err, removeRepositoryAttachmentRecord(record))
			}
			delete(a.manager.records, a.Environment.Ref().ID)
			a.manager.mu.Unlock()
		}
	})
	return a.err
}

type repositoryChildAttachment struct {
	attachment *RepositoryAttachment
	parent     session.EnvironmentRef
	forkBase   string
}

type repositoryAttachmentRecord struct {
	binding          control.Binding
	repository       RepositoryVMRecord
	worktreePath     string
	sourceRoot       string
	metadataPath     string
	branch           string
	attachment       *RepositoryAttachment
	worktreeRetained bool
	deleted          bool
	deleting         bool
}

// RepositoryAttachmentManager adapts repository logical worktrees to session
// attachment and the engine's existing isolated-child fork/merge seams.
type RepositoryAttachmentManager struct {
	logical *RepositoryLogicalManager
	mu      sync.Mutex
	active  map[session.EnvironmentRef]*repositoryChildAttachment
	records map[string]*repositoryAttachmentRecord
}

func newRepositoryAttachmentManager(logical *RepositoryLogicalManager) (*RepositoryAttachmentManager, error) {
	manager := &RepositoryAttachmentManager{
		logical: logical,
		active:  make(map[session.EnvironmentRef]*repositoryChildAttachment),
		records: make(map[string]*repositoryAttachmentRecord),
	}
	if err := manager.loadRecords(); err != nil {
		return nil, err
	}
	return manager, nil
}

var (
	_ tool.EnvironmentForker = (*RepositoryAttachmentManager)(nil)
	_ tool.EnvironmentMerger = (*RepositoryAttachmentManager)(nil)
)

// Attach allocates a distinct logical worktree in the canonical repository VM.
func (m *RepositoryAttachmentManager) Attach(ctx context.Context, request LogicalEnvironmentRequest) (*RepositoryAttachment, error) {
	if m == nil || m.logical == nil {
		return nil, ErrEnvironmentUnavailable
	}
	logical, err := m.logical.Create(ctx, request)
	if err != nil {
		return nil, err
	}
	environment, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvironmentKind(logical.Ref.Kind), ID: logical.Ref.ID}, logical.Workspace, memledger.New(), logical.Runner)
	if err != nil {
		_ = logical.Close()
		return nil, err
	}
	attachment := &RepositoryAttachment{Logical: logical, Environment: environment, manager: m}
	m.mu.Lock()
	if _, exists := m.active[environment.Ref()]; exists {
		m.mu.Unlock()
		_ = logical.Close()
		return nil, errors.New("repository logical environment identity collided")
	}
	m.active[environment.Ref()] = &repositoryChildAttachment{attachment: attachment}
	m.mu.Unlock()
	return attachment, nil
}

// Reattach restores the exact persisted logical ref after authenticating the
// repository generation and retained worktree.
func (m *RepositoryAttachmentManager) Reattach(ctx context.Context, request LogicalEnvironmentRequest, ref session.EnvironmentRef) (*RepositoryAttachment, error) {
	if m == nil || m.logical == nil {
		return nil, ErrEnvironmentUnavailable
	}
	logical, err := m.logical.Reattach(ctx, request, EnvironmentRef{Kind: string(ref.Kind), ID: ref.ID})
	if err != nil {
		return nil, err
	}
	environment, err := tool.NewEnvironment(ref, logical.Workspace, memledger.New(), logical.Runner)
	if err != nil {
		_ = logical.Detach()
		return nil, err
	}
	attachment := &RepositoryAttachment{Logical: logical, Environment: environment, manager: m}
	m.mu.Lock()
	if _, exists := m.active[ref]; exists {
		m.mu.Unlock()
		_ = logical.Detach()
		return nil, errors.New("repository logical environment is already attached")
	}
	m.active[ref] = &repositoryChildAttachment{attachment: attachment}
	m.mu.Unlock()
	return attachment, nil
}

// Detach releases only process-local handles and retains the logical worktree.
func (m *RepositoryAttachmentManager) Detach(ref session.EnvironmentRef) error {
	entry := m.lookupChild(ref)
	if entry == nil {
		return ErrEnvironmentUnavailable
	}
	m.mu.Lock()
	delete(m.active, ref)
	m.mu.Unlock()
	return entry.attachment.Logical.Detach()
}

// Fork captures the parent's exact Git tree, then attaches a distinct logical
// worktree to the same repository VM. The returned cleanup detaches only the child.
func (m *RepositoryAttachmentManager) Fork(ctx context.Context, parent tool.Environment, label string) (tool.Environment, func() error, string, error) {
	if label == "" {
		return tool.Environment{}, nil, "", ErrInvalidFork
	}
	parentAttachment := m.lookupChild(parent.Ref())
	if parentAttachment == nil || parentAttachment.attachment.Environment.Workspace() != parent.Workspace() {
		return tool.Environment{}, nil, "", fmt.Errorf("%w: parent attachment is not active", ErrInvalidFork)
	}
	base, err := captureForkBase(ctx, parentAttachment.attachment.Logical.WorktreePath)
	if err != nil {
		return tool.Environment{}, nil, "", fmt.Errorf("capture repository child base: %w", err)
	}
	request := LogicalEnvironmentRequest{
		Owner:        parentAttachment.attachment.Logical.Repository.Owner,
		Checkout:     parentAttachment.attachment.Logical.WorktreePath,
		BaseRevision: base,
	}
	child, err := m.Attach(ctx, request)
	if err != nil {
		return tool.Environment{}, nil, "", err
	}
	m.mu.Lock()
	entry := m.active[child.Environment.Ref()]
	entry.parent = parent.Ref()
	entry.forkBase = base
	m.mu.Unlock()
	return child.Environment, child.Close, "", nil
}

// Merge reuses the established conflict-aware isolated-child patch path. A
// conflict never closes or removes the child attachment.
func (m *RepositoryAttachmentManager) Merge(ctx context.Context, child, parent tool.Environment) error {
	parentAttachment := m.lookupChild(parent.Ref())
	childAttachment := m.lookupChild(child.Ref())
	if parentAttachment == nil || childAttachment == nil || childAttachment.parent != parent.Ref() || childAttachment.forkBase == "" {
		return ErrInvalidFork
	}
	toRef := func(ref session.EnvironmentRef) EnvironmentRef {
		return EnvironmentRef{Kind: string(ref.Kind), ID: ref.ID}
	}
	parentRecord := EnvironmentRecord{
		State: EnvironmentReady, Ref: toRef(parent.Ref()),
		WorktreePath: parentAttachment.attachment.Logical.WorktreePath,
	}
	childRecord := EnvironmentRecord{
		State: EnvironmentReady, Ref: toRef(child.Ref()), ParentRef: parentRecord.Ref,
		WorktreePath: childAttachment.attachment.Logical.WorktreePath, ForkBase: childAttachment.forkBase,
	}
	return (&LifecycleChildren{}).Merge(ctx, parentRecord, childRecord)
}

func (m *RepositoryAttachmentManager) lookup(ref session.EnvironmentRef) *RepositoryAttachment {
	entry := m.lookupChild(ref)
	if entry == nil {
		return nil
	}
	return entry.attachment
}

func (m *RepositoryAttachmentManager) lookupChild(ref session.EnvironmentRef) *repositoryChildAttachment {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[ref]
}

func (m *RepositoryAttachmentManager) register(binding control.Binding, attachment *RepositoryAttachment) error {
	if m == nil || attachment == nil || binding.Ref == "" || binding.Ref != attachment.Environment.Ref().ID {
		return errors.New("repository logical attachment binding is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.records[binding.Ref]; existing != nil && existing.binding != binding {
		return errors.New("repository logical attachment binding collided")
	}
	record := &repositoryAttachmentRecord{
		binding: binding, repository: attachment.Logical.Repository,
		worktreePath: attachment.Logical.WorktreePath, sourceRoot: attachment.Logical.Repository.GitCommonDirectory,
		metadataPath: attachment.Logical.MetadataPath, branch: attachment.Logical.Branch, attachment: attachment,
	}
	if err := persistRepositoryAttachment(record); err != nil {
		return err
	}
	m.records[binding.Ref] = record
	return nil
}

func (m *RepositoryAttachmentManager) inventory(owner string) []repositoryAttachmentRecord {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]repositoryAttachmentRecord, 0, len(m.records))
	for _, record := range m.records {
		if record.binding.Owner == owner {
			out = append(out, *record)
		}
	}
	return out
}

func (m *RepositoryAttachmentManager) health(ctx context.Context, record RepositoryVMRecord) error {
	if m == nil || m.logical == nil || m.logical.registry == nil {
		return ErrEnvironmentUnavailable
	}
	return m.logical.registry.inspect(ctx, record)
}

func (m *RepositoryAttachmentManager) delete(ctx context.Context, binding control.Binding) (LifecycleDeleteResult, error) {
	if m == nil {
		return LifecycleDeleteResult{}, ErrEnvironmentUnavailable
	}
	m.mu.Lock()
	record := m.records[binding.Ref]
	if record == nil || record.binding != binding || record.deleted || record.deleting {
		m.mu.Unlock()
		return LifecycleDeleteResult{}, control.ErrBindingMismatch
	}
	record.deleting = true
	attachment := record.attachment
	if attachment == nil {
		attachment = &RepositoryAttachment{Logical: &LogicalEnvironment{
			Repository: record.repository, Ref: EnvironmentRef{Kind: Kind, ID: record.binding.Ref},
			WorktreePath: record.worktreePath, SourceRoot: record.sourceRoot,
			MetadataPath: record.metadataPath, Branch: record.branch, preparer: m.logical.preparer,
		}}
	}
	m.mu.Unlock()
	retained, err := attachment.Logical.DeletePreservingDirty(ctx)
	if err != nil {
		m.mu.Lock()
		record.deleting = false
		m.mu.Unlock()
		return LifecycleDeleteResult{}, err
	}
	m.mu.Lock()
	if retained {
		updated := *record
		updated.attachment = nil
		updated.deleted = true
		updated.worktreeRetained = true
		updated.deleting = false
		if err := persistRepositoryAttachment(&updated); err != nil {
			record.deleting = false
			m.mu.Unlock()
			return LifecycleDeleteResult{}, err
		}
		if record.attachment != nil {
			delete(m.active, record.attachment.Environment.Ref())
		}
		*record = updated
	} else {
		if err := removeRepositoryAttachmentRecord(record); err != nil {
			record.deleting = false
			m.mu.Unlock()
			return LifecycleDeleteResult{}, err
		}
		if record.attachment != nil {
			delete(m.active, record.attachment.Environment.Ref())
		}
		delete(m.records, binding.Ref)
	}
	m.mu.Unlock()
	return LifecycleDeleteResult{WorktreePath: record.worktreePath, WorktreeRetained: retained}, nil
}
