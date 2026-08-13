// Package memmemory provides a concurrent in-memory reference implementation of
// tool.MemoryStore and its optional tool.MemoryLifecycleStore capability.
package memmemory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

const (
	defaultSearchLimit = 10
	maxRevisionsPerKey = 64
)

type record struct {
	revisions []tool.MemoryRevision
	undone    map[tool.MemoryVersion]bool
	truncated bool
}

// Store is a process-local memory store. It is safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	records map[string]*record
	next    uint64
	now     func() time.Time
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{records: make(map[string]*record), now: time.Now}
}

var (
	_ tool.MemoryStore          = (*Store)(nil)
	_ tool.MemoryLifecycleStore = (*Store)(nil)
)

// RememberEntry preserves the legacy MemoryStore behavior: only blank keys are
// rejected and writes overwrite unconditionally.
func (s *Store) RememberEntry(ctx context.Context, entry tool.MemoryEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(entry.Key) == "" {
		return fmt.Errorf("memmemory: empty key")
	}
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	if err := tool.ValidateMemoryContentWrite(entry.Key, entry.Value, entry.Description, attribution); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendActive(ctx, entry, tool.MemoryOriginImported)
	return nil
}

// Recall returns the active value for key. Deleted records are legacy misses.
func (s *Store) Recall(ctx context.Context, key string) (tool.MemoryEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return tool.MemoryEntry{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rev, ok := s.active(key)
	if !ok {
		return tool.MemoryEntry{}, false, nil
	}
	return entryOf(rev), true, nil
}

// List returns full active entries matching prefix in key order.
func (s *Store) List(ctx context.Context, prefix string) ([]tool.MemoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]tool.MemoryEntry, 0, len(s.records))
	for key := range s.records {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if rev, ok := s.active(key); ok {
			out = append(out, entryOf(rev))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Forget unconditionally deletes an active legacy entry. The tombstone remains
// available through Inspect; deleting a missing key is a no-op.
func (s *Store) Forget(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.active(key); ok {
		s.appendDeleted(ctx, key, tool.MemoryOriginImported)
	}
	return nil
}

// Index returns active routing entries with values omitted.
func (s *Store) Index(ctx context.Context) ([]tool.MemoryEntry, error) {
	entries, err := s.List(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Description = description(entries[i])
		entries[i].Value = ""
	}
	return entries, nil
}

// Search performs deterministic case-insensitive lexical matching over active
// keys, descriptions, and values.
func (s *Store) Search(ctx context.Context, query string, k int) ([]tool.MemoryEntry, error) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil, nil
	}
	if k <= 0 {
		k = defaultSearchLimit
	}
	entries, err := s.List(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]tool.MemoryEntry, 0, min(k, len(entries)))
	for _, entry := range entries {
		haystack := strings.ToLower(entry.Key + " " + entry.Description + " " + entry.Value)
		matched := false
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		entry.Description = description(entry)
		entry.Value = ""
		out = append(out, entry)
		if len(out) == k {
			break
		}
	}
	return out, nil
}

// RememberVersioned atomically creates or replaces an active record when its
// current version equals expected.
func (s *Store) RememberVersioned(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return tool.MemoryRecord{}, err
	}
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	if err := tool.ValidateMemoryEntryWrite(entry, attribution); err != nil {
		return tool.MemoryRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != "" {
		if err := s.compare(entry.Key, expected); err != nil {
			return tool.MemoryRecord{}, err
		}
	}
	s.appendActive(ctx, entry, tool.MemoryOriginExplicit)
	return s.snapshot(entry.Key), nil
}

// Inspect returns current state and complete history, including tombstones.
func (s *Store) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return tool.MemoryRecord{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.records[key]; !ok {
		return tool.MemoryRecord{}, false, nil
	}
	return s.snapshot(key), true, nil
}

// ForgetVersioned atomically appends a deletion when expected is current.
func (s *Store) ForgetVersioned(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return tool.MemoryRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.compare(key, expected); err != nil {
		return tool.MemoryRecord{}, err
	}
	r, ok := s.records[key]
	if !ok || len(r.revisions) == 0 || r.revisions[len(r.revisions)-1].Status == tool.MemoryStatusDeleted {
		return tool.MemoryRecord{}, fmt.Errorf("memmemory: %q: %w", key, tool.ErrMemoryNotFound)
	}
	s.appendDeleted(ctx, key, tool.MemoryOriginExplicit)
	return s.snapshot(key), nil
}

