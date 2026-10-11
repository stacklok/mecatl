package session_test

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestRecordToolResultsRejectsMismatchedOccurrenceAtomically(t *testing.T) {
	s := session.New("pairing", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	first := session.NewToolCall("reused", "Read", nil)
	first.InvocationID = "inv-first"
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{first})); err != nil {
		t.Fatal(err)
	}
	result := session.NewToolResult(first.ID, "ok")
	if err := s.RecordToolResults([]session.ToolResult{result}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.InvocationID = "inv-second"
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{second})); err != nil {
		t.Fatal(err)
	}
	before := len(s.Conversation.Messages)
	counters := s.Counters
	wrong := session.NewToolResult(second.ID, "wrong")
	wrong.InvocationID = first.InvocationID
	if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult("unknown", "legacy"), wrong}); err == nil {
		t.Fatal("accepted mismatched explicit invocation ID")
	}
	if len(s.Conversation.Messages) != before || s.Counters != counters {
		t.Fatal("rejected batch changed history or counters")
	}
	if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult(second.ID, "ok")}); err != nil {
		t.Fatal(err)
	}
	if got := s.Conversation.Messages[before].ToolResult.InvocationID; got != second.InvocationID {
		t.Fatalf("result occurrence = %q, want %q", got, second.InvocationID)
	}
	if err := s.RestoreInvocationIDs(nil, nil); err != nil {
		t.Fatalf("recorded history cannot reload: %v", err)
	}
}

func TestLegacyRepeatedProviderPairingIdentifiersAreSeparateOccurrences(t *testing.T) {
	s := session.New("legacy", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	c := session.NewToolCall("reused", "Read", []byte(`{}`))
	for i := 0; i < 2; i++ {
		if err := s.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{c})); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult(c.ID, "ok")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RestoreInvocationIDs(nil, nil); err != nil {
		t.Fatal(err)
	}
	messages := s.Conversation.Messages
	a, b := messages[0].ToolCalls[0].InvocationID, messages[2].ToolCalls[0].InvocationID
	if a == "" || b == "" || a == b || messages[1].ToolResult.InvocationID != a || messages[3].ToolResult.InvocationID != b {
		t.Fatalf("legacy pair identity: %q %q results %q %q", a, b, messages[1].ToolResult.InvocationID, messages[3].ToolResult.InvocationID)
	}
	if err := s.ValidateIncomingCalls([]session.ToolCall{c}); err != nil {
		t.Fatalf("completed provider ID reuse: %v", err)
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{c})); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateIncomingCalls([]session.ToolCall{c}); err == nil {
		t.Fatal("accepted outstanding duplicate")
	}
	if err := s.ValidateIncomingCalls([]session.ToolCall{session.NewToolCall("new", "Read", nil), session.NewToolCall("new", "Read", nil)}); err == nil {
		t.Fatal("accepted within-turn duplicate")
	}
}
