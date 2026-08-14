package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// NamespacedStore presents one logical MemoryStore namespace over a shared
// backing store. Every operation, including index/search and consolidation,
// is confined to namespace; callers never see the internal key prefix.
type NamespacedStore struct {
	store     tool.MemoryStore
	namespace string
}

var _ tool.MemoryStore = (*NamespacedStore)(nil)

type namespacedLifecycleStore struct {
	*NamespacedStore
	lifecycle tool.MemoryLifecycleStore
}

type namespacedConvergenceStore struct {
	*namespacedLifecycleStore
	convergence tool.MemoryConvergenceStore
}

var (
	_ tool.MemoryLifecycleStore   = (*namespacedLifecycleStore)(nil)
	_ tool.MemoryConvergenceStore = (*namespacedConvergenceStore)(nil)
)

// NewNamespacedStore confines a MemoryStore to namespace. Namespace is an
// adapter-private boundary, not a model-visible key prefix. The returned store
// advertises MemoryLifecycleStore exactly when the backing store does.
func NewNamespacedStore(store tool.MemoryStore, namespace string) tool.MemoryStore {
	if store == nil {
		panic("memory: NewNamespacedStore requires a non-nil MemoryStore")
	}
	if strings.TrimSpace(namespace) == "" {
		panic("memory: NewNamespacedStore requires a non-empty namespace")
	}
	base := &NamespacedStore{store: store, namespace: strings.TrimSuffix(namespace, "/") + "/"}
	if convergence, ok := store.(tool.MemoryConvergenceStore); ok {
		lifecycle := &namespacedLifecycleStore{NamespacedStore: base, lifecycle: convergence}
		return &namespacedConvergenceStore{namespacedLifecycleStore: lifecycle, convergence: convergence}
	}
	if lifecycle, ok := store.(tool.MemoryLifecycleStore); ok {
		return &namespacedLifecycleStore{NamespacedStore: base, lifecycle: lifecycle}
	}
	return base
}

func (s *NamespacedStore) key(key string) string { return s.namespace + key }

func (s *NamespacedStore) trim(entries []tool.MemoryEntry) []tool.MemoryEntry {
	out := make([]tool.MemoryEntry, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Key, s.namespace) {
			continue
		}
		entry.Key = strings.TrimPrefix(entry.Key, s.namespace)
		out = append(out, entry)
	}
	return out
}

// RememberEntry stores entry under this namespace.
func (s *NamespacedStore) RememberEntry(ctx context.Context, entry tool.MemoryEntry) error {
	entry.Key = s.key(entry.Key)
	return s.store.RememberEntry(ctx, entry)
}

// Recall retrieves key from this namespace.
func (s *NamespacedStore) Recall(ctx context.Context, key string) (tool.MemoryEntry, bool, error) {
	entry, ok, err := s.store.Recall(ctx, s.key(key))
	if ok {
		entry.Key = key
	}
	return entry, ok, err
}

// List returns namespace entries matching prefix.
func (s *NamespacedStore) List(ctx context.Context, prefix string) ([]tool.MemoryEntry, error) {
	entries, err := s.store.List(ctx, s.key(prefix))
	if err != nil {
		return nil, err
	}
	return s.trim(entries), nil
}

// Index returns namespace entries without their values.
func (s *NamespacedStore) Index(ctx context.Context) ([]tool.MemoryEntry, error) {
	entries, err := s.List(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Description = descriptionOrFirstLine(entries[i].Description, entries[i].Value)
		entries[i].Value = ""
	}
	return entries, nil
}

// Search ranks entries within this namespace.
func (s *NamespacedStore) Search(ctx context.Context, query string, limit int) ([]tool.MemoryEntry, error) {
	// The backing store's ranked Search cannot safely filter after ranking: entries
	// in another namespace could consume the requested page. Rank this namespace's
	// own snapshot using the same lexical search implementation.
	entries, err := s.List(ctx, "")
	if err != nil {
		return nil, err
	}
	return bm25Rank(entries, query, limit), nil
}

// Forget removes key from this namespace.
func (s *NamespacedStore) Forget(ctx context.Context, key string) error {
	return s.store.Forget(ctx, s.key(key))
}

func (s *namespacedLifecycleStore) trimRecord(record tool.MemoryRecord) tool.MemoryRecord {
	record.Current.Key = strings.TrimPrefix(record.Current.Key, s.namespace)
	for i := range record.Revisions {
		record.Revisions[i].Key = strings.TrimPrefix(record.Revisions[i].Key, s.namespace)
	}
	return record
}

func (s *namespacedLifecycleStore) logicalError(err error) error {
	var conflict *tool.MemoryVersionConflictError
	if errors.As(err, &conflict) {
		logical := *conflict
		logical.Key = strings.TrimPrefix(logical.Key, s.namespace)
		return &logical
	}
	return err
}

func (s *namespacedLifecycleStore) RememberVersioned(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	entry.Key = s.key(entry.Key)
	record, err := s.lifecycle.RememberVersioned(ctx, entry, expected)
	return s.trimRecord(record), s.logicalError(err)
}

func (s *namespacedLifecycleStore) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	record, ok, err := s.lifecycle.Inspect(ctx, s.key(key))
	return s.trimRecord(record), ok, s.logicalError(err)
}

