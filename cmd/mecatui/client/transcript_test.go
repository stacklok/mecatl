package client

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type fakeTranscriptClient struct {
	mecatlv1.HarnessServiceClient
	response *mecatlv1.GetSessionTranscriptResponse
	request  *mecatlv1.GetSessionTranscriptRequest
}

func (f *fakeTranscriptClient) GetSessionTranscript(_ context.Context, req *mecatlv1.GetSessionTranscriptRequest, _ ...grpc.CallOption) (*mecatlv1.GetSessionTranscriptResponse, error) {
	f.request = req
	return f.response, nil
}

func TestGetSessionTranscriptMapsProtoFreeAuthoritativeData(t *testing.T) {
	fake := &fakeTranscriptClient{response: &mecatlv1.GetSessionTranscriptResponse{
		Complete: true,
		Activity: &mecatlv1.ActivityReplayStatus{Available: true},
		Messages: []*mecatlv1.ConversationMessage{{
			Role: "assistant", Text: "visible", Reasoning: "must-not-cross-client-boundary",
			ToolCalls: []*mecatlv1.ToolCall{{Id: "call-1", Name: "Read", Args: `{"path":"x"}`}},
		}},
	}}
	cl := &Client{svc: fake}

	got, err := cl.GetSessionTranscript(context.Background(), "opaque-id")
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if fake.request.GetSessionId() != "opaque-id" {
		t.Fatalf("request id = %q", fake.request.GetSessionId())
	}
	if !got.Complete || !got.Activity.Available || got.Activity.Complete || got.Activity.Authoritative {
		t.Fatalf("status = %+v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Text != "visible" || len(got.Messages[0].ToolCalls) != 1 {
		t.Fatalf("messages = %+v", got.Messages)
	}
	if got.Messages[0].Reasoning != "" || got.Messages[0].ProviderPhase != "" || got.Messages[0].ReasoningItemID != "" {
		t.Fatalf("provider-private state entered transcript client: %+v", got.Messages[0])
	}
}
