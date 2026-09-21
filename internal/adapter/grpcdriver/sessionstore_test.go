package grpcdriver

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// startBufconnServer stands up an in-memory gRPC server (services registered
// by register) and returns its listener. No real sockets: the fixture mirrors
// internal/adapter/server's bufconn tests, tearing itself down via t.Cleanup
// (gs.Stop + lis.Close). It mounts the server with the protocol's REQUIRED
// minimum snapshot capacity (grpc.MaxRecvMsgSize(MaxSnapshotBytes)) — the
// same posture a conforming driver must take (see server.go) — so the
// storeconformance "large snapshot" subtest exercises a spec-conforming
// driver, not gRPC's 4 MiB default.
func startBufconnServer(t *testing.T, register func(gs *grpc.Server), serverOpts ...grpc.ServerOption) *bufconn.Listener {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(append([]grpc.ServerOption{grpc.MaxRecvMsgSize(MaxSnapshotBytes)}, serverOpts...)...)
	register(gs)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})
	return lis
}

// dialBufconnListener connects a lazy client conn over the bufconn listener,
// with optional extra client dial options (e.g. per-RPC credentials), closing
// it via t.Cleanup. The base options come from the PRODUCTION dialOptions
// assembly (loopback target, zero dialConfig ⇒ plaintext, snapshot-sized call
// options), so the fixture's client posture cannot drift from Dial's.
func dialBufconnListener(t *testing.T, lis *bufconn.Listener, extra ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	base, err := dialOptions("127.0.0.1:0", dialConfig{})
	if err != nil {
		t.Fatalf("dialOptions: %v", err)
	}
	opts := append(base, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}))
	opts = append(opts, extra...)
	conn, err := grpc.NewClient("passthrough:///bufnet", opts...)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// dialBufconn is the one-step fixture: server up, client conn back.
func dialBufconn(t *testing.T, register func(gs *grpc.Server), serverOpts ...grpc.ServerOption) *grpc.ClientConn {
	t.Helper()
	return dialBufconnListener(t, startBufconnServer(t, register, serverOpts...))
}

func mustNewSessionStore(t *testing.T, conn grpc.ClientConnInterface) *SessionStore {
	t.Helper()
	st, err := NewSessionStore(context.Background(), conn)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	return st
}

// newWiredSessionStore returns a grpcdriver SessionStore client over a
// bufconn server wrapping a fresh memstore.
func newWiredSessionStore(t *testing.T) *SessionStore {
	t.Helper()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, NewSessionStoreServer(memstore.New()))
	})
	return mustNewSessionStore(t, conn)
}

type oldSessionStoreServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
}

type emptySessionCapabilitiesServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
}

func (emptySessionCapabilitiesServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return &driverv1.SessionStoreCapabilitiesResponse{}, nil
}

func currentSessionCapabilities() *driverv1.SessionStoreCapabilitiesResponse {
	return &driverv1.SessionStoreCapabilitiesResponse{Contract: sessionStoreContract}
}

func TestSessionStoreCapabilityNegotiationRequiresCurrentContract(t *testing.T) {
	t.Run("unimplemented capability RPC is rejected", func(t *testing.T) {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSessionStoreServiceServer(gs, oldSessionStoreServer{})
		})
		if st, err := NewSessionStore(context.Background(), conn); err == nil || st != nil {
			t.Fatalf("old driver negotiation = (%T, %v), want rejection", st, err)
		}
	})

	t.Run("empty capability response is rejected", func(t *testing.T) {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSessionStoreServiceServer(gs, emptySessionCapabilitiesServer{})
		})
		if st, err := NewSessionStore(context.Background(), conn); err == nil || st != nil {
			t.Fatalf("empty negotiation = (%T, %v), want rejection", st, err)
		}
	})

	t.Run("wrapped capable backend reports every optional operation", func(t *testing.T) {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSessionStoreServiceServer(gs, NewSessionStoreServer(memstore.New()))
		})
		st := mustNewSessionStore(t, conn)
		if !st.list || !st.metadataPaging || !st.SupportsSessionDelete() {
			t.Fatalf("negotiated capabilities = list:%v metadata:%v delete:%v, want all true", st.list, st.metadataPaging, st.delete)
		}
	})
}

type blockingSessionCapabilitiesServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
}

func (blockingSessionCapabilitiesServer) Capabilities(ctx context.Context, _ *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type failingSessionCapabilitiesServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
}

func (failingSessionCapabilitiesServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return nil, status.Error(codes.Unavailable, "capability backend unavailable")
}

func TestNewSessionStoreFailsClosedOnCapabilityProbeFailure(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSessionStoreServiceServer(gs, blockingSessionCapabilitiesServer{})
		})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		st, err := NewSessionStore(ctx, conn)
		if st != nil {
			t.Fatalf("timed-out negotiation returned partial store %T", st)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("negotiation error = %v, want deadline classification", err)
		}
	})

	t.Run("rpc failure", func(t *testing.T) {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSessionStoreServiceServer(gs, failingSessionCapabilitiesServer{})
		})
		st, err := NewSessionStore(context.Background(), conn)
		if st != nil {
			t.Fatalf("failed negotiation returned partial store %T", st)
		}
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("negotiation error = %v, want Unavailable", err)
		}
	})
}

func TestMetadataCursorRequiresCompleteBinding(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	base := port.SessionMetadataCursor{ID: "session", ModifiedAt: now, Generation: "generation", Scope: "scope", Continuation: "continuation"}
	if _, err := pageMetadataRequest(port.SessionMetadataPageRequest{Limit: 1, Cursor: &base}); err != nil {
		t.Fatalf("bound cursor rejected: %v", err)
	}
	for name, mutate := range map[string]func(*port.SessionMetadataCursor){
		"generation":   func(c *port.SessionMetadataCursor) { c.Generation = "" },
		"scope":        func(c *port.SessionMetadataCursor) { c.Scope = "" },
		"continuation": func(c *port.SessionMetadataCursor) { c.Continuation = "" },
	} {
		t.Run(name, func(t *testing.T) {
			cursor := base
			mutate(&cursor)
			if _, err := pageMetadataRequest(port.SessionMetadataPageRequest{Limit: 1, Cursor: &cursor}); err == nil {
				t.Fatal("partial cursor was accepted")
			}
		})
	}
}

func TestMetadataCursorBoundRoundTripAndUnboundResponseRejection(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	bound := &driverv1.SessionMetadataCursor{
		ModifiedAt: timestamppb.New(now), SessionId: "session", Generation: "generation", Scope: "scope", Continuation: "continuation",
	}
	page, err := metadataPageFromProto(&driverv1.PageSessionMetadataResponse{NextCursor: bound}, false)
	if err != nil {
		t.Fatalf("bound cursor response: %v", err)
	}
	if page.NextCursor == nil || page.NextCursor.Generation != "generation" || page.NextCursor.Scope != "scope" || page.NextCursor.Continuation != "continuation" {
		t.Fatalf("bound cursor changed: %+v", page.NextCursor)
	}
	unbound := &driverv1.SessionMetadataCursor{ModifiedAt: timestamppb.New(now), SessionId: "session"}
	if _, err := metadataPageFromProto(&driverv1.PageSessionMetadataResponse{NextCursor: unbound}, false); err == nil {
		t.Fatal("unbound response cursor was accepted")
	}
}

