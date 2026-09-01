package learning_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

var _ learning.AttemptRepository = repositoryContract{}

type repositoryContract struct{}

func (repositoryContract) Create(context.Context, learning.AttemptPartition, learning.AttemptCreate) (learning.AttemptRecord, error) {
	return learning.AttemptRecord{}, nil
}
func (repositoryContract) Get(context.Context, learning.AttemptPartition, learning.AttemptID) (learning.AttemptRecord, bool, error) {
	return learning.AttemptRecord{}, false, nil
}
func (repositoryContract) List(context.Context, learning.AttemptPartition, learning.AttemptList) (learning.AttemptPage, error) {
	return learning.AttemptPage{}, nil
}
func (repositoryContract) DiscoverWork(context.Context, learning.AttemptWorkList) (learning.AttemptWorkPage, error) {
	return learning.AttemptWorkPage{}, nil
}
func (repositoryContract) AcquireClaim(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, time.Time, time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	return learning.AttemptRecord{}, learning.AttemptClaim{}, nil
}
func (repositoryContract) RenewClaim(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, learning.AttemptClaim, time.Time, time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	return learning.AttemptRecord{}, learning.AttemptClaim{}, nil
}
func (repositoryContract) Checkpoint(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, learning.AttemptClaim, time.Time, learning.AttemptCheckpoint) (learning.AttemptRecord, error) {
	return learning.AttemptRecord{}, nil
}
func (repositoryContract) ReleaseClaim(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, learning.AttemptClaim, time.Time) (learning.AttemptRecord, error) {
	return learning.AttemptRecord{}, nil
}
func (repositoryContract) Finalize(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, learning.AttemptClaim, time.Time, learning.AttemptFinalization) (learning.AttemptRecord, error) {
	return learning.AttemptRecord{}, nil
}
func (repositoryContract) Retry(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, time.Time) (learning.AttemptRecord, error) {
	return learning.AttemptRecord{}, nil
}
func (repositoryContract) Abandon(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, time.Time) (learning.AttemptRecord, error) {
	return learning.AttemptRecord{}, nil
}
func (repositoryContract) Delete(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, time.Time) error {
	return nil
}
func (repositoryContract) DeleteTerminalBefore(context.Context, learning.AttemptPartition, time.Time, int) (int, error) {
	return 0, nil
}

func TestAttemptRepository_LegalTransitionTableIsClosed(t *testing.T) {
	t.Parallel()

	states := []learning.AttemptState{
		learning.AttemptQueued,
		learning.AttemptRunning,
		learning.AttemptCompleted,
		learning.AttemptFailed,
		learning.AttemptAbandoned,
	}
	legal := map[[2]learning.AttemptState]bool{
		{learning.AttemptQueued, learning.AttemptRunning}:    true,
		{learning.AttemptQueued, learning.AttemptAbandoned}:  true,
		{learning.AttemptRunning, learning.AttemptRunning}:   true,
		{learning.AttemptRunning, learning.AttemptQueued}:    true,
		{learning.AttemptRunning, learning.AttemptCompleted}: true,
		{learning.AttemptRunning, learning.AttemptFailed}:    true,
		{learning.AttemptRunning, learning.AttemptAbandoned}: true,
		{learning.AttemptFailed, learning.AttemptQueued}:     true,
	}
	for _, from := range states {
		for _, to := range states {
			if got := learning.ValidAttemptTransition(from, to); got != legal[[2]learning.AttemptState{from, to}] {
				t.Errorf("ValidAttemptTransition(%q, %q) = %v, want %v", from, to, got, legal[[2]learning.AttemptState{from, to}])
			}
		}
	}
	if learning.ValidAttemptTransition(learning.AttemptState("future"), learning.AttemptQueued) {
		t.Fatal("unknown source state was accepted")
	}
}

