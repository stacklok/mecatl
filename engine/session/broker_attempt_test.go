package session

import (
	"crypto/sha256"
	"testing"
	"time"
)

func TestBrokerAttemptOccurrenceAndPairedResultFence(t *testing.T) {
	s := brokerAuthoritySession(t, nil)
	ref, catalogue := brokerAuthorityRefs()
	now := time.Unix(10, 0)
	if err := s.AdoptBrokerCatalogue(ref, catalogue, BrokerConnectionRef(catalogue), now.Add(time.Hour), []string{"remote"}); err != nil {
		t.Fatal(err)
	}

	attempt, next := NewBrokerAttempt(), NewBrokerAttempt()
	if !attempt.Valid() || !next.Valid() || attempt == next || (BrokerAttempt{}).Valid() {
		t.Fatalf("invalid occurrence identities: %q, %q", attempt.ID, next.ID)
	}
	call := NewToolCall("call", "remote", []byte(`{}`))
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAssistant(NewAssistantMessage("", "", []ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	s.brokerAccess.Current = &BrokerHostAttempt{Attempt: attempt, CallID: call.ID, Digest: sha256.Sum256(call.Args)}
	s.brokerAttemptCompleted = attempt
	if err := s.RecordToolResults([]ToolResult{NewToolResult("other", "unrelated")}); err != nil {
		t.Fatal(err)
	}
	if s.brokerAccess.Current == nil {
		t.Fatal("unpaired result cleared broker uncertainty")
	}
	if err := s.RecordToolResults([]ToolResult{NewToolResult(call.ID, "completed")}); err != nil {
		t.Fatal(err)
	}
	if s.brokerAccess.Current != nil || s.brokerAttemptCompleted != (BrokerAttempt{}) {
		t.Fatal("matching paired result did not clear the occurrence")
	}
}

func TestRestoredBrokerAttemptRemainsUncertainAfterResult(t *testing.T) {
	s := brokerAuthoritySession(t, nil)
	ref, catalogue := brokerAuthorityRefs()
	now := time.Unix(10, 0)
	if err := s.AdoptBrokerCatalogue(ref, catalogue, BrokerConnectionRef(catalogue), now.Add(time.Hour), []string{"remote"}); err != nil {
		t.Fatal(err)
	}
	call := NewToolCall("call", "remote", []byte(`{}`))
	access, _ := s.BrokerAccess()
	access.Current = &BrokerHostAttempt{Attempt: NewBrokerAttempt(), CallID: call.ID, Digest: sha256.Sum256(call.Args)}
	if err := s.RestoreBrokerAccess(access); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAssistant(NewAssistantMessage("", "", []ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolResults([]ToolResult{NewToolResult(call.ID, "late")}); err != nil {
		t.Fatal(err)
	}
	current, ok := s.BrokerAccess()
	if !ok || current.Current == nil {
		t.Fatal("restored uncertainty was cleared by a result without its live occurrence")
	}
}
