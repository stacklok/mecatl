package grpcdriver

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

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
	client         driverv1.SessionStoreServiceClient
	list           bool
	metadataPaging bool
	delete         bool
	lineage        bool
	create         bool
}

// compile-time assertions that SessionStore satisfies the base port and keeps
// the optional operation interfaces unconditionally for compatibility. The
// negotiated flags are authoritative: unsupported calls return the existing
// port sentinels without advertising those operations to inventory consumers.
var (
	_ port.SessionStore         = (*SessionStore)(nil)
	_ port.SessionCreator       = (*SessionStore)(nil)
	_ port.PrunableStore        = (*SessionStore)(nil)
	_ port.SessionMetadataPager = (*SessionStore)(nil)
	_ port.SessionDeleteSupport = (*SessionStore)(nil)
	_ port.SessionLineageReader = (*SessionStore)(nil)
)

const sessionCapabilityTimeout = 5 * time.Second

// NewSessionStore wraps an established driver connection (see Dial), probing
// optional operations once. UNIMPLEMENTED means an older Save/Load-only driver;
// any other probe failure fails construction rather than guessing capabilities.
func NewSessionStore(ctx context.Context, conn grpc.ClientConnInterface) (*SessionStore, error) {
	st := &SessionStore{client: driverv1.NewSessionStoreServiceClient(conn)}
	probeCtx, cancel := context.WithTimeout(ctx, sessionCapabilityTimeout)
	defer cancel()
	caps, err := st.client.Capabilities(probeCtx, &driverv1.SessionStoreCapabilitiesRequest{})
	if status.Code(err) == codes.Unimplemented {
		return st, nil
	}
	if err != nil {
		return nil, rpcErr(probeCtx, "negotiate session-store capabilities", err)
	}
	st.list = caps.GetList()
	st.metadataPaging = caps.GetMetadataPaging()
	st.delete = caps.GetDelete()
	st.lineage = caps.GetLineage()
	st.create = caps.GetCreate()
	return st, nil
}

// SupportsSessionDelete reports the negotiated backend capability. SessionStore
// keeps implementing PrunableStore unconditionally for compatibility.
func (st *SessionStore) SupportsSessionDelete() bool { return st.delete }

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

// Create atomically publishes s when the negotiated driver supports SessionCreator.
func (st *SessionStore) Create(ctx context.Context, s *session.Session) error {
	if !st.create {
		return fmt.Errorf("grpcdriver: create: unsupported")
	}
	line, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	if _, err := st.client.Create(ctx, &driverv1.SaveRequest{SessionId: string(s.ID), Snapshot: &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: line}}); err != nil {
		if status.Code(err) == codes.AlreadyExists {
			return fmt.Errorf("grpcdriver: create %q: %w", s.ID, port.ErrSessionAlreadyExists)
		}
		return rpcErr(ctx, "create", err)
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
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureStore, rpcErr(ctx, "load", err))
	}
	snap := resp.GetSnapshot()
	if got := snap.GetFormat(); got != SnapshotFormat {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, fmt.Errorf("grpcdriver: load %q: unknown snapshot format %q (this client speaks %q)", id, got, SnapshotFormat))
	}
	sess, err := sessnap.Unmarshal(snap.GetPayload())
	if err != nil {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, fmt.Errorf("grpcdriver: load %q: %w", id, err))
	}
	// Wrong-session guard: a driver that mis-keys its storage (or always
	// returns "the" session) must surface as a loud infra failure here, never
	// as a silently-adopted foreign session.
	if sess.ID != id {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, fmt.Errorf("grpcdriver: load %q: driver returned the snapshot of a DIFFERENT session %q (mis-keyed driver)", id, sess.ID))
	}
	return sess, nil
}

