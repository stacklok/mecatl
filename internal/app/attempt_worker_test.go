package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type attemptWorkerClock struct{ now time.Time }

func (c *attemptWorkerClock) Now() time.Time { return c.now }

func newAttemptWorkerRecord(t *testing.T, clock *attemptWorkerClock) (*memattempt.Store, learning.AttemptPartition, learning.AttemptRecord) {
	t.Helper()
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	source := learning.AttemptSource{SessionID: session.SessionID("source"), RunID: learning.DurableRunID("run_aaaaaaaaaaaaaaaaaaaaaaaaaa"), CanonicalDigest: learning.CanonicalDigest(digest)}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHostRequested, source, learning.CurrentPromptBinding{
		Ordinal: 0, Digest: learning.CanonicalDigest(strings.Repeat("b", 64)), Origin: learning.PromptOriginCurrentPrincipal,
	})
	if err != nil {
		t.Fatal(err)
	}
	partition, err := learning.DeriveAttemptPartition("caller")
	if err != nil {
		t.Fatal(err)
	}
	id, err := learning.DeterministicAttemptID("caller", source)
	if err != nil {
		t.Fatal(err)
	}
	repo := memattempt.New(clock)
	record, err := repo.Create(context.Background(), partition, learning.AttemptCreate{ID: id, Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	return repo, partition, record
}

type renewalRepository struct {
	learning.AttemptRepository
	renewed chan int
	mu      sync.Mutex
	count   int
	fail    bool
}

func (r *renewalRepository) RenewClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, version learning.AttemptVersion, claim learning.AttemptClaim, now, expires time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	if r.fail {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrAttemptClaimLost
	}
	record, renewed, err := r.AttemptRepository.RenewClaim(ctx, partition, id, version, claim, now, expires)
	if err == nil {
		r.mu.Lock()
		r.count++
		count := r.count
		r.mu.Unlock()
		select {
		case r.renewed <- count:
		default:
		}
	}
	return record, renewed, err
}

func waitRenewal(t *testing.T, renewed <-chan int, minimum int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case count := <-renewed:
			if count >= minimum {
				return
			}
		case <-deadline.C:
			t.Fatalf("claim was not renewed %d times", minimum)
		}
	}
}

func TestAttemptWorkerRenewsClaimDuringLongWork(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(60, 0)}
	base, partition, record := newAttemptWorkerRecord(t, clock)
	repository := &renewalRepository{AttemptRepository: base, renewed: make(chan int, 8)}
	worker := attemptWorker{
		repository: repository, partition: partition, id: record.ID, now: clock.Now,
		claimTTL: time.Minute, claimRenewInterval: 5 * time.Millisecond,
		evidence: func(context.Context, learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			waitRenewal(t, repository.renewed, 1)
			return learning.FailureNone, nil
		},
		reflect: func(context.Context) (learning.Outcome, error) {
			waitRenewal(t, repository.renewed, 2)
			return learning.Outcome{Kind: learning.OutcomeProposed}, nil
		},
		publish: func(context.Context, learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
			waitRenewal(t, repository.renewed, 3)
			return learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: "proposal-renewed"}, learning.FailureNone, nil
		},
	}
	got, err := worker.Run(context.Background())
	if err != nil || got.State != learning.AttemptCompleted {
		t.Fatalf("Run = %+v, err=%v", got, err)
	}
}

func TestAttemptWorkerCancelsWorkWhenRenewalIsLost(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(70, 0)}
	base, partition, record := newAttemptWorkerRecord(t, clock)
	repository := &renewalRepository{AttemptRepository: base, renewed: make(chan int), fail: true}
	cancelled := make(chan struct{})
	worker := attemptWorker{
		repository: repository, partition: partition, id: record.ID, now: clock.Now,
		claimTTL: time.Minute, claimRenewInterval: 5 * time.Millisecond,
		evidence: func(ctx context.Context, _ learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			<-ctx.Done()
			close(cancelled)
			return learning.FailureNone, ctx.Err()
		},
	}
	_, err := worker.Run(context.Background())
	if !errors.Is(err, learning.ErrAttemptClaimLost) {
		t.Fatalf("Run error = %v, want ErrAttemptClaimLost", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("renewal loss did not cancel active work")
	}
}