// TestLoadMissWrapsSentinel pins the §C table's not-found row: a driver
// NOT_FOUND surfaces as ErrNotFound wrapping port.ErrSessionNotFound, with
// the id in the message.
func TestLoadMissWrapsSentinel(t *testing.T) {
	st := newWiredSessionStore(t)
	_, err := st.Load(context.Background(), "missing-id")
	if err == nil {
		t.Fatal("Load(missing) = nil error, want not-found")
	}
	if !errors.Is(err, port.ErrSessionNotFound) {
		t.Errorf("Load(missing) error = %v, want errors.Is(_, port.ErrSessionNotFound)", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load(missing) error = %v, want errors.Is(_, grpcdriver.ErrNotFound)", err)
	}
}

// TestLoadUnknownFormatIsInfraError pins the format-versioning rule: a
// snapshot whose envelope carries an unknown format tag is an INFRASTRUCTURE
// error naming the format — never ErrSessionNotFound.
func TestLoadUnknownFormatIsInfraError(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, unknownFormatServer{})
	})
	st := mustNewSessionStore(t, conn)
	_, err := st.Load(context.Background(), "any-id")
	if err == nil {
		t.Fatal("Load(unknown format) = nil error, want an infra error")
	}
	if errors.Is(err, port.ErrSessionNotFound) {
		t.Errorf("Load(unknown format) error = %v: must NEVER be ErrSessionNotFound", err)
	}
	if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureSnapshot {
		t.Errorf("Load(unknown format) class = %s, want snapshot", got)
	}
	if want := "sessnap-json/99"; !strings.Contains(err.Error(), want) {
		t.Errorf("Load(unknown format) error %q does not name the offending format %q", err, want)
	}
}

// unknownFormatServer returns a syntactically valid envelope under a format
// tag this client does not speak.
type unknownFormatServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
}

func (unknownFormatServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return currentSessionCapabilities(), nil
}

func TestLoadTransportFailureIsClassifiedStore(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, unavailableLoadServer{})
	})
	st := mustNewSessionStore(t, conn)
	_, err := st.Load(context.Background(), "any-id")
	if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureStore {
		t.Fatalf("Load transport class = %s, want store: %v", got, err)
	}
}

func TestLoadMalformedSnapshotIsClassifiedSnapshot(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, snapshotLoadServer{payload: []byte("not-json")})
	})
	st := mustNewSessionStore(t, conn)
	_, err := st.Load(context.Background(), "requested")
	if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureSnapshot {
		t.Fatalf("Load malformed snapshot class = %s, want snapshot: %v", got, err)
	}
}

func TestLoadMismatchedReturnedIDIsClassifiedSnapshot(t *testing.T) {
	sess := session.New("different", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "r1"}, session.Limits{}, time.Unix(1, 0))
	payload, err := sessnap.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, snapshotLoadServer{payload: payload})
	})
	st := mustNewSessionStore(t, conn)
	_, err = st.Load(context.Background(), "requested")
	if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureSnapshot {
		t.Fatalf("Load mismatched ID class = %s, want snapshot: %v", got, err)
	}
}

type snapshotLoadServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
	payload []byte
}

func (snapshotLoadServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return currentSessionCapabilities(), nil
}

func (s snapshotLoadServer) Load(context.Context, *driverv1.LoadRequest) (*driverv1.LoadResponse, error) {
	return &driverv1.LoadResponse{Snapshot: &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: s.payload}}, nil
}

type unavailableLoadServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
}

func (unavailableLoadServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return currentSessionCapabilities(), nil
}

func (unavailableLoadServer) Load(context.Context, *driverv1.LoadRequest) (*driverv1.LoadResponse, error) {
	return nil, status.Error(codes.Unavailable, "backend unavailable")
}

func (unknownFormatServer) Load(context.Context, *driverv1.LoadRequest) (*driverv1.LoadResponse, error) {
	return &driverv1.LoadResponse{
		Snapshot: &driverv1.SessionSnapshot{Format: "sessnap-json/99", Payload: []byte(`{}`)},
	}, nil
}

// TestSaveNilSessionNoRPC pins the §C nil-save row: Save(nil) fails
// CLIENT-side with sessnap.ErrNilSession and performs zero RPCs.
func TestSaveNilSessionNoRPC(t *testing.T) {
	counter := &countingSessionServer{}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, counter)
	})
	st := mustNewSessionStore(t, conn)
	err := st.Save(context.Background(), nil)
	if !errors.Is(err, sessnap.ErrNilSession) {
		t.Errorf("Save(nil) error = %v, want errors.Is(_, sessnap.ErrNilSession)", err)
	}
	if counter.saves != 0 {
		t.Errorf("Save(nil) performed %d RPCs, want 0 (client-side encode failure)", counter.saves)
	}
}

