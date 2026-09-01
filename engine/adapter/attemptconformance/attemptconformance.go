// Package attemptconformance provides the shared lifecycle contract for
// learning.AttemptRepository adapters. Memory, durable local, and remote driver
// implementations run this same suite; storage layout and transport are not part
// of the contract.
package attemptconformance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

// Harness supplies a fresh repository and a deterministic clock control. SetNow
// changes the time observed by Create; lifecycle methods receive their effective
// time explicitly through the AttemptRepository contract.
type Harness struct {
	Repository learning.AttemptRepository
	SetNow     func(time.Time)
}

// Factory returns a fresh, isolated repository harness for each subtest.
type Factory func(*testing.T) Harness

// Run executes the complete AttemptRepository lifecycle contract.
//
//nolint:gocyclo // keeping the shared lifecycle cases together makes adapter coverage auditable
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("deterministic idempotent creation and partition isolation", func(t *testing.T) {
		h := newHarness(t, factory)
		ctx := context.Background()
		partitionA := partition(t, "caller-a")
		partitionB := partition(t, "caller-b")
		create := fixture(t, "a")

		first := mustCreate(t, h.Repository, partitionA, create)
		again := mustCreate(t, h.Repository, partitionA, create)
		if first != again {
			t.Fatalf("idempotent Create changed the record: first=%+v again=%+v", first, again)
		}
		changed := fixture(t, "different")
		changed.ID = create.ID
		if _, err := h.Repository.Create(ctx, partitionA, changed); !errors.Is(err, learning.ErrAttemptCreateConflict) {
			t.Fatalf("Create identity collision error = %v, want ErrAttemptCreateConflict", err)
		}
		if _, found, err := h.Repository.Get(ctx, partitionB, create.ID); err != nil || found {
			t.Fatalf("foreign Get found=%v err=%v, want an isolated miss", found, err)
		}
		foreign := mustCreate(t, h.Repository, partitionB, create)
		if foreign.ID != first.ID {
			t.Fatalf("same caller-derived ID changed across repository partitions: %q != %q", foreign.ID, first.ID)
		}
		page, err := h.Repository.List(ctx, partitionA, learning.AttemptList{})
		if err != nil || len(page.Records) != 1 || page.Records[0].ID != first.ID {
			t.Fatalf("partition A List = %+v, err=%v", page, err)
		}
		assertRecord(t, first, learning.AttemptQueued)
	})

	t.Run("opaque CAS rejects stale mutations without a write", func(t *testing.T) {
		h := newHarness(t, factory)
		ctx := context.Background()
		p := partition(t, "cas")
		now := baseTime()
		created := mustCreate(t, h.Repository, p, fixture(t, "cas"))
		running, claim, err := h.Repository.AcquireClaim(ctx, p, created.ID, created.Version, now, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if running.Version == created.Version {
			t.Fatal("successful mutation did not replace the opaque version")
		}
		if _, err := h.Repository.ReleaseClaim(ctx, p, running.ID, created.Version, claim, now); !errors.Is(err, learning.ErrAttemptVersionConflict) {
			t.Fatalf("stale ReleaseClaim error = %v, want ErrAttemptVersionConflict", err)
		}
		stored := mustGet(t, h.Repository, p, running.ID)
		if stored != running {
			t.Fatalf("stale CAS changed stored record: got=%+v want=%+v", stored, running)
		}
	})

	t.Run("concurrent CAS permits one claim owner", func(t *testing.T) {
		h := newHarness(t, factory)
		ctx := context.Background()
		p := partition(t, "concurrent-cas")
		now := baseTime()
		created := mustCreate(t, h.Repository, p, fixture(t, "concurrent-cas"))

		const contenders = 16
		start := make(chan struct{})
		results := make(chan error, contenders)
		var wg sync.WaitGroup
		for range contenders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _, err := h.Repository.AcquireClaim(ctx, p, created.ID, created.Version, now, now.Add(time.Minute))
				results <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		succeeded := 0
		conflicted := 0
		for err := range results {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, learning.ErrAttemptVersionConflict):
				conflicted++
			default:
				t.Fatalf("concurrent AcquireClaim error = %v", err)
			}
		}
		if succeeded != 1 || conflicted != contenders-1 {
			t.Fatalf("concurrent AcquireClaim successes=%d conflicts=%d, want 1 and %d", succeeded, conflicted, contenders-1)
		}
		stored := mustGet(t, h.Repository, p, created.ID)
		if stored.State != learning.AttemptRunning || stored.ClaimGeneration != 1 {
			t.Fatalf("stored concurrent winner = %+v", stored)
		}
	})

	t.Run("claims expire and successors fence prior workers", func(t *testing.T) {
		h := newHarness(t, factory)
		ctx := context.Background()
		p := partition(t, "claims")
		now := baseTime()
		created := mustCreate(t, h.Repository, p, fixture(t, "claims"))
		running, firstClaim, err := h.Repository.AcquireClaim(ctx, p, created.ID, created.Version, now, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = h.Repository.AcquireClaim(ctx, p, running.ID, running.Version, now.Add(time.Second), now.Add(2*time.Minute)); !errors.Is(err, learning.ErrAttemptClaimConflict) {
			t.Fatalf("live successor acquisition error = %v, want ErrAttemptClaimConflict", err)
		}
		renewed, renewedClaim, err := h.Repository.RenewClaim(ctx, p, running.ID, running.Version, firstClaim, now.Add(10*time.Second), now.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if renewedClaim.Generation != firstClaim.Generation || !renewedClaim.ExpiresAt.After(firstClaim.ExpiresAt) {
			t.Fatalf("renewed claim = %+v, want same generation and later expiry than %+v", renewedClaim, firstClaim)
		}
		successorNow := renewedClaim.ExpiresAt
		assertClaimFenced(t, h.Repository, p, renewed, renewedClaim, successorNow, "expired")
		successor, successorClaim, err := h.Repository.AcquireClaim(ctx, p, renewed.ID, renewed.Version, successorNow, successorNow.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if successorClaim.Generation <= renewedClaim.Generation {
			t.Fatalf("successor generation = %d, want > %d", successorClaim.Generation, renewedClaim.Generation)
		}
		assertClaimFenced(t, h.Repository, p, successor, renewedClaim, successorNow, "superseded")
		queued, err := h.Repository.ReleaseClaim(ctx, p, successor.ID, successor.Version, successorClaim, successorNow)
		if err != nil || queued.State != learning.AttemptQueued || queued.ClaimGeneration != 0 || !queued.ClaimExpiresAt.IsZero() {
			t.Fatalf("ReleaseClaim = %+v, err=%v", queued, err)
		}
		assertClaimFenced(t, h.Repository, p, queued, successorClaim, successorNow, "released")
	})

	t.Run("checkpoints are monotonic and replay-safe", func(t *testing.T) {
		h := newHarness(t, factory)
		ctx := context.Background()
		p := partition(t, "checkpoints")
		now := baseTime()
		created := mustCreate(t, h.Repository, p, fixture(t, "checkpoints"))
		current, claim, err := h.Repository.AcquireClaim(ctx, p, created.ID, created.Version, now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		steps := []learning.AttemptCheckpoint{
			{Stage: learning.AttemptCheckpointEvidenceVerified},
			{Stage: learning.AttemptCheckpointReflectionComplete},
			{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: "proposal-one"},
			{Stage: learning.AttemptCheckpointSkillLinked, ProposalID: "proposal-one", SkillID: "skill-one"},
		}
		for _, step := range steps {
			current, err = h.Repository.Checkpoint(ctx, p, current.ID, current.Version, claim, now, step)
			if err != nil {
				t.Fatalf("Checkpoint(%q): %v", step.Stage, err)
			}
		}
		replayed, err := h.Repository.Checkpoint(ctx, p, current.ID, current.Version, claim, now, steps[len(steps)-1])
		if err != nil || replayed.CheckpointStage != learning.AttemptCheckpointSkillLinked {
			t.Fatalf("idempotent checkpoint replay = %+v, err=%v", replayed, err)
		}
		rollback := learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: "proposal-one"}
		if _, err = h.Repository.Checkpoint(ctx, p, replayed.ID, replayed.Version, claim, now, rollback); !errors.Is(err, learning.ErrAttemptTransition) {
			t.Fatalf("checkpoint rollback error = %v, want ErrAttemptTransition", err)
		}
		changed := learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointSkillLinked, ProposalID: "proposal-other", SkillID: "skill-other"}
		if _, err = h.Repository.Checkpoint(ctx, p, replayed.ID, replayed.Version, claim, now, changed); !errors.Is(err, learning.ErrAttemptTransition) {
			t.Fatalf("same-stage checkpoint rewrite error = %v, want ErrAttemptTransition", err)
		}
	})

	t.Run("legal states finalize retry and abandon", func(t *testing.T) {
		h := newHarness(t, factory)
		ctx := context.Background()
		p := partition(t, "states")
		now := baseTime()
		created := mustCreate(t, h.Repository, p, fixture(t, "states"))
		if _, err := h.Repository.Retry(ctx, p, created.ID, created.Version, now); !errors.Is(err, learning.ErrAttemptTransition) {
			t.Fatalf("queued Retry error = %v, want ErrAttemptTransition", err)
		}
		running, claim, err := h.Repository.AcquireClaim(ctx, p, created.ID, created.Version, now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		failed, err := h.Repository.Finalize(ctx, p, running.ID, running.Version, claim, now, learning.AttemptFinalization{State: learning.AttemptFailed, Outcome: learning.AttemptOutcomeFailed, FailureCode: learning.FailureUnavailable})
		if err != nil {
			t.Fatal(err)
		}
		retried, err := h.Repository.Retry(ctx, p, failed.ID, failed.Version, now)
		if err != nil || retried.State != learning.AttemptQueued || retried.AttemptGeneration != failed.AttemptGeneration+1 {
			t.Fatalf("Retry = %+v, err=%v", retried, err)
		}
		assertClaimFenced(t, h.Repository, p, retried, claim, now, "retried")
		abandoned, err := h.Repository.Abandon(ctx, p, retried.ID, retried.Version, now)
		if err != nil || abandoned.State != learning.AttemptAbandoned || abandoned.Outcome != learning.AttemptOutcomeAbandoned {
			t.Fatalf("Abandon = %+v, err=%v", abandoned, err)
		}
		assertClaimFenced(t, h.Repository, p, abandoned, claim, now, "abandoned")
		if _, err = h.Repository.Retry(ctx, p, abandoned.ID, abandoned.Version, now); !errors.Is(err, learning.ErrAttemptTransition) {
			t.Fatalf("terminal Retry error = %v, want ErrAttemptTransition", err)
		}
	})

	t.Run("listing is bounded filtered and cursor-paginated", func(t *testing.T) {
		h := newHarness(t, factory)
		ctx := context.Background()
		p := partition(t, "pages")
		for _, suffix := range []string{"d", "a", "c", "b"} {
			mustCreate(t, h.Repository, p, fixture(t, "page-"+suffix))
		}
		first, err := h.Repository.List(ctx, p, learning.AttemptList{Limit: 2, State: learning.AttemptQueued})
		if err != nil || len(first.Records) != 2 || first.Next == "" {
			t.Fatalf("first page = %+v, err=%v", first, err)
		}
		second, err := h.Repository.List(ctx, p, learning.AttemptList{After: first.Next, Limit: 2, State: learning.AttemptQueued})
		if err != nil || len(second.Records) != 2 || second.Next != "" {
			t.Fatalf("second page = %+v, err=%v", second, err)
		}
		if first.Records[0].ID >= first.Records[1].ID || first.Records[1].ID >= second.Records[0].ID || second.Records[0].ID >= second.Records[1].ID {
			t.Fatalf("pages are not globally ordered: first=%+v second=%+v", first.Records, second.Records)
		}
		if _, err = h.Repository.List(ctx, p, learning.AttemptList{Limit: learning.MaxAttemptPageSize + 1}); !errors.Is(err, learning.ErrInvalidAttempt) {
			t.Fatalf("oversized page error = %v, want ErrInvalidAttempt", err)
		}
	})

	RunRetentionAndDeletion(t, factory)
}

// RunRetentionAndDeletion pins the caller-partitioned cleanup and claimed-work
// deletion rules independently so acceptance tests can name this exact scenario.
func RunRetentionAndDeletion(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("retention and deletion respect claims and partition", func(t *testing.T) {
		h := newHarness(t, factory)
		ctx := context.Background()
		p := partition(t, "retention-owner")
		foreign := partition(t, "retention-foreign")
		old := baseTime()
		cutoff := old.Add(24 * time.Hour)

		h.SetNow(old)
		oldTerminalA := createTerminal(t, h.Repository, p, fixture(t, "old-terminal-a"), old)
		oldTerminalB := createTerminal(t, h.Repository, p, fixture(t, "old-terminal-b"), old)
		foreignTerminal := createTerminal(t, h.Repository, foreign, fixture(t, "foreign-terminal"), old)
		claimed := mustCreate(t, h.Repository, p, fixture(t, "claimed"))
		claimed, _, err := h.Repository.AcquireClaim(ctx, p, claimed.ID, claimed.Version, old, old.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		queued := mustCreate(t, h.Repository, p, fixture(t, "queued"))

		h.SetNow(cutoff)
		atCutoffCreated := mustCreate(t, h.Repository, p, fixture(t, "at-cutoff"))
		atCutoffRunning, atCutoffClaim, err := h.Repository.AcquireClaim(ctx, p, atCutoffCreated.ID, atCutoffCreated.Version, cutoff, cutoff.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		atCutoff, err := h.Repository.Finalize(ctx, p, atCutoffRunning.ID, atCutoffRunning.Version, atCutoffClaim, cutoff, learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded})
		if err != nil {
			t.Fatal(err)
		}
		if err = h.Repository.Delete(ctx, p, claimed.ID, claimed.Version, cutoff); !errors.Is(err, learning.ErrAttemptClaimConflict) {
			t.Fatalf("Delete expired claimed nonterminal error = %v, want ErrAttemptClaimConflict", err)
		}
		if got := mustGet(t, h.Repository, p, claimed.ID); got != claimed {
			t.Fatalf("claimed attempt changed after refused delete: got=%+v want=%+v", got, claimed)
		}
		if err = h.Repository.Delete(ctx, p, atCutoff.ID, atCutoffRunning.Version, cutoff); !errors.Is(err, learning.ErrAttemptVersionConflict) {
			t.Fatalf("Delete with stale opaque version error = %v, want ErrAttemptVersionConflict", err)
		}

		deleted, err := h.Repository.DeleteTerminalBefore(ctx, p, cutoff, 1)
		if err != nil || deleted != 1 {
			t.Fatalf("DeleteTerminalBefore = %d, err=%v; want one", deleted, err)
		}
		remainingOld := 0
		for _, id := range []learning.AttemptID{oldTerminalA.ID, oldTerminalB.ID} {
			if _, found, getErr := h.Repository.Get(ctx, p, id); getErr != nil {
				t.Fatal(getErr)
			} else if found {
				remainingOld++
			}
		}
		if remainingOld != 1 {
			t.Fatalf("retention batch left %d old terminal attempts, want 1", remainingOld)
		}
		deleted, err = h.Repository.DeleteTerminalBefore(ctx, p, cutoff, 1)
		if err != nil || deleted != 1 {
			t.Fatalf("second DeleteTerminalBefore = %d, err=%v; want one", deleted, err)
		}
		assertMissing(t, h.Repository, p, oldTerminalA.ID)
		assertMissing(t, h.Repository, p, oldTerminalB.ID)
		mustGet(t, h.Repository, foreign, foreignTerminal.ID)
		mustGet(t, h.Repository, p, claimed.ID)
		mustGet(t, h.Repository, p, queued.ID)
		mustGet(t, h.Repository, p, atCutoff.ID)

		if err = h.Repository.Delete(ctx, foreign, atCutoff.ID, atCutoff.Version, cutoff); !errors.Is(err, learning.ErrAttemptNotFound) {
			t.Fatalf("foreign-partition Delete error = %v, want ErrAttemptNotFound", err)
		}
		if err = h.Repository.Delete(ctx, p, queued.ID, queued.Version, cutoff); err != nil {
			t.Fatalf("Delete unclaimed queued: %v", err)
		}
		if err = h.Repository.Delete(ctx, p, atCutoff.ID, atCutoff.Version, cutoff); err != nil {
			t.Fatalf("Delete terminal: %v", err)
		}
		assertMissing(t, h.Repository, p, queued.ID)
		assertMissing(t, h.Repository, p, atCutoff.ID)
		if _, err = h.Repository.DeleteTerminalBefore(ctx, p, cutoff, 0); !errors.Is(err, learning.ErrInvalidAttempt) {
			t.Fatalf("zero retention limit error = %v, want ErrInvalidAttempt", err)
		}
	})
}

func newHarness(t *testing.T, factory Factory) Harness {
	t.Helper()
	h := factory(t)
	if h.Repository == nil || h.SetNow == nil {
		t.Fatal("attemptconformance Factory returned an incomplete Harness")
	}
	h.SetNow(baseTime())
	return h
}

func baseTime() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }

func partition(t *testing.T, identity string) learning.AttemptPartition {
	t.Helper()
	p, err := learning.DeriveAttemptPartition(identity)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fixture(t *testing.T, suffix string) learning.AttemptCreate {
	t.Helper()
	digest := learning.CanonicalDigest(fmt.Sprintf("%064x", len(suffix)+1))
	source := learning.AttemptSource{SessionID: session.SessionID("session-" + suffix), RunID: learning.DurableRunID("run-" + suffix), CanonicalDigest: digest}
	prompt := learning.CurrentPromptBinding{Ordinal: 1, Digest: digest, Origin: learning.PromptOriginCurrentPrincipal}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHard, source, prompt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := learning.DeterministicAttemptID("fixture-caller", source)
	if err != nil {
		t.Fatal(err)
	}
	return learning.AttemptCreate{ID: id, Provenance: provenance}
}

func mustCreate(t *testing.T, repository learning.AttemptRepository, p learning.AttemptPartition, create learning.AttemptCreate) learning.AttemptRecord {
	t.Helper()
	record, err := repository.Create(context.Background(), p, create)
	if err != nil {
		t.Fatal(err)
	}
	assertRecord(t, record, learning.AttemptQueued)
	return record
}

func createTerminal(t *testing.T, repository learning.AttemptRepository, p learning.AttemptPartition, create learning.AttemptCreate, now time.Time) learning.AttemptRecord {
	t.Helper()
	created := mustCreate(t, repository, p, create)
	running, claim, err := repository.AcquireClaim(context.Background(), p, created.ID, created.Version, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := repository.Finalize(context.Background(), p, running.ID, running.Version, claim, now, learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	assertRecord(t, terminal, learning.AttemptCompleted)
	return terminal
}

func mustGet(t *testing.T, repository learning.AttemptRepository, p learning.AttemptPartition, id learning.AttemptID) learning.AttemptRecord {
	t.Helper()
	record, found, err := repository.Get(context.Background(), p, id)
	if err != nil || !found {
		t.Fatalf("Get(%q) found=%v err=%v", id, found, err)
	}
	return record
}

func assertClaimFenced(t *testing.T, repository learning.AttemptRepository, partition learning.AttemptPartition, current learning.AttemptRecord, stale learning.AttemptClaim, now time.Time, state string) {
	t.Helper()
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "renew", run: func() error {
			_, _, err := repository.RenewClaim(context.Background(), partition, current.ID, current.Version, stale, now, now.Add(time.Minute))
			return err
		}},
		{name: "checkpoint", run: func() error {
			_, err := repository.Checkpoint(context.Background(), partition, current.ID, current.Version, stale, now, learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointEvidenceVerified})
			return err
		}},
		{name: "release", run: func() error {
			_, err := repository.ReleaseClaim(context.Background(), partition, current.ID, current.Version, stale, now)
			return err
		}},
		{name: "finalize", run: func() error {
			_, err := repository.Finalize(context.Background(), partition, current.ID, current.Version, stale, now, learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded})
			return err
		}},
	}
	for _, operation := range operations {
		if err := operation.run(); !errors.Is(err, learning.ErrAttemptClaimLost) {
			t.Fatalf("%s claim %s error = %v, want ErrAttemptClaimLost", state, operation.name, err)
		}
		if stored := mustGet(t, repository, partition, current.ID); stored != current {
			t.Fatalf("%s claim %s changed attempt: got=%+v want=%+v", state, operation.name, stored, current)
		}
	}
}

func assertMissing(t *testing.T, repository learning.AttemptRepository, p learning.AttemptPartition, id learning.AttemptID) {
	t.Helper()
	if _, found, err := repository.Get(context.Background(), p, id); err != nil || found {
		t.Fatalf("Get deleted %q found=%v err=%v", id, found, err)
	}
}

func assertRecord(t *testing.T, record learning.AttemptRecord, state learning.AttemptState) {
	t.Helper()
	if err := learning.ValidateAttemptRecord(record); err != nil {
		t.Fatalf("invalid repository record: %+v: %v", record, err)
	}
	if record.State != state {
		t.Fatalf("record state = %q, want %q", record.State, state)
	}
}
