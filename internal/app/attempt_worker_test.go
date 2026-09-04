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
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skillmaterialize"
	"github.com/stacklok/mecatl/engine/agent"
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

func (r *renewalRepository) RenewClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, version learning.AttemptVersion, claim learning.AttemptClaim, duration time.Duration) (learning.AttemptRecord, learning.AttemptClaim, error) {
	if r.fail {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrAttemptClaimLost
	}
	record, renewed, err := r.AttemptRepository.RenewClaim(ctx, partition, id, version, claim, duration)
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

func TestAttemptWorkerCallbackDeadlinePersistsBackoffThenRetryExhausted(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"prepare", "evidence", "reflection", "publish"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			clock := &attemptWorkerClock{now: time.Unix(80, 0)}
			repository, partition, created := newAttemptWorkerRecord(t, clock)
			callbackReturned := make(chan struct{}, defaultAttemptSetupMaxAttempts)
			block := func(ctx context.Context) error {
				<-ctx.Done()
				callbackReturned <- struct{}{}
				return ctx.Err()
			}
			worker := attemptWorker{
				repository: repository, partition: partition, id: created.ID,
				claimTTL: time.Second, claimRenewInterval: time.Millisecond,
				callbackTimeout: 20 * time.Millisecond,
				setupRetryBase:  time.Second, setupMaxAttempts: 2,
				prepare: func(ctx context.Context, _ learning.AttemptRecord) (learning.AttemptFailureCode, error) {
					if stage == "prepare" {
						return learning.FailureNone, block(ctx)
					}
					return learning.FailureNone, nil
				},
				evidence: func(ctx context.Context, _ learning.AttemptRecord) (learning.AttemptFailureCode, error) {
					if stage == "evidence" {
						return learning.FailureNone, block(ctx)
					}
					return learning.FailureNone, nil
				},
				reflect: func(ctx context.Context) (learning.Outcome, error) {
					if stage == "reflection" {
						return learning.Outcome{}, block(ctx)
					}
					return learning.Outcome{Kind: learning.OutcomeProposed}, nil
				},
				publish: func(ctx context.Context, _ learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
					if stage == "publish" {
						return learning.AttemptCheckpoint{}, learning.FailureNone, block(ctx)
					}
					return learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: "proposal"}, learning.FailureNone, nil
				},
			}

			for attempt := 1; attempt <= 2; attempt++ {
				got, err := worker.Run(context.Background())
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("attempt %d error = %v, want deadline", attempt, err)
				}
				select {
				case <-callbackReturned:
				default:
					t.Fatalf("attempt %d returned before %s callback cooperatively stopped", attempt, stage)
				}
				if attempt == 1 {
					if got.State != learning.AttemptRunning || got.State.Terminal() || got.FailureCode != learning.FailureNone {
						t.Fatalf("deadline did not persist transient retry: %+v", got)
					}
					clock.now = got.ClaimExpiresAt
					continue
				}
				if got.State != learning.AttemptFailed || got.FailureCode != learning.FailureRetryExhausted {
					t.Fatalf("deadline retry cap = %+v, want failed/retry_exhausted", got)
				}
			}
		})
	}
}

func TestAttemptWorkerRenewsClaimDuringLongWork(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(60, 0)}
	base, partition, record := newAttemptWorkerRecord(t, clock)
	repository := &renewalRepository{AttemptRepository: base, renewed: make(chan int, 8)}
	worker := attemptWorker{
		repository: repository, partition: partition, id: record.ID,
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
		repository: repository, partition: partition, id: record.ID,
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

type blockingRenewalRepository struct {
	learning.AttemptRepository
	started  chan struct{}
	returned chan struct{}
	mu       sync.Mutex
	blocked  bool
}

func (r *blockingRenewalRepository) RenewClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, version learning.AttemptVersion, claim learning.AttemptClaim, duration time.Duration) (learning.AttemptRecord, learning.AttemptClaim, error) {
	r.mu.Lock()
	if r.blocked {
		r.mu.Unlock()
		return r.AttemptRepository.RenewClaim(ctx, partition, id, version, claim, duration)
	}
	r.blocked = true
	r.mu.Unlock()
	close(r.started)
	<-ctx.Done()
	close(r.returned)
	return learning.AttemptRecord{}, learning.AttemptClaim{}, ctx.Err()
}