// List returns the driver's full stored-session inventory (ids +
// last-modified times). An unset modified_at maps to the zero time — the
// retention sweep's age pass then treats the entry as arbitrarily old, which
// fails SAFE only because the sweep also never touches unprefixed ids; a
// driver SHOULD return real times.
func (st *SessionStore) List(ctx context.Context) ([]port.StoredSession, error) {
	if !st.list {
		return nil, fmt.Errorf("grpcdriver: list: %w", port.ErrPruneUnsupported)
	}
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

// ReadSessionLineage asks the trusted driver for bounded content-free direct edges.
func (st *SessionStore) ReadSessionLineage(ctx context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	if err := port.ValidateSessionLineageQuery(query); err != nil {
		return port.SessionLineageResult{}, err
	}
	if !st.lineage {
		return port.SessionLineageResult{}, fmt.Errorf("grpcdriver: lineage: %w", port.ErrSessionLineageUnsupported)
	}
	resp, err := st.client.ReadLineage(ctx, &driverv1.ReadSessionLineageRequest{RootSessionId: string(query.RootID), RootIncarnation: string(query.RootIncarnation), Limit: int32(query.Limit)}) // #nosec G115 -- bounded to 256
	if err != nil {
		return port.SessionLineageResult{}, rpcErr(ctx, "read lineage", err)
	}
	if len(resp.GetRecords()) > query.Limit {
		return port.SessionLineageResult{}, fmt.Errorf("grpcdriver: lineage response exceeds requested limit")
	}
	result := port.SessionLineageResult{Truncated: resp.GetTruncated(), Records: make([]port.SessionLineageRecord, 0, len(resp.GetRecords()))}
	for _, entry := range resp.GetRecords() {
		row, err := lineageRecordFromProto(entry)
		if err != nil {
			return port.SessionLineageResult{}, err
		}
		if row.ID != query.RootID && !lineageRecordDirectlyRelated(row, query) {
			return port.SessionLineageResult{}, fmt.Errorf("grpcdriver: lineage response escaped requested incarnation")
		}
		result.Records = append(result.Records, row)
	}
	if err := validateLineageOrder(result.Records, query.RootID); err != nil {
		return port.SessionLineageResult{}, err
	}
	return result, nil
}

func lineageRecordDirectlyRelated(row port.SessionLineageRecord, query port.SessionLineageQuery) bool {
	rel := row.Relationship
	return rel.ParentSessionID == query.RootID && rel.ParentIncarnation == query.RootIncarnation ||
		rel.OriginSessionID == query.RootID && rel.OriginIncarnation == query.RootIncarnation ||
		rel.DebugTargetID == query.RootID && rel.DebugTargetIncarnation == query.RootIncarnation
}

func lineageRecordFromProto(entry *driverv1.SessionLineageEntry) (port.SessionLineageRecord, error) {
	rel := session.SessionRelationship{
		ParentSessionID: session.SessionID(entry.GetParentSessionId()), ParentIncarnation: session.IncarnationID(entry.GetParentIncarnation()), CallID: session.ToolCallID(entry.GetCallId()),
		ScheduleName: entry.GetScheduleName(), OriginSessionID: session.SessionID(entry.GetOriginSessionId()), OriginIncarnation: session.IncarnationID(entry.GetOriginIncarnation()),
		TeamID: entry.GetTeamId(), MemberName: entry.GetMemberName(), DebugTargetID: session.SessionID(entry.GetDebugTargetSessionId()), DebugTargetIncarnation: session.IncarnationID(entry.GetDebugTargetIncarnation()),
	}
	if entry.BranchIndex != nil {
		index := int(entry.GetBranchIndex())
		rel.BranchIndex = &index
	}
	row := port.SessionLineageRecord{ID: session.SessionID(entry.GetSessionId()), Kind: session.SessionKind(entry.GetKind()), Relationship: rel, State: port.SessionLineageState(entry.GetState()), Incarnation: entry.GetIncarnation()}
	if len(entry.GetOwnerScope()) != len(row.OwnerScope) {
		return port.SessionLineageRecord{}, fmt.Errorf("grpcdriver: invalid lineage owner scope")
	}
	copy(row.OwnerScope[:], entry.GetOwnerScope())
	if ts := entry.GetDeletedAt(); ts != nil {
		if ts.CheckValid() != nil {
			return port.SessionLineageRecord{}, fmt.Errorf("grpcdriver: invalid lineage deletion time")
		}
		row.DeletedAt = ts.AsTime()
	}
	if row.ID == "" || !session.IncarnationID(row.Incarnation).Valid() || session.ValidateSessionMetadata(row.Kind, row.Relationship) != nil ||
		(row.State != port.SessionLineageRetained && row.State != port.SessionLineagePruned) ||
		(row.State == port.SessionLineageRetained && !row.DeletedAt.IsZero()) || (row.State == port.SessionLineagePruned && row.DeletedAt.IsZero()) {
		return port.SessionLineageRecord{}, fmt.Errorf("grpcdriver: invalid lineage response")
	}
	return row, nil
}

func validateLineageOrder(records []port.SessionLineageRecord, root session.SessionID) error {
	for i := 1; i < len(records); i++ {
		if !lineageWireLess(records[i-1], records[i], root) {
			return fmt.Errorf("grpcdriver: lineage records out of order")
		}
	}
	return nil
}

func lineageWireLess(a, b port.SessionLineageRecord, root session.SessionID) bool {
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

func metadataAfter(row port.SessionDiscoveryMeta, cursor *port.SessionMetadataCursor) bool {
	return row.ModifiedAt.Before(cursor.ModifiedAt) ||
		(row.ModifiedAt.Equal(cursor.ModifiedAt) && row.ID > cursor.ID)
}

// legacyOrBoundCursorFields reports whether Generation/Scope/Continuation are
// either all absent (an older peer's pre-generation-binding cursor, still a
// legitimate value per port.PaginateSessionMetadata) or all present (this
// protocol's generation-bound cursor) -- never a partial mix, which would
// indicate a corrupt or forged cursor. A version-skewed peer that never
// populates these fields degrades to ErrSessionMetadataCursorRestart on the
// next page rather than a hard reject; see docs/adr/0226-session-storage-maintenance.md.
func legacyOrBoundCursorFields(generation, scope, continuation string) bool {
	if generation == "" && scope == "" && continuation == "" {
		return true
	}
	return generation != "" && scope != "" && continuation != ""
}

func validPortMetadataCursor(cursor *port.SessionMetadataCursor) bool {
	return cursor != nil && cursor.ID != "" && utf8.ValidString(string(cursor.ID)) &&
		legacyOrBoundCursorFields(cursor.Generation, cursor.Scope, cursor.Continuation)
}

func validateMetadataPage(page port.SessionMetadataPage, request port.SessionMetadataPageRequest) error {
	if len(page.Sessions) > request.Limit {
		return fmt.Errorf("returned %d rows for limit %d", len(page.Sessions), request.Limit)
	}
	if page.TotalCount < 0 || page.TotalCount < len(page.Sessions) {
		return fmt.Errorf("invalid total count %d", page.TotalCount)
	}
	for i, row := range page.Sessions {
		if row.ID == "" || !utf8.ValidString(string(row.ID)) {
			return fmt.Errorf("invalid session id")
		}
		if request.OwnershipEnforced && (request.Owner == nil || !request.Owner.SameIdentity(row.Owner)) {
			return fmt.Errorf("row outside requested owner scope")
		}
		if request.Cursor != nil && !metadataAfter(row, request.Cursor) {
			return fmt.Errorf("row before cursor")
		}
		if i > 0 && !metadataAfter(row, &port.SessionMetadataCursor{
			ModifiedAt: page.Sessions[i-1].ModifiedAt,
			ID:         page.Sessions[i-1].ID,
		}) {
			return fmt.Errorf("rows out of keyset order")
		}
	}
	if page.NextCursor != nil {
		if len(page.Sessions) == 0 || !validPortMetadataCursor(page.NextCursor) {
			return fmt.Errorf("invalid next cursor")
		}
		last := page.Sessions[len(page.Sessions)-1]
		if !page.NextCursor.ModifiedAt.Equal(last.ModifiedAt) || page.NextCursor.ID != last.ID {
			return fmt.Errorf("next cursor does not identify final row")
		}
	}
	return nil
}

// PageSessionMetadata asks the remote driver for one bounded owner-filtered
// metadata page. UNIMPLEMENTED is the optional pager's permanent unsupported
// posture, not a transient transport failure.
func (st *SessionStore) PageSessionMetadata(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	if !st.metadataPaging {
		return port.SessionMetadataPage{}, fmt.Errorf("grpcdriver: page metadata: %w", port.ErrSessionMetadataPagingUnsupported)
	}
	req, err := pageMetadataRequest(request)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	resp, err := st.client.PageMetadata(ctx, req)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return port.SessionMetadataPage{}, fmt.Errorf("grpcdriver: page metadata: %w: %v", port.ErrSessionMetadataPagingUnsupported, err)
		}
		if status.Code(err) == codes.Aborted {
			return port.SessionMetadataPage{}, fmt.Errorf("grpcdriver: page metadata: %w", port.ErrSessionMetadataCursorRestart)
		}
		return port.SessionMetadataPage{}, rpcErr(ctx, "page metadata", err)
	}
	page, err := metadataPageFromProto(resp)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	if err := validateMetadataPage(page, request); err != nil {
		return port.SessionMetadataPage{}, fmt.Errorf("grpcdriver: page metadata: invalid driver response: %w", err)
	}
	return page, nil
}

