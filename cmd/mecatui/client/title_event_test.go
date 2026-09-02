package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestTitleEventProjectsAuthoritativeMetadata(t *testing.T) {
	t.Parallel()

	msg, ok := EventToMsg(&mecatlv1.Event{Type: "session.title", Title: &mecatlv1.SessionTitle{
		Title: "Generated title", Provenance: "generated", GenerationState: "generated",
		LatestAttempt: &mecatlv1.TitleAttemptSummary{Id: "attempt-1", Outcome: "succeeded", CreatedAtUnix: 42},
		LatestUsage:   &mecatlv1.AuxiliaryUsageSummary{Operation: "session_title", ProviderId: "provider", ModelId: "model", InputTokens: 3, OutputTokens: 2, RecordedAtUnix: 43, Outcome: "succeeded"},
	}}).(SessionTitleMsg)
	if !ok {
		t.Fatalf("message = %T, want SessionTitleMsg", msg)
	}
	if msg.Title != "Generated title" || msg.Provenance != "generated" || msg.GenerationState != "generated" || msg.LatestAttempt.ID != "attempt-1" || msg.LatestUsage.InputTokens != 3 {
		t.Fatalf("title message = %#v", msg)
	}
}
