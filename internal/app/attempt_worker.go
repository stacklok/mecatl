package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

const defaultAttemptClaimTTL = 2 * time.Minute

// attemptWorker owns the durable attempt state machine. Callbacks may perform
// expensive or downstream work, but only repository checkpoints authorize the
// worker to skip that work after a crash.
type attemptWorker struct {
	repository         learning.AttemptRepository
	partition          learning.AttemptPartition
	id                 learning.AttemptID
	claimTTL           time.Duration
	claimRenewInterval time.Duration

	evidence func(context.Context, learning.AttemptRecord) (learning.AttemptFailureCode, error)
	reflect  func(context.Context) (learning.Outcome, error)
	publish  func(context.Context, learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error)

	// Crash seams pin reconciliation at both sides of durable work.
	afterClaim      func() error
	afterCheckpoint func(learning.AttemptCheckpointStage) error
}

type attemptClaimLease struct {
	mu         sync.Mutex
	repository learning.AttemptRepository
	partition  learning.AttemptPartition
	id         learning.AttemptID
	record     learning.AttemptRecord
	claim      learning.AttemptClaim
}

func (l *attemptClaimLease) snapshot() learning.AttemptRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.record
}

func (l *attemptClaimLease) renew(ctx context.Context, ttl time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, claim, err := l.repository.RenewClaim(ctx, l.partition, l.id, l.record.Version, l.claim, ttl)
	if err == nil {
		l.record, l.claim = record, claim
	}
	return err
}

func (l *attemptClaimLease) checkpoint(ctx context.Context, value learning.AttemptCheckpoint) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, err := l.repository.Checkpoint(ctx, l.partition, l.id, l.record.Version, l.claim, value)
	if err == nil {
		l.record = record
	}
	return err
}

func (l *attemptClaimLease) release(ctx context.Context) (learning.AttemptRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, err := l.repository.ReleaseClaim(ctx, l.partition, l.id, l.record.Version, l.claim)
	if err == nil {
		l.record = record
	}
	return record, err
}

func (l *attemptClaimLease) finalize(ctx context.Context, value learning.AttemptFinalization) (learning.AttemptRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, err := l.repository.Finalize(ctx, l.partition, l.id, l.record.Version, l.claim, value)
	if err == nil {
		l.record = record
	}
	return record, err
}

type attemptClaimRenewer struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

func startAttemptClaimRenewer(parent context.Context, cancelWork context.CancelFunc, lease *attemptClaimLease, ttl, interval time.Duration) *attemptClaimRenewer {
	ctx, cancel := context.WithCancel(parent)
	r := &attemptClaimRenewer{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := lease.renew(ctx, ttl); err != nil {
					r.mu.Lock()
					r.err = err
					r.mu.Unlock()
					cancelWork()
					return
				}
			}
		}
	}()
	return r
}

func (r *attemptClaimRenewer) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *attemptClaimRenewer) stop() error {
	r.cancel()
	<-r.done
	return r.failure()
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

	ttl := w.claimTTL
	if ttl <= 0 {
		ttl = defaultAttemptClaimTTL
	}
	record, claim, err := w.repository.AcquireClaim(ctx, w.partition, w.id, record.Version, ttl)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if w.afterClaim != nil {
		if err := w.afterClaim(); err != nil {
			return learning.AttemptRecord{}, err
		}
	}
	lease := &attemptClaimLease{repository: w.repository, partition: w.partition, id: w.id, record: record, claim: claim}

	// A linked downstream artifact is proof that publication committed. Never
	// repeat the write merely because terminal finalization was interrupted.
	if record.CheckpointStage == learning.AttemptCheckpointProposalLinked || record.CheckpointStage == learning.AttemptCheckpointSkillLinked {
		return lease.finalize(ctx, learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded})
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	interval := w.claimRenewInterval
	if interval <= 0 {
		interval = ttl / 3
	}
	if interval <= 0 {
		interval = time.Millisecond
	}
	renewer := startAttemptClaimRenewer(workCtx, cancelWork, lease, ttl, interval)
	stopped := false
	stopRenewal := func() error {
		if stopped {
			return renewer.failure()
		}
		stopped = true
		return renewer.stop()
	}
	defer func() { _ = stopRenewal() }()
	claimFailure := func() error {
		if err := renewer.failure(); err != nil {
			return err
		}
		return workCtx.Err()
	}
	checkpoint := func(value learning.AttemptCheckpoint) error {
		if err := claimFailure(); err != nil {
			return err
		}
		if err := lease.checkpoint(workCtx, value); err != nil {
			return err
		}
		if w.afterCheckpoint != nil {
			return w.afterCheckpoint(value.Stage)
		}
		return nil
	}
	finalize := func(value learning.AttemptFinalization) (learning.AttemptRecord, error) {
		if err := stopRenewal(); err != nil {
			return learning.AttemptRecord{}, err
		}
		if err := ctx.Err(); err != nil {
			return learning.AttemptRecord{}, err
		}
		return lease.finalize(ctx, value)
	}
	finalizeFailure := func(code learning.AttemptFailureCode) (learning.AttemptRecord, error) {
		if code == learning.FailureNone {
			code = learning.FailureInternal
		}
		return finalize(learning.AttemptFinalization{State: learning.AttemptFailed, Outcome: learning.AttemptOutcomeFailed, FailureCode: code})
	}

	if record.CheckpointStage == learning.AttemptCheckpointNone {
		if w.evidence == nil {
			return finalizeFailure(learning.FailureEvidenceUnavailable)
		}
		failure, evidenceErr := w.evidence(workCtx, lease.snapshot())
		if claimErr := claimFailure(); claimErr != nil {
			return learning.AttemptRecord{}, errors.Join(evidenceErr, claimErr)
		}
		if evidenceErr != nil {
			if errors.Is(evidenceErr, errLearningEvidenceNotReady) {
				if stopErr := stopRenewal(); stopErr != nil {
					return learning.AttemptRecord{}, errors.Join(evidenceErr, stopErr)
				}
				released, releaseErr := lease.release(ctx)
				return released, errors.Join(evidenceErr, releaseErr)
			}
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
	outcome, reflectErr := w.reflect(workCtx)
	if claimErr := claimFailure(); claimErr != nil {
		return learning.AttemptRecord{}, errors.Join(reflectErr, claimErr)
	}
	if reflectErr != nil {
		terminal, finalErr := finalizeFailure(learning.FailureUnavailable)
		return terminal, errors.Join(reflectErr, finalErr)
	}
	if lease.snapshot().CheckpointStage != learning.AttemptCheckpointReflectionComplete {
		if err := checkpoint(learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointReflectionComplete}); err != nil {
			return learning.AttemptRecord{}, err
		}
	}
	if outcome.Kind == learning.OutcomeAbstained {
		return finalize(learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeAbstained})
	}
	if outcome.Kind != learning.OutcomeProposed || w.publish == nil {
		return finalizeFailure(learning.FailureEvaluationRejected)
	}

	downstream, failure, publishErr := w.publish(workCtx, outcome)
	if claimErr := claimFailure(); claimErr != nil {
		return learning.AttemptRecord{}, errors.Join(publishErr, claimErr)
	}
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
	return finalize(learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded})
}
