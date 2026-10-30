package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type eligibilityRecoveryPlacement struct {
	appTestPlacementProvider
	calls int
}

func (p *eligibilityRecoveryPlacement) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.calls++
	return p.appTestPlacementProvider.Reattach(ctx, req)
}

type eligibilityRecoveryProposals struct {
	*memproposal.Store
	calls int
}

func (p *eligibilityRecoveryProposals) StageBatch(ctx context.Context, part learning.ProposalPartition, digest string, candidates []learning.Candidate, signals []learning.Signal) ([]learning.ProposalRecord, error) {
	p.calls++
	return p.Store.StageBatch(ctx, part, digest, candidates, signals)
}

func (p *eligibilityRecoveryProposals) StageBatchManifest(ctx context.Context, part learning.ProposalPartition, manifest learning.MaterializationManifest, candidates []learning.Candidate, signals []learning.Signal) ([]learning.ProposalRecord, error) {
	p.calls++
	return p.Store.StageBatchManifest(ctx, part, manifest, candidates, signals)
}

func TestAttemptRecoveryPersistedSourceEligibility(t *testing.T) {
	for _, stage := range []learning.AttemptCheckpointStage{learning.AttemptCheckpointEvidenceVerified, learning.AttemptCheckpointReflectionComplete} {
		t.Run(string(stage), func(t *testing.T) {
			for _, excluded := range []bool{false, true} {
				name := "eligible"
				if excluded {
					name = "excluded"
				}
				t.Run(name, func(t *testing.T) {
					ctx := t.Context()
					// Reuse the current, exact-run fixture, including real prompt and
					// trajectory digests, rather than a fabricated admission identity.
					seed, input, events := secondRunPublicationInput(t)
					source, err := seed.Load(ctx, input.Trajectory.SessionID)
					if err != nil {
						t.Fatal(err)
					}
					sourceDir := t.TempDir()
					sources, err := jsonlstore.New(sourceDir)
					if err != nil {
						t.Fatal(err)
					}
					if !source.LearningEligible() {
						t.Fatal("fixture must initially be eligible")
					}
					if err := sources.Save(ctx, source); err != nil {
						t.Fatal(err)
					}
					for _, event := range events {
						if err := sources.Append(ctx, source.ID, event); err != nil {
							t.Fatal(err)
						}
					}
					digest, err := automaticTrajectoryDigest(input)
					if err != nil {
						t.Fatal(err)
					}
					prompt, err := currentPromptBinding(input, learning.AdmissionHostRequested)
					if err != nil {
						t.Fatal(err)
					}
					attemptSource := learning.AttemptSource{SessionID: source.ID, RunID: learning.DurableRunID(source.RunID()), CanonicalDigest: learning.CanonicalDigest(digest)}
					provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHostRequested, attemptSource, prompt)
					if err != nil {
						t.Fatal(err)
					}
					principal := reflectionPrincipal(source.Owner)
					partition, err := learning.DeriveAttemptPartition(principal)
					if err != nil {
						t.Fatal(err)
					}
					id, err := learning.DeterministicAttemptID(principal, attemptSource)
					if err != nil {
						t.Fatal(err)
					}
					clock := &attemptWorkerClock{now: time.Unix(125, 0)}
					attempts := memattempt.New(clock)
					record, err := attempts.Create(ctx, partition, learning.AttemptCreate{ID: id, Provenance: provenance})
					if err != nil {
						t.Fatal(err)
					}
					// Prove the persisted fixture was executable before checkpointing
					// and excluding it, so another evidence defect cannot mask the guard.
					if _, _, failure, err := newLearningEvidenceLoader(sources, sources).loadForExecution(ctx, partition, record, input.Trajectory.Workspace); err != nil || failure != learning.FailureNone {
						t.Fatalf("initial evidence: failure=%s err=%v", failure, err)
					}
					record, claim, err := attempts.AcquireClaim(ctx, partition, id, record.Version, time.Second)
					if err != nil {
						t.Fatal(err)
					}
					for _, checkpoint := range []learning.AttemptCheckpointStage{learning.AttemptCheckpointEvidenceVerified, learning.AttemptCheckpointReflectionComplete} {
						record, err = attempts.Checkpoint(ctx, partition, id, record.Version, claim, learning.AttemptCheckpoint{Stage: checkpoint})
						if err != nil {
							t.Fatal(err)
						}
						if checkpoint == stage {
							break
						}
					}
					if excluded {
						source.ExcludeFromLearning()
						if err := sources.Save(ctx, source); err != nil {
							t.Fatal(err)
						}
					}
					// Recovery sees a newly opened codec boundary, not the source pointer.
					sources, err = jsonlstore.New(sourceDir)
					if err != nil {
						t.Fatal(err)
					}
					reloaded, err := sources.Load(ctx, source.ID)
					if err != nil || reloaded.LearningEligible() == excluded {
						t.Fatalf("persisted eligibility: source=%+v err=%v", reloaded, err)
					}
					clock.now = clock.now.Add(2 * time.Second)
					work, err := attempts.DiscoverWork(ctx, learning.AttemptWorkList{Limit: 1})
					if err != nil || len(work.Work) != 1 || work.Work[0].Record.CheckpointStage != stage {
						t.Fatalf("expired checkpoint not recoverable: %+v err=%v", work, err)
					}

					provider := mockllm.New(mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"operator_fact","key":"user/output","value":"concise","evidence":["m:1"]}]}`))
					registry := regForTest(provider, providerOpenAI, "mock")
					proposals := &eligibilityRecoveryProposals{Store: memproposal.New()}
					placements := &eligibilityRecoveryPlacement{}
					memory, skills := memmemory.New(), memskill.New()
					cfg := Config{Model: "mock", UserModelDir: t.TempDir(), LearningMode: learning.Review, LearningSensitivity: learning.Balanced}
					if err := recoverAttempt(ctx, cfg, registry, sources, sources, attempts, proposals, catalogAssets{userModelStore: memory, learnedSkills: skills, reflectionRepository: proposals}, placements, "legacy-local", work.Work[0]); err != nil {
						t.Fatal(err)
					}
					after, found, err := attempts.Get(ctx, partition, id)
					if err != nil || !found || after.Version == record.Version {
						t.Fatalf("recovery did not advance attempt: %+v found=%v err=%v", after, found, err)
					}
					proposalPart := learning.ProposalPartition{Principal: principal}
					page, err := proposals.List(ctx, proposalPart, learning.ProposalList{})
					if err != nil {
						t.Fatal(err)
					}
					if excluded {
						if after.State != learning.AttemptFailed || after.FailureCode != learning.FailureEvidenceUnavailable || after.ProposalID != "" || after.SkillID != "" {
							t.Fatalf("excluded source recovered: %+v", after)
						}
						if placements.calls != 0 || provider.Calls() != 0 || proposals.calls != 0 || len(page.Records) != 0 {
							t.Fatalf("excluded source caused effects: placement=%d provider=%d publication=%d proposals=%d", placements.calls, provider.Calls(), proposals.calls, len(page.Records))
						}
					} else {
						if after.State != learning.AttemptCompleted || after.Outcome != learning.AttemptOutcomeSucceeded || after.FailureCode != learning.FailureNone || after.ProposalID == "" {
							t.Fatalf("eligible source failed recovery: %+v", after)
						}
						if placements.calls != 1 || provider.Calls() != 1 || proposals.calls != 1 || len(page.Records) != 1 || page.Records[0].ID != after.ProposalID {
							t.Fatalf("eligible recovery effects: placement=%d provider=%d publication=%d proposals=%+v", placements.calls, provider.Calls(), proposals.calls, page.Records)
						}
					}
					versions, err := skills.List(ctx, learning.SkillPartition{Principal: principal, Project: input.Trajectory.Workspace}, learning.SkillList{})
					if err != nil || len(versions.Versions) != 0 {
						t.Fatalf("unexpected skill effects: %+v err=%v", versions, err)
					}
					entries, err := memory.List(ctx, "")
					if err != nil || len(entries) != 0 {
						t.Fatalf("unexpected memory effects in review mode: %+v err=%v", entries, err)
					}
				})
			}
		})
	}
}