func pageMetadataRequest(request port.SessionMetadataPageRequest) (*driverv1.PageSessionMetadataRequest, error) {
	if request.Limit <= 0 || request.Limit > 1<<31-1 {
		return nil, fmt.Errorf("grpcdriver: page metadata: limit must be between 1 and %d", 1<<31-1)
	}
	req := &driverv1.PageSessionMetadataRequest{Limit: int32(request.Limit), OwnershipEnforced: request.OwnershipEnforced}
	if request.Cursor != nil {
		if !validPortMetadataCursor(request.Cursor) {
			return nil, fmt.Errorf("grpcdriver: page metadata: cursor is invalid")
		}
		if err := timestamppb.New(request.Cursor.ModifiedAt).CheckValid(); err != nil {
			return nil, fmt.Errorf("grpcdriver: page metadata: cursor modified time: %w", err)
		}
		req.Cursor = &driverv1.SessionMetadataCursor{
			ModifiedAt: timestamppb.New(request.Cursor.ModifiedAt), SessionId: string(request.Cursor.ID),
			Generation: request.Cursor.Generation, Scope: request.Cursor.Scope, Continuation: request.Cursor.Continuation,
		}
	}
	if request.Owner != nil {
		req.OwnerIssuer = request.Owner.Issuer
		req.OwnerSubject = request.Owner.Subject
	}
	return req, nil
}

