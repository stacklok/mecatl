package grpcdriver

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

func activitySession(t *testing.T, id session.SessionID, active bool) *session.Session {
	t.Helper()
	s := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "r1"}, session.Limits{}, time.Unix(1, 0))
	if active {
		if err := s.RecordUserPrompt("prompt", nil); err != nil {
			t.Fatalf("RecordUserPrompt: %v", err)
		}
	}
	return s
}

type activityCaptureServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
	delegate driverv1.SessionStoreServiceServer
	saves    []*driverv1.SaveRequest
	creates  []*driverv1.SaveRequest
}

func (s *activityCaptureServer) Capabilities(ctx context.Context, req *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return s.delegate.Capabilities(ctx, req)
}
func (s *activityCaptureServer) Save(ctx context.Context, req *driverv1.SaveRequest) (*driverv1.SaveResponse, error) {
	s.saves = append(s.saves, req)
	return s.delegate.Save(ctx, req)
}
func (s *activityCaptureServer) Create(ctx context.Context, req *driverv1.SaveRequest) (*driverv1.SaveResponse, error) {
	s.creates = append(s.creates, req)
	return s.delegate.Create(ctx, req)
}
func (s *activityCaptureServer) PageMetadata(ctx context.Context, req *driverv1.PageSessionMetadataRequest) (*driverv1.PageSessionMetadataResponse, error) {
	return s.delegate.PageMetadata(ctx, req)
}

type activityNoProjectionCaptureServer struct{ *activityCaptureServer }

func (*activityNoProjectionCaptureServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return &driverv1.SessionStoreCapabilitiesResponse{Create: true, Contract: sessionStoreContract}, nil
}

func TestDraftAwareSessionInventory_Scenario2_StoresProjectActivityAtomically(t *testing.T) {
	ctx := context.Background()
	stores := []struct {
		name string
		new  func(*testing.T) port.SessionStore
	}{
		{name: "memstore", new: func(*testing.T) port.SessionStore { return memstore.New() }},
		{name: "jsonl", new: func(t *testing.T) port.SessionStore {
			st, err := jsonlstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("jsonlstore.New: %v", err)
			}
			return st
		}},
		{name: "redis", new: func(t *testing.T) port.SessionStore {
			mr := miniredis.RunT(t)
			st, err := redisstore.New(mr.Addr())
			if err != nil {
				t.Fatalf("redisstore.New: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}},
	}
	for _, tt := range stores {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.new(t)
			creator := st.(port.SessionCreator)
			// A failed first publication must be invisible to both discovery
			// projections: there is no snapshot from which a row could be made.
			if err := creator.Create(ctx, nil); err == nil {
				t.Fatal("nil initial Create succeeded")
			}
			pager := st.(port.SessionMetadataPager)
			page, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
			if err != nil || len(page.Sessions) != 0 || page.TotalCount != 0 {
				t.Fatalf("failed initial Create published discovery state: %+v, %v", page, err)
			}
			if err := creator.Create(ctx, activitySession(t, "session", false)); err != nil {
				t.Fatalf("Create: %v", err)
			}
			page, err = pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
			if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Activity != session.ActivityDraft {
				t.Fatalf("draft page = %+v, %v", page, err)
			}
			if err := creator.Create(ctx, activitySession(t, "session", true)); err == nil {
				t.Fatal("duplicate Create succeeded")
			}
			page, err = pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
			if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Activity != session.ActivityDraft {
				t.Fatalf("failed Create changed activity projection: %+v, %v", page, err)
			}
			// A failed Save must likewise leave both the previous snapshot and
			// its scalar projection untouched.
			if err := st.Save(ctx, nil); err == nil {
				t.Fatal("nil Save succeeded")
			}
			page, err = pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
			if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Activity != session.ActivityDraft {
				t.Fatalf("failed Save changed snapshot/projection: %+v, %v", page, err)
			}
			if err := st.Save(ctx, activitySession(t, "session", true)); err != nil {
				t.Fatalf("Save: %v", err)
			}
			page, err = pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
			if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Activity != session.ActivityActive {
				t.Fatalf("active page = %+v, %v", page, err)
			}
			// Exercise each real backend's bounded page contract, rather than
			// only the shared in-memory pagination helper.
			owner := &session.Principal{Issuer: "issuer", Subject: "owner"}
			owned := activitySession(t, "owned", false)
			owned.Owner = owner
			if err := creator.Create(ctx, owned); err != nil {
				t.Fatalf("owned Create: %v", err)
			}
			foreign := activitySession(t, "foreign", false)
			foreign.Owner = &session.Principal{Issuer: "issuer", Subject: "other"}
			if err := creator.Create(ctx, foreign); err != nil {
				t.Fatalf("foreign Create: %v", err)
			}
			first, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1, OwnershipEnforced: true, Owner: owner})
			if err != nil || len(first.Sessions) != 1 || first.TotalCount != 1 || first.Sessions[0].Owner.Subject != "owner" {
				t.Fatalf("real %s owner/bound page = %+v, %v", tt.name, first, err)
			}
		})
	}
}

