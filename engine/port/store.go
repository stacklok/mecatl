package port

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

var (
	// ErrSessionNotFound is the port-level sentinel a SessionStore.Load wraps
	// (with %w) when no session is stored under the requested id — distinct
	// from a genuine infrastructure failure (I/O error, decode failure). It lets
	// a consumer in a layer that may NOT import the store adapters (e.g.
	// engine/agent's InspectMemberTool) distinguish "no such session" from "the
	// store is broken" via errors.Is, without reaching for an adapter's own
	// not-found sentinel. Every SessionStore adapter MUST wrap this for the
	// not-found case.
	ErrSessionNotFound = errors.New("port: session not found")

	// ErrSessionAlreadyExists is wrapped by SessionCreator.Create when an
	// authoritative snapshot already exists under the requested session id.
	ErrSessionAlreadyExists = errors.New("port: session already exists")
)

// SessionStore persists and retrieves server-side session state, enabling
// pause/resume and reload. Adapters provide an in-memory store (default) and an
// append-only JSONL replay log.
//
// EVENT-SOURCED Load (the reconstruction contract). mecatl's own adapters persist a
// snapshot (engine/adapter/sessnap) and Load deserializes it. A host whose system of
// record is an append-only EVENT LOG instead may implement Load by FOLDING its event
// stream into a *session.Session — engine/adapter/eventsource.Fold is the reference
// implementation. Such a backend MUST populate the fields a caller relies on:
//
//   - MUST round-trip (a folded session must carry these):
//     Conversation (the user/assistant/tool message sequence, tool-pairing-valid —
//     user-role turns INCLUDED, since the loop emits the log-only EvUserPrompt at every
//     user-message record site), State, the recorded stop reason, the pending ask (when
//     awaiting), the failure permanence flag (ResultPayload.Permanent — so a
//     permanently-failed session reconstructs with FailurePermanence()==true and the
//     recover advisory fires), cumulative Usage (the SUM of every per-run EvResult.Usage
//     — the budget brake reads it), and the metadata the events do not carry (id, mode,
//     limits, workspace, profile, provider/model selector, reasoning effort,
//     authoritative title/provenance, session kind/relationship, adoption source/request
//     digest, createdAt — supplied out-of-band, e.g. eventsource.SessionMeta). A legacy empty title/provenance may be
//     derived from the first genuine EvUserPrompt.
//   - Run-scoped: Counters reflect only the LATEST run segment (they reset on Reopen);
//     the run plumbing (diagnostics binding, askID serials) is rebuilt fresh.
//
// REPLAY-FIDELITY LIMITATION (the one residual gap): the opaque assistant-message replay
// fields — Message.Reasoning, Message.ProviderPhase, ToolCall.ItemID — are NOT carried on
// the event stream (they reach the conversation only via Session.RecordAssistant), so a
// pure event fold is byte-identical-replay faithful ONLY for providers that leave them
// empty (plain chat). A host that needs byte-identical replay for a reasoning provider
// must carry those fields in its OWN richer event schema. See engine/COMPATIBILITY.md
// ("Session reconstruction contract") and ADR 0038.
type SessionStore interface {
	// Save persists the current state of s.
	Save(ctx context.Context, s *session.Session) error
	// Load retrieves the session with the given id. The not-found case MUST wrap
	// port.ErrSessionNotFound; any other error is an infrastructure failure.
	Load(ctx context.Context, id session.SessionID) (*session.Session, error)
}

// SessionCreator is the OPTIONAL atomic first-publication capability of a
// SessionStore. Create publishes s only when no authoritative snapshot exists
// under s.ID. The existence check and publication MUST be one backend-atomic
// operation across all handles sharing that backend; a Load-then-Save sequence
// does not satisfy this contract.
//
// Any existing snapshot, including one with the same owner and content, causes
// Create to return an error wrapping ErrSessionAlreadyExists. That collision
// MUST NOT mutate the existing snapshot, derivative metadata or generations,
// event log, or tool-call sidecar. Save remains the update/upsert operation for
// a snapshot whose initial Create succeeded.
type SessionCreator interface {
	Create(ctx context.Context, s *session.Session) error
}

