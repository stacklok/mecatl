package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/attemptstore"
	memoryadapter "github.com/stacklok/mecatl/internal/adapter/memory"
)

type attemptRecovery struct {
	cancel context.CancelFunc
	done   sync.WaitGroup
}

func startAttemptRecovery(parent context.Context, cfg Config, reg *providerRegistry, sessions port.SessionStore, events port.EventLog, repository learning.AttemptRepository, proposals learning.ProposalRepository, assets catalogAssets) (*attemptRecovery, error) {
	source, ok := repository.(interface {
		Pending(context.Context) ([]attemptstore.PendingAttempt, error)
	})
	if !ok || events == nil || proposals == nil {
		return nil, nil
	}
	pending, err := source.Pending(parent)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithCancel(parent)
	recovery := &attemptRecovery{cancel: cancel}
	recovery.done.Add(1)
	go func() {
		defer recovery.done.Done()
		for _, item := range pending {
			if ctx.Err() != nil {
				return
			}
			if err := recoverAttempt(ctx, cfg, reg, sessions, events, repository, proposals, assets, item); err != nil &&
				!errors.Is(err, context.Canceled) && !errors.Is(err, learning.ErrAttemptClaimConflict) && !errors.Is(err, learning.ErrAttemptVersionConflict) {
				cfg.diag().Log(ctx, port.LevelWarn, "durable learning attempt recovery failed", "attempt_id", item.Record.ID)
			}
		}
	}()
	return recovery, nil
}

func (r *attemptRecovery) Close() {
	if r == nil {
		return
	}
	r.cancel()
	r.done.Wait()
}

//nolint:gocyclo // exact-source reconstruction and claim-fenced publication stay visibly ordered
func recoverAttempt(ctx context.Context, cfg Config, reg *providerRegistry, sessions port.SessionStore, events port.EventLog, repository learning.AttemptRepository, proposals learning.ProposalRepository, assets catalogAssets, item attemptstore.PendingAttempt) error {
	source, err := sessions.Load(ctx, item.Record.Provenance.Source.SessionID)
	if err != nil {
		return err
	}
	providerID := source.ProviderID
	if providerID == "" {
		providerID = reg.Default()
	}
	entry, ok := reg.Lookup(providerID)
	if !ok || entry.provider == nil {
		return errors.New("learning source provider is unavailable")
	}
	model := source.ModelID
	if model == "" {
		model = reg.DefaultModelFor(providerID)
		if model == "" && providerID == reg.Default() {
			model = cfg.Model
		}
	}
	workerCfg := cfg
	workerCfg.Workspace = source.Workspace
	workerCfg.Model = model
	workerCfg.LearningMode, workerCfg.LearningSensitivity, workerCfg.SkillActivationPolicy = learningPolicyForWorkspace(cfg, source.Workspace)
	reflector, err := agent.NewEvidenceReflector(entry.provider, model, buildTokenCounter(workerCfg), agent.ReflectionLimits{})
	if err != nil {
		return err
	}
	loader := newLearningEvidenceLoader(sessions, events)
	var input learning.Input
	var projection learning.Projection
	load := func(record learning.AttemptRecord) learning.AttemptFailureCode {
		var failure learning.AttemptFailureCode
		input, projection, failure = loader.loadInput(ctx, item.Partition, record)
		return failure
	}
	procedure := buildProcedureProcessor(workerCfg, assets)
	worker := attemptWorker{
		repository: repository,
		partition:  item.Partition,
		id:         item.Record.ID,
		now:        time.Now,
		evidence: func(_ context.Context, record learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			return load(record), nil
		},
		reflect: func(reflectCtx context.Context) (learning.Outcome, error) {
			if len(projection.Messages) == 0 {
				current, found, getErr := repository.Get(reflectCtx, item.Partition, item.Record.ID)
				if getErr != nil {
					return learning.Outcome{}, getErr
				}
				if !found || load(current) != learning.FailureNone {
					return learning.Outcome{}, learning.ErrInvalidEvidence
				}
			}
			return reflector.ReflectProjection(reflectCtx, projection)
		},
		publish: func(publishCtx context.Context, outcome learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
			if len(outcome.Candidates) == 0 {
				return learning.AttemptCheckpoint{}, learning.FailureEvaluationRejected, nil
			}
			digest, digestErr := reflectionInputDigest(input)
			if digestErr != nil {
				return learning.AttemptCheckpoint{}, learning.FailureEvaluationRejected, digestErr
			}
			principal := reflectionPrincipal(source.Owner)
			processed, processErr := processReflectionOutcome(memoryadapter.WithWorkspace(publishCtx, source.Workspace), proposals,
				assets.userModelStore, assets.memStore, principal, input, digest, outcome, input.Signals,
				workerCfg.LearningMode, projectIngestionAdmitted(workerCfg), workerCfg.Workspace, procedure)
			if processErr != nil {
				failure := learning.FailurePublicationFailed
				if errors.Is(processErr, learning.ErrInvalidOutcome) || errors.Is(processErr, learning.ErrInvalidProposal) {
					failure = learning.FailureEvaluationRejected
				}
				return learning.AttemptCheckpoint{}, failure, processErr
			}
			_ = processed
			candidate := outcome.Candidates[0]
			partition := learning.ProposalPartition{Principal: principal}
			if candidate.Kind == learning.CandidateProjectFact || candidate.Kind == learning.CandidateProcedure {
				partition.Project = source.Workspace
			}
			proposalID, idErr := learning.DeterministicProposalID(partition, digest, candidate)
			if idErr != nil {
				return learning.AttemptCheckpoint{}, learning.FailureEvaluationRejected, idErr
			}
			return learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: proposalID}, learning.FailureNone, nil
		},
	}
	_, err = worker.Run(ctx)
	return err
}
