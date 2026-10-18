package learning_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyReasoningIdentifierRedactedWithoutToolsAfterPruneReload(t *testing.T) {
	source, err := sessnap.Unmarshal([]byte(`{"id":"reasoning-source","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"user","text":"unselected prefix"},{"role":"assistant","text":"visible private-reasoning-id text","reasoning":"opaque-replay","reasoning_item_id":"private-reasoning-id"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	old := learning.Input{Trajectory: learning.Trajectory{SessionID: source.ID, Messages: []session.Message{session.NewUserMessage("unselected prefix"), session.NewAssistantMessage("visible private-reasoning-id text", nil)}}}
	ref, err := learning.MessageEvidenceRef(old, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	for pass := range 3 {
		citation, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID, Messages: source.Conversation.Messages}, nil, source.Incarnation(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(citation.Preview, "private-reasoning-id") || strings.Contains(citation.Preview, "opaque-replay") || !strings.Contains(citation.Preview, "[historical-identifier]") {
			t.Fatalf("pass %d unsafe preview: %s", pass, citation.Preview)
		}
		projection, err := learning.ProjectVerifiedInput(learning.NewInput(learning.Trajectory{SessionID: source.ID, Messages: source.Conversation.Messages}, nil, nil, nil))
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(projection)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"private-reasoning-id", "opaque-replay", "ReasoningItemID", "reasoning_item_id", "Legacy"} {
			if strings.Contains(string(wire), private) {
				t.Fatalf("private provenance entered model projection: %s", wire)
			}
		}
		retained := session.CloneMessages(source.Conversation.Messages[len(source.Conversation.Messages)-1:])
		retained[0].Replay = nil
		if err := source.ReplaceHistoryAtBoundary(retained); err != nil {
			t.Fatal(err)
		}
		encoded, err := sessnap.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		source, err = sessnap.Unmarshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
	}
}