func (s *namespacedLifecycleStore) ForgetVersioned(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	record, err := s.lifecycle.ForgetVersioned(ctx, s.key(key), expected)
	return s.trimRecord(record), s.logicalError(err)
}

func (s *namespacedLifecycleStore) UndoLatest(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	record, err := s.lifecycle.UndoLatest(ctx, s.key(key), expected)
	return s.trimRecord(record), s.logicalError(err)
}

func (s *namespacedConvergenceStore) RememberIfCurrent(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	entry.Key = s.key(entry.Key)
	record, err := s.convergence.RememberIfCurrent(ctx, entry, expected)
	return s.trimRecord(record), s.logicalError(err)
}

type memoryWorkspaceKey struct{}

// WithWorkspace annotates a run context for the caller-scoped project memory
// adapter. It is intentionally adapter-local: workspaces are already session
// state and do not widen any engine port.
func WithWorkspace(ctx context.Context, workspace string) context.Context {
	return context.WithValue(ctx, memoryWorkspaceKey{}, workspace)
}

// CallerStore chooses a namespace from the verified caller on each operation.
// Project stores additionally bind that namespace to the run's workspace.
type CallerStore struct {
	store   tool.MemoryStore
	project bool
}

var _ tool.MemoryStore = (*CallerStore)(nil)

type callerLifecycleStore struct {
	*CallerStore
}

type callerConvergenceStore struct {
	*callerLifecycleStore
}

var (
	_ tool.MemoryLifecycleStore   = (*callerLifecycleStore)(nil)
	_ tool.MemoryConvergenceStore = (*callerConvergenceStore)(nil)
)

// NewCallerStore returns a store partitioned by verified caller and, when
// project is true, by workspace. The returned store advertises
// MemoryLifecycleStore exactly when the backing store does.
func NewCallerStore(store tool.MemoryStore, project bool) tool.MemoryStore {
	if store == nil {
		panic("memory: NewCallerStore requires a non-nil MemoryStore")
	}
	base := &CallerStore{store: store, project: project}
	if _, ok := store.(tool.MemoryConvergenceStore); ok {
		return &callerConvergenceStore{callerLifecycleStore: &callerLifecycleStore{CallerStore: base}}
	}
	if _, ok := store.(tool.MemoryLifecycleStore); ok {
		return &callerLifecycleStore{CallerStore: base}
	}
	return base
}

func (s *CallerStore) scoped(ctx context.Context) (tool.MemoryStore, error) {
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		return nil, fmt.Errorf("memory: verified caller is required")
	}
	identity := principal.Issuer + "\x00" + principal.Subject
	if s.project {
		workspace, _ := ctx.Value(memoryWorkspaceKey{}).(string)
		if strings.TrimSpace(workspace) == "" {
			return nil, fmt.Errorf("memory: workspace is required for project memory")
		}
		identity += "\x00" + workspace
	}
	digest := sha256.Sum256([]byte(identity))
	return NewNamespacedStore(s.store, fmt.Sprintf("caller/id-%x", digest[:])), nil
}

// RememberEntry stores entry in the verified caller's namespace.
func (s *CallerStore) RememberEntry(ctx context.Context, entry tool.MemoryEntry) error {
	store, err := s.scoped(ctx)
	if err != nil {
		return err
	}
	return store.RememberEntry(ctx, entry)
}

// Recall retrieves key from the verified caller's namespace.
func (s *CallerStore) Recall(ctx context.Context, key string) (tool.MemoryEntry, bool, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return tool.MemoryEntry{}, false, err
	}
	return store.Recall(ctx, key)
}

// List returns verified caller entries matching prefix.
func (s *CallerStore) List(ctx context.Context, prefix string) ([]tool.MemoryEntry, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return nil, err
	}
	return store.List(ctx, prefix)
}

// Index returns verified caller entries without their values.
func (s *CallerStore) Index(ctx context.Context) ([]tool.MemoryEntry, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return nil, err
	}
	return store.Index(ctx)
}

// Search ranks entries in the verified caller's namespace.
func (s *CallerStore) Search(ctx context.Context, query string, limit int) ([]tool.MemoryEntry, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return nil, err
	}
	return store.Search(ctx, query, limit)
}

// Forget removes key from the verified caller's namespace.
func (s *CallerStore) Forget(ctx context.Context, key string) error {
	store, err := s.scoped(ctx)
	if err != nil {
		return err
	}
	return store.Forget(ctx, key)
}

func (s *callerLifecycleStore) scopedLifecycle(ctx context.Context) (tool.MemoryLifecycleStore, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return nil, err
	}
	return store.(tool.MemoryLifecycleStore), nil
}

func (s *callerLifecycleStore) RememberVersioned(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	store, err := s.scopedLifecycle(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.RememberVersioned(ctx, entry, expected)
}

func (s *callerLifecycleStore) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	store, err := s.scopedLifecycle(ctx)
	if err != nil {
		return tool.MemoryRecord{}, false, err
	}
	return store.Inspect(ctx, key)
}

func (s *callerLifecycleStore) ForgetVersioned(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	store, err := s.scopedLifecycle(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.ForgetVersioned(ctx, key, expected)
}

func (s *callerLifecycleStore) UndoLatest(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	store, err := s.scopedLifecycle(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.UndoLatest(ctx, key, expected)
}

func (s *callerConvergenceStore) RememberIfCurrent(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.(tool.MemoryConvergenceStore).RememberIfCurrent(ctx, entry, expected)
}
