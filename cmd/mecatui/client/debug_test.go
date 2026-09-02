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
	if service.createCalls != 0 {
		t.Fatalf("server CreateSession calls = %d, want 0", service.createCalls)
	}
}

func TestCreateDebugSessionProjectsBoundNoFSRequest(t *testing.T) {
	fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{SessionDebug: true}, sessions: []*mecatlv1.SessionSummary{{SessionId: "target-123"}}}
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
}

func TestCreateDebugSessionFailsClosedAndCleansUpUnsupportedCreate(t *testing.T) {
	fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{}, sessions: []*mecatlv1.SessionSummary{{SessionId: "target"}}}
	cl := &Client{svc: fake}
	id, _, _, _, err := cl.CreateDebugSession(context.Background(), "target", 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "does not support") || id != "" {
		t.Fatalf("result id=%q err=%v", id, err)
	}
	if fake.closed != "debug-created" {
		t.Fatalf("unsupported server cleanup closed %q", fake.closed)
	}
}

func TestPredictableSessionHandles_Scenario1_OnlyHandleWidthAPI(t *testing.T) {
	if got := SessionHandle("123456789012-rest"); got != "123456789012" {
		t.Fatalf("SessionHandle = %q", got)
	}
	if got := SessionHandle("short"); got != "short" {
		t.Fatalf("SessionHandle short = %q", got)
	}
	if SessionHandleWidth != 12 {
		t.Fatalf("SessionHandleWidth = %d, want 12", SessionHandleWidth)
	}
}

func TestCreateDebugSessionResolution(t *testing.T) {
	const handle = "123456789012"
	tests := []struct {
		name        string
		target      string
		sessions    []*mecatlv1.SessionSummary
		listErr     error
		createErr   error
		wantTarget  string
		wantErr     string
		wantCreates int
		wantLists   int
	}{
		{name: "exact equality precedes projection", target: handle, sessions: []*mecatlv1.SessionSummary{{SessionId: handle + "-projected"}, {SessionId: handle}}, wantTarget: handle, wantCreates: 1, wantLists: 1},
		{name: "unique projection", target: handle, sessions: []*mecatlv1.SessionSummary{{SessionId: handle + "-full"}, {SessionId: "other"}}, wantTarget: handle + "-full", wantCreates: 1, wantLists: 1},
		{name: "duplicate inventory row is one match", target: handle, sessions: []*mecatlv1.SessionSummary{{SessionId: handle + "-full"}, {SessionId: handle + "-full"}}, wantTarget: handle + "-full", wantCreates: 1, wantLists: 1},
		{name: "ambiguous projection", target: handle, sessions: []*mecatlv1.SessionSummary{{SessionId: handle + "-one"}, {SessionId: handle + "-two"}}, wantErr: "full exact session ID", wantLists: 1},
		{name: "no match falls through exact", target: handle, createErr: errors.New("server exact lookup: not found"), wantTarget: handle, wantErr: "server exact lookup: not found", wantCreates: 1, wantLists: 1},
		{name: "inventory failure falls through exact", target: handle, listErr: errors.New("inventory unavailable"), createErr: errors.New("server exact lookup: denied"), wantTarget: handle, wantErr: "server exact lookup: denied", wantCreates: 1, wantLists: 1},
		{name: "long exact bypasses inventory", target: handle + "-full", wantTarget: handle + "-full", wantCreates: 1},
		{name: "non-handle exact bypasses inventory", target: "-leading", wantTarget: "-leading", wantCreates: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{SessionDebug: true}, sessions: tc.sessions, listErr: tc.listErr, createErr: tc.createErr}
			cl := &Client{svc: fake}
			_, resolved, _, _, err := cl.CreateDebugSession(context.Background(), tc.target, 0, ModelSelection{})
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if fake.createCalls != tc.wantCreates || fake.listCalls != tc.wantLists {
				t.Fatalf("calls create=%d list=%d, want create=%d list=%d", fake.createCalls, fake.listCalls, tc.wantCreates, tc.wantLists)
			}
			if tc.wantCreates > 0 {
				if got := fake.request.GetDebugTargetSessionId(); got != tc.wantTarget || resolved != tc.wantTarget {
					t.Fatalf("request target=%q resolved=%q, want %q", got, resolved, tc.wantTarget)
				}
			}
		})
	}
}

func TestPredictableSessionHandles_Scenario2_HandleGrammarAndUnifiedTarget(t *testing.T) {
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
			fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{SessionDebug: true}}
			if tc.candidate {
				fake.sessions = []*mecatlv1.SessionSummary{{SessionId: tc.fullID}}
			}
			cl := &Client{svc: fake}
			_, _, _, _, err := cl.CreateDebugSession(context.Background(), tc.target, 0, ModelSelection{})
			if tc.reject {
				if err == nil || fake.createCalls != 0 || fake.listCalls != 0 {
					t.Fatalf("rejected target error=%v create=%d list=%d", err, fake.createCalls, fake.listCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantLists := 0
			wantTarget := tc.target
			if tc.candidate {
				wantLists = 1
				wantTarget = tc.fullID
			}
			if fake.listCalls != wantLists || fake.request.GetDebugTargetSessionId() != wantTarget {
				t.Fatalf("lists=%d target=%q, want lists=%d target=%q", fake.listCalls, fake.request.GetDebugTargetSessionId(), wantLists, wantTarget)
			}
		})
	}
}
