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
	if err := s.RecordFailureMetadata(RetryMetadata{Disposition: RetryDisposition(99), Progress: StreamProgressPrecommit}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("invalid disposition error = %v", err)
	}
	if err := s.RecordFailureMetadata(RetryMetadata{Disposition: RetryDispositionRetryable, Progress: StreamProgress(99)}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("invalid progress error = %v", err)
	}
	if got := s.FailureMetadata(); got != (RetryMetadata{}) {
		t.Fatalf("invalid values persisted: %+v", got)
	}
}

func TestFailureMetadataGuardAndClear(t *testing.T) {
	s := New("retry", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, Limits{}, time.Now())
	if err := s.RecordFailureMetadata(RetryMetadata{Disposition: RetryDispositionRetryable, Progress: StreamProgressVisible}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("RecordFailureMetadata(idle) = %v, want ErrIllegalTransition", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailureMetadata(RetryMetadata{Disposition: RetryDispositionRetryable, Progress: StreamProgressVisible}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLastError("first failure"); err != nil {
		t.Fatal(err)
	}
	if got := s.FailureMetadata(); got != (RetryMetadata{Disposition: RetryDispositionRetryable, Progress: StreamProgressVisible}) {
		t.Fatalf("FailureMetadata = %+v", got)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	if got := s.FailureMetadata(); got != (RetryMetadata{}) {
		t.Fatalf("metadata after Recover = %+v, want zero", got)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if got := s.FailureMetadata(); got != (RetryMetadata{}) || s.LastError() != "" {
		t.Fatalf("unstamped subsequent failure retained stale metadata: %+v, last=%q", got, s.LastError())
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
	if err := s.RecordFailureMetadata(RetryMetadata{Disposition: RetryDispositionRetryable, Progress: StreamProgressVisible}); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareFailedStepRetry(); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.FailedStepRetryPending(); !ok || got != (RetryMetadata{Disposition: RetryDispositionRetryable, Progress: StreamProgressVisible}) {
		t.Fatalf("pending intent = (%+v,%v)", got, ok)
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
	if _, ok := s.FailedStepRetryPending(); !ok || s.State != StateIdle {
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
	if err := s.RecordFailureMetadata(RetryMetadata{Disposition: RetryDispositionRetryable, Progress: StreamProgressPrecommit}); err != nil {
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
	if err := s.RecordFailureMetadata(RetryMetadata{Disposition: RetryDispositionPermanent, Progress: StreamProgressVisible}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.FailedStepRetryPending(); ok {
		t.Fatal("second failure retained prepared retry intent")
	}
	if got := s.FailureMetadata(); got != (RetryMetadata{Disposition: RetryDispositionPermanent, Progress: StreamProgressVisible}) {
		t.Fatalf("second failure metadata = %+v", got)
	}
}