// countingSessionServer counts Save RPCs (no concurrency in the test, a
// plain int suffices).
type countingSessionServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
	saves int
}

func (*countingSessionServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return currentSessionCapabilities(), nil
}

func (s *countingSessionServer) Save(context.Context, *driverv1.SaveRequest) (*driverv1.SaveResponse, error) {
	s.saves++
	return &driverv1.SaveResponse{}, nil
}

// TestContextCancelPassthrough pins the §C ctx row: after a failed RPC with a
// done caller ctx, the returned error satisfies errors.Is(_, ctx.Err()) so
// the harness's cancellation handling holds over the wire.
func TestContextCancelPassthrough(t *testing.T) {
	st := newWiredSessionStore(t)

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := st.Load(ctx, "any-id")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Load(cancelled ctx) error = %v, want errors.Is(_, context.Canceled)", err)
		}
	})
	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := st.Load(ctx, "any-id")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Load(expired ctx) error = %v, want errors.Is(_, context.DeadlineExceeded)", err)
		}
	})
}

// TestSaveLoadOverWire is the basic happy path outside the conformance
// suite: a session crosses encode→wire→decode→store and back intact enough
// to identify (the full field-wise contract lives in storeconformance).
func TestSaveLoadOverWire(t *testing.T) {
	st := newWiredSessionStore(t)
	ctx := context.Background()
	s := session.New("wire-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err := s.RecordUserPrompt("hello driver", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(ctx, "wire-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != "wire-1" || len(got.Conversation.Messages) != 1 || got.Conversation.Messages[0].Text != "hello driver" {
		t.Errorf("Load round trip = %+v, want the saved session back", got)
	}
}

func TestADR_0291_DriverStorageCarriesExactPrivateEnvironmentRef(t *testing.T) {
	want := session.EnvironmentRef{Kind: "remote", ID: "opaque-private-id", Revision: "inventory-r17"}
	entry, err := metadataToProto(port.SessionDiscoveryMeta{ID: "s1", EnvironmentRef: want})
	if err != nil {
		t.Fatalf("metadataToProto: %v", err)
	}
	if entry.GetEnvironmentRef() == nil || entry.GetEnvironmentRef().GetRevision() != want.Revision {
		t.Fatalf("driver environment ref = %+v, want exact private ref %+v", entry.GetEnvironmentRef(), want)
	}
	got := metadataFromProto(entry, true)
	if got.EnvironmentRef != want {
		t.Fatalf("round-tripped environment ref = %+v, want %+v", got.EnvironmentRef, want)
	}
}

// TestServerWrapperSaveRejectsBadEnvelope pins the server wrapper's Save
// pre-validation: every malformed envelope shape — blank session_id, missing
// snapshot, unknown format, empty payload, undecodable payload, and a
// top-level session_id that disagrees with the payload's id — is
// INVALID_ARGUMENT (never stored, never Internal). Invoked directly on the
// wrapper: the validation is the wrapper's own, no wire needed.
func TestServerWrapperSaveRejectsBadEnvelope(t *testing.T) {
	srv := NewSessionStoreServer(memstore.New())
	ctx := context.Background()

	goodPayload, err := sessnap.Marshal(session.New("id-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("sessnap.Marshal: %v", err)
	}

	cases := []struct {
		name string
		req  *driverv1.SaveRequest
	}{
		{
			name: "blank session_id",
			req:  &driverv1.SaveRequest{SessionId: "", Snapshot: &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: goodPayload}},
		},
		{
			name: "nil snapshot",
			req:  &driverv1.SaveRequest{SessionId: "id-1"},
		},
		{
			name: "unknown format",
			req:  &driverv1.SaveRequest{SessionId: "id-1", Snapshot: &driverv1.SessionSnapshot{Format: "sessnap-json/99", Payload: goodPayload}},
		},
		{
			name: "empty payload",
			req:  &driverv1.SaveRequest{SessionId: "id-1", Snapshot: &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: nil}},
		},
		{
			name: "undecodable payload",
			req:  &driverv1.SaveRequest{SessionId: "id-1", Snapshot: &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: []byte("{not json")}},
		},
		{
			name: "session_id disagrees with payload id",
			req:  &driverv1.SaveRequest{SessionId: "id-OTHER", Snapshot: &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: goodPayload}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := srv.Save(ctx, c.req)
			if err == nil {
				t.Fatal("Save(bad envelope) = nil error, want InvalidArgument")
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("Save(bad envelope) code = %v (%v), want InvalidArgument", got, err)
			}
		})
	}

	// And the well-formed control: the same payload under the MATCHING id is
	// accepted (so the table above fails for validation, not incidentals).
	if _, err := srv.Save(ctx, &driverv1.SaveRequest{
		SessionId: "id-1",
		Snapshot:  &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: goodPayload},
	}); err != nil {
		t.Fatalf("Save(well-formed control) = %v, want nil", err)
	}
}