func TestAttemptRepository_ClaimAndMutationInputsFailClosed(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	claim := learning.AttemptClaim{Generation: 7, ExpiresAt: now.Add(time.Minute)}
	if !claim.ValidAt(now) || claim.ValidAt(claim.ExpiresAt) {
		t.Fatalf("claim validity does not use the half-open [acquired, expiry) fence: %+v", claim)
	}
	if (learning.AttemptClaim{Generation: 0, ExpiresAt: now.Add(time.Minute)}).ValidAt(now) {
		t.Fatal("zero-generation claim was accepted")
	}

	validCheckpoints := []learning.AttemptCheckpoint{
		{Stage: learning.AttemptCheckpointEvidenceVerified},
		{Stage: learning.AttemptCheckpointReflectionComplete},
		{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: "proposal-1"},
		{Stage: learning.AttemptCheckpointSkillLinked, SkillID: "skill-1"},
	}
	for _, checkpoint := range validCheckpoints {
		if err := checkpoint.Validate(); err != nil {
			t.Errorf("checkpoint %+v error = %v", checkpoint, err)
		}
	}
	for _, checkpoint := range []learning.AttemptCheckpoint{
		{},
		{Stage: learning.AttemptCheckpointEvidenceVerified, ProposalID: "proposal-1"},
		{Stage: learning.AttemptCheckpointProposalLinked},
		{Stage: learning.AttemptCheckpointSkillLinked},
		{Stage: learning.AttemptCheckpointStage("future")},
	} {
		if err := checkpoint.Validate(); !errors.Is(err, learning.ErrInvalidAttempt) {
			t.Errorf("checkpoint %+v error = %v, want ErrInvalidAttempt", checkpoint, err)
		}
	}

	if !learning.AttemptCheckpointNone.CanAdvanceTo(learning.AttemptCheckpointEvidenceVerified) ||
		!learning.AttemptCheckpointEvidenceVerified.CanAdvanceTo(learning.AttemptCheckpointEvidenceVerified) ||
		learning.AttemptCheckpointSkillLinked.CanAdvanceTo(learning.AttemptCheckpointProposalLinked) ||
		learning.AttemptCheckpointReflectionComplete.CanAdvanceTo(learning.AttemptCheckpointNone) {
		t.Fatal("checkpoint progression accepted rollback or rejected forward/idempotent progress")
	}

	for _, final := range []learning.AttemptFinalization{
		{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded},
		{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeAbstained},
		{State: learning.AttemptFailed, Outcome: learning.AttemptOutcomeFailed, FailureCode: learning.FailureEvidenceUnavailable},
		{State: learning.AttemptAbandoned, Outcome: learning.AttemptOutcomeAbandoned},
	} {
		if err := final.Validate(); err != nil {
			t.Errorf("finalization %+v error = %v", final, err)
		}
	}
	if err := (learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeFailed}).Validate(); !errors.Is(err, learning.ErrAttemptTransition) {
		t.Fatalf("invalid finalization error = %v, want ErrAttemptTransition", err)
	}
}

func TestAttemptRepository_IdempotentCreateAndTypedConflictContract(t *testing.T) {
	t.Parallel()

	partition, err := learning.DeriveAttemptPartition("issuer\x00subject")
	if err != nil || partition == "" || string(partition) == "issuer\x00subject" {
		t.Fatalf("DeriveAttemptPartition() = %q, %v; want opaque non-empty partition", partition, err)
	}
	if _, err := learning.DeriveAttemptPartition(""); !errors.Is(err, learning.ErrInvalidAttempt) {
		t.Fatalf("empty partition material error = %v, want ErrInvalidAttempt", err)
	}

	for _, target := range []error{
		learning.ErrAttemptNotFound,
		learning.ErrAttemptCreateConflict,
		learning.ErrAttemptVersionConflict,
		learning.ErrAttemptTransition,
		learning.ErrAttemptClaimConflict,
		learning.ErrAttemptClaimLost,
	} {
		if wrapped := errors.Join(errors.New("adapter context"), target); !errors.Is(wrapped, target) {
			t.Fatalf("typed conflict %v is not matchable through wrapping", target)
		}
	}
}