func metadataPageFromProto(resp *driverv1.PageSessionMetadataResponse) (port.SessionMetadataPage, error) {
	page := port.SessionMetadataPage{TotalCount: int(resp.GetTotalCount()), Sessions: make([]port.SessionDiscoveryMeta, 0, len(resp.GetSessions()))}
	for _, entry := range resp.GetSessions() {
		if entry == nil {
			return port.SessionMetadataPage{}, fmt.Errorf("grpcdriver: page metadata: driver returned a nil row")
		}
		if ts := entry.GetModifiedAt(); ts != nil && ts.CheckValid() != nil {
			return port.SessionMetadataPage{}, fmt.Errorf("grpcdriver: page metadata: driver returned an invalid modified time")
		}
		if ts := entry.GetCreatedAt(); ts != nil && ts.CheckValid() != nil {
			return port.SessionMetadataPage{}, fmt.Errorf("grpcdriver: page metadata: driver returned an invalid creation time")
		}
		page.Sessions = append(page.Sessions, metadataFromProto(entry))
	}
	if cursor := resp.GetNextCursor(); cursor != nil {
		if cursor.GetModifiedAt() == nil || cursor.GetModifiedAt().CheckValid() != nil || cursor.GetSessionId() == "" ||
			!legacyOrBoundCursorFields(cursor.GetGeneration(), cursor.GetScope(), cursor.GetContinuation()) {
			return port.SessionMetadataPage{}, fmt.Errorf("grpcdriver: page metadata: driver returned an invalid next cursor")
		}
		page.NextCursor = &port.SessionMetadataCursor{
			ID: session.SessionID(cursor.GetSessionId()), ModifiedAt: cursor.GetModifiedAt().AsTime(),
			Generation: cursor.GetGeneration(), Scope: cursor.GetScope(), Continuation: cursor.GetContinuation(),
		}
	}
	return page, nil
}

