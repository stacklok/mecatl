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
	"fmt"
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
	savedAt map[session.SessionID]time.Time
	now     func() time.Time
}

// compile-time assertions that Store satisfies the port plus the optional
// retention seam.
var (
	_ port.SessionStore         = (*Store)(nil)
	_ port.PrunableStore        = (*Store)(nil)
	_ port.SessionMetadataPager = (*Store)(nil)
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

// New constructs an empty in-memory Store.
func New(opts ...Option) *Store {
	st := &Store{
		sessions: make(map[session.SessionID]sessnap.Snapshot),
		savedAt:  make(map[session.SessionID]time.Time),
		now:      time.Now,
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
	st.mu.Lock()
	st.sessions[s.ID] = snap
	st.savedAt[s.ID] = st.now()
	st.mu.Unlock()
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
	return snap.Restore()
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
			Title: snap.Title, TitleProvenance: snap.TitleProvenance, Workspace: snap.Workspace,
			Kind: kind, Relationship: snap.Relationship, Owner: snap.Owner,
		})
	}
	st.mu.RUnlock()
	return port.PaginateSessionMetadata(rows, request), nil
}

// Delete removes the session stored under id. It is idempotent: an unknown id
// is success (port.PrunableStore contract).
func (st *Store) Delete(_ context.Context, id session.SessionID) error {
	st.mu.Lock()
	delete(st.sessions, id)
	delete(st.savedAt, id)
	st.mu.Unlock()
	return nil
}
