package session

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestMigrationHoldRejectsLifecycleAndPromptMutations(t *testing.T) {
	for _, state := range []State{StateAwaiting, StateAuthorizing} {
		t.Run(string(state), func(t *testing.T) {
			s := newTestSession(Limits{})
			s.Conversation.Append(NewUserMessage("inspectable"))
			payload := []byte("private legacy continuation bytes")
			mustOK(t, s.RestoreIdentityMigrationHold(state, IdentityMigrationHold{Reason: "legacy-continuation-unproven", Payload: payload}))
			before := CloneMessages(s.Conversation.Messages)
			for name, mutate := range map[string]func() error{
				"begin":                   s.BeginTurn,
				"resume":                  func() error { _, err := s.ResumeWith(); return err },
				"claim":                   func() error { _, err := s.ClaimAuthorization(); return err },
				"interrupt authorization": func() error { _, err := s.InterruptAuthorization(); return err },
				"abort authorization":     func() error { _, err := s.AbortAuthorization("failed"); return err },
				"prompt":                  func() error { return s.RecordUserPrompt("must not append", nil) },
				"complete":                s.Complete,
				"stop":                    func() error { return s.Stop(StopError) },
				"cancel":                  s.Cancel,
				"fail":                    s.Fail,
				"reopen":                  s.Reopen,
				"recover":                 s.Recover,
				"interrupt":               s.Interrupt,
				"abandon":                 s.Abandon,
				"retry":                   s.PrepareFailedStepRetry,
				"mode":                    func() error { return s.SetMode(ModeAccept) },
			} {
				t.Run(name, func(t *testing.T) {
					if err := mutate(); !errors.Is(err, ErrMigrationHeld) {
						t.Fatalf("mutation = %v", err)
					}
					if s.State != state || !reflect.DeepEqual(before, s.Conversation.Messages) || !bytes.Equal(s.IdentityHoldSnapshot().Payload, payload) {
						t.Fatal("hold changed")
					}
				})
			}
			if _, ok := s.PendingAsk(); ok {
				t.Fatal("held ask became runnable")
			}
			if _, ok := s.PendingAuthorization(); ok {
				t.Fatal("held authorization became runnable")
			}
			mustOK(t, s.ValidateIdentityPersistence())
		})
	}
}

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
