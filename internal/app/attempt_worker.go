package app

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

const defaultAttemptClaimTTL = 2 * time.Minute

type durableAttemptWork struct {
	repository learning.AttemptRepository
	partition  learning.AttemptPartition
	id         learning.AttemptID
}

// attemptWorker owns the durable attempt state machine. Callbacks may perform
// expensive or downstream work, but only repository checkpoints authorize the
// worker to skip that work after a crash.
type attemptWorker struct {
	repository learning.AttemptRepository
	partition  learning.AttemptPartition
	id         learning.AttemptID
	now        func() time.Time
	claimTTL   time.Duration

	evidence func(context.Context, learning.AttemptRecord) (learning.AttemptFailureCode, error)
	reflect  func(context.Context) (learning.Outcome, error)
	publish  func(context.Context, learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error)

	// Crash seams pin reconciliation at both sides of durable work.
	afterClaim      func() error
	afterCheckpoint func(learning.AttemptCheckpointStage) error
}

// Run advances one claimed attempt through the closed checkpoint/finalization
// matrix. Keeping the matrix in one function makes every crash boundary visible.
//
//nolint:gocyclo // explicit durable lifecycle table is clearer than split state hidden across helpers
func (w *attemptWorker) Run(ctx context.Context) (learning.AttemptRecord, error) {
	record, found, err := w.repository.Get(ctx, w.partition, w.id)
	if err != nil || !found {
		if err == nil {
			err = learning.ErrAttemptNotFound
		}
		return learning.AttemptRecord{}, err
	}
	if record.State.Terminal() {
		return record, nil
	}

	now := w.now().UTC()
	ttl := w.claimTTL
	if ttl <= 0 {
		ttl = defaultAttemptClaimTTL
	}
	record, claim, err := w.repository.AcquireClaim(ctx, w.partition, w.id, record.Version, now, now.Add(ttl))
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if w.afterClaim != nil {
		if err := w.afterClaim(); err != nil {
			return learning.AttemptRecord{}, err
		}
	}
	checkpoint := func(value learning.AttemptCheckpoint) error {
		record, err = w.repository.Checkpoint(ctx, w.partition, w.id, record.Version, claim, w.now().UTC(), value)
		if err != nil {
			return err
		}
		if w.afterCheckpoint != nil {
			return w.afterCheckpoint(value.Stage)
		}
		return nil
	}
	finalizeFailure := func(code learning.AttemptFailureCode) (learning.AttemptRecord, error) {
		if code == learning.FailureNone {
			code = learning.FailureInternal
		}
		return w.repository.Finalize(ctx, w.partition, w.id, record.Version, claim, w.now().UTC(), learning.AttemptFinalization{
			State: learning.AttemptFailed, Outcome: learning.AttemptOutcomeFailed, FailureCode: code,
		})
	}

	// A linked downstream artifact is proof that publication committed. Never
	// repeat the write merely because terminal finalization was interrupted.
	if record.CheckpointStage == learning.AttemptCheckpointProposalLinked || record.CheckpointStage == learning.AttemptCheckpointSkillLinked {
		return w.repository.Finalize(ctx, w.partition, w.id, record.Version, claim, w.now().UTC(), learning.AttemptFinalization{
			State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded,
		})
	}

	if record.CheckpointStage == learning.AttemptCheckpointNone {
		if w.evidence == nil {
			return finalizeFailure(learning.FailureEvidenceUnavailable)
		}
		failure, evidenceErr := w.evidence(ctx, record)
		if evidenceErr != nil {
			terminal, finalErr := finalizeFailure(learning.FailureEvidenceUnavailable)
			return terminal, errors.Join(evidenceErr, finalErr)
		}
		if failure != learning.FailureNone {
			return finalizeFailure(failure)
		}
		if err := checkpoint(learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointEvidenceVerified}); err != nil {
			return learning.AttemptRecord{}, err
		}
	}

	if w.reflect == nil {
		return finalizeFailure(learning.FailureUnavailable)
	}
	outcome, reflectErr := w.reflect(ctx)
	if reflectErr != nil {
		terminal, finalErr := finalizeFailure(learning.FailureUnavailable)
		return terminal, errors.Join(reflectErr, finalErr)
	}
	if record.CheckpointStage != learning.AttemptCheckpointReflectionComplete {
		if err := checkpoint(learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointReflectionComplete}); err != nil {
			return learning.AttemptRecord{}, err
		}
	}
	if outcome.Kind == learning.OutcomeAbstained {
		return w.repository.Finalize(ctx, w.partition, w.id, record.Version, claim, w.now().UTC(), learning.AttemptFinalization{
			State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeAbstained,
		})
	}
	if outcome.Kind != learning.OutcomeProposed || w.publish == nil {
		return finalizeFailure(learning.FailureEvaluationRejected)
	}

	downstream, failure, publishErr := w.publish(ctx, outcome)
	if publishErr != nil {
		if failure == learning.FailureNone {
			failure = learning.FailurePublicationFailed
		}
		terminal, finalErr := finalizeFailure(failure)
		return terminal, errors.Join(publishErr, finalErr)
	}
	if failure != learning.FailureNone {
		return finalizeFailure(failure)
	}
	if downstream.Stage != learning.AttemptCheckpointProposalLinked && downstream.Stage != learning.AttemptCheckpointSkillLinked {
		return finalizeFailure(learning.FailurePublicationFailed)
	}
	if err := checkpoint(downstream); err != nil {
		return learning.AttemptRecord{}, err
	}
	return w.repository.Finalize(ctx, w.partition, w.id, record.Version, claim, w.now().UTC(), learning.AttemptFinalization{
		State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded,
	})
}
