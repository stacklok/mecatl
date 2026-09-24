package sessnap

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestUserPromptProvenanceRoundTripAndLegacyFailClosed(t *testing.T) {
	s := session.New("provenance", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
	if err := s.RecordPrincipalPromptWithParts("trusted", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUserPrompt("legacy unknown", nil); err != nil {
		t.Fatal(err)
	}
	wire, err := Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	messages := got.Conversation.Messages
	if messages[0].UserPromptProvenance != session.UserPromptProvenancePrincipal {
		t.Fatalf("principal provenance = %q", messages[0].UserPromptProvenance)
	}
	if messages[1].UserPromptProvenance != session.UserPromptProvenanceUnknown {
		t.Fatalf("legacy provenance = %q, want unknown", messages[1].UserPromptProvenance)
	}
}
