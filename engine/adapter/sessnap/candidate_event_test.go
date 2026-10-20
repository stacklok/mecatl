package sessnap

import (
	"encoding/json"
	"testing"
)

func TestCandidateEventsourceHoldAbandoned(t *testing.T) {
	data := legacyFixture(t, "candidate-held-event")
	first := restoreFixture(t, data)
	second := restoreFixture(t, data)
	if !sameStoredHistory(t, first.Conversation.Messages, second.Conversation.Messages) {
		t.Fatal("independent reads changed normalization")
	}
	assertAbandonmentCycles(t, first)
}

func TestCandidateEventsourceHoldRejectsMalformedEvent(t *testing.T) {
	for _, event := range []string{`null`, `[]`, `{"Type":"tool.call"}`, `{"Type":"permission.ask","Ask":3}`} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(legacyFixture(t, "candidate-held-event"), &fields); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(struct {
			Version int    `json:"version"`
			State   string `json:"state"`
			Pending []byte `json:"pending"`
			Event   []byte `json:"event"`
		}{1, "awaiting", []byte(`{"AskID":"ask","Call":"old"}`), []byte(event)})
		if err != nil {
			t.Fatal(err)
		}
		fields["identity_hold"], err = json.Marshal(candidateHold{Reason: "legacy-continuation-unproven", Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Unmarshal(data); err == nil {
			t.Fatalf("accepted malformed legacy event %s", event)
		}
	}
}
