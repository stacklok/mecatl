package grpcdriver

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// SnapshotFormat is the snapshot envelope format tag this client writes on
// Save and accepts on Load: the payload is exactly sessnap.Marshal output
// (one JSON line). The driver round-trips the tag verbatim; it changes ONLY
// if the encoding itself is replaced (sessnap's own schema evolution is
// additive and needs no bump). Load rejects any other tag with an
// infrastructure error — never ErrNotFound.
//
// SIGNPOST — a future format bump MUST be read-set-accept / write-newest:
// the readers (this client's Load, the server wrapper's Save decode) must
// keep ACCEPTING every previously-shipped format tag while Save WRITES only
// the newest. A bump that switches the write tag and rejects the old tag in
// the same step bricks every session already stored under the old format —
// the driver round-trips envelopes verbatim and cannot migrate them.
const SnapshotFormat = "sessnap-json/1"

// MaxSnapshotBytes is the protocol's REQUIRED MINIMUM message capacity for a
// snapshot envelope: 64 MiB. A session snapshot legitimately reaches multiple
// MiB (inline media parts alone may carry session.MaxPromptMediaBytes = 20 MiB
// in one prompt, base64-inflated by sessnap's JSON encoding), so gRPC's
// default 4 MiB receive cap would brick Save/Load mid-session. Dial raises
// the client's per-call send AND receive limits to this value; a conforming
// driver MUST accept payloads up to it on its server too
// (grpc.MaxRecvMsgSize(MaxSnapshotBytes) — see the server-wrapper notes in
// server.go and the SessionSnapshot doc in session_store.proto). Pinned by
// the storeconformance "large snapshot" subtest run over bufconn.
const MaxSnapshotBytes = 64 << 20

// ErrNotFound is returned by Load when the driver has no snapshot under the
// given id. It wraps port.ErrSessionNotFound so a consumer that may not
// import this adapter can distinguish not-found from an infra failure via
// errors.Is.
var ErrNotFound = fmt.Errorf("grpcdriver: session not found: %w", port.ErrSessionNotFound)

// SessionStore is a port.SessionStore over a remote SessionStoreService
// driver. Encode/decode happens HERE (sessnap), harness-side: the driver only
// ever sees the opaque envelope.
type SessionStore struct {
	client driverv1.SessionStoreServiceClient
}

// compile-time assertions that SessionStore satisfies the port plus the
// optional retention seam. The client implements PrunableStore UNCONDITIONALLY
// — a driver backed by a non-enumerable store answers List/Delete with
// UNIMPLEMENTED, which this client maps to port.ErrPruneUnsupported (wrapped);
// the composition sweeper recognises that sentinel, logs ONE INFO, and
// stickily disables further sweeps, so such a driver degrades gracefully to
// "never swept" (without a recurring WARN) rather than failing the harness.
var (
	_ port.SessionStore  = (*SessionStore)(nil)
	_ port.PrunableStore = (*SessionStore)(nil)
)

// NewSessionStore wraps an established driver connection (see Dial) as a
// port.SessionStore.
func NewSessionStore(conn grpc.ClientConnInterface) *SessionStore {
	return &SessionStore{client: driverv1.NewSessionStoreServiceClient(conn)}
}

// Save encodes s via sessnap and persists it under s.ID on the driver,
// overwriting any prior snapshot. A nil session fails client-side with
// sessnap.ErrNilSession (no RPC), matching the local stores.
func (st *SessionStore) Save(ctx context.Context, s *session.Session) error {
	line, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	if _, err := st.client.Save(ctx, &driverv1.SaveRequest{
		SessionId: string(s.ID),
		Snapshot:  &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: line},
	}); err != nil {
		return rpcErr(ctx, "save", err)
	}
	return nil
}

