package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	memoryadapter "github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const defaultAttemptDiscoveryInterval = time.Second

type attemptRecovery struct {
	cancel context.CancelFunc
	done   sync.WaitGroup
}

func newAttemptRecoveryLoop(parent context.Context, repository learning.AttemptRepository, interval time.Duration, process func(context.Context, learning.AttemptWork) error, reportDiscoveryError func(error)) *attemptRecovery {
	if interval <= 0 {
		interval = defaultAttemptDiscoveryInterval
	}
	ctx, cancel := context.WithCancel(parent)
	recovery := &attemptRecovery{cancel: cancel}
	recovery.done.Add(1)
	go func() {
		defer recovery.done.Done()
		timer := time.NewTimer(0)
		defer timer.Stop()
		failedDiscovery := false
		var cursor learning.AttemptWorkCursor
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			page, err := repository.DiscoverWork(ctx, learning.AttemptWorkList{After: cursor, Limit: learning.MaxAttemptWorkBatch})
			if err != nil {
				if !failedDiscovery && reportDiscoveryError != nil && !errors.Is(err, context.Canceled) {
					reportDiscoveryError(err)
				}
				failedDiscovery = true
			} else {
				failedDiscovery = false
				for _, item := range page.Work {
					if ctx.Err() != nil {
						return
					}
					_ = process(ctx, item)
				}
			}
			if err == nil && page.Next != (learning.AttemptWorkCursor{}) {
				cursor = page.Next
				timer.Reset(0)
			} else {
				if err == nil {
					cursor = learning.AttemptWorkCursor{}
				}
				timer.Reset(interval)
			}
		}
	}()
	return recovery
}

func startAttemptRecovery(parent context.Context, cfg Config, reg *providerRegistry, sessions port.SessionStore, events port.EventLog, repository learning.AttemptRepository, proposals learning.ProposalRepository, assets catalogAssets, placements server.PlacementProvider, placementScope server.PlacementScope) *attemptRecovery {
	if repository == nil || events == nil || proposals == nil {
		return nil
	}
	recovery := newAttemptRecoveryLoop(parent, repository, defaultAttemptDiscoveryInterval, func(ctx context.Context, item learning.AttemptWork) error {
		err := recoverAttempt(ctx, cfg, reg, sessions, events, repository, proposals, assets, placements, placementScope, item)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errLearningEvidenceNotReady) && !errors.Is(err, learning.ErrAttemptClaimConflict) && !errors.Is(err, learning.ErrAttemptVersionConflict) {
			cfg.diag().Log(ctx, port.LevelWarn, "durable learning attempt recovery failed", "attempt_id", item.Record.ID)
		}
		return err
	}, func(error) {
		cfg.diag().Log(parent, port.LevelWarn, "durable learning attempt discovery unavailable")
	})
	return recovery
}

func (r *attemptRecovery) Close() {
	if r == nil {
		return
	}
	r.cancel()
	r.done.Wait()
}