// StoredSession is one stored session's retention-relevant identity: its id
// plus when its snapshot was last modified (Save time, file mtime, or the
// store's nearest equivalent). It deliberately carries NO session content —
// listing is a retention/inventory concern, never a load.
type StoredSession struct {
	ID         session.SessionID
	ModifiedAt time.Time
}

// SessionMeta is the lightweight picker metadata for a stored session: the
// fields a session LISTING (the /sessions picker) needs to render a row WITHOUT
// loading the full conversation. It is a PROJECTION of the latest snapshot —
// state, turn count, model id, title, and creation time — with the large
// conversation (messages array) skipped entirely. Kind and Relationship preserve
// the validated producer taxonomy needed to classify the row without parsing its ID.
// The store adapter populates it by reading ONLY the last snapshot line into a
// small struct, so listing N sessions is O(N × last-line-read) rather than O(N × filesize).
//
// It is owned by the PORT (so the server adapter references the shape without
// importing any concrete store) and implemented by a store via the optional
// MetaLister interface — discovered by type assertion, exactly like
// PrunableStore. A store that does NOT implement MetaLister falls back to the
// Load-per-row path (correct, just slower). State carries the persisted
// session.State verbatim; an invalid/unknown state is left empty (the row still
// surfaces its id/mtime, matching the Load-fails zeroed-fields behaviour).
type SessionMeta struct {
	// ID is the stored session's opaque logical id, byte-exact. A backend whose
	// physical key or filename is a LOSSY transform of the id (so that two
	// distinct ids could share one) MUST recover the id from stored content
	// instead of from the key. A lossless key is free to be the source: keying
	// verbatim, or trimming a fixed prefix, satisfies this.
	ID session.SessionID
	// ModifiedAt is the last-write timestamp (file mtime, or the store's
	// nearest equivalent).
	ModifiedAt time.Time
	// State is the persisted lifecycle state (idle/running/awaiting/completed/
	// ...). Empty when the snapshot could not be decoded or carries an unknown
	// state.
	State session.State
	// Turns is the persisted model-call count. Zero when the snapshot could not
	// be decoded.
	Turns int
	// ModelID is the resolved model id this session ran on (bare string, no
	// provider context). Empty when the session never resolved a model or the
	// snapshot could not be decoded.
	ModelID string
	// CreatedAt is the creation timestamp. Zero when the snapshot could not be
	// decoded.
	CreatedAt time.Time
	// Title is the human-readable session label (seeded once from the first
	// genuine user prompt, clamped). Populated from the snapshot Title ONLY — a
	// session whose Title was never seeded (a pre-Title snapshot, or a
	// multimodal-only first prompt) carries "" here; the caller may fall back to
	// the lazy deriveTitle walk via a full Load if it needs the derived value.
	Title string
	// Owner is the verified caller the session is attributed to (ADR 0204), or
	// nil when the session is ownerless.
	Owner *session.Principal
}

// SessionDiscoveryMeta is the additive bounded-inventory projection. It keeps
// SessionMeta source-compatible while carrying the trusted taxonomy and workspace
// needed by discovery clients.
type SessionDiscoveryMeta struct {
	ID              session.SessionID
	ModifiedAt      time.Time
	State           session.State
	Turns           int
	ModelID         string
	CreatedAt       time.Time
	Title           string
	TitleProvenance session.TitleProvenance
	Owner           *session.Principal
	Workspace       string
	Kind            session.SessionKind
	Relationship    session.SessionRelationship
	// EstimatedBytes is a content-free backend estimate of bytes reclaimed by
	// deleting this session family. Zero means unavailable, never a measured
	// assertion that the family occupies no storage.
	EstimatedBytes int64
}

