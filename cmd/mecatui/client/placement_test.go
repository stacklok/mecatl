package client

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type fakePlacementClient struct {
	mecatlv1.HarnessServiceClient
	clearReq *mecatlv1.ClearSessionRequest
}

func (f *fakePlacementClient) ClearSession(_ context.Context, req *mecatlv1.ClearSessionRequest, _ ...grpc.CallOption) (*mecatlv1.ClearSessionResponse, error) {
	f.clearReq = req
	return &mecatlv1.ClearSessionResponse{SessionId: "successor"}, nil
}

func (*fakePlacementClient) GetSession(context.Context, *mecatlv1.GetSessionRequest, ...grpc.CallOption) (*mecatlv1.GetSessionResponse, error) {
	return &mecatlv1.GetSessionResponse{Session: &mecatlv1.Session{
		State: "idle", Placement: &mecatlv1.PlacementMetadata{Kind: "git", Label: "feature", Branch: "topic", Revision: "abc"},
		ResolvedModel: &mecatlv1.ResolvedModel{ProviderId: "openai", ModelId: "gpt-5", ReasoningEffort: "high"},
	}}, nil
}

func TestClearSessionUsesSourceAndNoSelector(t *testing.T) {
	fake := &fakePlacementClient{}
	cl := newFakeClient(fake)
	id, snapshot, err := cl.ClearSession(t.Context(), "source", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "successor" || fake.clearReq.GetSourceSessionId() != "source" || fake.clearReq.WorktreeSelector != nil {
		t.Fatalf("clear request/result = %q, %+v", id, fake.clearReq)
	}
	if snapshot.Placement != (Placement{Kind: "git", Label: "feature", Branch: "topic", Revision: "abc"}) {
		t.Fatalf("safe placement = %+v", snapshot.Placement)
	}
}

func TestClearSessionSendsOnlyOpaqueWorktreeSelector(t *testing.T) {
	fake := &fakePlacementClient{}
	cl := newFakeClient(fake)
	selector := "opaque-hmac-selector"
	if _, _, err := cl.ClearSession(t.Context(), "source", &selector); err != nil {
		t.Fatal(err)
	}
	if fake.clearReq.GetWorktreeSelector() != selector {
		t.Fatalf("selector = %q", fake.clearReq.GetWorktreeSelector())
	}
}
