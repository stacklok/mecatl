package session

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func newEnrollmentSession(t *testing.T) *Session {
	t.Helper()
	s := New("enrollment-session", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
	if err := s.BindAuthority(Authority{Provenance: "test"}); err != nil {
		t.Fatalf("BindAuthority: %v", err)
	}
	return s
}

func testWorkspaceEnrollment() PendingWorkspaceEnrollment {
	return PendingWorkspaceEnrollment{
		ID:               WorkspaceEnrollmentID("enroll_123"),
		RequiredServices: 3,
		ExpiresAt:        time.Unix(100, 0).UTC(),
	}
}

func TestWorkspaceEnrollmentLifecycleIsIndependent(t *testing.T) {
	s := newEnrollmentSession(t)
	want := testWorkspaceEnrollment()
	input := want
	if err := s.BeginWorkspaceEnrollment(input); err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	input.ID = "input-mutated"
	if s.State != StateIdle {
		t.Fatalf("State = %q, want idle", s.State)
	}
	if _, ok := s.PendingAsk(); ok {
		t.Fatal("PendingAsk unexpectedly changed")
	}
	if _, ok := s.PendingAuthorization(); ok {
		t.Fatal("PendingAuthorization unexpectedly changed")
	}

	got, ok := s.PendingWorkspaceEnrollment()
	if !ok || got != want {
		t.Fatalf("PendingWorkspaceEnrollment = %+v, %v; want %+v, true", got, ok, want)
	}
	got.ID = "mutated"
	again, _ := s.PendingWorkspaceEnrollment()
	if again != want {
		t.Fatalf("accessor mutation changed aggregate: %+v", again)
	}

	if err := s.AbortWorkspaceEnrollment("other"); err == nil {
		t.Fatal("AbortWorkspaceEnrollment accepted mismatched ID")
	}
	if again, ok = s.PendingWorkspaceEnrollment(); !ok || again != want {
		t.Fatalf("mismatched abort changed pending enrollment: %+v, %v", again, ok)
	}
	if err := s.AbortWorkspaceEnrollment(want.ID); err != nil {
		t.Fatalf("AbortWorkspaceEnrollment: %v", err)
	}
	if _, ok := s.PendingWorkspaceEnrollment(); ok {
		t.Fatal("enrollment remains after exact-ID abort")
	}
	if s.State != StateIdle {
		t.Fatalf("abort changed State to %q", s.State)
	}
}

func TestPendingWorkspaceEnrollmentRejectsHistoryAndLifecycleMutation(t *testing.T) {
	operations := []struct {
		name string
		run  func(*Session) error
	}{
		{"RecordUserPromptWithParts", func(s *Session) error { return s.RecordUserPromptWithParts("prompt", nil, nil) }},
		{"RecordAssistant", func(s *Session) error { return s.RecordAssistant(NewAssistantMessage("answer", "", nil)) }},
		{"RecordToolResults", func(s *Session) error { return s.RecordToolResults([]ToolResult{NewToolResult("call", "result")}) }},
		{"BeginTurn", func(s *Session) error { return s.BeginTurn() }},
		{"SeedHistory", func(s *Session) error { return s.SeedHistory([]Message{NewUserMessage("seed")}) }},
		{"ReplaceHistory", func(s *Session) error { return s.ReplaceHistory([]Message{NewUserMessage("replacement")}) }},
		{"ReplaceHistoryAtBoundary", func(s *Session) error { return s.ReplaceHistoryAtBoundary([]Message{NewUserMessage("replacement")}) }},
		{"PauseForApproval", func(s *Session) error { return s.PauseForApproval(PendingAsk{AskID: "ask"}) }},
		{"PauseForAuthorization", func(s *Session) error { return s.PauseForAuthorization(PendingAuthorization{}) }},
		{"Complete", func(s *Session) error { return s.Complete() }},
		{"Stop", func(s *Session) error { return s.Stop(StopBudget) }},
		{"Cancel", func(s *Session) error { return s.Cancel() }},
		{"Fail", func(s *Session) error { return s.Fail() }},
		{"Reopen", func(s *Session) error { return s.Reopen() }},
		{"Interrupt", func(s *Session) error { return s.Interrupt() }},
		{"Recover", func(s *Session) error { return s.Recover() }},
		{"Abandon", func(s *Session) error { return s.Abandon() }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			s := newEnrollmentSession(t)
			pending := testWorkspaceEnrollment()
			mustOK(t, s.BeginWorkspaceEnrollment(pending))
			state, counters := s.State, s.Counters
			if err := operation.run(s); err == nil {
				t.Fatalf("%s succeeded while workspace enrollment was pending", operation.name)
			}
			if s.State != state || s.Counters != counters || len(s.Conversation.Messages) != 0 {
				t.Fatalf("rejected %s mutated aggregate: state=%q counters=%+v messages=%d", operation.name, s.State, s.Counters, len(s.Conversation.Messages))
			}
			if _, ok := s.RecordedStopReason(); ok {
				t.Fatalf("rejected %s recorded a stop reason", operation.name)
			}
			got, ok := s.PendingWorkspaceEnrollment()
			if !ok || got != pending {
				t.Fatalf("rejected %s changed enrollment: %+v, %v", operation.name, got, ok)
			}
			if _, ok := s.PendingAsk(); ok {
				t.Fatalf("rejected %s installed PendingAsk", operation.name)
			}
			if _, ok := s.PendingAuthorization(); ok {
				t.Fatalf("rejected %s installed PendingAuthorization", operation.name)
			}
		})
	}
}

