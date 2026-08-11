package memory

import (
	"context"
	"crypto/sha256"
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

// NewNamespacedStore confines a MemoryStore to namespace. Namespace is an
// adapter-private boundary, not a model-visible key prefix.
func NewNamespacedStore(store tool.MemoryStore, namespace string) *NamespacedStore {
	if store == nil {
		panic("memory: NewNamespacedStore requires a non-nil MemoryStore")
	}
	if strings.TrimSpace(namespace) == "" {
		panic("memory: NewNamespacedStore requires a non-empty namespace")
	}
	return &NamespacedStore{store: store, namespace: namespace + "\x00"}
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

// NewCallerStore returns a store partitioned by verified caller and, when
// project is true, by workspace.
func NewCallerStore(store tool.MemoryStore, project bool) *CallerStore {
	if store == nil {
		panic("memory: NewCallerStore requires a non-nil MemoryStore")
	}
	return &CallerStore{store: store, project: project}
}

func (s *CallerStore) scoped(ctx context.Context) (*NamespacedStore, error) {
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
	return NewNamespacedStore(s.store, fmt.Sprintf("caller/%x", digest[:])), nil
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
