package session

import (
	"errors"
	"testing"
	"time"
)

func TestRetryVocabularyValidation(t *testing.T) {
	for _, disposition := range []RetryDisposition{RetryDispositionUnknown, RetryDispositionRetryable, RetryDispositionPermanent} {
		if !disposition.Valid() {
			t.Fatalf("valid disposition %d rejected", disposition)
		}
	}
	if RetryDisposition(99).Valid() {
		t.Fatal("invalid disposition accepted")
	}
	for _, progress := range []StreamProgress{StreamProgressUnknown, StreamProgressPrecommit, StreamProgressVisible, StreamProgressComplete} {
		if !progress.Valid() {
			t.Fatalf("valid progress %d rejected", progress)
		}
	}
	if StreamProgress(99).Valid() {
		t.Fatal("invalid progress accepted")
	}
}

func TestRecordFailureMetadataRejectsInvalidVocabulary(t *testing.T) {
	s := New("invalid", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, Limits{}, time.Now())
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailureMetadata(RetryDisposition(99), StreamProgressPrecommit); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("invalid disposition error = %v", err)
	}
	if err := s.RecordFailureMetadata(RetryDispositionRetryable, StreamProgress(99)); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("invalid progress error = %v", err)
	}
	if d, p := s.FailureMetadata(); d != RetryDispositionUnknown || p != StreamProgressUnknown {
		t.Fatalf("invalid values persisted: (%d, %d)", d, p)
	}
}

func TestFailureMetadataGuardAndClear(t *testing.T) {
	s := New("retry", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, Limits{}, time.Now())
	if err := s.RecordFailureMetadata(RetryDispositionRetryable, StreamProgressVisible); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("RecordFailureMetadata(idle) = %v, want ErrIllegalTransition", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailureMetadata(RetryDispositionRetryable, StreamProgressVisible); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLastError("first failure"); err != nil {
		t.Fatal(err)
	}
	if d, p := s.FailureMetadata(); d != RetryDispositionRetryable || p != StreamProgressVisible {
		t.Fatalf("FailureMetadata = (%v,%v)", d, p)
	}
	if s.FailurePermanence() {
		t.Fatal("retryable failure projected permanent")
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	if d, p := s.FailureMetadata(); d != RetryDispositionUnknown || p != StreamProgressUnknown {
		t.Fatalf("metadata after Recover = (%v,%v), want unknown", d, p)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if d, p := s.FailureMetadata(); d != RetryDispositionUnknown || p != StreamProgressUnknown || s.FailurePermanence() || s.LastError() != "" {
		t.Fatalf("unstamped subsequent failure retained stale metadata: (%v,%v), permanent=%v, last=%q", d, p, s.FailurePermanence(), s.LastError())
	}
}

func TestFailedStepRetryIntentSurvivesAbandonAndBlocksPrompt(t *testing.T) {
	s := New("pending", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, Limits{}, time.Now())
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailureMetadata(RetryDispositionRetryable, StreamProgressVisible); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareFailedStepRetry(); err != nil {
		t.Fatal(err)
	}
	if d, p, ok := s.FailedStepRetryPending(); !ok || d != RetryDispositionRetryable || p != StreamProgressVisible {
		t.Fatalf("pending intent = (%v,%v,%v)", d, p, ok)
	}
	if err := s.RecordUserPrompt("must not skip retry", nil); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("RecordUserPrompt while retry pending = %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Abandon(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.FailedStepRetryPending(); !ok || s.State != StateIdle {
		t.Fatalf("Abandon lost retry intent: state=%s pending=%v", s.State, ok)
	}
}

func TestFailedStepRetrySecondFailureReplacesIntent(t *testing.T) {
	s := New("second", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, Limits{}, time.Now())
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailureMetadata(RetryDispositionRetryable, StreamProgressPrecommit); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareFailedStepRetry(); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailureMetadata(RetryDispositionPermanent, StreamProgressVisible); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.FailedStepRetryPending(); ok {
		t.Fatal("second failure retained prepared retry intent")
	}
	if d, p := s.FailureMetadata(); d != RetryDispositionPermanent || p != StreamProgressVisible {
		t.Fatalf("second failure metadata = (%v,%v)", d, p)
	}
}

func TestFailurePermanenceCompatibilitySetsTypedDisposition(t *testing.T) {
	s := New("legacy", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, Limits{}, time.Now())
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailurePermanence(true); err != nil {
		t.Fatal(err)
	}
	d, _ := s.FailureMetadata()
	if d != RetryDispositionPermanent || !s.FailurePermanence() {
		t.Fatalf("compatibility projection = (%v,%v)", d, s.FailurePermanence())
	}
}
