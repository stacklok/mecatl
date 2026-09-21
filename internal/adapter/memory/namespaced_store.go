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

type duplicateRetirementStore interface {
	RetireDuplicate(context.Context, string, tool.MemoryVersion, string, tool.MemoryVersion) (tool.MemoryRecord, error)
}

type synthesisStore interface {
	SynthesizeReplacement(context.Context, tool.MemoryEntry, tool.MemoryVersion, []string, []tool.MemoryVersion) (tool.MemoryRecord, error)
}

type namespacedDuplicateStore struct {
	*NamespacedStore
	retirement duplicateRetirementStore
}

type namespacedSynthesisStore struct {
	*NamespacedStore
	synthesis synthesisStore
}

type namespacedReviewedStore struct {
	*namespacedDuplicateStore
	synthesis synthesisStore
}

// NewNamespacedStore confines a MemoryStore to namespace. Namespace is an
// adapter-private boundary, not a model-visible key prefix.
func NewNamespacedStore(store tool.MemoryStore, namespace string) tool.MemoryStore {
	if store == nil {
		panic("memory: NewNamespacedStore requires a non-nil MemoryStore")
	}
	if strings.TrimSpace(namespace) == "" {
		panic("memory: NewNamespacedStore requires a non-empty namespace")
	}
	base := &NamespacedStore{store: store, namespace: strings.TrimSuffix(namespace, "/") + "/"}
	retirement, hasRetirement := store.(duplicateRetirementStore)
	synthesis, hasSynthesis := store.(synthesisStore)
	switch {
	case hasRetirement && hasSynthesis:
		return &namespacedReviewedStore{namespacedDuplicateStore: &namespacedDuplicateStore{NamespacedStore: base, retirement: retirement}, synthesis: synthesis}
	case hasRetirement:
		return &namespacedDuplicateStore{NamespacedStore: base, retirement: retirement}
	case hasSynthesis:
		return &namespacedSynthesisStore{NamespacedStore: base, synthesis: synthesis}
	default:
		return base
	}
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

// Remember stores entry under this namespace with mandatory CAS.
func (s *NamespacedStore) Remember(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	entry.Key = s.key(entry.Key)
	record, err := s.store.Remember(ctx, entry, expected)
	return s.trimRecord(record), s.logicalError(err)
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

func (s *NamespacedStore) trimRecord(record tool.MemoryRecord) tool.MemoryRecord {
	record.Current.Key = strings.TrimPrefix(record.Current.Key, s.namespace)
	for i := range record.Revisions {
		record.Revisions[i].Key = strings.TrimPrefix(record.Revisions[i].Key, s.namespace)
	}
	return record
}

func (s *NamespacedStore) logicalError(err error) error {
	var conflict *tool.MemoryVersionConflictError
	if errors.As(err, &conflict) {
		logical := *conflict
		logical.Key = strings.TrimPrefix(logical.Key, s.namespace)
		return &logical
	}
	return err
}

func (s *NamespacedStore) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	record, ok, err := s.store.Inspect(ctx, s.key(key))
	return s.trimRecord(record), ok, s.logicalError(err)
}

func (s *NamespacedStore) Forget(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	record, err := s.store.Forget(ctx, s.key(key), expected)
	return s.trimRecord(record), s.logicalError(err)
}

func (s *NamespacedStore) Undo(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	record, err := s.store.Undo(ctx, s.key(key), expected)
	return s.trimRecord(record), s.logicalError(err)
}

func (s *namespacedDuplicateStore) RetireDuplicate(ctx context.Context, survivorKey string, survivorVersion tool.MemoryVersion, sourceKey string, sourceVersion tool.MemoryVersion) (tool.MemoryRecord, error) {
	record, err := s.retirement.RetireDuplicate(ctx, s.key(survivorKey), survivorVersion, s.key(sourceKey), sourceVersion)
	return s.trimRecord(record), s.logicalError(err)
}

func (s *namespacedSynthesisStore) SynthesizeReplacement(ctx context.Context, survivor tool.MemoryEntry, survivorVersion tool.MemoryVersion, sourceKeys []string, sourceVersions []tool.MemoryVersion) (tool.MemoryRecord, error) {
	return synthesizeNamespaced(ctx, s.NamespacedStore, s.synthesis, survivor, survivorVersion, sourceKeys, sourceVersions)
}

func (s *namespacedReviewedStore) SynthesizeReplacement(ctx context.Context, survivor tool.MemoryEntry, survivorVersion tool.MemoryVersion, sourceKeys []string, sourceVersions []tool.MemoryVersion) (tool.MemoryRecord, error) {
	return synthesizeNamespaced(ctx, s.NamespacedStore, s.synthesis, survivor, survivorVersion, sourceKeys, sourceVersions)
}

func synthesizeNamespaced(ctx context.Context, namespace *NamespacedStore, store synthesisStore, survivor tool.MemoryEntry, survivorVersion tool.MemoryVersion, sourceKeys []string, sourceVersions []tool.MemoryVersion) (tool.MemoryRecord, error) {
	survivor.Key = namespace.key(survivor.Key)
	physicalSources := make([]string, len(sourceKeys))
	for i, key := range sourceKeys {
		physicalSources[i] = namespace.key(key)
	}
	record, err := store.SynthesizeReplacement(ctx, survivor, survivorVersion, physicalSources, append([]tool.MemoryVersion(nil), sourceVersions...))
	return namespace.trimRecord(record), namespace.logicalError(err)
}

type memoryWorkspaceKey struct{}

// WithWorkspace annotates a run context for the caller-scoped project memory
// adapter. It is intentionally adapter-local: workspaces are already session
// state and do not widen any engine port.
func WithWorkspace(ctx context.Context, workspace string) context.Context {
	return context.WithValue(ctx, memoryWorkspaceKey{}, workspace)
}

// WorkspaceFromContext returns the private runtime workspace root attached by
// server composition. It is intentionally not derived from a durable
// EnvironmentRef, whose ID is provider-opaque.
func WorkspaceFromContext(ctx context.Context) string {
	workspace, _ := ctx.Value(memoryWorkspaceKey{}).(string)
	return workspace
}

var errCallerStoreIdentityRequired = errors.New("memory: verified caller is required")

// CallerStore chooses a namespace from the verified caller on each operation.
// Project stores additionally bind that namespace to the run's workspace.
type CallerStore struct {
	store   tool.MemoryStore
	project bool
}

var _ tool.MemoryStore = (*CallerStore)(nil)

type callerDuplicateStore struct {
	*CallerStore
}

type callerSynthesisStore struct {
	*CallerStore
}

type callerReviewedStore struct {
	*callerDuplicateStore
}

// NewCallerStore returns a store partitioned by verified caller and, when
// project is true, by workspace.
func NewCallerStore(store tool.MemoryStore, project bool) tool.MemoryStore {
	if store == nil {
		panic("memory: NewCallerStore requires a non-nil MemoryStore")
	}
	base := &CallerStore{store: store, project: project}
	_, hasRetirement := store.(duplicateRetirementStore)
	_, hasSynthesis := store.(synthesisStore)
	switch {
	case hasRetirement && hasSynthesis:
		return &callerReviewedStore{callerDuplicateStore: &callerDuplicateStore{CallerStore: base}}
	case hasRetirement:
		return &callerDuplicateStore{CallerStore: base}
	case hasSynthesis:
		return &callerSynthesisStore{CallerStore: base}
	default:
		return base
	}
}

func (s *CallerStore) scoped(ctx context.Context) (tool.MemoryStore, error) {
	principal := session.PrincipalFromContext(ctx)
	if principal == nil || principal.GrantType == session.GrantTypeSystem {
		return nil, errCallerStoreIdentityRequired
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

// Remember stores entry in the verified caller's namespace with mandatory CAS.
func (s *CallerStore) Remember(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.Remember(ctx, entry, expected)
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

func (s *CallerStore) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return tool.MemoryRecord{}, false, err
	}
	return store.Inspect(ctx, key)
}

func (s *CallerStore) Forget(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.Forget(ctx, key, expected)
}

func (s *CallerStore) Undo(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.Undo(ctx, key, expected)
}

func (s *callerDuplicateStore) RetireDuplicate(ctx context.Context, survivorKey string, survivorVersion tool.MemoryVersion, sourceKey string, sourceVersion tool.MemoryVersion) (tool.MemoryRecord, error) {
	store, err := s.scoped(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.(duplicateRetirementStore).RetireDuplicate(ctx, survivorKey, survivorVersion, sourceKey, sourceVersion)
}

func (s *callerSynthesisStore) SynthesizeReplacement(ctx context.Context, survivor tool.MemoryEntry, survivorVersion tool.MemoryVersion, sourceKeys []string, sourceVersions []tool.MemoryVersion) (tool.MemoryRecord, error) {
	return callerSynthesize(ctx, s.CallerStore, survivor, survivorVersion, sourceKeys, sourceVersions)
}

func (s *callerReviewedStore) SynthesizeReplacement(ctx context.Context, survivor tool.MemoryEntry, survivorVersion tool.MemoryVersion, sourceKeys []string, sourceVersions []tool.MemoryVersion) (tool.MemoryRecord, error) {
	return callerSynthesize(ctx, s.CallerStore, survivor, survivorVersion, sourceKeys, sourceVersions)
}

func callerSynthesize(ctx context.Context, caller *CallerStore, survivor tool.MemoryEntry, survivorVersion tool.MemoryVersion, sourceKeys []string, sourceVersions []tool.MemoryVersion) (tool.MemoryRecord, error) {
	store, err := caller.scoped(ctx)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	return store.(synthesisStore).SynthesizeReplacement(ctx, survivor, survivorVersion, sourceKeys, sourceVersions)
}
