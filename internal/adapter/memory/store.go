// Package memory implements harness pattern 3 (tiered memory): a conservative,
// cross-session memory facility exposed to the model as two tools (Remember and
// Recall) backed by a pluggable tool.MemoryStore.
//
// This package contains BOTH the file-backed store adapter AND the tool.Tool
// values that drive it. They are kept together deliberately: the tools are thin
// adapters over the store seam (constructor-injected, mirroring NewBashTool),
// and shipping them as one opt-in unit lets the composition root wire memory with
// a single import. The tools depend only on the tool.MemoryStore interface, so a
// fake in-memory store can stand in for tests.
//
// SCOPING: a Store is per-PROJECT. The composition root calls New(dir) once per
// project/workspace directory; entries persist across process restarts for that
// directory and are isolated from other projects. See tool.MemoryStore.
package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/tool"
)

const (
	// memoryFileName is the JSON file, under the store's directory, that holds
	// all entries for one project.
	memoryFileName = "memory.json"
	// lockFileName is a STABLE sentinel co-located with the data file, used only
	// for cross-process advisory locking (flock). It is deliberately NOT the data
	// file itself: every save renames a temp file over memory.json, which would
	// break a flock held against the old inode. The sentinel is never renamed, so
	// the flock association is stable for the store's lifetime.
	lockFileName = "memory.lock"
	// lockRetryDelay is how often TryLock(Context)/TryRLock(Context) re-probes a
	// contended lock while waiting. Small enough to feel instant under light
	// contention.
	lockRetryDelay = 5 * time.Millisecond
	// lockTimeout bounds how long any single read-modify-write waits for the
	// cross-process lock before giving up with a clear error, so a stuck or
	// crashed holder cannot deadlock a run indefinitely.
	lockTimeout = 5 * time.Second
	// Lifecycle resource ceilings are deliberately fixed, conservative defaults:
	// 64 KiB per value/description, 64 retained revisions per key, 4096 keys, and
	// an 8 MiB encoded document. They bound one locked transaction without adding
	// a configurable quota subsystem.
	maxMemoryFieldBytes   = 64 * 1024
	maxRevisionsPerKey    = 64
	maxMemoryEntries      = 4096
	maxMemoryDocumentSize = 8 * 1024 * 1024
)

// Store is a file-backed, cross-process-safe tool.MemoryStore. It persists
// entries as a single JSON document at <dir>/memory.json and commits every write
// atomically (temp file + rename) so a crash mid-write cannot corrupt or truncate
// the on-disk file. A fresh Store opened over the same dir sees previously written
// entries, giving durability across process restarts.
//
// Concurrency / locking. The store is safe for both in-process and cross-process
// concurrent use, and — critically — does not LOSE updates under either:
//
//   - In-process: s.mu serialises every method on a single Store, and is also
//     held across the full read-modify-write of a mutation. This is the only lock
//     that protects the flock handle, which is NOT goroutine-safe when shared
//     across goroutines on one fd.
//   - Cross-process (several mecated/mecatui instances, agent-team / subagent runs
//     sharing one memory.json): a gofrs/flock advisory lock on the STABLE sentinel
//     <dir>/memory.lock guards the read-modify-write. Writes (RememberEntry,
//     Remember, Forget) take an EXCLUSIVE lock; reads (Recall, List, Index) take a
//     SHARED lock. The lock spans the whole load→mutate→save sequence, so two
//     processes can no longer interleave read-modify-write and clobber each other
//     (the lost-update bug the bare temp+rename did not prevent).
//
// Acquisition order is always s.mu THEN flock; the flock is acquired with a
// bounded TryLockContext/TryRLockContext (lockRetryDelay / lockTimeout, also
// honouring ctx cancellation) so a stuck holder fails loud instead of deadlocking,
// and released (Unlock) before return on every path including errors.
//
// INVARIANT — at most ONE *Store per directory per process. gofrs/flock uses BSD
// flock(2), which contends across file descriptors even WITHIN a single process.
// Two *Store values opened over the SAME dir in the SAME process therefore hold
// DISTINCT fds on the sentinel and would self-deadlock: the second writer's
// exclusive acquire blocks on the first's lock until the lockTimeout expires (a
// loud error, but a needless one). The composition root upholds this today by
// constructing one shared *Store per project (see internal/app). If per-session
// memory is ever needed, SHARE a single *Store keyed by absolute dir (the way the
// session-engine map is keyed) rather than calling New per session — do NOT open a
// second *Store over a dir already owned in-process.
type Store struct {
	mu     sync.Mutex
	path   string
	lock   *flock.Flock
	rename func(string, string) error
}

