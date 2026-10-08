// Package memstore implements an in-memory, concurrency-safe port.SessionStore.
// It is the default store: fast and offline, suitable for single-process use and
// tests. Sessions are keyed by session.SessionID in a mutex-guarded map.
//
// Save and Load deep-copy the session through a sessnap snapshot round-trip, so
// a stored session cannot be mutated through a reference the caller still holds
// (and vice versa). This keeps the store's copy authoritative and isolated.
package memstore

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// ErrNotFound is returned by Load when no session is stored under the given id. It
// wraps port.ErrSessionNotFound so a consumer that may not import this adapter (e.g.
// engine/agent) can distinguish not-found from an infra failure via errors.Is.
var ErrNotFound = fmt.Errorf("memstore: session not found: %w", port.ErrSessionNotFound)

// Store is a concurrency-safe in-memory SessionStore.
type Store struct {
	mu       sync.RWMutex
	sessions map[session.SessionID]sessnap.Snapshot
	// savedAt records each session's last Save time, read via now (injected
	// for determinism; the real clock by default). It backs the optional
	// port.PrunableStore List/Delete retention seam.
	savedAt             map[session.SessionID]time.Time
	estimatedBytes      map[session.SessionID]int64
	activities          map[session.SessionID]session.ActivityState
	lineageByKey        map[string]port.SessionLineageRecord
	lineageByID         map[session.SessionID]map[string]port.SessionLineageRecord
	lineageEdges        map[string]map[string]port.SessionLineageRecord
	lineageReadObserver func(string)
	deleteFailures      map[session.SessionID]error
	now                 func() time.Time
	// generation is a monotonic counter bumped on every Save/Delete, and
	// handed to port.PaginateSessionMetadataBound as the cheap O(1)
	// "has anything changed" signal a bound cursor is checked against. This
	// is coarser than jsonlstore's real per-family bookkeeping (any mutation
	// anywhere invalidates every outstanding cursor, not just ones scoped to
	// the changed row) but matches how the real indexed adapters' own
	// rebuild-generation counters work (e.g. redisstore's
	// metadataRebuildGenerationKey) — a single global counter, not a
	// per-owner one; owner-scope mismatches are caught separately by the
	// Scope field, not Generation.
	generation uint64
}

// compile-time assertions that Store satisfies the port plus the optional
// retention seam.
var (
	_ port.SessionStore         = (*Store)(nil)
	_ port.SessionCreator       = (*Store)(nil)
	_ port.PrunableStore        = (*Store)(nil)
	_ port.SessionMetadataPager = (*Store)(nil)
	_ port.SessionLineageReader = (*Store)(nil)
)

// Option configures a Store at construction.
type Option func(*Store)

// WithNow injects the clock Save uses to stamp each snapshot's ModifiedAt
// (List's ordering input). Tests inject a fake for deterministic retention
// assertions; production keeps the default time.Now. A nil now is ignored.
//
// (A plain func rather than port.Clock keeps the option dependency-free for
// callers; wrap a port.Clock as clock.Now where one is already in hand.)
func WithNow(now func() time.Time) Option {
	return func(st *Store) {
		if now != nil {
			st.now = now
		}
	}
}

// WithDeleteFailure scripts a deterministic Delete failure for the reference
// adapter. It exists for offline conformance of retryable maintenance paths.
func WithDeleteFailure(id session.SessionID, err error) Option {
	return func(st *Store) {
		if err != nil {
			st.deleteFailures[id] = err
		}
	}
}

// New constructs an empty in-memory Store.
func New(opts ...Option) *Store {
	st := &Store{
		sessions:       make(map[session.SessionID]sessnap.Snapshot),
		savedAt:        make(map[session.SessionID]time.Time),
		estimatedBytes: make(map[session.SessionID]int64),
		activities:     make(map[session.SessionID]session.ActivityState),
		lineageByKey:   make(map[string]port.SessionLineageRecord),
		lineageByID:    make(map[session.SessionID]map[string]port.SessionLineageRecord),
		lineageEdges:   make(map[string]map[string]port.SessionLineageRecord),
		deleteFailures: make(map[session.SessionID]error),
		now:            time.Now,
	}
	for _, opt := range opts {
		opt(st)
	}
	return st
}

