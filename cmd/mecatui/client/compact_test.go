package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type fakeCompactClient struct {
	mecatlv1.HarnessServiceClient
	resp *mecatlv1.CompactSessionResponse
	err  error
	req  *mecatlv1.CompactSessionRequest
}

func (f *fakeCompactClient) CompactSession(_ context.Context, req *mecatlv1.CompactSessionRequest, _ ...grpc.CallOption) (*mecatlv1.CompactSessionResponse, error) {
	f.req = req
	return f.resp, f.err
}

func TestCompactSessionAndCommand(t *testing.T) {
	fake := &fakeCompactClient{resp: &mecatlv1.CompactSessionResponse{Compacted: true}}
	cl := &Client{svc: fake}
	msg, ok := CompactSessionCmd(context.Background(), cl, "session-1", 7)().(SessionCompactedMsg)
	if !ok || msg.Err != nil || !msg.Compacted || msg.SessionID != "session-1" || msg.RequestToken != 7 {
		t.Fatalf("message = %#v", msg)
	}
	if fake.req.GetSessionId() != "session-1" {
		t.Fatalf("request session_id = %q", fake.req.GetSessionId())
	}

	fake.err = errors.New("unavailable")
	msg = CompactSessionCmd(context.Background(), cl, "session-2", 8)().(SessionCompactedMsg)
	if msg.Err == nil || msg.Compacted {
		t.Fatalf("error message = %#v", msg)
	}
}