func metadataFromProto(entry *driverv1.SessionMetadataEntry) port.SessionDiscoveryMeta {
	meta := port.SessionDiscoveryMeta{
		ID:              session.SessionID(entry.GetSessionId()),
		State:           session.State(entry.GetState()),
		Turns:           int(entry.GetTurns()),
		ModelID:         entry.GetModelId(),
		Title:           entry.GetTitle(),
		TitleProvenance: session.TitleProvenance(entry.GetTitleProvenance()),
		EnvironmentRef:  environmentRefFromProto(entry.GetEnvironmentRef()),
		Kind:            session.SessionKind(entry.GetKind()),
		EstimatedBytes:  entry.GetEstimatedBytes(),
		Relationship: session.SessionRelationship{
			ParentSessionID:        session.SessionID(entry.GetParentSessionId()),
			ParentIncarnation:      session.IncarnationID(entry.GetParentIncarnation()),
			CallID:                 session.ToolCallID(entry.GetCallId()),
			ScheduleName:           entry.GetScheduleName(),
			OriginSessionID:        session.SessionID(entry.GetOriginSessionId()),
			OriginIncarnation:      session.IncarnationID(entry.GetOriginIncarnation()),
			TeamID:                 entry.GetTeamId(),
			MemberName:             entry.GetMemberName(),
			DebugTargetID:          session.SessionID(entry.GetDebugTargetSessionId()),
			DebugTargetIncarnation: session.IncarnationID(entry.GetDebugTargetIncarnation()),
		},
	}
	if entry.GetModifiedAt() != nil {
		meta.ModifiedAt = entry.GetModifiedAt().AsTime()
	}
	if entry.GetCreatedAt() != nil {
		meta.CreatedAt = entry.GetCreatedAt().AsTime()
	}
	if entry.BranchIndex != nil {
		index := int(entry.GetBranchIndex())
		meta.Relationship.BranchIndex = &index
	}
	if entry.GetHasOwner() {
		meta.Owner = &session.Principal{
			Issuer: entry.GetOwnerIssuer(), Subject: entry.GetOwnerSubject(),
			GrantType: session.GrantType(entry.GetOwnerGrantType()), Name: entry.GetOwnerName(),
		}
	}
	return meta
}

func environmentRefFromProto(ref *driverv1.StoredEnvironmentRef) session.EnvironmentRef {
	if ref == nil {
		return session.EnvironmentRef{}
	}
	return session.EnvironmentRef{Kind: session.EnvironmentKind(ref.GetKind()), ID: ref.GetId(), Revision: ref.GetRevision()}
}

// Delete removes the snapshot stored under id on the driver. It is idempotent
// harness-side: a driver NOT_FOUND (a thin driver surfacing its primitive's
// miss) maps to success, per the port.PrunableStore contract.
func (st *SessionStore) Delete(ctx context.Context, id session.SessionID) error {
	if !st.delete {
		return fmt.Errorf("grpcdriver: delete: %w", port.ErrPruneUnsupported)
	}
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