func TestAttemptWorkerDeadlineStopsAndJoinsClaimRenewal(t *testing.T) {
	t.Parallel()
	clock := &attemptWorkerClock{now: time.Unix(75, 0)}
	base, partition, record := newAttemptWorkerRecord(t, clock)
	repository := &blockingRenewalRepository{AttemptRepository: base, started: make(chan struct{}), returned: make(chan struct{})}
	worker := attemptWorker{
		repository: repository, partition: partition, id: record.ID,
		claimTTL: time.Second, claimRenewInterval: time.Millisecond, callbackTimeout: 20 * time.Millisecond,
		evidence: func(ctx context.Context, _ learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			<-repository.started
			<-ctx.Done()
			return learning.FailureNone, ctx.Err()
		},
	}

	got, err := worker.Run(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline", err)
	}
	select {
	case <-repository.returned:
	default:
		t.Fatal("Run returned before the blocked renewal stopped and joined")
	}
	wantBackoffExpiry := clock.now.Add(defaultAttemptSetupRetryBase)
	if got.State != learning.AttemptRunning || got.State.Terminal() || !got.ClaimExpiresAt.Equal(wantBackoffExpiry) {
		t.Fatalf("deadline did not retain persisted retry/backoff through renewal join: got %+v, want expiry %v", got, wantBackoffExpiry)
	}
}

func TestAttemptWorkerReflectionFailureClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		failure learning.AttemptFailureCode
	}{
		{
			name:    "invalid_output_is_evaluation_rejected",
			err:     errors.Join(errors.New("strict decoder rejected output"), agent.ErrReflectionOutput),
			failure: learning.FailureEvaluationRejected,
		},
		{
			name:    "provider_error_remains_unavailable",
			err:     errors.Join(errors.New("provider request failed"), agent.ErrReflectionProvider),
			failure: learning.FailureUnavailable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := &attemptWorkerClock{now: time.Unix(10, 0)}
			repository, partition, record := newAttemptWorkerRecord(t, clock)
			worker := attemptWorker{
				repository: repository,
				partition:  partition,
				id:         record.ID,
				evidence: func(context.Context, learning.AttemptRecord) (learning.AttemptFailureCode, error) {
					return learning.FailureNone, nil
				},
				reflect: func(context.Context) (learning.Outcome, error) {
					return learning.Outcome{}, tc.err
				},
			}

			got, err := worker.Run(context.Background())
			if !errors.Is(err, tc.err) {
				t.Fatalf("Run error = %v, want wrapping %v", err, tc.err)
			}
			if got.State != learning.AttemptFailed || got.Outcome != learning.AttemptOutcomeFailed || got.FailureCode != tc.failure {
				t.Fatalf("terminal = state %q outcome %q failure %q, want failed/%q", got.State, got.Outcome, got.FailureCode, tc.failure)
			}
		})
	}
}