// MetaLister is the OPTIONAL cheap-listing seam a SessionStore adapter may
// additionally implement to enumerate picker metadata WITHOUT loading the full
// conversation of every stored session. It is a separate interface —
// SessionStore itself stays the minimal Save/Load pair — and consumers discover
// it by type assertion: a store that does not implement it falls back to the
// Load-per-row path. The metadata is a PROJECTION of the latest snapshot line,
// so it is the same latest-line-wins source Load trusts, just decoded into a
// small struct that skips the messages array.
//
// Contract:
//   - MetaList returns ALL stored sessions' picker metadata (id + state +
//     turns + model id + title + creation + last-modified), in no guaranteed
//     order. A row whose last snapshot line cannot be decoded
//     (truncated/empty/corrupt file) is skipped best-effort rather than failing
//     the whole inventory — the same tolerance List applies.
//   - It applies NO filtering — which rows to show is the CALLER's business.
type MetaLister interface {
	// MetaList returns every stored session's picker metadata, reading only the
	// last snapshot line of each (never the full conversation).
	MetaList(ctx context.Context) ([]SessionMeta, error)
}

// ErrSessionMetadataPagingUnsupported is returned by a metadata pager whose
// backend cannot enumerate bounded inventory pages. It is a permanent
// capability posture, distinct from a transient storage failure.
var ErrSessionMetadataPagingUnsupported = errors.New("port: store does not support session metadata paging")

// ErrSessionMetadataCursorRestart reports that a metadata cursor no longer
// identifies the same generation and owner/filter scope. Callers must discard
// the cursor and restart at page one; adapters never continue across the
// mismatch.
var ErrSessionMetadataCursorRestart = errors.New("port: session metadata cursor requires restart")

// SessionMetadataCursor is an adapter-issued keyset position. Public transports
// encode the whole value as an opaque token. Generation and Scope bind a page
// sequence to one backend view and filter set; Continuation is an opaque value
// owned and validated only by the issuing pager. ModifiedAt and ID retain the
// neutral ordering boundary used for response validation.
type SessionMetadataCursor struct {
	ModifiedAt   time.Time
	ID           session.SessionID
	Generation   string
	Scope        string
	Continuation string
}

// SessionMetadataPageRequest asks an optional pager for one bounded metadata
// page. Ownership is part of the storage query so filtering happens before page
// formation and TotalCount; a nil Owner with OwnershipEnforced selects no rows.
type SessionMetadataPageRequest struct {
	Limit             int
	Cursor            *SessionMetadataCursor
	OwnershipEnforced bool
	Owner             *session.Principal
}

// SessionMetadataPage is one best-effort keyset page. Concurrent saves may move
// rows to an earlier page; the response remains bounded and owner-filtered.
type SessionMetadataPage struct {
	Sessions   []SessionDiscoveryMeta
	NextCursor *SessionMetadataCursor
	TotalCount int
}

// SessionMetadataPager is the OPTIONAL bounded inventory seam. SessionStore
// remains the required Save/Load pair. Implementations order rows by
// (ModifiedAt DESC, ID ASC), filter ownership before paging/counting, return at
// most request.Limit rows, and use strict keyset continuation after Cursor.
type SessionMetadataPager interface {
	PageSessionMetadata(ctx context.Context, request SessionMetadataPageRequest) (SessionMetadataPage, error)
}

// SessionStorageHealth is an aggregate, content-free measurement derived from
// a backend's bounded metadata index. Availability bits distinguish an honest
// zero measurement from a value the backend cannot provide.
type SessionStorageHealth struct {
	Available                 bool
	UnavailableReason         string
	CurrentBytes              int64
	CurrentBytesAvailable     bool
	ReclaimableBytes          int64
	ReclaimableBytesAvailable bool
	SessionCount              int64
	FileCount                 int64
	V1Count                   int64
	V2Count                   int64
	MainCount                 int64
	ChildCount                int64
	ScheduledCount            int64
	UnknownCount              int64
	CorruptCount              int64
}

// SessionStorageHealthProvider is the OPTIONAL bounded storage-health seam.
// Implementations must use only an existing metadata index and cheap file/object
// metadata. They must not load session snapshots or traverse transcripts.
type SessionStorageHealthProvider interface {
	SessionStorageHealth(ctx context.Context) (SessionStorageHealth, error)
}

// PaginateSessionMetadata applies the shared owner-filter, ordering, and keyset
// rules to an adapter's metadata scan. It is retained for callers that form a
// single page without a generation-bound continuation. Pager implementations
// should use PaginateSessionMetadataBound.
func PaginateSessionMetadata(rows []SessionDiscoveryMeta, request SessionMetadataPageRequest) SessionMetadataPage {
	page, _ := paginateSessionMetadata(rows, request, "", false)
	return page
}

