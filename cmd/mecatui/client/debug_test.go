package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

func TestCreateDebugSessionProjectsBoundNoFSRequest(t *testing.T) {
	fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{SessionDebug: true}}
	cl := &Client{svc: fake}
	id, caps, _, err := cl.CreateDebugSession(context.Background(), "target-123", mecatlv1.PermissionMode_PERMISSION_MODE_PLAN, ModelSelection{ProviderID: "p", ModelID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "debug-created" || !caps.SessionDebug {
		t.Fatalf("result = %q %+v", id, caps)
	}
	if got := fake.request; got.GetProfile() != "no-fs" || got.GetWorkspace() != "" || got.GetDebugTargetSessionId() != "target-123" {
		t.Fatalf("request = %+v, want no-fs, empty workspace, bound target", got)
	}
	if fake.listCalls != 0 {
		t.Fatalf("non-header-width full ID listed inventory %d times", fake.listCalls)
	}
}

func TestCreateDebugSessionFailsClosedAndCleansUpUnsupportedCreate(t *testing.T) {
	fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{}}
	cl := &Client{svc: fake}
	id, _, _, err := cl.CreateDebugSession(context.Background(), "target", 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "does not support") || id != "" {
		t.Fatalf("result id=%q err=%v", id, err)
	}
	if fake.closed != "debug-created" {
		t.Fatalf("unsupported server cleanup closed %q", fake.closed)
	}
}

func TestDisplaySessionIDUsesSharedWidth(t *testing.T) {
	if got := DisplaySessionID("123456789012-rest"); got != "123456789012" {
		t.Fatalf("DisplaySessionID = %q", got)
	}
	if got := DisplaySessionID("short"); got != "short" {
		t.Fatalf("DisplaySessionID short = %q", got)
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
	if _, _, _, err := cl.CreateDebugSession(context.Background(), "123456789012", 0, ModelSelection{}); err != nil {
		t.Fatal(err)
	}
	if got := fake.request.GetDebugTargetSessionId(); got != "123456789012-full-target" {
		t.Fatalf("debug target = %q", got)
	}
}

func TestCreateDebugSessionExactHeaderWidthIDPrecedesPrefix(t *testing.T) {
	const target = "123456789012"
	fake := &debugHarness{
		caps: &mecatlv1.ServerCapabilities{SessionDebug: true},
		sessions: []*mecatlv1.SessionSummary{
			{SessionId: target + "-longer"},
			{SessionId: target},
		},
	}
	cl := &Client{svc: fake}
	if _, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{}); err != nil {
		t.Fatal(err)
	}
	if got := fake.request.GetDebugTargetSessionId(); got != target {
		t.Fatalf("debug target = %q", got)
	}
}

func TestCreateDebugSessionRejectsAmbiguousHeaderIDBeforeCreate(t *testing.T) {
	const target = "123456789012"
	fake := &debugHarness{sessions: []*mecatlv1.SessionSummary{
		{SessionId: target + "-one"},
		{SessionId: target + "-two"},
	}}
	cl := &Client{svc: fake}
	_, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "prefix") || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "full session ID") {
		t.Fatalf("error = %v", err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("CreateSession calls = %d", fake.createCalls)
	}
}

func TestCreateDebugSessionMissingHeaderIDPreservesServerNotFound(t *testing.T) {
	const target = "123456789012"
	notFound := status.Error(codes.NotFound, "session not found")
	fake := &debugHarness{createErr: notFound}
	cl := &Client{svc: fake}
	_, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("error = %v, want NotFound", err)
	}
	if got := fake.request.GetDebugTargetSessionId(); got != target {
		t.Fatalf("debug target = %q", got)
	}
}

func TestCreateDebugSessionListFailurePreventsCreate(t *testing.T) {
	fake := &debugHarness{listErr: errors.New("inventory unavailable")}
	cl := &Client{svc: fake}
	_, _, _, err := cl.CreateDebugSession(context.Background(), "123456789012", 0, ModelSelection{})
	if err == nil || !strings.Contains(err.Error(), "list sessions") || !strings.Contains(err.Error(), "inventory unavailable") {
		t.Fatalf("error = %v", err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("CreateSession calls = %d", fake.createCalls)
	}
}

func TestCreateDebugSessionLongFullIDBypassesInventory(t *testing.T) {
	const target = "123456789012-full-target"
	fake := &debugHarness{caps: &mecatlv1.ServerCapabilities{SessionDebug: true}, listErr: errors.New("must not list")}
	cl := &Client{svc: fake}
	if _, _, _, err := cl.CreateDebugSession(context.Background(), target, 0, ModelSelection{}); err != nil {
		t.Fatal(err)
	}
	if fake.listCalls != 0 || fake.request.GetDebugTargetSessionId() != target {
		t.Fatalf("list calls=%d target=%q", fake.listCalls, fake.request.GetDebugTargetSessionId())
	}
}
