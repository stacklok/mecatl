package client

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"unicode"

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
	if got := fake.request; got.GetProfile() != "no-fs" || got.GetDebugTargetSessionId() != "target-123" {
		t.Fatalf("request = %+v, want no-fs and bound target", got)
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
	if got := SessionHandle("1234567890雪-rest"); got != "1234567890雪" {
		t.Fatalf("SessionHandle must not split graphemes: %q", got)
	}
	if got := SessionHandle("safe\x1b\n\t\u202e-handle"); got != "safe-handle" {
		t.Fatalf("SessionHandle must remove control and format characters: %q", got)
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
		{name: "ambiguous displayed ID", target: handle, sessions: []*mecatlv1.SessionSummary{{SessionId: handle + "-one"}, {SessionId: handle + "-two"}}, wantErr: "full exact session ID", wantLists: 1},
		{name: "no match falls through exact", target: handle, createErr: errors.New("server exact lookup: not found"), wantTarget: handle, wantErr: "server exact lookup: not found", wantCreates: 1, wantLists: 1},
		{name: "inventory failure falls through exact", target: handle, listErr: errors.New("inventory unavailable"), createErr: errors.New("server exact lookup: denied"), wantTarget: handle, wantErr: "server exact lookup: denied", wantCreates: 1, wantLists: 1},
		{name: "long exact wins", target: handle + "-full", sessions: []*mecatlv1.SessionSummary{{SessionId: handle + "-full"}}, wantTarget: handle + "-full", wantCreates: 1, wantLists: 1},
		{name: "leading hyphen displayed ID resolves", target: "-leading-res", sessions: []*mecatlv1.SessionSummary{{SessionId: "-leading-rest"}}, wantTarget: "-leading-rest", wantCreates: 1, wantLists: 1},
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

func TestCreateDebugSessionResolvesRenderedControlSafeHandle(t *testing.T) {
	const fullID = "visible\x1b\n\t\u202e-handle-rest"
	handle := SessionHandle(fullID)
	if handle != "visible-hand" || strings.ContainsFunc(handle, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		t.Fatalf("SessionHandle(%q) = %q, want a control-safe actionable handle", fullID, handle)
	}

	fake := &debugHarness{
		caps:     &mecatlv1.ServerCapabilities{SessionDebug: true},
		sessions: []*mecatlv1.SessionSummary{{SessionId: fullID}},
	}
	cl := &Client{svc: fake}
	_, resolved, _, _, err := cl.CreateDebugSession(context.Background(), handle, 0, ModelSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if fake.request.GetDebugTargetSessionId() != fullID || resolved != fullID {
		t.Fatalf("rendered handle %q resolved target=%q, returned=%q, want %q", handle, fake.request.GetDebugTargetSessionId(), resolved, fullID)
	}
}

func TestCreateDebugSessionResolvesAnyDisplayedUTF8Handle(t *testing.T) {
	tests := []struct {
		name, target, fullID string
	}{
		{name: "leading hyphen", target: "-legacy-hand", fullID: "-legacy-handle"},
		{name: "percent", target: "%2F-full-id-", fullID: "%2F-full-id-rest"},
		{name: "slash", target: "abc/def-rest", fullID: "abc/def-rest-more"},
		{name: "multibyte", target: "éclair-sessi", fullID: "éclair-session-more"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &debugHarness{
				caps:     &mecatlv1.ServerCapabilities{SessionDebug: true},
				sessions: []*mecatlv1.SessionSummary{{SessionId: tc.fullID}},
			}
			cl := &Client{svc: fake}
			_, resolved, _, _, err := cl.CreateDebugSession(context.Background(), tc.target, 0, ModelSelection{})
			if err != nil {
				t.Fatal(err)
			}
			if fake.listCalls != 1 || fake.request.GetDebugTargetSessionId() != tc.fullID || resolved != tc.fullID {
				t.Fatalf("lists=%d target=%q resolved=%q, want %q", fake.listCalls, fake.request.GetDebugTargetSessionId(), resolved, tc.fullID)
			}
		})
	}
}