type metadataPagerOnlyStore struct {
	port.SessionStore
	port.SessionMetadataPager
}

func TestSessionStoreCapabilitiesRequiresProvenActivityProjection(t *testing.T) {
	server := &sessionStoreServer{store: metadataPagerOnlyStore{}}
	caps, err := server.Capabilities(context.Background(), &driverv1.SessionStoreCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.GetContract() != sessionStoreContract {
		t.Fatalf("contract = %q, want %q", caps.GetContract(), sessionStoreContract)
	}
	if !caps.GetMetadataPaging() {
		t.Fatal("metadata paging capability = false, want true")
	}
	if caps.GetActivityProjection() {
		t.Fatal("activity projection capability = true for an unproved metadata pager")
	}
}
func TestDraftAwareSessionInventory_Scenario2_DriverActivityCapabilityAndOpaqueRoundTrip(t *testing.T) {
	var capture *activityCaptureServer
	conn := dialBufconn(t, func(gs *grpc.Server) {
		capture = &activityCaptureServer{delegate: NewSessionStoreServer(memstore.New())}
		driverv1.RegisterSessionStoreServiceServer(gs, capture)
	})
	st := mustNewSessionStore(t, conn)
	if !st.activityProjection {
		t.Fatal("supporting driver did not negotiate activity projection")
	}
	if err := st.Create(context.Background(), activitySession(t, "draft", false)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(capture.creates) != 1 || capture.creates[0].GetActivityState() != string(session.ActivityDraft) || len(capture.creates[0].GetSnapshot().GetPayload()) == 0 {
		t.Fatalf("Create did not carry scalar with opaque payload: %+v", capture.creates)
	}
	page, err := st.PageSessionMetadata(context.Background(), port.SessionMetadataPageRequest{Limit: 1})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Activity != session.ActivityDraft {
		t.Fatalf("PageMetadata = %+v, %v", page, err)
	}

	// Activity projection remains a genuinely optional backend operation under
	// the mandatory current contract. Without it the client omits the scalar and
	// normalizes any discovery row to unknown.
	var noProjection *activityNoProjectionCaptureServer
	noProjectionConn := dialBufconn(t, func(gs *grpc.Server) {
		noProjection = &activityNoProjectionCaptureServer{activityCaptureServer: &activityCaptureServer{delegate: NewSessionStoreServer(memstore.New())}}
		driverv1.RegisterSessionStoreServiceServer(gs, noProjection)
	})
	noProjectionStore := mustNewSessionStore(t, noProjectionConn)
	if noProjectionStore.activityProjection {
		t.Fatal("driver without projection capability negotiated activity projection")
	}
	if err := noProjectionStore.Save(context.Background(), activitySession(t, "current", true)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(noProjection.saves) != 1 || noProjection.saves[0].GetActivityState() != "" {
		t.Fatalf("Save sent unsupported activity scalar: %+v", noProjection.saves)
	}
	row := metadataFromProto(&driverv1.SessionMetadataEntry{SessionId: "current", ActivityState: string(session.ActivityActive)}, false)
	if row.Activity != session.ActivityUnknown {
		t.Fatalf("unsupported driver activity = %q, want unknown", row.Activity)
	}
}

func TestDraftAwareSessionInventory_Scenario2_LegacyProjectionFailsClosed(t *testing.T) {
	row := metadataFromProto(&driverv1.SessionMetadataEntry{SessionId: "legacy"}, true)
	if row.Activity != session.ActivityUnknown {
		t.Fatalf("legacy activity = %q, want unknown", row.Activity)
	}
	row = metadataFromProto(&driverv1.SessionMetadataEntry{SessionId: "corrupt", ActivityState: "not-an-activity"}, true)
	if row.Activity != session.ActivityUnknown {
		t.Fatalf("corrupt activity = %q, want unknown", row.Activity)
	}
}

func TestInvariant_session_metadata_activity_projection_preserves_paging_contract(t *testing.T) {
	rows := []port.SessionDiscoveryMeta{
		{ID: "new", ModifiedAt: time.Unix(2, 0), Activity: session.ActivityDraft},
		{ID: "old", ModifiedAt: time.Unix(1, 0), Activity: session.ActivityActive},
	}
	first, err := port.PaginateSessionMetadataBound(rows, port.SessionMetadataPageRequest{Limit: 1}, "generation")
	if err != nil || len(first.Sessions) != 1 || first.Sessions[0].ID != "new" || first.TotalCount != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := port.PaginateSessionMetadataBound(rows, port.SessionMetadataPageRequest{Limit: 1, Cursor: first.NextCursor}, "generation")
	if err != nil || len(second.Sessions) != 1 || second.Sessions[0].ID != "old" || second.TotalCount != 2 {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}
