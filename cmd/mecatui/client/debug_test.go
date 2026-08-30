package client

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type debugHarness struct {
	mecatlv1.HarnessServiceClient
	request *mecatlv1.CreateSessionRequest
	caps    *mecatlv1.ServerCapabilities
	closed  string
}

func (f *debugHarness) CreateSession(_ context.Context, req *mecatlv1.CreateSessionRequest, _ ...grpc.CallOption) (*mecatlv1.CreateSessionResponse, error) {
	f.request = req
	return &mecatlv1.CreateSessionResponse{SessionId: "debug-created", Capabilities: f.caps}, nil
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