// saveLoadOnlyStore strips memstore down to the bare port.SessionStore pair,
// so the server wrapper sees a NON-prunable backend.
type saveLoadOnlyStore struct{ inner port.SessionStore }

func (s saveLoadOnlyStore) Save(ctx context.Context, sess *session.Session) error {
	return s.inner.Save(ctx, sess)
}

func (s saveLoadOnlyStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.inner.Load(ctx, id)
}

// TestListDeleteUnsupportedForNonPrunableBackend pins capability negotiation:
// a wrapped plain Save/Load backend reports both operations unsupported, and the
// client keeps its unconditional PrunableStore methods while returning the
// existing sentinel without issuing an unsupported operation RPC.
func TestListDeleteUnsupportedForNonPrunableBackend(t *testing.T) {
	ctx := context.Background()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, NewSessionStoreServer(saveLoadOnlyStore{inner: memstore.New()}))
	})
	st := mustNewSessionStore(t, conn)

	if support := port.SessionDeleteSupport(st); support.SupportsSessionDelete() {
		t.Fatal("non-prunable backend advertised delete support")
	}

	_, err := st.List(ctx)
	if err == nil {
		t.Fatal("List over a non-prunable backend = nil error, want UNIMPLEMENTED")
	}
	if !errors.Is(err, port.ErrPruneUnsupported) {
		t.Errorf("List error = %v, want errors.Is(_, port.ErrPruneUnsupported)", err)
	}
	if err := st.Delete(ctx, "any-id"); err == nil {
		t.Fatal("Delete over a non-prunable backend = nil error, want UNIMPLEMENTED")
	} else if !errors.Is(err, port.ErrPruneUnsupported) {
		t.Errorf("Delete error = %v, want errors.Is(_, port.ErrPruneUnsupported)", err)
	}
}

// notFoundDeleteServer is a thin driver whose Delete surfaces its primitive's
// NOT_FOUND instead of the contract's idempotent OK.
type notFoundDeleteServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
}

func (notFoundDeleteServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return &driverv1.SessionStoreCapabilitiesResponse{Delete: true, Contract: sessionStoreContract}, nil
}

func (notFoundDeleteServer) Delete(context.Context, *driverv1.DeleteSessionRequest) (*driverv1.DeleteSessionResponse, error) {
	return nil, status.Error(codes.NotFound, "no such session")
}

// TestDeleteToleratesDriverNotFound pins the client's idempotency mapping: a
// driver NOT_FOUND on Delete is success (the port contract says unknown id =
// success), so a thin driver built over a NOT_FOUND-returning primitive still
// conforms.
func TestDeleteToleratesDriverNotFound(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, notFoundDeleteServer{})
	})
	st := mustNewSessionStore(t, conn)
	if err := st.Delete(context.Background(), "ghost"); err != nil {
		t.Errorf("Delete mapping a driver NOT_FOUND = %v, want nil (idempotent success)", err)
	}
}