//nolint:gocyclo // exact-source reconstruction and claim-fenced publication stay visibly ordered
func recoverAttempt(ctx context.Context, cfg Config, reg *providerRegistry, sessions port.SessionStore, events port.EventLog, repository learning.AttemptRepository, proposals learning.ProposalRepository, assets catalogAssets, placements server.PlacementProvider, placementScope server.PlacementScope, item learning.AttemptWork) error {
	loader := newLearningEvidenceLoader(sessions, events)
	var source *session.Session
	var workspace string
	var closePlacement func() error
	defer func() {
		if closePlacement != nil {
			_ = closePlacement()
		}
	}()
	var reflector *agent.EvidenceReflector
	var workerCfg Config
	var input learning.Input
	var projection learning.Projection
	var loadErr error
	load := func(loadCtx context.Context, record learning.AttemptRecord) learning.AttemptFailureCode {
		var failure learning.AttemptFailureCode
		input, projection, failure, loadErr = loader.loadForExecution(loadCtx, item.Partition, record, workspace)
		return failure
	}
	setupProvider := func() error {
		providerID := source.ProviderID
		if providerID == "" {
			providerID = reg.Default()
		}
		entry, ok := reg.Lookup(providerID)
		if !ok || entry.provider == nil {
			return errAttemptSetupTransient
		}
		model := source.ModelID
		if model == "" {
			model = reg.DefaultModelFor(providerID)
			if model == "" && providerID == reg.Default() {
				model = cfg.Model
			}
		}
		workerCfg = cfg
		workerCfg.Workspace = workspace
		workerCfg.Model = model
		workerCfg.LearningMode, workerCfg.LearningSensitivity, workerCfg.SkillActivationPolicy = learningPolicyForWorkspace(cfg, workspace)
		var err error
		reflector, err = agent.NewEvidenceReflector(entry.provider, model, buildTokenCounter(workerCfg), agent.ReflectionLimits{})
		if err != nil {
			return errors.Join(errAttemptSetupTransient, err)
		}
		return nil
	}
	worker := attemptWorker{
		repository:      repository,
		partition:       item.Partition,
		id:              item.Record.ID,
		callbackTimeout: cfg.LearningAttemptTimeout,
		prepare: func(prepareCtx context.Context, record learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			loaded, err := sessions.Load(prepareCtx, record.Provenance.Source.SessionID)
			if err != nil || loaded == nil {
				return learning.FailureEvidenceUnavailable, nil
			}
			source = loaded
			reattacher, ok := placements.(server.PlacementReattacher)
			if !ok {
				return learning.FailureNone, errAttemptSetupTransient
			}
			binding, err := reattacher.Reattach(prepareCtx, server.PlacementReattachRequest{
				Ref: source.EnvironmentRef, Principal: source.Owner, Scope: placementScope,
			})
			if err != nil || binding.Ref != source.EnvironmentRef || binding.Environment.Workspace() == nil {
				return learning.FailureNone, errAttemptSetupTransient
			}
			workspace, err = server.PlacementGovernanceRoot(binding)
			if err != nil {
				return learning.FailureNone, errAttemptSetupTransient
			}
			closePlacement = binding.Close
			if record.CheckpointStage == learning.AttemptCheckpointNone {
				return learning.FailureNone, nil
			}
			return learning.FailureNone, setupProvider()
		},
		evidence: func(evidenceCtx context.Context, record learning.AttemptRecord) (learning.AttemptFailureCode, error) {
			failure := load(evidenceCtx, record)
			if failure != learning.FailureNone || loadErr != nil {
				return failure, loadErr
			}
			return learning.FailureNone, setupProvider()
		},
		reflect: func(reflectCtx context.Context) (learning.Outcome, error) {
			if len(projection.Messages) == 0 {
				current, found, getErr := repository.Get(reflectCtx, item.Partition, item.Record.ID)
				if getErr != nil {
					return learning.Outcome{}, getErr
				}
				if !found || load(reflectCtx, current) != learning.FailureNone {
					return learning.Outcome{}, learning.ErrInvalidEvidence
				}
			}
			return reflector.ReflectProjection(reflectCtx, projection)
		},
		publish: func(publishCtx context.Context, outcome learning.Outcome) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
			if len(outcome.Candidates) == 0 {
				return learning.AttemptCheckpoint{}, learning.FailureEvaluationRejected, nil
			}
			digest := string(item.Record.Provenance.Source.CanonicalDigest)
			principal := reflectionPrincipal(source.Owner)
			procedure := buildProcedureProcessor(workerCfg, assets)
			processed, processErr := processReflectionOutcome(memoryadapter.WithWorkspace(publishCtx, workspace), proposals,
				assets.userModelStore, assets.memStore, principal, input, digest, outcome, input.Signals,
				workerCfg.LearningMode, projectIngestionAdmitted(workerCfg), workspace, procedure)
			if processErr != nil {
				failure := learning.FailurePublicationFailed
				if errors.Is(processErr, learning.ErrInvalidOutcome) || errors.Is(processErr, learning.ErrInvalidProposal) {
					failure = learning.FailureEvaluationRejected
				}
				return learning.AttemptCheckpoint{}, failure, processErr
			}
			return checkpointFromReflectionReceipt(processed)
		},
	}
	_, err := worker.Run(ctx)
	return err
}