var (
	_ tool.MemoryStore          = (*Store)(nil)
	_ tool.MemoryLifecycleStore = (*Store)(nil)
)

// New constructs a file-backed Store rooted at dir, creating dir (and parents)
// if it does not exist. The store is scoped to dir: it owns <dir>/memory.json and
// the cross-process lock sentinel <dir>/memory.lock. Pass a per-project directory
// so memory is isolated per project.
//
// Construct AT MOST ONE *Store per dir per process. Because the cross-process lock
// uses BSD flock(2) (per-fd, cross-fd-contending even in one process), a SECOND
// *Store opened over the same dir in this process would self-deadlock its own lock
// acquisition until the lockTimeout, since the two *Store values hold separate fds
// on the sentinel. Share one *Store keyed by absolute dir instead. See the Store
// type doc for the full rationale.
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("memory: New requires a non-empty dir")
	}
	// 0o700: memory can hold sensitive curated facts; the per-project store has
	// no reason to be group/other-readable.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("memory: create dir %q: %w", dir, err)
	}
	return &Store{
		path:   filepath.Join(dir, memoryFileName),
		lock:   flock.New(filepath.Join(dir, lockFileName)),
		rename: os.Rename,
	}, nil
}

// persisted remains compatible with the original {"entries": ...} document.
// History is additive and omitted until the first mutation of a key. Older
// binaries ignore it while reading, but a downgraded binary that subsequently
// writes memory.json will discard history; current values remain compatible.
type persisted struct {
	Entries          map[string]record              `json:"entries"`
	History          map[string][]persistedRevision `json:"history,omitempty"`
	HistoryTruncated map[string]bool                `json:"history_truncated,omitempty"`
}

