package client

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type debugHarness struct {
	mecatlv1.HarnessServiceClient
	request     *mecatlv1.CreateSessionRequest
	caps        *mecatlv1.ServerCapabilities
	closed      string
	sessions    []*mecatlv1.SessionSummary
	listErr     error
	createErr   error
	createCalls int
	listCalls   int
}

func (f *debugHarness) CreateSession(_ context.Context, req *mecatlv1.CreateSessionRequest, _ ...grpc.CallOption) (*mecatlv1.CreateSessionResponse, error) {
	f.createCalls++
	f.request = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &mecatlv1.CreateSessionResponse{SessionId: "debug-created", Capabilities: f.caps}, nil
}

func (f *debugHarness) ListSessions(_ context.Context, _ *mecatlv1.ListSessionsRequest, _ ...grpc.CallOption) (*mecatlv1.ListSessionsResponse, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &mecatlv1.ListSessionsResponse{Sessions: f.sessions}, nil
}

func (f *debugHarness) CloseSession(_ context.Context, req *mecatlv1.CloseSessionRequest, _ ...grpc.CallOption) (*mecatlv1.CloseSessionResponse, error) {
	f.closed = req.GetSessionId()
	return &mecatlv1.CloseSessionResponse{}, nil
}

type invalidUTF8DebugServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	createCalls int
}

func (s *invalidUTF8DebugServer) CreateSession(context.Context, *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	s.createCalls++
	return &mecatlv1.CreateSessionResponse{}, nil
}

func TestPredictableSessionHandles_Scenario1_InvalidUTF8HasNoHandleOrDebugCreate(t *testing.T) {
	target := string([]byte{0xff, 'x'})
	if got := SessionHandle(target); got != "" {
		t.Fatalf("SessionHandle(invalid UTF-8) = %q, want empty", got)
	}

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	service := &invalidUTF8DebugServer{}
	mecatlv1.RegisterHarnessServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///invalid-utf8-debug", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cl := &Client{svc: mecatlv1.NewHarnessServiceClient(conn)}
	if _, _, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{}); err == nil {
		t.Fatal("CreateDebugSession accepted invalid UTF-8 target")
	}
	if _, _, _, _, err := cl.CreateDebugSessionExact(context.Background(), target, 0, ModelSelection{}); err == nil {
		t.Fatal("CreateDebugSessionExact accepted invalid UTF-8 target")
	}
	if service.createCalls != 0 {
		t.Fatalf("server CreateSession calls = %d, want 0", service.createCalls)
	}
}