// Save persists a deep copy of s under s.ID, overwriting any prior state.
func (st *Store) Save(_ context.Context, s *session.Session) error {
	if s == nil {
		return sessnap.ErrNilSession
	}
	snap, err := sessnap.Of(s)
	if err != nil {
		return err
	}
	estimatedBytes := estimateSnapshotBytes(snap)
	st.mu.Lock()
	prior, existed := st.sessions[s.ID]
	if existed && !sameLineageIncarnation(prior, s) {
		st.putLineage(lineageSnapshotRecord(s.ID, prior, port.SessionLineagePruned, st.now()))
	}
	st.sessions[s.ID] = snap
	st.putLineage(lineageSnapshotRecord(s.ID, snap, port.SessionLineageRetained, time.Time{}))
	st.savedAt[s.ID] = st.now()
	st.estimatedBytes[s.ID] = estimatedBytes
	st.activities[s.ID] = session.ActivityOf(s.Conversation.Messages)
	st.generation++
	st.mu.Unlock()
	return nil
}

// Create atomically publishes a deep copy of s only when its ID is absent.
func (st *Store) Create(_ context.Context, s *session.Session) error {
	if s == nil {
		return sessnap.ErrNilSession
	}
	snap, err := sessnap.Of(s)
	if err != nil {
		return err
	}
	estimatedBytes := estimateSnapshotBytes(snap)

	st.mu.Lock()
	defer st.mu.Unlock()
	if _, exists := st.sessions[s.ID]; exists {
		return fmt.Errorf("memstore: create %q: %w", s.ID, port.ErrSessionAlreadyExists)
	}
	st.sessions[s.ID] = snap
	st.putLineage(lineageSnapshotRecord(s.ID, snap, port.SessionLineageRetained, time.Time{}))
	st.savedAt[s.ID] = st.now()
	st.estimatedBytes[s.ID] = estimatedBytes
	st.activities[s.ID] = session.ActivityOf(s.Conversation.Messages)
	st.generation++
	return nil
}

// Load returns a freshly reconstructed copy of the session stored under id. It
// returns ErrNotFound if no such session exists.
func (st *Store) Load(_ context.Context, id session.SessionID) (*session.Session, error) {
	st.mu.RLock()
	snap, ok := st.sessions[id]
	st.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	sess, err := snap.Restore()
	if err != nil {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, err)
	}
	return sess, nil
}

// List returns every stored session's id and last Save time, in no guaranteed
// order. It satisfies the optional port.PrunableStore retention seam.
func (st *Store) List(_ context.Context) ([]port.StoredSession, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]port.StoredSession, 0, len(st.sessions))
	for id := range st.sessions {
		out = append(out, port.StoredSession{ID: id, ModifiedAt: st.savedAt[id]})
	}
	return out, nil
}

// SupportsSessionActivityProjection reports that the in-memory metadata page
// atomically reflects the latest snapshot's activity.
func (*Store) SupportsSessionActivityProjection() bool { return true }

// PageSessionMetadata returns one owner-filtered keyset page from a consistent
// in-memory snapshot of the store maps.
func (st *Store) PageSessionMetadata(_ context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	st.mu.RLock()
	rows := make([]port.SessionDiscoveryMeta, 0, len(st.sessions))
	for id, snap := range st.sessions {
		kind := snap.Kind
		if kind == "" {
			kind = session.SessionKindUnknown
		}
		rows = append(rows, port.SessionDiscoveryMeta{
			ID: id, ModifiedAt: st.savedAt[id], State: snap.State,
			Turns: snap.Counters.Turns, ModelID: snap.ModelID, CreatedAt: snap.CreatedAt,
			Title: snap.Title, TitleProvenance: snap.TitleProvenance, EnvironmentRef: snap.EnvironmentRef,
			Kind: kind, Relationship: snap.Relationship, Owner: snap.Owner,
			Activity:       st.activities[id],
			EstimatedBytes: st.estimatedBytes[id],
		})
	}
	generation := st.generation
	st.mu.RUnlock()
	return port.PaginateSessionMetadataBound(rows, request, strconv.FormatUint(generation, 10))
}