// UndoLatest atomically appends a compensating revision restoring the state
// immediately before the current revision.
func (s *Store) UndoLatest(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return tool.MemoryRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.compare(key, expected); err != nil {
		return tool.MemoryRecord{}, err
	}
	r, ok := s.records[key]
	if !ok || len(r.revisions) == 0 {
		return tool.MemoryRecord{}, fmt.Errorf("memmemory: %q: %w", key, tool.ErrMemoryNotFound)
	}
	target := -1
	for i := len(r.revisions) - 1; i >= 0; i-- {
		candidate := r.revisions[i]
		if candidate.Origin != tool.MemoryOriginUndo && !r.undone[candidate.Version] {
			target = i
			break
		}
	}
	if target < 0 {
		return tool.MemoryRecord{}, fmt.Errorf("memmemory: %q: no mutation remains to undo", key)
	}
	if target == 0 && r.truncated {
		return tool.MemoryRecord{}, fmt.Errorf("memmemory: %q: cannot undo beyond retained history", key)
	}
	if r.undone == nil {
		r.undone = make(map[tool.MemoryVersion]bool)
	}
	r.undone[r.revisions[target].Version] = true
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	attribution.Origin = tool.MemoryOriginUndo
	ctx = tool.WithMemoryAttribution(ctx, attribution)
	if target == 0 || r.revisions[target-1].Status == tool.MemoryStatusDeleted {
		s.appendDeleted(ctx, key, tool.MemoryOriginUndo)
	} else {
		s.appendActive(ctx, entryOf(r.revisions[target-1]), tool.MemoryOriginUndo)
	}
	return s.snapshot(key), nil
}

func (s *Store) compare(key string, expected tool.MemoryVersion) error {
	var actual tool.MemoryVersion
	if r, ok := s.records[key]; ok && len(r.revisions) != 0 {
		actual = r.revisions[len(r.revisions)-1].Version
	}
	if actual != expected {
		return &tool.MemoryVersionConflictError{Key: key, Expected: expected, Actual: actual}
	}
	return nil
}

func (s *Store) appendActive(ctx context.Context, entry tool.MemoryEntry, fallbackOrigin tool.MemoryOrigin) {
	r := s.ensure(entry.Key)
	if len(r.revisions) != 0 {
		last := &r.revisions[len(r.revisions)-1]
		if last.Status == tool.MemoryStatusActive {
			last.Status = tool.MemoryStatusSuperseded
		}
	}
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	if attribution.Origin == "" {
		attribution.Origin = fallbackOrigin
	}
	r.revisions = append(r.revisions, tool.MemoryRevision{
		Key: entry.Key, Value: entry.Value, Description: strings.TrimSpace(entry.Description),
		Version: s.version(), Status: tool.MemoryStatusActive, Writer: attribution.Writer,
		Origin: attribution.Origin, Source: attribution.Source, UpdatedAt: s.now().UTC(),
	})
	r.retainLatest()
}

func (s *Store) appendDeleted(ctx context.Context, key string, origin tool.MemoryOrigin) {
	r := s.ensure(key)
	if len(r.revisions) != 0 {
		last := &r.revisions[len(r.revisions)-1]
		if last.Status == tool.MemoryStatusActive {
			last.Status = tool.MemoryStatusSuperseded
		}
	}
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	if origin == tool.MemoryOriginUndo {
		attribution.Origin = origin
	} else if attribution.Origin == "" {
		attribution.Origin = origin
	}
	r.revisions = append(r.revisions, tool.MemoryRevision{
		Key: key, Version: s.version(), Status: tool.MemoryStatusDeleted,
		Writer: attribution.Writer, Origin: attribution.Origin, Source: attribution.Source,
		UpdatedAt: s.now().UTC(),
	})
	r.retainLatest()
}

func (r *record) retainLatest() {
	if len(r.revisions) <= maxRevisionsPerKey {
		return
	}
	r.revisions = append([]tool.MemoryRevision(nil), r.revisions[len(r.revisions)-maxRevisionsPerKey:]...)
	r.truncated = true
}

func (s *Store) ensure(key string) *record {
	r := s.records[key]
	if r == nil {
		r = &record{}
		s.records[key] = r
	}
	return r
}

func (s *Store) version() tool.MemoryVersion {
	s.next++
	return tool.MemoryVersion(fmt.Sprintf("mem-%d", s.next))
}

func (s *Store) active(key string) (tool.MemoryRevision, bool) {
	r := s.records[key]
	if r == nil || len(r.revisions) == 0 {
		return tool.MemoryRevision{}, false
	}
	current := r.revisions[len(r.revisions)-1]
	return current, current.Status == tool.MemoryStatusActive
}

func (s *Store) snapshot(key string) tool.MemoryRecord {
	r := s.records[key]
	revisions := append([]tool.MemoryRevision(nil), r.revisions...)
	return tool.MemoryRecord{Current: revisions[len(revisions)-1], Revisions: revisions}
}

func entryOf(revision tool.MemoryRevision) tool.MemoryEntry {
	return tool.MemoryEntry{Key: revision.Key, Value: revision.Value, Description: revision.Description, UpdatedAt: revision.UpdatedAt}
}

func description(entry tool.MemoryEntry) string {
	if value := strings.TrimSpace(entry.Description); value != "" {
		return value
	}
	for _, line := range strings.Split(entry.Value, "\n") {
		if value := strings.TrimSpace(line); value != "" {
			return value
		}
	}
	return ""
}