// record is the legacy/current active-value projection. Deleted records are
// absent here and retained only as lifecycle tombstones in History.
type record struct {
	Value       string    `json:"value"`
	Description string    `json:"description,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type persistedRevision struct {
	Key         string             `json:"key"`
	Value       string             `json:"value,omitempty"`
	Description string             `json:"description,omitempty"`
	Version     tool.MemoryVersion `json:"version"`
	Status      tool.MemoryStatus  `json:"status"`
	Writer      tool.MemoryWriter  `json:"writer,omitempty"`
	Origin      tool.MemoryOrigin  `json:"origin,omitempty"`
	Source      tool.MemorySource  `json:"source,omitempty"`
	UpdatedAt   time.Time          `json:"updated_at"`
	UndoOf      tool.MemoryVersion `json:"undo_of,omitempty"`
}

// withExclusiveLock acquires s.mu THEN the cross-process EXCLUSIVE flock, runs fn
// against the loaded store, and saves only if fn returns no error. The flock is
// released before return on every path. ctx bounds the lock wait.
func (s *Store) withExclusiveLock(ctx context.Context, fn func(data *persisted) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	locked, err := s.lock.TryLockContext(ctx, lockRetryDelay)
	if err != nil {
		return fmt.Errorf("memory: acquire write lock %q: %w", s.lock.Path(), err)
	}
	if !locked {
		return fmt.Errorf("memory: could not acquire write lock %q within %s (held by another process?)", s.lock.Path(), lockBudget(ctx))
	}
	defer func() { _ = s.lock.Unlock() }()

	data, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(&data); err != nil {
		return err
	}
	if err := data.enforceLimits(); err != nil {
		return err
	}
	return s.save(data)
}

// withSharedLock acquires s.mu THEN the cross-process SHARED flock and runs fn
// against the loaded store. The flock is released before return on every path.
// ctx bounds the lock wait.
func (s *Store) withSharedLock(ctx context.Context, fn func(data persisted) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	locked, err := s.lock.TryRLockContext(ctx, lockRetryDelay)
	if err != nil {
		return fmt.Errorf("memory: acquire read lock %q: %w", s.lock.Path(), err)
	}
	if !locked {
		return fmt.Errorf("memory: could not acquire read lock %q within %s (held by another process?)", s.lock.Path(), lockBudget(ctx))
	}
	defer func() { _ = s.lock.Unlock() }()

	data, err := s.load()
	if err != nil {
		return err
	}
	return fn(data)
}

// lockCtx derives a context with the lock timeout from the caller's ctx, so the
// flock wait is bounded even when the caller passes context.Background(). If the
// caller's ctx already has a shorter deadline, that shorter deadline wins (the
// timeout is a CEILING, not a floor).
func lockCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, lockTimeout)
}

// lockBudget reports the effective wait budget remaining on ctx, for use in the
// timeout error message so it reflects the ACTUAL deadline (which may be shorter
// than lockTimeout if the caller passed a tighter ctx) rather than hardcoding the
// ceiling. It returns the rounded time until ctx's deadline, falling back to the
// lockTimeout ceiling when ctx carries no deadline.
func lockBudget(ctx context.Context) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return lockTimeout
	}
	return time.Until(dl).Round(time.Millisecond)
}

// RememberEntry stores e, overwriting any existing entry under e.Key and bumping
// UpdatedAt to now. e.Description is stored as-is (empty is allowed; the index
// derives one). An empty key is rejected.
func (s *Store) RememberEntry(ctx context.Context, e tool.MemoryEntry) error {
	if strings.TrimSpace(e.Key) == "" {
		return fmt.Errorf("memory: RememberEntry requires a non-empty key")
	}
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	if err := tool.ValidateMemoryContentWrite(e.Key, e.Value, e.Description, attribution); err != nil {
		return err
	}
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	return s.withExclusiveLock(lctx, func(data *persisted) error {
		data.materializeLegacy(e.Key)
		r := record{
			Value:       e.Value,
			Description: strings.TrimSpace(e.Description),
			UpdatedAt:   time.Now().UTC(),
		}
		data.Entries[e.Key] = r
		return data.appendActive(ctx, e.Key, r, tool.MemoryOriginImported)
	})
}

// Remember stores value under key with no explicit description (the index
// derives one from the value), overwriting any existing entry and bumping its
// UpdatedAt. An empty key is rejected. It is a convenience wrapper over
// RememberEntry, kept so callers that do not care about descriptions stay
// unchanged. It is intentionally NOT part of tool.MemoryStore — the interface
// carries RememberEntry only; this concrete convenience survives for direct
// *Store users.
func (s *Store) Remember(ctx context.Context, key, value string) error {
	return s.RememberEntry(ctx, tool.MemoryEntry{Key: key, Value: value})
}

// Recall returns the entry for the exact key. A miss is (zero, false, nil).
func (s *Store) Recall(ctx context.Context, key string) (tool.MemoryEntry, bool, error) {
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	var (
		out   tool.MemoryEntry
		found bool
	)
	err := s.withSharedLock(lctx, func(data persisted) error {
		r, ok := data.Entries[key]
		if !ok {
			return nil
		}
		out = tool.MemoryEntry{Key: key, Value: r.Value, Description: r.Description, UpdatedAt: r.UpdatedAt}
		found = true
		return nil
	})
	if err != nil {
		return tool.MemoryEntry{}, false, err
	}
	return out, found, nil
}

// List returns all entries whose key has the given prefix, sorted by key. An
// empty prefix returns every entry.
func (s *Store) List(ctx context.Context, prefix string) ([]tool.MemoryEntry, error) {
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	var out []tool.MemoryEntry
	err := s.withSharedLock(lctx, func(data persisted) error {
		out = make([]tool.MemoryEntry, 0, len(data.Entries))
		for k, r := range data.Entries {
			if strings.HasPrefix(k, prefix) {
				out = append(out, tool.MemoryEntry{Key: k, Value: r.Value, Description: r.Description, UpdatedAt: r.UpdatedAt})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Index returns the tier-0 routing table: every entry with the VALUE OMITTED and
// Description filled (explicit, else derived from the value's first line), sorted
// by key. It applies NO size cap — the consumer (the prompt assembler) caps and
// renders. It is the cheap, always-in-context summary view.
func (s *Store) Index(ctx context.Context) ([]tool.MemoryEntry, error) {
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	var out []tool.MemoryEntry
	err := s.withSharedLock(lctx, func(data persisted) error {
		out = make([]tool.MemoryEntry, 0, len(data.Entries))
		for k, r := range data.Entries {
			out = append(out, tool.MemoryEntry{
				Key:         k,
				Description: deriveDescription(r),
				UpdatedAt:   r.UpdatedAt,
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Search ranks entries by BM25 lexical relevance to query (over each entry's
// key + derived description + value) and returns the top k best-first, with the
// VALUE OMITTED and Description filled — mirroring Index's result shape. Ranking
// runs INSIDE the shared lock, over the freshly loaded data, so it sees a
// consistent snapshot and never races a concurrent write. An empty or
// whitespace-only query short-circuits to an empty result (no lock, no error);
// k <= 0 uses the default page size. See bm25Rank for the scoring/ordering rules.
func (s *Store) Search(ctx context.Context, query string, k int) ([]tool.MemoryEntry, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	var out []tool.MemoryEntry
	err := s.withSharedLock(lctx, func(data persisted) error {
		entries := make([]tool.MemoryEntry, 0, len(data.Entries))
		for key, r := range data.Entries {
			entries = append(entries, tool.MemoryEntry{
				Key:         key,
				Value:       r.Value,
				Description: deriveDescription(r),
				UpdatedAt:   r.UpdatedAt,
			})
		}
		out = bm25Rank(entries, query, k)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Defence in depth: bm25Rank already omits values, but guarantee no value
	// ever escapes Search regardless of future ranking changes.
	for i := range out {
		out[i].Value = ""
	}
	return out, nil
}

// deriveDescription returns r's tier-0 one-line description, delegating to the
// shared derivation: explicit Description if set, else the value's first non-empty
// line.
func deriveDescription(r record) string {
	return descriptionOrFirstLine(r.Description, r.Value)
}

// descriptionOrFirstLine is the single source of truth for the tier-0 one-liner
// derivation rule, shared by the store's Index (deriveDescription) and the
// Remember tool's index-line echo (tools.go). It returns the explicit description
// (trimmed) if non-empty, else the value's first non-empty line. It never returns
// more than one line, so the index stays one line per entry.
func descriptionOrFirstLine(description, value string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	for _, line := range strings.Split(value, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// Forget deletes the active entry for key. It remains idempotent for legacy
// callers, while an existing value is retained as lifecycle history plus a
// tombstone.
func (s *Store) Forget(ctx context.Context, key string) error {
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	return s.withExclusiveLock(lctx, func(data *persisted) error {
		if _, ok := data.Entries[key]; !ok {
			return nil
		}
		data.materializeLegacy(key)
		return data.appendDeleted(ctx, key, tool.MemoryOriginImported, "")
	})
}

// RememberVersioned atomically creates or replaces a record when expected is
// the current opaque version. A legacy flat record is materialized as the first
// imported revision inside the same locked write.
func (s *Store) RememberVersioned(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	if err := tool.ValidateMemoryEntryWrite(entry, attribution); err != nil {
		return tool.MemoryRecord{}, err
	}
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	var out tool.MemoryRecord
	err := s.withExclusiveLock(lctx, func(data *persisted) error {
		if expected != "" {
			if err := data.compare(entry.Key, expected); err != nil {
				return err
			}
		}
		data.materializeLegacy(entry.Key)
		r := record{Value: entry.Value, Description: strings.TrimSpace(entry.Description), UpdatedAt: time.Now().UTC()}
		data.Entries[entry.Key] = r
		if err := data.appendActive(ctx, entry.Key, r, tool.MemoryOriginExplicit); err != nil {
			return err
		}
		out = data.snapshot(entry.Key)
		return nil
	})
	return out, err
}

// Inspect returns the current state and complete history, including deleted
// tombstones. Legacy flat records are projected as a stable imported baseline
// without rewriting memory.json.
func (s *Store) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	var (
		out   tool.MemoryRecord
		found bool
	)
	err := s.withSharedLock(lctx, func(data persisted) error {
		if _, ok := data.Entries[key]; !ok && len(data.History[key]) == 0 {
			return nil
		}
		data.materializeLegacy(key)
		out, found = data.snapshot(key), true
		return nil
	})
	return out, found, err
}

// ForgetVersioned atomically appends a tombstone when expected is current.
func (s *Store) ForgetVersioned(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	var out tool.MemoryRecord
	err := s.withExclusiveLock(lctx, func(data *persisted) error {
		if err := data.compare(key, expected); err != nil {
			return err
		}
		if _, ok := data.Entries[key]; !ok {
			return fmt.Errorf("memory: %q: %w", key, tool.ErrMemoryNotFound)
		}
		data.materializeLegacy(key)
		if err := data.appendDeleted(ctx, key, tool.MemoryOriginExplicit, ""); err != nil {
			return err
		}
		out = data.snapshot(key)
		return nil
	})
	return out, err
}

// UndoLatest appends a compensating revision for the newest mutation not already
// compensated. Recording the target version makes repeated undo walk backward
// and prevents concurrent callers from reversing one mutation twice.
func (s *Store) UndoLatest(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	lctx, cancel := lockCtx(ctx)
	defer cancel()
	var out tool.MemoryRecord
	err := s.withExclusiveLock(lctx, func(data *persisted) error {
		if err := data.compare(key, expected); err != nil {
			return err
		}
		data.materializeLegacy(key)
		history := data.History[key]
		if len(history) == 0 {
			return fmt.Errorf("memory: %q: %w", key, tool.ErrMemoryNotFound)
		}
		undone := make(map[tool.MemoryVersion]bool)
		for _, revision := range history {
			if revision.UndoOf != "" {
				undone[revision.UndoOf] = true
			}
		}
		target := -1
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].UndoOf == "" && !undone[history[i].Version] {
				target = i
				break
			}
		}
		if target < 0 {
			return fmt.Errorf("memory: %q: no mutation remains to undo", key)
		}
		if target == 0 && data.HistoryTruncated[key] {
			return fmt.Errorf("memory: %q: cannot undo beyond retained history", key)
		}
		var previous *persistedRevision
		if target > 0 {
			candidate := history[target-1]
			previous = &candidate
		}
		if previous == nil || previous.Status == tool.MemoryStatusDeleted {
			if err := data.appendDeleted(ctx, key, tool.MemoryOriginUndo, history[target].Version); err != nil {
				return err
			}
		} else {
			r := record{Value: previous.Value, Description: previous.Description, UpdatedAt: time.Now().UTC()}
			data.Entries[key] = r
			if err := data.appendActiveUndo(ctx, key, r, history[target].Version); err != nil {
				return err
			}
		}
		out = data.snapshot(key)
		return nil
	})
	return out, err
}

func (data *persisted) compare(key string, expected tool.MemoryVersion) error {
	actual := data.currentVersion(key)
	if actual != expected {
		return &tool.MemoryVersionConflictError{Key: key, Expected: expected, Actual: actual}
	}
	return nil
}

func (data *persisted) currentVersion(key string) tool.MemoryVersion {
	if history := data.History[key]; len(history) != 0 {
		return history[len(history)-1].Version
	}
	if current, ok := data.Entries[key]; ok {
		return legacyRevision(key, current).Version
	}
	return ""
}

func (data *persisted) materializeLegacy(key string) {
	if len(data.History[key]) != 0 {
		return
	}
	current, ok := data.Entries[key]
	if !ok {
		return
	}
	if data.History == nil {
		data.History = make(map[string][]persistedRevision)
	}
	data.History[key] = []persistedRevision{legacyRevision(key, current)}
}

func legacyRevision(key string, current record) persistedRevision {
	digest := sha256.Sum256([]byte(key + "\x00" + current.Value + "\x00" + current.Description + "\x00" + current.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	return persistedRevision{Key: key, Value: current.Value, Description: current.Description,
		Version: tool.MemoryVersion("legacy-" + hex.EncodeToString(digest[:12])), Status: tool.MemoryStatusActive,
		Origin: tool.MemoryOriginImported, UpdatedAt: current.UpdatedAt}
}

func (data *persisted) appendActive(ctx context.Context, key string, current record, origin tool.MemoryOrigin) error {
	return data.appendRevision(ctx, persistedRevision{Key: key, Value: current.Value, Description: current.Description, Status: tool.MemoryStatusActive, Origin: origin, UpdatedAt: current.UpdatedAt})
}

func (data *persisted) appendActiveUndo(ctx context.Context, key string, current record, undoOf tool.MemoryVersion) error {
	return data.appendRevision(ctx, persistedRevision{Key: key, Value: current.Value, Description: current.Description, Status: tool.MemoryStatusActive, Origin: tool.MemoryOriginUndo, UpdatedAt: current.UpdatedAt, UndoOf: undoOf})
}

func (data *persisted) appendDeleted(ctx context.Context, key string, origin tool.MemoryOrigin, undoOf tool.MemoryVersion) error {
	delete(data.Entries, key)
	return data.appendRevision(ctx, persistedRevision{Key: key, Status: tool.MemoryStatusDeleted, Origin: origin, UpdatedAt: time.Now().UTC(), UndoOf: undoOf})
}

func (data *persisted) appendRevision(ctx context.Context, revision persistedRevision) error {
	if data.History == nil {
		data.History = make(map[string][]persistedRevision)
	}
	history := data.History[revision.Key]
	if len(history) != 0 && history[len(history)-1].Status == tool.MemoryStatusActive {
		history[len(history)-1].Status = tool.MemoryStatusSuperseded
	}
	version, err := newMemoryVersion()
	if err != nil {
		return err
	}
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	if attribution.Origin != "" && revision.Origin != tool.MemoryOriginUndo {
		revision.Origin = attribution.Origin
	}
	revision.Version = version
	revision.Writer = attribution.Writer
	revision.Source = attribution.Source
	history = append(history, revision)
	if len(history) > maxRevisionsPerKey {
		history = append([]persistedRevision(nil), history[len(history)-maxRevisionsPerKey:]...)
		data.markHistoryTruncated(revision.Key)
	}
	data.History[revision.Key] = history
	return nil
}

func (data *persisted) markHistoryTruncated(key string) {
	if data.HistoryTruncated == nil {
		data.HistoryTruncated = make(map[string]bool)
	}
	data.HistoryTruncated[key] = true
}

func newMemoryVersion() (tool.MemoryVersion, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("memory: generate revision: %w", err)
	}
	return tool.MemoryVersion("local-" + hex.EncodeToString(raw[:])), nil
}

func (data persisted) snapshot(key string) tool.MemoryRecord {
	history := data.History[key]
	revisions := make([]tool.MemoryRevision, len(history))
	for i, revision := range history {
		revisions[i] = tool.MemoryRevision{Key: revision.Key, Value: revision.Value, Description: revision.Description,
			Version: revision.Version, Status: revision.Status, Writer: revision.Writer, Origin: revision.Origin,
			Source: revision.Source, UpdatedAt: revision.UpdatedAt}
	}
	return tool.MemoryRecord{Current: revisions[len(revisions)-1], Revisions: revisions}
}

func (data *persisted) enforceLimits() error {
	keys := make(map[string]struct{}, len(data.Entries)+len(data.History))
	for key, current := range data.Entries {
		keys[key] = struct{}{}
		if len(current.Value) > maxMemoryFieldBytes || len(current.Description) > maxMemoryFieldBytes {
			return fmt.Errorf("memory: %q exceeds the %d-byte value/description limit", key, maxMemoryFieldBytes)
		}
	}
	for key, history := range data.History {
		keys[key] = struct{}{}
		for _, revision := range history {
			if len(revision.Value) > maxMemoryFieldBytes || len(revision.Description) > maxMemoryFieldBytes {
				return fmt.Errorf("memory: %q exceeds the %d-byte value/description limit", key, maxMemoryFieldBytes)
			}
		}
		if len(history) > maxRevisionsPerKey {
			data.History[key] = append([]persistedRevision(nil), history[len(history)-maxRevisionsPerKey:]...)
			data.markHistoryTruncated(key)
		}
	}
	if len(keys) > maxMemoryEntries {
		return fmt.Errorf("memory: entry limit exceeded (%d > %d)", len(keys), maxMemoryEntries)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encode for limit check: %w", err)
	}
	if len(raw) > maxMemoryDocumentSize {
		return fmt.Errorf("memory: document limit exceeded (%d > %d bytes)", len(raw), maxMemoryDocumentSize)
	}
	return nil
}

// load reads and decodes the on-disk file. A missing file is an empty store, not
// an error. The caller must hold s.mu AND the (shared or exclusive) flock.
func (s *Store) load() (persisted, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return persisted{Entries: map[string]record{}}, nil
		}
		return persisted{}, fmt.Errorf("memory: read %q: %w", s.path, err)
	}
	var data persisted
	if err := json.Unmarshal(raw, &data); err != nil {
		return persisted{}, fmt.Errorf("memory: parse %q: %w", s.path, err)
	}
	if data.Entries == nil {
		data.Entries = map[string]record{}
	}
	return data, nil
}

// save atomically writes data: it encodes to a temp file in the same directory,
// fsyncs it, then renames over the target so a reader never sees a partial file.
// The caller must hold s.mu AND the exclusive flock.
func (s *Store) save(data persisted) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encode: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".memory-*.json.tmp")
	if err != nil {
		return fmt.Errorf("memory: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("memory: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("memory: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: close temp: %w", err)
	}
	if err := s.rename(tmpName, s.path); err != nil {
		return fmt.Errorf("memory: rename temp into place: %w", err)
	}
	return nil
}