// estimateSnapshotBytes is an allocation-free approximation of the persisted
// JSON payload. Save pays the transcript walk once; metadata paging reads only
// the cached scalar. Fixed overhead accounts for field names and scalar values,
// while every variable-length persisted payload contributes its byte length.
func estimateSnapshotBytes(snap sessnap.Snapshot) int64 {
	size := int64(256 + len(snap.ID) + len(snap.State) + len(snap.Mode) +
		len(snap.StopReason) + len(snap.Kind) + len(snap.Profile) + len(snap.ProviderID) +
		len(snap.ModelID) + len(snap.ReasoningEffort) + len(snap.Title) + len(snap.TitleProvenance) +
		len(snap.LastError) + len(snap.Incarnation) + len(snap.EnvironmentRef.Kind) + len(snap.EnvironmentRef.ID) + len(snap.EnvironmentRef.Revision))

	if authority := snap.Authority; authority != nil {
		size += int64(96 + len(authority.Provenance) + len(authority.DefinitionIdentity))
		for _, tool := range authority.CapabilitySet.Tools {
			size += int64(4 + len(tool))
		}
	}

	rel := snap.Relationship
	size += int64(len(rel.ScheduleName) + len(rel.OriginSessionID) + len(rel.OriginIncarnation) + len(rel.ParentSessionID) + len(rel.ParentIncarnation) +
		len(rel.CallID) + len(rel.TeamID) + len(rel.MemberName) + len(rel.DebugTargetID) + len(rel.DebugTargetIncarnation))
	if rel.BranchIndex != nil {
		size += 16
	}
	if snap.Pending != nil {
		size += int64(96 + len(snap.Pending.AskID) + len(snap.Pending.Tool) + len(snap.Pending.Args) +
			len(snap.Pending.Reason) + len(snap.Pending.Call))
	}
	if snap.Owner != nil {
		size += int64(64 + len(snap.Owner.Issuer) + len(snap.Owner.Subject) +
			len(snap.Owner.GrantType) + len(snap.Owner.Name))
	}
	if len(snap.TokenUsage) > 0 {
		size += int64(96 * len(snap.TokenUsage))
	}

	for _, message := range snap.Messages {
		size += int64(96 + len(message.Role) + len(message.Text) + len(message.Reasoning) +
			len(message.ProviderPhase) + len(message.ReasoningItemID))
		for _, call := range message.ToolCalls {
			size += int64(64 + len(call.ID) + len(call.Name) + len(call.Args) + len(call.ItemID))
		}
		if message.ToolResult != nil {
			result := message.ToolResult
			size += int64(64 + len(result.CallID) + len(result.Content))
			for _, part := range result.Parts {
				size += estimateContentBytes(part)
			}
		}
		for _, part := range message.Parts {
			size += int64(48 + len(part.Kind) + len(part.MIMEType) + len(part.URL) + encodedBytesLen(len(part.Data)))
		}
	}
	return size
}

func estimateContentBytes(part session.Content) int64 {
	size := int64(128 + len(part.BlockKind) + len(part.Kind) + len(part.MIMEType) +
		len(part.URL) + len(part.Text) + len(part.Name) + len(part.Title) + len(part.Description) +
		len(part.LastModified) + encodedBytesLen(len(part.Data)))
	for _, audience := range part.Audience {
		size += int64(4 + len(audience))
	}
	return size
}

func encodedBytesLen(n int) int {
	return base64.StdEncoding.EncodedLen(n)
}

func lineageSnapshotRecord(id session.SessionID, snap sessnap.Snapshot, state port.SessionLineageState, deletedAt time.Time) port.SessionLineageRecord {
	kind := snap.Kind
	if kind == "" {
		kind = session.SessionKindUnknown
	}
	return port.SessionLineageRecord{ID: id, Kind: kind, Relationship: snap.Relationship, OwnerScope: session.PrincipalScopeHash(snap.Owner), Incarnation: string(session.PersistedIncarnationID(snap.Incarnation, id, snap.CreatedAt.UnixNano(), snap.Owner)), State: state, DeletedAt: deletedAt}
}

func sameLineageIncarnation(snap sessnap.Snapshot, current *session.Session) bool {
	if snap.Incarnation != "" {
		return snap.Incarnation == current.Incarnation()
	}
	return session.PersistedIncarnationID("", snap.ID, snap.CreatedAt.UnixNano(), snap.Owner) == current.Incarnation()
}

func lineageRecordKey(id session.SessionID, incarnation string) string {
	return string(id) + "\x00" + incarnation
}

func lineageEdgeSubject(row port.SessionLineageRecord) string {
	rel := row.Relationship
	switch {
	case rel.ParentSessionID != "":
		return lineageRecordKey(rel.ParentSessionID, string(rel.ParentIncarnation))
	case rel.OriginSessionID != "":
		return lineageRecordKey(rel.OriginSessionID, string(rel.OriginIncarnation))
	case rel.DebugTargetID != "":
		return lineageRecordKey(rel.DebugTargetID, string(rel.DebugTargetIncarnation))
	default:
		return ""
	}
}

// putLineage updates only the record's ID partition and its old/new direct-edge
// partitions. st.mu must be held by the caller.
func (st *Store) putLineage(row port.SessionLineageRecord) {
	key := lineageRecordKey(row.ID, row.Incarnation)
	if old, ok := st.lineageByKey[key]; ok {
		if subject := lineageEdgeSubject(old); subject != "" {
			delete(st.lineageEdges[subject], key)
		}
	}
	st.lineageByKey[key] = row
	byID := st.lineageByID[row.ID]
	if byID == nil {
		byID = make(map[string]port.SessionLineageRecord)
		st.lineageByID[row.ID] = byID
	}
	byID[key] = row
	if subject := lineageEdgeSubject(row); subject != "" {
		edges := st.lineageEdges[subject]
		if edges == nil {
			edges = make(map[string]port.SessionLineageRecord)
			st.lineageEdges[subject] = edges
		}
		edges[key] = row
	}
}