func TestBeginWorkspaceEnrollmentRejectsOtherPendingState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Session)
	}{
		{"permission ask", func(s *Session) { s.pending = &PendingAsk{AskID: "ask"} }},
		{"external authorization", func(s *Session) { s.pendingAuthorization = &PendingAuthorization{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newEnrollmentSession(t)
			tc.setup(s)
			if err := s.BeginWorkspaceEnrollment(testWorkspaceEnrollment()); err == nil {
				t.Fatal("BeginWorkspaceEnrollment accepted other pending state")
			}
			if s.State != StateIdle || len(s.Conversation.Messages) != 0 {
				t.Fatalf("rejected begin mutated aggregate: state=%q messages=%d", s.State, len(s.Conversation.Messages))
			}
			if _, ok := s.PendingWorkspaceEnrollment(); ok {
				t.Fatal("rejected begin installed enrollment")
			}
		})
	}
}

func TestBeginWorkspaceEnrollmentPrerequisites(t *testing.T) {
	pending := testWorkspaceEnrollment()
	tests := []struct {
		name  string
		setup func(*Session)
	}{
		{"unbound authority", func(*Session) {}},
		{"not idle", func(s *Session) { _ = s.BindAuthority(Authority{Provenance: "test"}); _ = s.BeginTurn() }},
		{"conversation not empty", func(s *Session) {
			_ = s.BindAuthority(Authority{Provenance: "test"})
			s.Conversation.Append(Message{Role: RoleUser, Text: "prompt"})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New("s", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
			tc.setup(s)
			state := s.State
			if err := s.BeginWorkspaceEnrollment(pending); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("error = %v, want ErrIllegalTransition", err)
			}
			if s.State != state {
				t.Fatalf("rejected begin changed State: %q -> %q", state, s.State)
			}
			if _, ok := s.PendingWorkspaceEnrollment(); ok {
				t.Fatal("rejected begin installed enrollment")
			}
		})
	}
}

func TestBeginWorkspaceEnrollmentRejectsMalformedAndReplacement(t *testing.T) {
	valid := testWorkspaceEnrollment()
	tests := []struct {
		name   string
		mutate func(*PendingWorkspaceEnrollment)
	}{
		{"empty ID", func(p *PendingWorkspaceEnrollment) { p.ID = "" }},
		{"whitespace ID", func(p *PendingWorkspaceEnrollment) { p.ID = "bad id" }},
		{"NUL ID", func(p *PendingWorkspaceEnrollment) { p.ID = "bad\x00id" }},
		{"invalid UTF-8 ID", func(p *PendingWorkspaceEnrollment) { p.ID = WorkspaceEnrollmentID(string([]byte{0xff})) }},
		{"oversized ID", func(p *PendingWorkspaceEnrollment) {
			p.ID = WorkspaceEnrollmentID(strings.Repeat("x", maxWorkspaceEnrollmentIDBytes+1))
		}},
		{"no services", func(p *PendingWorkspaceEnrollment) { p.RequiredServices = 0 }},
		{"zero expiry", func(p *PendingWorkspaceEnrollment) { p.ExpiresAt = time.Time{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newEnrollmentSession(t)
			pending := valid
			tc.mutate(&pending)
			if err := s.BeginWorkspaceEnrollment(pending); err == nil {
				t.Fatal("BeginWorkspaceEnrollment accepted malformed value")
			}
			if _, ok := s.PendingWorkspaceEnrollment(); ok {
				t.Fatal("malformed begin installed enrollment")
			}
		})
	}

	exactlyBounded := valid
	exactlyBounded.ID = WorkspaceEnrollmentID(strings.Repeat("x", maxWorkspaceEnrollmentIDBytes))
	boundedSession := newEnrollmentSession(t)
	if err := boundedSession.BeginWorkspaceEnrollment(exactlyBounded); err != nil {
		t.Fatalf("exactly %d-byte ID rejected: %v", maxWorkspaceEnrollmentIDBytes, err)
	}

	s := newEnrollmentSession(t)
	if err := s.BeginWorkspaceEnrollment(valid); err != nil {
		t.Fatal(err)
	}
	replacement := valid
	replacement.ID = "replacement"
	if err := s.BeginWorkspaceEnrollment(replacement); err == nil {
		t.Fatal("BeginWorkspaceEnrollment replaced pending enrollment")
	}
	got, _ := s.PendingWorkspaceEnrollment()
	if got != valid {
		t.Fatalf("replacement attempt changed pending enrollment: %+v", got)
	}
}
