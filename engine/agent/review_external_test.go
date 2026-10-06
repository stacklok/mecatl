package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
)

type externalReviewDetailSink struct{ got agent.ReviewDetail }

func (s *externalReviewDetailSink) PublishReviewDetail(_ context.Context, detail agent.ReviewDetail) {
	s.got = detail
}

func TestReviewDetailSinkCanBindChildToExportedRoot(t *testing.T) {
	sink := &externalReviewDetailSink{}
	var contract agent.ReviewDetailSink = sink
	contract.PublishReviewDetail(context.Background(), agent.ReviewDetail{
		RootSessionID: "root-session",
		SessionID:     "subagent-child",
		ReviewID:      "review-1",
	})
	if sink.got.RootSessionID != "root-session" || sink.got.SessionID != "subagent-child" {
		t.Fatalf("root/session = %q/%q", sink.got.RootSessionID, sink.got.SessionID)
	}
}
