package port

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// ErrSessionNotFound is the port-level sentinel a SessionStore.Load wraps (with %w)
// when no session is stored under the requested id — distinct from a genuine
// infrastructure failure (I/O error, decode failure). It lets a consumer in a layer
// that may NOT import the store adapters (e.g. engine/agent's InspectMemberTool)
// distinguish "no such session" from "the store is broken" via errors.Is, without
// reaching for an adapter's own not-found sentinel. Every SessionStore adapter MUST
// wrap this for the not-found case.
var ErrSessionNotFound = errors.New("port: session not found")

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
//     — the budget brake reads it), and the creation metadata the events do not carry
//     (id, mode, limits, workspace, profile, provider/model selector, createdAt —
//     supplied out-of-band, e.g. eventsource.SessionMeta).
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
// conversation (messages array) skipped entirely. The store adapter populates
// it by reading ONLY the last snapshot line and decoding into a small struct,
// so listing N sessions is O(N × last-line-read) rather than O(N × filesize).
//
// It is owned by the PORT (so the server adapter references the shape without
// importing any concrete store) and implemented by a store via the optional
// MetaLister interface — discovered by type assertion, exactly like
// PrunableStore. A store that does NOT implement MetaLister falls back to the
// Load-per-row path (correct, just slower). State carries the persisted
// session.State verbatim; an invalid/unknown state is left empty (the row still
// surfaces its id/mtime, matching the Load-fails zeroed-fields behaviour).
type SessionMeta struct {
	// ID is the stored session's real id (decoded from the snapshot, not the
	// filename — safeName is not invertible).
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
