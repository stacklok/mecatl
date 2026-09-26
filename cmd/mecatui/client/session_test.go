package client

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestSnapshotFromUsesSessionMediaCapabilities(t *testing.T) {
	textOnly := snapshotFrom(&mecatlv1.Session{
		SessionCapabilities: &mecatlv1.SessionCapabilities{},
	})
	if textOnly.Capabilities.Image || !textOnly.Capabilities.SessionMediaPresent {
		t.Fatalf("text-only snapshot capabilities = %+v", textOnly.Capabilities)
	}
	withoutMedia := snapshotFrom(&mecatlv1.Session{})
	if withoutMedia.Capabilities.SessionMediaPresent {
		t.Fatalf("absent session media unexpectedly marked present: %+v", withoutMedia.Capabilities)
	}
}

type sessionCapabilitiesServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	global       *mecatlv1.ServerCapabilities
	globalErr    error
	sessionMedia *mecatlv1.SessionCapabilities
}

func (s *sessionCapabilitiesServer) GetCompatibilityInfo(context.Context, *mecatlv1.GetCompatibilityInfoRequest) (*mecatlv1.GetCompatibilityInfoResponse, error) {
	if s.globalErr != nil {
		return nil, s.globalErr
	}
	return &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: 1, Capabilities: s.global}, nil
}

func (s *sessionCapabilitiesServer) GetSession(context.Context, *mecatlv1.GetSessionRequest) (*mecatlv1.GetSessionResponse, error) {
	return &mecatlv1.GetSessionResponse{Session: &mecatlv1.Session{SessionId: "resume-session", SessionCapabilities: s.sessionMedia}}, nil
}

func newSessionCapabilitiesClient(t *testing.T, server *sessionCapabilitiesServer) *Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///session-capabilities", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Client{svc: mecatlv1.NewHarnessServiceClient(conn)}
}

func TestGetSessionCapabilitiesOverlaysSessionMediaOnGlobalAdvertisement(t *testing.T) {
	cl := newSessionCapabilitiesClient(t, &sessionCapabilitiesServer{
		global:       &mecatlv1.ServerCapabilities{Steer: true, Teams: true, Image: true, Audio: true},
		sessionMedia: &mecatlv1.SessionCapabilities{Audio: true},
	})

	snapshot, err := cl.GetSession(t.Context(), "resume-session")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Capabilities.Steer || !snapshot.Capabilities.Teams || snapshot.Capabilities.Image || !snapshot.Capabilities.Audio || !snapshot.Capabilities.SessionMediaPresent {
		t.Fatalf("resume capabilities = %+v", snapshot.Capabilities)
	}
}

func TestGetSessionCapabilitiesFallsBackWhenCompatibilityInfoIsUnavailable(t *testing.T) {
	cl := newSessionCapabilitiesClient(t, &sessionCapabilitiesServer{
		globalErr:    status.Error(codes.Unimplemented, "older server"),
		sessionMedia: &mecatlv1.SessionCapabilities{Image: true},
	})

	snapshot, err := cl.GetSession(t.Context(), "resume-session")
	if err != nil {
		t.Fatalf("GetSession returned compatibility error: %v", err)
	}
	if !snapshot.Capabilities.Image || snapshot.Capabilities.Audio || !snapshot.Capabilities.SessionMediaPresent || snapshot.Capabilities.Steer || snapshot.Capabilities.Teams {
		t.Fatalf("older-server resume capabilities = %+v", snapshot.Capabilities)
	}
}

// TestSnapshotFromReadsState asserts snapshotFrom projects the proto Session's
// State field (issue #245 Phase 1) — the picker row needs it to render an
// "open existing session" affordance. Covers the populated and nil cases.
func TestSnapshotFromReadsState(t *testing.T) {
	snap := snapshotFrom(&mecatlv1.Session{State: "completed"})
	if snap.State != "completed" {
		t.Fatalf("State = %q, want %q", snap.State, "completed")
	}

	// nil session degrades to the default-mode snapshot with an empty State.
	nilSnap := snapshotFrom(nil)
	if nilSnap.State != "" {
		t.Fatalf("nil State = %q, want empty", nilSnap.State)
	}
	if nilSnap.Mode != ModeDefaultString {
		t.Fatalf("nil Mode = %q, want %q", nilSnap.Mode, ModeDefaultString)
	}
}

// TestSnapshotFromReadsTitle asserts snapshotFrom projects the proto Session's
// Title field — the footer/heal surface may surface it. Covers the populated,
// empty, and nil cases (nil-safe via the getter).
func TestSnapshotFromReadsTitle(t *testing.T) {
	snap := snapshotFrom(&mecatlv1.Session{TitleMetadata: &mecatlv1.SessionTitle{Title: "Fix the CI", Provenance: "generated", Revision: 7}})
	if snap.Title != "Fix the CI" || snap.TitleProvenance != "generated" || snap.TitleRevision != 7 {
		t.Fatalf("title/provenance/revision = %q/%q/%d, want Fix the CI/generated/7", snap.Title, snap.TitleProvenance, snap.TitleRevision)
	}
	empty := snapshotFrom(&mecatlv1.Session{})
	if empty.Title != "" {
		t.Fatalf("empty Title = %q, want empty", empty.Title)
	}
	nilSnap := snapshotFrom(nil)
	if nilSnap.Title != "" {
		t.Fatalf("nil Title = %q, want empty", nilSnap.Title)
	}
}

func TestSnapshotFromProjectsMainUsageAndOptionalContextOccupancy(t *testing.T) {
	snap := snapshotFrom(&mecatlv1.Session{
		TokenUsage: map[string]*mecatlv1.TokenUsage{
			"main": {Total: &mecatlv1.Usage{InputTokens: 120_000, OutputTokens: 4_000, CacheReadTokens: 90_000}},
		},
		LatestContextOccupancy: &mecatlv1.ContextOccupancy{InputTokens: 40_000, Estimated: true},
	})
	if snap.Usage != (Usage{InputTokens: 120_000, OutputTokens: 4_000, CacheReadTokens: 90_000}) {
		t.Fatalf("main usage = %+v", snap.Usage)
	}
	if snap.ContextOccupancy == nil || *snap.ContextOccupancy != (ContextOccupancy{InputTokens: 40_000, Estimated: true}) {
		t.Fatalf("context occupancy = %+v", snap.ContextOccupancy)
	}
	legacy := snapshotFrom(&mecatlv1.Session{TokenUsage: map[string]*mecatlv1.TokenUsage{"main": {Total: &mecatlv1.Usage{InputTokens: 120_000}}}})
	if legacy.ContextOccupancy != nil || legacy.Usage.InputTokens != 120_000 {
		t.Fatalf("legacy snapshot = %+v", legacy)
	}
}