func TestCreateDebugSessionProjectsBoundNoFSRequest(t *testing.T) {
	fake := &debugHarness{
		caps:     &mecatlv1.ServerCapabilities{SessionDebug: true},
		sessions: []*mecatlv1.SessionSummary{{SessionId: "target-123"}},
	}
	cl := &Client{svc: fake}
	id, target, caps, _, err := cl.CreateDebugSession(context.Background(), "target-123", mecatlv1.PermissionMode_PERMISSION_MODE_PLAN, ModelSelection{ProviderID: "p", ModelID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "debug-created" || target != "target-123" || !caps.SessionDebug {
		t.Fatalf("result = %q target=%q %+v", id, target, caps)
	}
	if got := fake.request; got.GetProfile() != "no-fs" || got.GetWorkspace() != "" || got.GetDebugTargetSessionId() != "target-123" {
		t.Fatalf("request = %+v, want no-fs, empty workspace, bound target", got)
	}
	if fake.listCalls != 1 {
		t.Fatalf("short handle listed inventory %d times, want 1", fake.listCalls)
	}
}

func TestCreateDebugSessionFailsClosedAndCleansUpUnsupportedCreate(t *testing.T) {
	fake := &debugHarness{
		caps:     &mecatlv1.ServerCapabilities{},
		sessions: []*mecatlv1.SessionSummary{{SessionId: "target"}},
	}
	cl := &Client{svc: fake}
	id, _, _, _, err := cl.CreateDebugSession(context.Background(), "target", 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "does not support") || id != "" {
		t.Fatalf("result id=%q err=%v", id, err)
	}
	if fake.closed != "debug-created" {
		t.Fatalf("unsupported server cleanup closed %q", fake.closed)
	}
}

func TestSessionHandleUsesSharedWidth(t *testing.T) {
	if got := SessionHandle("123456789012-rest"); got != "123456789012" {
		t.Fatalf("SessionHandle = %q", got)
	}
	if got := SessionHandle("short"); got != "short" {
		t.Fatalf("SessionHandle short = %q", got)
	}
}

func TestCreateDebugSessionResolvesUniqueHeaderID(t *testing.T) {
	fake := &debugHarness{
		caps: &mecatlv1.ServerCapabilities{SessionDebug: true},
		sessions: []*mecatlv1.SessionSummary{
			{SessionId: "123456789012-full-target"},
			{SessionId: "unrelated-session"},
		},
	}
	cl := &Client{svc: fake}
	_, resolvedTarget, _, _, err := cl.CreateDebugSession(context.Background(), "123456789012", 0, ModelSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedTarget != "123456789012-full-target" {
		t.Fatalf("resolved target = %q", resolvedTarget)
	}
	if got := fake.request.GetDebugTargetSessionId(); got != "123456789012-full-target" {
		t.Fatalf("debug target = %q", got)
	}
}

func TestCreateDebugSessionExactBypassesProjection(t *testing.T) {
	for _, target := range []string{"123456789012", "short", "-leading", "legacy\x1b-id"} {
		t.Run(target, func(t *testing.T) {
			fake := &debugHarness{
				caps:    &mecatlv1.ServerCapabilities{SessionDebug: true},
				listErr: errors.New("exact path must not list inventory"),
			}
			cl := &Client{svc: fake}
			if _, resolved, _, _, err := cl.CreateDebugSessionExact(context.Background(), target, 0, ModelSelection{}); err != nil {
				t.Fatal(err)
			} else if resolved != target {
				t.Fatalf("resolved target = %q, want %q", resolved, target)
			}
			if got := fake.request.GetDebugTargetSessionId(); got != target || fake.listCalls != 0 {
				t.Fatalf("debug target = %q, list calls = %d", got, fake.listCalls)
			}
		})
	}
}

func TestCreateDebugSessionRejectsAmbiguousHeaderIDBeforeCreate(t *testing.T) {
	const target = "123456789012"
	fake := &debugHarness{sessions: []*mecatlv1.SessionSummary{
		{SessionId: target + "-one"},
		{SessionId: target + "-two"},
	}}
	cl := &Client{svc: fake}
	_, _, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "handle") || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "/session") {
		t.Fatalf("error = %v", err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("CreateSession calls = %d", fake.createCalls)
	}
}

func TestCreateDebugSessionMissingHandleFailsBeforeCreate(t *testing.T) {
	const target = "123456789012"
	fake := &debugHarness{}
	cl := &Client{svc: fake}
	_, _, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "did not match") || !strings.Contains(err.Error(), "/session") {
		t.Fatalf("error = %v, want exact-copy guidance", err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("CreateSession calls = %d, want 0", fake.createCalls)
	}
}

func TestPredictableSessionHandles_Scenario1_InventoryFailureKeepsExplicitExactFallback(t *testing.T) {
	fake := &debugHarness{listErr: errors.New("inventory unavailable"), caps: &mecatlv1.ServerCapabilities{SessionDebug: true}}
	cl := &Client{svc: fake}
	_, _, _, _, err := cl.CreateDebugSession(context.Background(), "123456789012", 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "list sessions") || !strings.Contains(err.Error(), "inventory unavailable") || !strings.Contains(err.Error(), "/session") || !strings.Contains(err.Error(), "--exact") {
		t.Fatalf("error = %v", err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("CreateSession calls = %d", fake.createCalls)
	}
	if _, target, _, _, exactErr := cl.CreateDebugSessionExact(context.Background(), "123456789012", 0, ModelSelection{}); exactErr != nil || target != "123456789012" {
		t.Fatalf("CreateDebugSessionExact target=%q err=%v", target, exactErr)
	}
	if fake.listCalls != 1 || fake.request.GetDebugTargetSessionId() != "123456789012" {
		t.Fatalf("exact fallback listed %d times and sent target %q", fake.listCalls, fake.request.GetDebugTargetSessionId())
	}
}

func TestPredictableSessionHandles_Scenario1_OnlyHandleWidthAPI(t *testing.T) {
	if SessionHandleWidth != 12 {
		t.Fatalf("SessionHandleWidth = %d, want 12", SessionHandleWidth)
	}
}

func TestPredictableSessionHandles_Scenario2_HandleGrammarAndExactEscapeHatch(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		fullID    string
		candidate bool
		reject    bool
	}{
		{name: "empty", target: "", reject: true},
		{name: "one safe atom", target: "a", fullID: "a", candidate: true},
		{name: "exactly twelve safe", target: "abcdefghijkl", fullID: "abcdefghijkl", candidate: true},
		{name: "uppercase escape exactly fits", target: "123456789%2F", fullID: "123456789/", candidate: true},
		{name: "uppercase escapes", target: "%C3%A9", fullID: "é", candidate: true},
		{name: "escape cannot fit", target: "1234567890%2F"},
		{name: "too long", target: "abcdefghijklm"},
		{name: "lowercase escape", target: "%2f"},
		{name: "truncated escape", target: "%2"},
		{name: "malformed escape", target: "%GG"},
		{name: "leading hyphen", target: "-legacy"},
		{name: "literal percent", target: "abc%def"},
		{name: "other ascii", target: "abc/def"},
		{name: "multibyte", target: "é"},
		{name: "invalid utf8", target: string([]byte{0xff}), reject: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{SessionDebug: true}, listErr: errors.New("inventory must be bypassed")}
			if tc.candidate {
				fake.listErr = nil
				fake.sessions = []*mecatlv1.SessionSummary{{SessionId: tc.fullID}}
			}
			cl := &Client{svc: fake}
			_, _, _, _, err := cl.CreateDebugSession(context.Background(), tc.target, 0, ModelSelection{})
			if tc.reject {
				if err == nil || fake.createCalls != 0 || fake.listCalls != 0 {
					t.Fatalf("rejected target error=%v create calls=%d list calls=%d", err, fake.createCalls, fake.listCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateDebugSession: %v", err)
			}
			wantLists := 0
			if tc.candidate {
				wantLists = 1
			}
			if fake.listCalls != wantLists {
				t.Fatalf("ListSessions calls = %d, want %d", fake.listCalls, wantLists)
			}
			wantTarget := tc.target
			if tc.candidate {
				wantTarget = tc.fullID
			}
			if got := fake.request.GetDebugTargetSessionId(); got != wantTarget {
				t.Fatalf("request target = %q, want %q", got, wantTarget)
			}
		})
	}

	fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{SessionDebug: true}, listErr: errors.New("exact must bypass inventory")}
	cl := &Client{svc: fake}
	if _, target, _, _, err := cl.CreateDebugSessionExact(context.Background(), "short", 0, ModelSelection{}); err != nil || target != "short" {
		t.Fatalf("CreateDebugSessionExact target=%q err=%v", target, err)
	}
	if fake.listCalls != 0 || fake.request.GetDebugTargetSessionId() != "short" {
		t.Fatalf("exact path listed %d times and sent %q", fake.listCalls, fake.request.GetDebugTargetSessionId())
	}
}

func TestPredictableSessionHandles_Scenario2_AllProjectedMatchesPrecedeSelection(t *testing.T) {
	const target = "same-prefix-"
	fake := &debugHarness{
		caps: &mecatlv1.ServerCapabilities{SessionDebug: true},
		sessions: []*mecatlv1.SessionSummary{
			{SessionId: target + "long"},
			{SessionId: target},
			{SessionId: target + "long"},
		},
	}
	cl := &Client{svc: fake}
	_, _, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "--exact") {
		t.Fatalf("error = %v, want projected-match ambiguity with exact fallback", err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("CreateSession calls = %d, want 0", fake.createCalls)
	}
}

func TestPredictableSessionHandles_Scenario2_HandleAndExplicitExactPaths(t *testing.T) {
	const fullID = "legacy\x1b-session"
	handle := SessionHandle(fullID)
	fake := &debugHarness{
		caps: &mecatlv1.ServerCapabilities{SessionDebug: true},
		sessions: []*mecatlv1.SessionSummary{
			{SessionId: fullID},
			{SessionId: fullID},
		},
	}
	cl := &Client{svc: fake}
	_, resolved, _, _, err := cl.CreateDebugSession(context.Background(), handle, 0, ModelSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != fullID || fake.request.GetDebugTargetSessionId() != fullID {
		t.Fatalf("resolved=%q request target=%q, want %q", resolved, fake.request.GetDebugTargetSessionId(), fullID)
	}

	const shortExact = "short"
	if _, resolved, _, _, err = cl.CreateDebugSessionExact(context.Background(), shortExact, 0, ModelSelection{}); err != nil {
		t.Fatal(err)
	}
	if resolved != shortExact || fake.request.GetDebugTargetSessionId() != shortExact || fake.listCalls != 1 {
		t.Fatalf("exact resolved=%q request target=%q list calls=%d", resolved, fake.request.GetDebugTargetSessionId(), fake.listCalls)
	}
}

func TestPredictableSessionHandles_Scenario2_FailClosedWithExactGuidance(t *testing.T) {
	const target = "same-prefix-"
	tests := []struct {
		name     string
		sessions []*mecatlv1.SessionSummary
		listErr  error
	}{
		{name: "zero matches"},
		{name: "ambiguous", sessions: []*mecatlv1.SessionSummary{{SessionId: target + "one"}, {SessionId: target + "two"}}},
		{name: "inventory error", listErr: errors.New("inventory unavailable")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &debugHarness{sessions: tc.sessions, listErr: tc.listErr}
			cl := &Client{svc: fake}
			_, _, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{})
			if err == nil || !strings.Contains(err.Error(), "/session") || !strings.Contains(err.Error(), "--exact") {
				t.Fatalf("error = %v, want concrete /session exact-copy guidance", err)
			}
			if fake.createCalls != 0 {
				t.Fatalf("CreateSession calls = %d, want 0", fake.createCalls)
			}
		})
	}
}

func TestCreateDebugSessionLongFullIDBypassesInventory(t *testing.T) {
	const target = "123456789012-full-target"
	fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{SessionDebug: true}, listErr: errors.New("must not list")}
	cl := &Client{svc: fake}
	if _, _, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{}); err != nil {
		t.Fatal(err)
	}
	if fake.listCalls != 0 || fake.request.GetDebugTargetSessionId() != target {
		t.Fatalf("list calls=%d target=%q", fake.listCalls, fake.request.GetDebugTargetSessionId())
	}
}