func TestADR_0259_AbstentionIsASeparateTerminalOutcome(t *testing.T) {
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
				repository: repo, partition: partition, id: record.ID,
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

func TestADR_0259_AttemptReconciliationIsIdempotent(t *testing.T) {
	t.Parallel()
	clock := &attemptWorkerClock{now: time.Unix(20, 0)}
	repo, partition, record := newAttemptWorkerRecord(t, clock)
	publicationCalls := 0
	claimCrash := errors.New("simulated crash after claim")
	checkpointCrash := errors.New("simulated crash after durable publication checkpoint")
	worker := attemptWorker{
		repository: repo, partition: partition, id: record.ID, claimTTL: time.Second,
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

func TestADR_0259_CanonicalArtifactIdentitySurvivesRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := &attemptWorkerClock{now: time.Unix(50, 0)}
	attempts, attemptPartition, created := newAttemptWorkerRecord(t, clock)
	remoteProposals := memproposal.New()
	remoteSkills := memskill.New()
	proposals := &opaqueProposalRepository{inner: remoteProposals}
	skills := &opaqueSkillRepository{inner: remoteSkills}
	input, outcome, recomputedDigest := reflectionOutcomeFixture(t, learning.CandidateProcedure)
	canonicalDigest := string(created.Provenance.Source.CanonicalDigest)
	if recomputedDigest == canonicalDigest {
		t.Fatal("fixture must expose the admission/recovery digest mismatch")
	}
	partition := learning.ProposalPartition{Principal: "restart-principal", Project: input.Trajectory.Workspace}
	owner := "reflection"

	publish := func(publishCtx context.Context, record learning.AttemptRecord, reflected learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
		procedure := func(ctx context.Context, proposal learning.ProposalRecord, _ learning.Mode) error {
			_, err := skillmaterialize.Materialize(ctx, proposals, skills, learning.SkillPartition(proposal.Partition), owner, proposal, nil, learning.Decision{Kind: learning.DecisionApprove, Actor: "test"})
			return err
		}
		processed, err := processReflectionOutcome(publishCtx, proposals, nil, nil, partition.Principal, input,
			string(record.Provenance.Source.CanonicalDigest), reflected, nil, learning.Review, false, "", procedure)
		if err != nil {
			return learning.AttemptCheckpoint{}, learning.FailurePublicationFailed, err
		}
		return checkpointFromReflectionReceipt(processed)
	}
	newWorker := func(expireAfterPublish bool) *attemptWorker {
		claimed := created
		return &attemptWorker{
			repository: attempts, partition: attemptPartition, id: created.ID, claimTTL: time.Second,
			evidence: func(_ context.Context, record learning.AttemptRecord) (learning.AttemptFailureCode, error) {
				claimed = record
				return learning.FailureNone, nil
			},
			reflect: func(context.Context) (learning.Outcome, error) { return outcome, nil },
			publish: func(publishCtx context.Context, reflected learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
				checkpoint, failure, err := publish(publishCtx, claimed, reflected)
				if expireAfterPublish {
					clock.now = clock.now.Add(2 * time.Second)
				}
				return checkpoint, failure, err
			},
		}
	}

	if _, err := newWorker(true).Run(ctx); !errors.Is(err, learning.ErrAttemptClaimLost) {
		t.Fatalf("initial worker error = %v, want ErrAttemptClaimLost", err)
	}
	reconciled, err := newWorker(false).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.State != learning.AttemptCompleted || reconciled.Outcome != learning.AttemptOutcomeSucceeded || reconciled.ProposalID == "" || reconciled.SkillID == "" {
		t.Fatalf("restarted worker did not adopt authoritative artifacts: %+v", reconciled)
	}

	proposalPage, err := proposals.List(ctx, partition, learning.ProposalList{})
	if err != nil || len(proposalPage.Records) != 1 {
		t.Fatalf("logical proposal partition records = %d, err=%v", len(proposalPage.Records), err)
	}
	if proposalPage.Records[0].ID != reconciled.ProposalID || proposalPage.Records[0].SkillID != reconciled.SkillID || proposalPage.Records[0].InputDigest != canonicalDigest {
		t.Fatalf("attempt links do not match authoritative proposal: attempt=%+v proposal=%+v", reconciled, proposalPage.Records[0])
	}
	skillPage, err := skills.List(ctx, learning.SkillPartition(partition), learning.SkillList{OwnerAgent: owner})
	if err != nil || len(skillPage.Versions) != 1 || skillPage.Versions[0].ID != reconciled.SkillID {
		t.Fatalf("logical skill partition = %+v, err=%v", skillPage.Versions, err)
	}
	foreignProposals, err := proposals.List(ctx, learning.ProposalPartition{Principal: partition.Principal, Project: "/other"}, learning.ProposalList{})
	if err != nil || len(foreignProposals.Records) != 0 {
		t.Fatalf("proposal crossed partition: %+v, err=%v", foreignProposals.Records, err)
	}
	foreignSkills, err := skills.List(ctx, learning.SkillPartition{Principal: partition.Principal, Project: "/other"}, learning.SkillList{})
	if err != nil || len(foreignSkills.Versions) != 0 {
		t.Fatalf("skill crossed partition: %+v, err=%v", foreignSkills.Versions, err)
	}
	localProposalPage, err := remoteProposals.List(ctx, partition, learning.ProposalList{})
	if err != nil || len(localProposalPage.Records) != 0 {
		t.Fatalf("remote wrapper leaked untransformed proposal partition: %+v, err=%v", localProposalPage.Records, err)
	}
	localSkillPage, err := remoteSkills.List(ctx, learning.SkillPartition(partition), learning.SkillList{})
	if err != nil || len(localSkillPage.Versions) != 0 {
		t.Fatalf("remote wrapper leaked untransformed skill partition: %+v, err=%v", localSkillPage.Versions, err)
	}
}

func TestADR_0259_IncompleteTerminalEvidenceRemainsRetryable(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(45, 0)}
	repository, partition, created := newAttemptWorkerRecord(t, clock)
	worker := attemptWorker{
		repository: repository, partition: partition, id: created.ID,
		evidence: func(context.Context, learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			return learning.FailureNone, errLearningEvidenceNotReady
		},
	}

	got, err := worker.Run(context.Background())
	if !errors.Is(err, errLearningEvidenceNotReady) {
		t.Fatalf("Run error = %v, want retryable evidence-not-ready", err)
	}
	if got.State != learning.AttemptRunning || got.State.Terminal() || got.FailureCode != learning.FailureNone || got.ClaimExpiresAt.IsZero() {
		t.Fatalf("incomplete event sequence did not enter persisted retry backoff: %+v", got)
	}
}

func TestAttemptRecoveryTransientSetupRetriesAreBoundedAcrossRestart(t *testing.T) {
	t.Parallel()
	clock := &attemptWorkerClock{now: time.Unix(46, 0)}
	repository, partition, created := newAttemptWorkerRecord(t, clock)
	setupErr := errors.New("provider setup leaked secret-token")
	claimedSetups := 0

	for attempt := 1; attempt <= defaultAttemptSetupMaxAttempts; attempt++ {
		worker := attemptWorker{
			repository: repository, partition: partition, id: created.ID, claimTTL: time.Second, setupRetryBase: time.Second,
			prepare: func(_ context.Context, record learning.AttemptRecord) (learning.AttemptFailureCode, error) {
				if record.State != learning.AttemptRunning || record.ClaimGeneration == 0 {
					t.Fatalf("setup ran before claim: %+v", record)
				}
				claimedSetups++
				return learning.FailureNone, errors.Join(errAttemptSetupTransient, setupErr)
			},
		}
		got, err := worker.Run(context.Background())
		if !errors.Is(err, errAttemptSetupTransient) {
			t.Fatalf("attempt %d error = %v, want transient setup classification", attempt, err)
		}
		if attempt < defaultAttemptSetupMaxAttempts {
			if got.State != learning.AttemptRunning || got.State.Terminal() || got.FailureCode != learning.FailureNone {
				t.Fatalf("attempt %d prematurely terminal = %+v", attempt, got)
			}
			delay := attemptSetupRetryBackoff(time.Second, got.ClaimGeneration)
			if !got.ClaimExpiresAt.Equal(clock.now.Add(delay)) {
				t.Fatalf("attempt %d persisted backoff expiry = %v, want %v", attempt, got.ClaimExpiresAt, clock.now.Add(delay))
			}
			clock.now = clock.now.Add(delay)
			continue
		}
		if got.State != learning.AttemptFailed || got.FailureCode != learning.FailureRetryExhausted {
			t.Fatalf("bounded terminal = %+v, want failed/retry_exhausted", got)
		}
		if strings.Contains(string(got.FailureCode), "secret-token") {
			t.Fatalf("raw setup error persisted in terminal: %+v", got)
		}
	}
	if claimedSetups != defaultAttemptSetupMaxAttempts {
		t.Fatalf("claimed setup attempts = %d, want %d", claimedSetups, defaultAttemptSetupMaxAttempts)
	}
}

func TestADR_0259_IndependentDownstreamCommitReconcilesAfterClaimLoss(t *testing.T) {
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
		repository: attempts, partition: attemptPartition, id: created.ID, claimTTL: time.Second,
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