// Load fetches the most recent snapshot for id from the driver and restores
// it through sessnap. A driver NOT_FOUND maps to ErrNotFound (wrapping
// port.ErrSessionNotFound); an unknown envelope format, a payload that fails
// to decode, or a decoded session whose id is NOT the requested one (a
// mis-keyed driver) is an infrastructure error, never not-found.
func (st *SessionStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	resp, err := st.client.Load(ctx, &driverv1.LoadRequest{SessionId: string(id)})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return nil, rpcErr(ctx, "load", err)
	}
	snap := resp.GetSnapshot()
	if got := snap.GetFormat(); got != SnapshotFormat {
		return nil, fmt.Errorf("grpcdriver: load %q: unknown snapshot format %q (this client speaks %q)", id, got, SnapshotFormat)
	}
	sess, err := sessnap.Unmarshal(snap.GetPayload())
	if err != nil {
		return nil, fmt.Errorf("grpcdriver: load %q: %w", id, err)
	}
	// Wrong-session guard: a driver that mis-keys its storage (or always
	// returns "the" session) must surface as a loud infra failure here, never
	// as a silently-adopted foreign session.
	if sess.ID != id {
		return nil, fmt.Errorf("grpcdriver: load %q: driver returned the snapshot of a DIFFERENT session %q (mis-keyed driver)", id, sess.ID)
	}
	return sess, nil
}

// List returns the driver's full stored-session inventory (ids +
// last-modified times). An unset modified_at maps to the zero time — the
// retention sweep's age pass then treats the entry as arbitrarily old, which
// fails SAFE only because the sweep also never touches unprefixed ids; a
// driver SHOULD return real times.
func (st *SessionStore) List(ctx context.Context) ([]port.StoredSession, error) {
	resp, err := st.client.List(ctx, &driverv1.ListSessionsRequest{})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			// The driver's backend cannot enumerate at all — a permanent
			// posture, not a transient fault. Surface the port sentinel so the
			// retention sweeper can disable itself instead of WARNing forever.
			return nil, fmt.Errorf("grpcdriver: list: %w: %v", port.ErrPruneUnsupported, err)
		}
		return nil, rpcErr(ctx, "list", err)
	}
	entries := resp.GetSessions()
	out := make([]port.StoredSession, 0, len(entries))
	for _, e := range entries {
		s := port.StoredSession{ID: session.SessionID(e.GetSessionId())}
		if ts := e.GetModifiedAt(); ts != nil {
			s.ModifiedAt = ts.AsTime()
		}
		out = append(out, s)
	}
	return out, nil
}

// Delete removes the snapshot stored under id on the driver. It is idempotent
// harness-side: a driver NOT_FOUND (a thin driver surfacing its primitive's
// miss) maps to success, per the port.PrunableStore contract.
func (st *SessionStore) Delete(ctx context.Context, id session.SessionID) error {
	if _, err := st.client.Delete(ctx, &driverv1.DeleteSessionRequest{SessionId: string(id)}); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if status.Code(err) == codes.Unimplemented {
			// Same permanent-posture mapping as List: the backend cannot
			// delete, so callers see the port sentinel via errors.Is.
			return fmt.Errorf("grpcdriver: delete: %w: %v", port.ErrPruneUnsupported, err)
		}
		return rpcErr(ctx, "delete", err)
	}
	return nil
}

// rpcErr wraps a failed RPC's error. When the CALLER's ctx is already
// done, it wraps ctx.Err() instead, so errors.Is(err, context.Canceled /
// context.DeadlineExceeded) holds harness-side exactly as it does for the
// local stores. Everything else is an opaque infrastructure failure — NO
// transient/permanent classification (resilience, if ever needed, is a
// decorator; see the package doc).
func rpcErr(ctx context.Context, op string, err error) error {
	ctxErr := ctx.Err()
	if ctxErr == nil && status.Code(err) == codes.DeadlineExceeded {
		// gRPC's transport timer can report the deadline just before the
		// context timer publishes ctx.Err(). Only classify it as caller-owned
		// when the caller's actual deadline has elapsed.
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			ctxErr = context.DeadlineExceeded
		}
	}
	if ctxErr != nil {
		return fmt.Errorf("grpcdriver: %s: %w (rpc: %v)", op, ctxErr, err)
	}
	return fmt.Errorf("grpcdriver: %s: %w", op, err)
}