// PaginateSessionMetadataBound applies generation- and filter-bound pagination
// for scan-based adapters. It returns ErrSessionMetadataCursorRestart rather
// than mixing rows when the current inventory or owner scope differs from the
// cursor. Indexed adapters may implement the same contract with adapter-private
// opaque continuations instead of scanning.
//
// generation is the CALLER's own cheap, monotonic "has anything in this store
// changed" signal (e.g. a counter bumped on every Save/Delete) — this helper
// does not derive one from rows itself. An earlier version computed a
// generation by JSON-marshalling and SHA-256-hashing the entire filtered row
// set on every call; the real cost that removed is a full JSON encode +
// SHA-256 of every row on every page (a large constant factor) — the row
// copy/sort prepareSessionMetadataRows does is still O(rows) per call
// regardless, so this is not an asymptotic change. A caller with no cheaper
// signal available may still pass a content hash, but should prefer a real
// counter. generation must be non-empty: an empty value cannot mean "unbound"
// here (that's PaginateSessionMetadata) — silently downgrading would let a
// stale cursor mix rows instead of restarting, exactly what
// ErrSessionMetadataCursorRestart exists to prevent.
func PaginateSessionMetadataBound(rows []SessionDiscoveryMeta, request SessionMetadataPageRequest, generation string) (SessionMetadataPage, error) {
	if generation == "" {
		return SessionMetadataPage{}, fmt.Errorf("port: PaginateSessionMetadataBound requires a non-empty generation")
	}
	return paginateSessionMetadata(rows, request, generation, true)
}

const scanMetadataContinuation = "mecatl-scan-keyset-v1"

func paginateSessionMetadata(rows []SessionDiscoveryMeta, request SessionMetadataPageRequest, generation string, bind bool) (SessionMetadataPage, error) {
	filtered := prepareSessionMetadataRows(rows, request)
	scope := metadataPageScope(request)
	if bind && request.Cursor != nil && (request.Cursor.Generation != generation || request.Cursor.Scope != scope ||
		request.Cursor.Continuation != scanMetadataContinuation) {
		return SessionMetadataPage{}, ErrSessionMetadataCursorRestart
	}

	start := 0
	if request.Cursor != nil {
		start = len(filtered)
		for i, row := range filtered {
			if row.ModifiedAt.Before(request.Cursor.ModifiedAt) ||
				(row.ModifiedAt.Equal(request.Cursor.ModifiedAt) && row.ID > request.Cursor.ID) {
				start = i
				break
			}
		}
	}
	limit := request.Limit
	if limit < 0 {
		limit = 0
	}
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := SessionMetadataPage{Sessions: filtered[start:end], TotalCount: len(filtered)}
	if end < len(filtered) && end > start {
		last := filtered[end-1]
		page.NextCursor = &SessionMetadataCursor{
			ModifiedAt: last.ModifiedAt, ID: last.ID, Generation: generation, Scope: scope,
		}
		if bind {
			page.NextCursor.Continuation = scanMetadataContinuation
		}
	}
	return page, nil
}

func prepareSessionMetadataRows(rows []SessionDiscoveryMeta, request SessionMetadataPageRequest) []SessionDiscoveryMeta {
	filtered := make([]SessionDiscoveryMeta, 0, len(rows))
	for _, row := range rows {
		if request.OwnershipEnforced && (request.Owner == nil || !request.Owner.SameIdentity(row.Owner)) {
			continue
		}
		row.Owner = row.Owner.Clone()
		if row.Relationship.BranchIndex != nil {
			index := *row.Relationship.BranchIndex
			row.Relationship.BranchIndex = &index
		}
		filtered = append(filtered, row)
	}
	slices.SortFunc(filtered, CompareSessionMetadataOrder)
	return filtered
}