func TestADR_0254_AbstentionIsASeparateTerminalOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		reflect     func(context.Context) (learning.Outcome, error)
		publication func(context.Context, learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error)
		outcome     learning.AttemptOutcome
		failure     learning.AttemptFailureCode
	}{
		{name: "abstained_no_candidate", reflect: func(context.Context) (learning.Outcome, error) {
			return learning.Outcome{Kind: learning.OutcomeAbstained}, nil
		}, outcome: learning.AttemptOutcomeAbstained},
		{name: "evidence_failure", outcome: learning.AttemptOutcomeFailed, failure: learning.FailureEvidenceUnavailable},
		{name: "evaluation_rejection", reflect: func(context.Context) (learning.Outcome, error) {
			return learning.Outcome{Kind: learning.OutcomeProposed}, nil
		}, publication: func(context.Context, learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
			return learning.AttemptCheckpoint{}, learning.FailureEvaluationRejected, nil
		}, outcome: learning.AttemptOutcomeFailed, failure: learning.FailureEvaluationRejected},
		{name: "publication_failure", reflect: func(context.Context) (learning.Outcome, error) {
			return learning.Outcome{Kind: learning.OutcomeProposed}, nil
		}, publication: func(context.Context, learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
			return learning.AttemptCheckpoint{}, learning.FailurePublicationFailed, nil
		}, outcome: learning.AttemptOutcomeFailed, failure: learning.FailurePublicationFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := &attemptWorkerClock{now: time.Unix(10, 0)}
			repo, partition, record := newAttemptWorkerRecord(t, clock)
			worker := attemptWorker{
				repository: repo, partition: partition, id: record.ID, now: clock.Now,
				evidence: func(context.Context, learning.AttemptRecord) (learning.AttemptFailureCode, error) {
					if tc.name == "evidence_failure" {
						return learning.FailureEvidenceUnavailable, nil
					}
					return learning.FailureNone, nil
				},
				reflect: tc.reflect, publish: tc.publication,
			}
			got, err := worker.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.State.Terminal() == false || got.Outcome != tc.outcome || got.FailureCode != tc.failure {
				t.Fatalf("terminal = state %q outcome %q failure %q, want outcome %q failure %q", got.State, got.Outcome, got.FailureCode, tc.outcome, tc.failure)
			}
		})
	}
}

