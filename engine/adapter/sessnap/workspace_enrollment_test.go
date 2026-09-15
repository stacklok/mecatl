package sessnap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

func enrollmentSnapshotSession(t *testing.T) (*session.Session, session.PendingWorkspaceEnrollment) {
	t.Helper()
	s := session.New("enrollment", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0).UTC())
	if err := s.BindAuthority(session.Authority{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}},
		Provenance:    "test",
	}); err != nil {
		t.Fatalf("BindAuthority: %v", err)
	}
	pending := session.PendingWorkspaceEnrollment{
		ID:               "enroll_123",
		RequiredServices: 5,
		ExpiresAt:        time.Unix(100, 0).UTC(),
	}
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	return s, pending
}

func TestPendingWorkspaceEnrollmentSnapshotAndJSONRoundTrip(t *testing.T) {
	s, want := enrollmentSnapshotSession(t)
	snap, err := Of(s)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	if snap.PendingWorkspaceEnrollment == nil || *snap.PendingWorkspaceEnrollment != want {
		t.Fatalf("snapshot enrollment = %+v, want %+v", snap.PendingWorkspaceEnrollment, want)
	}

	snap.PendingWorkspaceEnrollment.ID = "snapshot-mutated"
	gotOriginal, _ := s.PendingWorkspaceEnrollment()
	if gotOriginal != want {
		t.Fatalf("snapshot aliases aggregate: %+v", gotOriginal)
	}
	*snap.PendingWorkspaceEnrollment = want

	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	decoded.PendingWorkspaceEnrollment.ID = "decoded-mutated"
	if *snap.PendingWorkspaceEnrollment != want {
		t.Fatalf("decoded snapshot aliases source snapshot: %+v", snap.PendingWorkspaceEnrollment)
	}
	*decoded.PendingWorkspaceEnrollment = want

	if err := s.AbortWorkspaceEnrollment(want.ID); err != nil {
		t.Fatalf("AbortWorkspaceEnrollment: %v", err)
	}
	if *snap.PendingWorkspaceEnrollment != want || *decoded.PendingWorkspaceEnrollment != want {
		t.Fatal("aggregate mutation changed an independent snapshot")
	}

	restored, err := decoded.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, ok := restored.PendingWorkspaceEnrollment()
	if !ok || got != want {
		t.Fatalf("restored enrollment = %+v, %v; want %+v, true", got, ok, want)
	}
	if restored.State != session.StateIdle || len(restored.Conversation.Messages) != 0 {
		t.Fatalf("restored enrollment crossed pre-prompt boundary: state=%q messages=%d", restored.State, len(restored.Conversation.Messages))
	}
	if _, ok := restored.PendingAsk(); ok {
		t.Fatal("restore unexpectedly set PendingAsk")
	}
	if _, ok := restored.PendingAuthorization(); ok {
		t.Fatal("restore unexpectedly set PendingAuthorization")
	}
}

func TestEstablishedIdleWorkspaceEnrollmentSnapshotRestores(t *testing.T) {
	s, want := enrollmentSnapshotSession(t)
	s.Conversation.Append(session.NewUserMessage("prompt"))
	snap, err := Of(s)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}

	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, ok := restored.PendingWorkspaceEnrollment()
	if !ok || got != want {
		t.Fatalf("restored enrollment = %+v, %v; want %+v, true", got, ok, want)
	}
	if restored.State != session.StateIdle || len(restored.Conversation.Messages) != 1 {
		t.Fatalf("restored established enrollment = state %q, messages %d; want idle, 1", restored.State, len(restored.Conversation.Messages))
	}
}
func TestMalformedPendingWorkspaceEnrollmentSnapshotFailsClosed(t *testing.T) {
	s, _ := enrollmentSnapshotSession(t)
	base, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("call-1", "external", json.RawMessage(`{"value":1}`))
	authorization := session.PendingAuthorization{
		Authorization: session.ExternalAuthorization{
			ID:        "authorization-1",
			Binding:   "opaque-binding",
			ExpiresAt: time.Unix(200, 0).UTC(),
		},
		Call: call,
	}
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"missing authority", func(s *Snapshot) { s.Authority = nil }},
		{"running state", func(s *Snapshot) { s.State = session.StateRunning }},
		{"permission pending", func(s *Snapshot) {
			s.State = session.StateAwaiting
			s.Pending = &session.PendingAsk{AskID: "ask-1", Tool: "Read", Call: "call-1"}
		}},
		{"authorization pending", func(s *Snapshot) {
			s.State = session.StateAuthorizing
			s.Messages = []messageDTO{toDTO(session.NewAssistantMessage("", "", []session.ToolCall{call}))}
			s.PendingAuthorization = toPendingAuthorizationDTO(authorization)
		}},
		{"terminal state", func(s *Snapshot) {
			s.State = session.StateCompleted
			s.StopReason = session.StopEndTurn
		}},
		{"empty ID", func(s *Snapshot) { s.PendingWorkspaceEnrollment.ID = "" }},
		{"oversized ID", func(s *Snapshot) {
			s.PendingWorkspaceEnrollment.ID = session.WorkspaceEnrollmentID(strings.Repeat("x", 257))
		}},
		{"no required services", func(s *Snapshot) { s.PendingWorkspaceEnrollment.RequiredServices = 0 }},
		{"zero expiry", func(s *Snapshot) { s.PendingWorkspaceEnrollment.ExpiresAt = time.Time{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := base
			pending := *base.PendingWorkspaceEnrollment
			snap.PendingWorkspaceEnrollment = &pending
			tc.mutate(&snap)
			if _, err := snap.Restore(); err == nil {
				t.Fatal("Restore accepted malformed workspace enrollment")
			}
		})
	}
}

func TestPendingWorkspaceEnrollmentSnapshotContainsOnlySafeCorrelation(t *testing.T) {
	s, _ := enrollmentSnapshotSession(t)
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(snap.PendingWorkspaceEnrollment)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["ID"] == nil || fields["RequiredServices"] == nil || fields["ExpiresAt"] == nil {
		t.Fatalf("persisted enrollment fields = %v, want only safe correlation fields", fields)
	}
}
