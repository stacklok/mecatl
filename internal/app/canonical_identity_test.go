package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// boundCallID selects a fixture's unique provider occurrence from accepted history.
// Public events/results continue to use the canonical ID, never the pairing token.
func boundCallID(t *testing.T, s *session.Session, pairing session.ProviderCallID) session.ToolCallID {
	t.Helper()
	var id session.ToolCallID
	count := 0
	for _, message := range s.Conversation.Messages {
		if message.Replay == nil {
			continue
		}
		for _, binding := range message.Replay.Bindings {
			if binding.PairingID == pairing {
				id = binding.CallID
				count++
			}
		}
	}
	if count != 1 || id == "" {
		t.Fatalf("provider occurrence %q has %d accepted bindings, want exactly one", pairing, count)
	}
	return id
}

func eventCallID(t *testing.T, events []session.Event, name string, occurrence int) session.ToolCallID {
	t.Helper()
	for _, event := range events {
		if event.ToolCall != nil && event.ToolCall.Name == name {
			if occurrence == 0 {
				return event.ToolCall.ID
			}
			occurrence--
		}
	}
	t.Fatalf("no accepted %s call at requested occurrence", name)
	return ""
}
