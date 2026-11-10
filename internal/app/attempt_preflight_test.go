package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type preflightFailureStore struct {
	port.SessionStore
	err error
}

func (s preflightFailureStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, s.err
}

func TestAttemptRecoveryUnprovenPreflightNeverClaimsOrFinalizes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, stage := range []learning.AttemptCheckpointStage{learning.AttemptCheckpointNone, learning.AttemptCheckpointProposalLinked, learning.AttemptCheckpointSkillLinked} {
		t.Run(string(stage), func(t *testing.T) {
			for _, failure := range []string{"timeout", "infrastructure", "nil-snapshot"} {
				t.Run(failure, func(t *testing.T) {
					clock := &attemptWorkerClock{now: time.Unix(125, 0)}
					repo, partition, before := newAttemptWorkerRecord(t, clock)
					if stage != learning.AttemptCheckpointNone {
						var claim learning.AttemptClaim
						var err error
						before, claim, err = repo.AcquireClaim(ctx, partition, before.ID, before.Version, time.Second)
						if err != nil {
							t.Fatal(err)
						}
						checkpoint := learning.AttemptCheckpoint{Stage: stage, ProposalID: "proposal-linked"}
						if stage == learning.AttemptCheckpointSkillLinked {
							checkpoint.SkillID = "skill-linked"
						}
						before, err = repo.Checkpoint(ctx, partition, before.ID, before.Version, claim, checkpoint)
						if err != nil {
							t.Fatal(err)
						}
						clock.now = clock.now.Add(2 * time.Second)
					}
					for range 2 {
						var store port.SessionStore = preflightFailureStore{}
						if failure == "infrastructure" {
							store = preflightFailureStore{err: errors.New("source backend unavailable")}
						}
						if failure == "timeout" {
							store = &blockingRecoverySessionStore{started: make(chan struct{}), returned: make(chan struct{})}
						}
						err := recoverAttempt(ctx, Config{LearningAttemptTimeout: 5 * time.Millisecond}, nil, store, nil, repo, nil, catalogAssets{}, appTestPlacementProvider{}, "legacy-local", learning.AttemptWork{Partition: partition, Record: before})
						if !errors.Is(err, errAttemptSetupTransient) {
							t.Fatalf("not retryable: %v", err)
						}
						if failure == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("lost timeout: %v", err)
						}
						after, found, err := repo.Get(ctx, partition, before.ID)
						if err != nil || !found || !reflect.DeepEqual(before, after) {
							t.Fatalf("preflight mutated attempt: before=%+v after=%+v %v", before, after, err)
						}
						page, err := repo.DiscoverWork(ctx, learning.AttemptWorkList{Limit: 1})
						if err != nil || len(page.Work) != 1 || page.Work[0].Record.Version != before.Version {
							t.Fatalf("retry no longer discoverable: %+v %v", page, err)
						}
					}
					if stage != learning.AttemptCheckpointNone {
						// Once readiness can actually be proved, linked artifacts reconcile without
						// repeating publication. Transient preflights did not consume that retry.
						ready := memstore.New()
						source := session.New(before.Provenance.Source.SessionID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "fixture", Revision: "v1"}, session.Limits{}, clock.now)
						if err := ready.Save(ctx, source); err != nil {
							t.Fatal(err)
						}
						if err := recoverAttempt(ctx, Config{}, nil, ready, nil, repo, nil, catalogAssets{}, appTestPlacementProvider{}, "legacy-local", learning.AttemptWork{Partition: partition, Record: before}); err != nil {
							t.Fatal(err)
						}
						after, _, err := repo.Get(ctx, partition, before.ID)
						if err != nil || after.State != learning.AttemptCompleted || after.Outcome != learning.AttemptOutcomeSucceeded {
							t.Fatalf("linked retry did not reconcile: %+v %v", after, err)
						}
					}
				})
			}
		})
	}
}

func TestAttemptRecoveryPreflightNotFoundKeepsMissingEvidencePolicy(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(125, 0)}
	repo, partition, record := newAttemptWorkerRecord(t, clock)
	err := recoverAttempt(t.Context(), Config{}, nil, memstore.New(), nil, repo, nil, catalogAssets{}, appTestPlacementProvider{}, "legacy-local", learning.AttemptWork{Partition: partition, Record: record})
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := repo.Get(t.Context(), partition, record.ID)
	if err != nil || after.State != learning.AttemptFailed || after.FailureCode != learning.FailureEvidenceUnavailable {
		t.Fatalf("missing source policy changed: %+v %v", after, err)
	}
}
