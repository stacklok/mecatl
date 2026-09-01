package memattempt_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/attemptconformance"
	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func TestMemattemptConformance(t *testing.T) {
	attemptconformance.Run(t, func(*testing.T) attemptconformance.Harness {
		clock := &fakeClock{}
		return attemptconformance.Harness{
			Repository: memattempt.New(clock),
			SetNow:     clock.set,
		}
	})
}

func TestADR_0254_StaleClaimCannotTransitionAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repository := memattempt.New(clock)
	partition, err := learning.DeriveAttemptPartition("claim-fencing-owner")
	if err != nil {
		t.Fatal(err)
	}

	create := func(suffix string) learning.AttemptRecord {
		t.Helper()
		digest := learning.CanonicalDigest(strings.Repeat(suffix, 64))
		source := learning.AttemptSource{SessionID: session.SessionID("session-" + suffix), RunID: learning.DurableRunID("run-" + suffix), CanonicalDigest: digest}
		provenance, provenanceErr := learning.NewAdmissionProvenance(learning.AdmissionHard, source, learning.CurrentPromptBinding{Ordinal: 1, Digest: digest, Origin: learning.PromptOriginCurrentPrincipal})
		if provenanceErr != nil {
			t.Fatal(provenanceErr)
		}
		id, idErr := learning.DeterministicAttemptID("claim-fencing-owner", source)
		if idErr != nil {
			t.Fatal(idErr)
		}
		record, createErr := repository.Create(ctx, partition, learning.AttemptCreate{ID: id, Provenance: provenance})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return record
	}
	assertFenced := func(name string, current learning.AttemptRecord, stale learning.AttemptClaim, at time.Time) {
		t.Helper()
		clock.set(at)
		operations := []struct {
			name string
			run  func() error
		}{
			{name: "renew", run: func() error {
				_, _, mutateErr := repository.RenewClaim(ctx, partition, current.ID, current.Version, stale, time.Minute)
				return mutateErr
			}},
			{name: "checkpoint", run: func() error {
				_, mutateErr := repository.Checkpoint(ctx, partition, current.ID, current.Version, stale, learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointEvidenceVerified})
				return mutateErr
			}},
			{name: "release", run: func() error {
				_, mutateErr := repository.ReleaseClaim(ctx, partition, current.ID, current.Version, stale)
				return mutateErr
			}},
			{name: "finalize", run: func() error {
				_, mutateErr := repository.Finalize(ctx, partition, current.ID, current.Version, stale, learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded})
				return mutateErr
			}},
		}
		for _, operation := range operations {
			if mutateErr := operation.run(); !errors.Is(mutateErr, learning.ErrAttemptClaimLost) {
				t.Fatalf("%s stale %s error = %v, want ErrAttemptClaimLost", name, operation.name, mutateErr)
			}
			stored, found, getErr := repository.Get(ctx, partition, current.ID)
			if getErr != nil || !found || stored != current {
				t.Fatalf("%s stale %s changed attempt: got=%+v found=%v err=%v want=%+v", name, operation.name, stored, found, getErr, current)
			}
		}
	}

	expiring := create("a")
	running, expiredClaim, err := repository.AcquireClaim(ctx, partition, expiring.ID, expiring.Version, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expiry := expiredClaim.ExpiresAt
	assertFenced("expired", running, expiredClaim, expiry)
	successor, successorClaim, err := repository.AcquireClaim(ctx, partition, running.ID, running.Version, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	assertFenced("superseded cannot finalize successor", successor, expiredClaim, expiry)
	released, err := repository.ReleaseClaim(ctx, partition, successor.ID, successor.Version, successorClaim)
	if err != nil {
		t.Fatal(err)
	}
	assertFenced("released", released, successorClaim, expiry)

	retrying := create("b")
	retryRunning, retryClaim, err := repository.AcquireClaim(ctx, partition, retrying.ID, retrying.Version, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := repository.Finalize(ctx, partition, retryRunning.ID, retryRunning.Version, retryClaim, learning.AttemptFinalization{State: learning.AttemptFailed, Outcome: learning.AttemptOutcomeFailed, FailureCode: learning.FailureUnavailable})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := repository.Retry(ctx, partition, failed.ID, failed.Version)
	if err != nil {
		t.Fatal(err)
	}
	assertFenced("retried", retried, retryClaim, now)

	abandoning := create("c")
	abandonRunning, abandonClaim, err := repository.AcquireClaim(ctx, partition, abandoning.ID, abandoning.Version, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	abandonedAt := abandonClaim.ExpiresAt
	clock.set(abandonedAt)
	abandoned, err := repository.Abandon(ctx, partition, abandonRunning.ID, abandonRunning.Version)
	if err != nil {
		t.Fatal(err)
	}
	assertFenced("abandoned cannot be rewritten to success", abandoned, abandonClaim, abandonedAt)
}