// CompareSessionMetadataOrder is the shared (ModifiedAt DESC, ID ASC) ordering
// every SessionMetadataPager/MetaLister implementation sorts session inventory
// rows by. It returns a negative number when a sorts before b, zero when the
// two share the same order key, and a positive number when a sorts after b.
func CompareSessionMetadataOrder(a, b SessionDiscoveryMeta) int {
	if c := b.ModifiedAt.Compare(a.ModifiedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

func metadataPageScope(request SessionMetadataPageRequest) string {
	if !request.OwnershipEnforced {
		return "all"
	}
	if request.Owner == nil {
		return "owner:none"
	}
	sum := session.PrincipalScopeHash(request.Owner)
	return "owner:" + hex.EncodeToString(sum[:])
}

// ErrPruneUnsupported is the port-level sentinel a PrunableStore's List or
// Delete wraps (with %w) when the store's BACKEND cannot enumerate/delete
// sessions at all — e.g. a remote driver answering UNIMPLEMENTED. It is the
// "this seam will never work here" signal, distinct from a transient
// infrastructure failure (I/O error, timeout): a consumer that sees it via
// errors.Is should stop consulting the seam (the composition layer's
// child-session GC logs one INFO and stickily disables further sweeps),
// whereas any other error is retried on the next sweep.
var ErrPruneUnsupported = errors.New("port: store does not support retention pruning")

// PrunableStore is the OPTIONAL retention seam a SessionStore adapter may
// additionally implement. It is a separate interface — SessionStore itself
// stays the minimal Save/Load pair — and consumers discover it by type
// assertion: a store that does not implement it is simply never swept (the
// composition-layer child-session GC degrades to a no-op).
//
// Contract:
//   - List returns ALL stored session ids (with their last-modified times).
//     It applies NO filtering — retention policy (which ids are prunable,
//     age thresholds, per-family caps) is entirely the CALLER's business.
//   - Delete removes the snapshot stored under id, plus any sidecar records
//     the adapter keeps for it (e.g. jsonlstore's tool-call log). It is
//     IDEMPOTENT: deleting an unknown id succeeds — callers tolerate
//     List/Delete races by construction.
type PrunableStore interface {
	// List returns every stored session's id and last-modified time, in no
	// guaranteed order.
	List(ctx context.Context) ([]StoredSession, error)
	// Delete removes the session stored under id. An unknown id is success
	// (idempotent); any returned error is an infrastructure failure.
	Delete(ctx context.Context, id session.SessionID) error
}

// ConditionalPrunableStore is the OPTIONAL atomic cleanup seam. Implementations
// acquire their session-family mutation exclusion, compare the complete expected
// metadata row with the current durable row, and keep that exclusion held through
// sidecar-first/snapshot-last deletion. A mismatch or missing row returns false
// without mutation; backend failures return an error.
type ConditionalPrunableStore interface {
	DeleteSessionIfUnchanged(ctx context.Context, expected SessionDiscoveryMeta) (bool, error)
}

// SessionDiscoveryMetaEqual reports whether two inventory rows represent the
// same durable cleanup precondition. Owner identity is compared semantically.
func SessionDiscoveryMetaEqual(a, b SessionDiscoveryMeta) bool {
	return a.ID == b.ID && a.ModifiedAt.Equal(b.ModifiedAt) && a.State == b.State &&
		a.Kind == b.Kind && sessionRelationshipsEqual(a.Relationship, b.Relationship) &&
		((a.Owner == nil && b.Owner == nil) || (a.Owner != nil && a.Owner.SameIdentity(b.Owner)))
}

func sessionRelationshipsEqual(a, b session.SessionRelationship) bool {
	if a.ScheduleName != b.ScheduleName || a.OriginSessionID != b.OriginSessionID ||
		a.ParentSessionID != b.ParentSessionID || a.CallID != b.CallID ||
		a.TeamID != b.TeamID || a.MemberName != b.MemberName || a.DebugTargetID != b.DebugTargetID {
		return false
	}
	return a.BranchIndex == nil && b.BranchIndex == nil ||
		a.BranchIndex != nil && b.BranchIndex != nil && *a.BranchIndex == *b.BranchIndex
}

// SessionDeleteSupport is the optional authoritative capability signal for a
// SessionStore that implements PrunableStore for compatibility even when its
// backend cannot delete sessions. Consumers should prefer this signal when it
// is present; a PrunableStore without it supports deletion by contract.
type SessionDeleteSupport interface {
	SupportsSessionDelete() bool
}
