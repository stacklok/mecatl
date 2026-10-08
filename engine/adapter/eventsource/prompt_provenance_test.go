package eventsource

import (
	"iter"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestFoldUserPromptProvenanceIsExplicitAndLegacyFailClosed(t *testing.T) {
	events := []session.Event{
		{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "principal", Provenance: session.UserPromptProvenancePrincipal}},
		{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "legacy unknown"}},
		{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "harness", Synthetic: true}},
		{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	seq := func(yield func(session.Event, error) bool) {
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}
	folded, err := Fold(SessionMeta{ID: "provenance", Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}, CreatedAt: time.Unix(0, 0)}, iter.Seq2[session.Event, error](seq))
	if err != nil {
		t.Fatal(err)
	}
	messages := folded.Conversation.Messages
	want := []session.UserPromptProvenance{session.UserPromptProvenancePrincipal, session.UserPromptProvenanceUnknown, session.UserPromptProvenanceHarness}
	if len(messages) != len(want) {
		t.Fatalf("messages = %d, want %d", len(messages), len(want))
	}
	for i := range want {
		if messages[i].UserPromptProvenance != want[i] {
			t.Fatalf("message %d provenance = %q, want %q", i, messages[i].UserPromptProvenance, want[i])
		}
	}
}
