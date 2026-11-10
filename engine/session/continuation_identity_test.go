package session

import "testing"

func TestResumeWithRequiresExactOutstandingCanonicalCall(t *testing.T) {
	for _, name := range []string{"valid", "missing", "empty", "duplicate", "answered", "wrong-tool"} {
		t.Run(name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mustOK(t, s.BeginTurn())
			calls := []ToolCall{NewToolCall("first", "Write", []byte(`{"path":"first"}`)), NewToolCall("second", "Write", []byte(`{"path":"second"}`))}
			ask := PendingAsk{AskID: "session:ask-control", Call: "second", Tool: "Write"}
			switch name {
			case "missing":
				ask.Call = "absent"
			case "empty":
				ask.Call = ""
			case "wrong-tool":
				ask.Tool = "Shell"
			}
			mustOK(t, s.RecordAssistant(NewAssistantMessage("", calls)))
			if name == "answered" {
				mustOK(t, s.RecordToolResults([]ToolResult{NewToolResult("second", "done")}))
			}
			mustOK(t, s.PauseForApproval(ask))
			if name == "duplicate" {
				// Simulate corrupt restored history, not accepted live ingress.
				s.Conversation.Messages[0].ToolCalls = append(s.Conversation.Messages[0].ToolCalls, calls[1])
			}
			_, err := s.ResumeWith()
			if name == "valid" {
				mustOK(t, err)
				if s.State != StateRunning {
					t.Fatal("valid continuation did not resume")
				}
			} else {
				if err == nil || s.State != StateAwaiting {
					t.Fatal("invalid continuation consumed")
				}
				retained, ok := s.PendingAsk()
				if !ok || retained.Call != ask.Call || retained.AskID != ask.AskID {
					t.Fatal("ask changed")
				}
			}
		})
	}
}

func TestAuthorizationIgnoresProviderItemButRejectsReusedCanonicalOccurrence(t *testing.T) {
	s := authorizationSession(t)
	p := authorizationPending()
	mustOK(t, s.PauseForAuthorization(p))
	claimed, err := s.ClaimAuthorization()
	mustOK(t, err)
	mustOK(t, s.ValidateAuthorizationContinuation(claimed))
	// A completed prior occurrence of the same canonical ID makes the target
	// ambiguous even when the trailing turn and deferred suffix look valid.
	prior := []Message{NewAssistantMessage("", []ToolCall{claimed.Call}), NewToolMessage(NewToolResult(claimed.Call.ID, "prior"))}
	s.Conversation.Messages = append(prior, s.Conversation.Messages...)
	if err := s.ValidateAuthorizationContinuation(claimed); err == nil {
		t.Fatal("reused canonical occurrence accepted")
	}
}
