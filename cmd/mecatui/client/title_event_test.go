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
	}}).(SessionTitleMsg)
	if !ok {
		t.Fatalf("message = %T, want SessionTitleMsg", msg)
	}
	if msg.Title != "Generated title" || msg.Provenance != "generated" || msg.GenerationState != "generated" || msg.LatestAttempt.ID != "attempt-1" {
		t.Fatalf("title message = %#v", msg)
	}
}