func lineageLess(a, b port.SessionLineageRecord, root session.SessionID) bool {
	if (a.ID == root) != (b.ID == root) {
		return a.ID == root
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	if a.State != b.State {
		return a.State == port.SessionLineageRetained
	}
	return a.Incarnation < b.Incarnation
}

func lineageMatches(row port.SessionLineageRecord, query port.SessionLineageQuery) bool {
	rel := row.Relationship
	return row.ID == query.RootID ||
		rel.ParentSessionID == query.RootID && rel.ParentIncarnation == query.RootIncarnation ||
		rel.OriginSessionID == query.RootID && rel.OriginIncarnation == query.RootIncarnation ||
		rel.DebugTargetID == query.RootID && rel.DebugTargetIncarnation == query.RootIncarnation
}

// ReadSessionLineage returns the root and its direct children from the content-free index.
func (st *Store) ReadSessionLineage(_ context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	if err := port.ValidateSessionLineageQuery(query); err != nil {
		return port.SessionLineageResult{}, err
	}
	st.mu.RLock()
	if query.RecordID != "" {
		key := lineageRecordKey(query.RecordID, string(query.RecordIncarnation))
		partition := lineageRecordKey(query.RootID, string(query.RootIncarnation))
		if st.lineageReadObserver != nil {
			st.lineageReadObserver("point:" + partition + ":" + key)
		}
		row, found := st.lineageEdges[partition][key]
		st.mu.RUnlock()
		if !found || !lineageMatches(row, query) {
			return port.SessionLineageResult{}, nil
		}
		return port.SessionLineageResult{Records: []port.SessionLineageRecord{row}}, nil
	}
	if st.lineageReadObserver != nil {
		st.lineageReadObserver("records:" + string(query.RootID))
		st.lineageReadObserver("edges:" + lineageRecordKey(query.RootID, string(query.RootIncarnation)))
	}
	byID := st.lineageByID[query.RootID]
	edges := st.lineageEdges[lineageRecordKey(query.RootID, string(query.RootIncarnation))]
	rows := make([]port.SessionLineageRecord, 0, len(byID)+len(edges))
	for _, row := range byID {
		rows = append(rows, row)
	}
	for _, row := range edges {
		rows = append(rows, row)
	}
	st.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return lineageLess(rows[i], rows[j], query.RootID) })
	result := port.SessionLineageResult{Records: rows}
	if len(result.Records) > query.Limit {
		result.Records = result.Records[:query.Limit]
		result.Truncated = true
	}
	return result, nil
}

// DeleteSessionIfUnchanged atomically revalidates metadata and deletes the
// in-memory family while holding the store mutex.
func (st *Store) DeleteSessionIfUnchanged(_ context.Context, expected port.SessionDiscoveryMeta) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	snap, ok := st.sessions[expected.ID]
	if !ok {
		return false, nil
	}
	kind := snap.Kind
	if kind == "" {
		kind = session.SessionKindUnknown
	}
	current := port.SessionDiscoveryMeta{
		ID: expected.ID, ModifiedAt: st.savedAt[expected.ID], State: snap.State,
		Kind: kind, Relationship: snap.Relationship, Owner: snap.Owner,
	}
	if !port.SessionDiscoveryMetaEqual(current, expected) {
		return false, nil
	}
	if err := st.deleteFailures[expected.ID]; err != nil {
		return false, err
	}
	row := lineageSnapshotRecord(expected.ID, snap, port.SessionLineagePruned, st.now())
	st.putLineage(row)
	delete(st.sessions, expected.ID)
	delete(st.savedAt, expected.ID)
	delete(st.estimatedBytes, expected.ID)
	st.generation++
	return true, nil
}

// Delete removes the session stored under id. It is idempotent: an unknown id
// is success (port.PrunableStore contract).
func (st *Store) Delete(_ context.Context, id session.SessionID) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.deleteFailures[id]; err != nil {
		return err
	}
	if snap, ok := st.sessions[id]; ok {
		st.putLineage(lineageSnapshotRecord(id, snap, port.SessionLineagePruned, st.now()))
		st.generation++
	}
	delete(st.sessions, id)
	delete(st.savedAt, id)
	delete(st.estimatedBytes, id)
	return nil
}