func TestADR_0254_AttemptReconciliationIsIdempotent(t *testing.T) {
	t.Parallel()
	clock := &attemptWorkerClock{now: time.Unix(20, 0)}
	repo, partition, record := newAttemptWorkerRecord(t, clock)
	publicationCalls := 0
	claimCrash := errors.New("simulated crash after claim")
	checkpointCrash := errors.New("simulated crash after durable publication checkpoint")
	worker := attemptWorker{
		repository: repo, partition: partition, id: record.ID, now: clock.Now, claimTTL: time.Second,
		evidence: func(context.Context, learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			return learning.FailureNone, nil
		},
		reflect: func(context.Context) (learning.Outcome, error) {
			return learning.Outcome{Kind: learning.OutcomeProposed}, nil
		},
		publish: func(context.Context, learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
			publicationCalls++
			return learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointSkillLinked, ProposalID: "proposal-deterministic", SkillID: "skill-deterministic"}, learning.FailureNone, nil
		},
		afterClaim: func() error { return claimCrash },
		afterCheckpoint: func(stage learning.AttemptCheckpointStage) error {
			if stage == learning.AttemptCheckpointSkillLinked {
				return checkpointCrash
			}
			return nil
		},
	}
	if _, err := worker.Run(context.Background()); !errors.Is(err, claimCrash) {
		t.Fatalf("first run error = %v, want post-claim crash", err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	worker.afterClaim = nil
	if _, err := worker.Run(context.Background()); !errors.Is(err, checkpointCrash) {
		t.Fatalf("second run error = %v, want post-checkpoint crash", err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	worker.afterCheckpoint = nil
	got, err := worker.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if publicationCalls != 1 {
		t.Fatalf("publication calls = %d, want one deterministic downstream write", publicationCalls)
	}
	if got.State != learning.AttemptCompleted || got.Outcome != learning.AttemptOutcomeSucceeded || got.ProposalID != "proposal-deterministic" || got.SkillID != "skill-deterministic" {
		t.Fatalf("reconciled terminal = %+v", got)
	}
	again, err := worker.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if publicationCalls != 1 || again.Version != got.Version {
		t.Fatalf("terminal replay duplicated work or changed CAS version: calls=%d first=%q replay=%q", publicationCalls, got.Version, again.Version)
	}
}

func TestADR_0254_IndependentDownstreamCommitReconcilesAfterClaimLoss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := &attemptWorkerClock{now: time.Unix(40, 0)}
	attempts, attemptPartition, created := newAttemptWorkerRecord(t, clock)
	proposals := memproposal.New()
	input, outcome, digest := reflectionOutcomeFixture(t, learning.CandidateOperatorFact)
	proposalPartition := learning.ProposalPartition{Principal: "claim-loss-principal"}
	proposalID, err := learning.DeterministicProposalID(proposalPartition, digest, outcome.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}

	publish := func(publishCtx context.Context, reflected learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
		if _, processErr := processReflectionOutcome(publishCtx, proposals, nil, nil, proposalPartition.Principal, input, digest, reflected, nil, learning.Review, false, ""); processErr != nil {
			return learning.AttemptCheckpoint{}, learning.FailurePublicationFailed, processErr
		}
		return learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: proposalID}, learning.FailureNone, nil
	}
	worker := attemptWorker{
		repository: attempts, partition: attemptPartition, id: created.ID, now: clock.Now, claimTTL: time.Second,
		evidence: func(context.Context, learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			return learning.FailureNone, nil
		},
		reflect: func(context.Context) (learning.Outcome, error) { return outcome, nil },
		publish: func(publishCtx context.Context, reflected learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
			checkpoint, failure, publishErr := publish(publishCtx, reflected)
			clock.now = clock.now.Add(2 * time.Second) // the independent commit wins after this worker's claim expires
			return checkpoint, failure, publishErr
		},
	}

	if _, err = worker.Run(ctx); !errors.Is(err, learning.ErrAttemptClaimLost) {
		t.Fatalf("late worker error = %v, want ErrAttemptClaimLost", err)
	}
	staleAttempt, found, err := attempts.Get(ctx, attemptPartition, created.ID)
	if err != nil || !found {
		t.Fatalf("attempt after claim loss found=%v err=%v", found, err)
	}
	if staleAttempt.State == learning.AttemptCompleted || staleAttempt.Outcome == learning.AttemptOutcomeSucceeded || staleAttempt.ProposalID != "" {
		t.Fatalf("independent downstream commit invented attempt success: %+v", staleAttempt)
	}
	committed, found, err := proposals.Get(ctx, proposalPartition, proposalID)
	if err != nil || !found || committed.Status != learning.ProposalStaged {
		t.Fatalf("independent proposal commit = %+v found=%v err=%v", committed, found, err)
	}

	worker.publish = publish
	reconciled, err := worker.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.State != learning.AttemptCompleted || reconciled.Outcome != learning.AttemptOutcomeSucceeded || reconciled.ProposalID != proposalID {
		t.Fatalf("compatible deterministic proposal was not adopted: %+v", reconciled)
	}
	page, err := proposals.List(ctx, proposalPartition, learning.ProposalList{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != committed.ID || page.Records[0].Version != committed.Version || page.Records[0].Status != committed.Status {
		t.Fatalf("reconciliation duplicated or rewrote downstream commit: before=%+v after=%+v", committed, page.Records)
	}
}
