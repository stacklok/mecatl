package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/adapter/skilllifecycle"
	"github.com/stacklok/mecatl/engine/adapter/skillmaterialize"
	"github.com/stacklok/mecatl/engine/adapter/skillvalidation"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func learnedSkillPartitions(ctx context.Context, workspace string, cfg Config) []learning.SkillPartition {
	global := learning.SkillPartition{Principal: reflectionPrincipal(session.PrincipalFromContext(ctx))}
	out := []learning.SkillPartition{global}
	if workspace != "" && projectIngestionAdmittedForRoot(cfg, workspace) {
		out = append(out, learning.SkillPartition{Principal: global.Principal, Project: workspace})
	}
	return out
}

func hydrateLearnedSkillPartitions(ctx context.Context, cfg Config, assets catalogAssets, workspace string) []learning.SkillPartition {
	partitions := learnedSkillPartitions(ctx, workspace, cfg)
	hydrateLearnedSkills(ctx, cfg, assets, partitions)
	return partitions
}

func hydrateLearnedSkills(ctx context.Context, cfg Config, assets catalogAssets, partitions []learning.SkillPartition) {
	if assets.liveSkills == nil || assets.learnedSkills == nil || len(partitions) == 0 {
		return
	}
	publisher := learnedSkillPublisher{repository: assets.learnedSkills, partitions: partitions, catalog: assets.liveSkills, serial: assets.skillPublication}
	if err := publisher.Publish(ctx); err != nil {
		cfg.diag().Log(ctx, port.LevelWarn, "learned-skill hydration failed; caller partition quarantined", "err", err)
	}
}

type hydratingSkillTool struct {
	live        skillfs.LiveTool
	hydrate     func(context.Context)
	specContext context.Context
}

func newHydratingSkillTool(ctx context.Context, cfg Config, assets catalogAssets, live skillfs.LiveTool, partitions []learning.SkillPartition) tool.Tool {
	return hydratingSkillTool{
		live: live,
		hydrate: func(callCtx context.Context) {
			hydrateLearnedSkills(callCtx, cfg, assets, partitions)
		},
		specContext: session.WithPrincipal(context.Background(), session.PrincipalFromContext(ctx)),
	}
}

func (t hydratingSkillTool) Spec() tool.ToolSpec {
	t.hydrate(t.specContext)
	return t.live.Spec()
}

func (hydratingSkillTool) ReadOnly() bool { return true }

func (t hydratingSkillTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	t.hydrate(ctx)
	return t.live.Execute(ctx, call, env)
}

var errLearnedSkillGenerationChanged = errors.New("learned skill partition generation changed during hydration")

func listActiveLearnedSkillsAtGeneration(ctx context.Context, repository learning.SkillRepository, partition learning.SkillPartition, owner string) ([]learning.SkillVersion, learning.SkillGeneration, error) {
	var out []learning.SkillVersion
	var after learning.SkillID
	var generation learning.SkillGeneration
	first := true
	for {
		page, err := repository.List(ctx, partition, learning.SkillList{After: after, Limit: learning.MaxSkillPageSize, State: learning.SkillActive, OwnerAgent: owner})
		if err != nil {
			return nil, 0, err
		}
		if first {
			generation, first = page.Generation, false
		} else if page.Generation != generation {
			return nil, page.Generation, errLearnedSkillGenerationChanged
		}
		out = append(out, page.Versions...)
		if page.Next == "" {
			current, generationErr := repository.Generation(ctx, partition)
			if generationErr != nil {
				return nil, generation, generationErr
			}
			if current != generation {
				return nil, current, errLearnedSkillGenerationChanged
			}
			return out, generation, nil
		}
		after = page.Next
	}
}

type learnedSkillPublication struct{ mu sync.Mutex }

type learnedSkillPublisher struct {
	repository learning.SkillRepository
	partitions []learning.SkillPartition
	owner      string
	catalog    *skillfs.AtomicCatalog
	serial     *learnedSkillPublication
}

func (learnedSkillPublisher) Quarantine(string) {
	// Publish invalidates the exact uncertain partition at its observed durable
	// generation before returning an error. A second name-based revocation here
	// could race and revoke a newer replacement.
}

func (p learnedSkillPublisher) Publish(ctx context.Context) error {
	if p.serial != nil {
		p.serial.mu.Lock()
		defer p.serial.mu.Unlock()
	}
	active := make([]learning.SkillVersion, 0)
	generations := make(map[learning.SkillPartition]learning.SkillGeneration, len(p.partitions))
	for _, partition := range p.partitions {
		generation, err := p.repository.Generation(ctx, partition)
		if err != nil {
			generation = learning.SkillGeneration(p.catalog.View(partition).Generation)
			p.catalog.ClearPartitionsAtGeneration(map[learning.SkillPartition]learning.SkillGeneration{partition: generation})
			return err
		}
		versions, observed, err := listActiveLearnedSkillsAtGeneration(ctx, p.repository, partition, p.owner)
		if err != nil {
			p.catalog.ClearPartitionsAtGeneration(map[learning.SkillPartition]learning.SkillGeneration{partition: generation})
			return err
		}
		generations[partition] = observed
		active = append(active, versions...)
	}
	p.catalog.RefreshPartitionsAtGeneration(generations, active)
	return nil
}

func learnedSkillInventory(ctx context.Context, repository learning.SkillRepository, partition learning.SkillPartition, external []tool.SkillMeta) ([]learning.SkillInventoryItem, error) {
	inventory := make([]learning.SkillInventoryItem, 0, len(external))
	for _, meta := range external {
		inventory = append(inventory, learning.SkillInventoryItem{Name: meta.Name})
	}
	var after learning.SkillID
	for {
		page, err := repository.List(ctx, partition, learning.SkillList{After: after, Limit: learning.MaxSkillPageSize})
		if err != nil {
			return nil, err
		}
		for _, version := range page.Versions {
			inventory = append(inventory, learning.SkillInventoryItem{Name: version.Bundle.Name, OwnerAgent: version.OwnerAgent, AgentOwned: true, Bundle: version.Bundle, SkillID: version.ID, Version: version.Version})
		}
		if page.Next == "" {
			return inventory, nil
		}
		after = page.Next
	}
}

//nolint:gocyclo // materialization, publication, telemetry, and fail-safe branches stay in one transaction flow
func buildProcedureProcessor(cfg Config, assets catalogAssets) func(context.Context, learning.ProposalRecord, learning.Mode) error {
	if cfg.LearningMode == learning.Off || assets.learnedSkills == nil || assets.reflectionRepository == nil {
		return nil
	}
	return func(ctx context.Context, record learning.ProposalRecord, mode learning.Mode) error {
		partition := learning.SkillPartition{Principal: record.Partition.Principal, Project: record.Partition.Project}
		inventory, err := learnedSkillInventory(ctx, assets.learnedSkills, partition, assets.skills)
		if err != nil {
			return err
		}
		materialized, err := skillmaterialize.Materialize(ctx, assets.reflectionRepository, assets.learnedSkills, partition, assets.skillOwner, record, inventory, learning.Decision{Kind: learning.DecisionApprove, Actor: "skill-pipeline", At: time.Now().UTC()})
		if err != nil {
			return err
		}
		evaluator := cfg.SkillEvaluator
		var publisher skilllifecycle.Publisher
		publishable := partition.Project == "" || (partition.Project == cfg.Workspace && projectIngestionAdmitted(cfg))
		if publishable && assets.liveSkills != nil {
			global := learning.SkillPartition{Principal: partition.Principal}
			partitions := []learning.SkillPartition{global}
			if partition != global {
				partitions = append(partitions, partition)
			}
			publisher = learnedSkillPublisher{repository: assets.learnedSkills, partitions: partitions, owner: assets.skillOwner, catalog: assets.liveSkills}
			if assets.skillPublication != nil {
				assets.skillPublication.mu.Lock()
				defer assets.skillPublication.mu.Unlock()
			}
		} else if mode == learning.Auto {
			// A shared process catalog cannot safely expose another caller/project
			// partition. Keep it staged until a partition-bound catalog is available.
			mode = learning.Review
		}
		receipt, processErr := (skilllifecycle.Pipeline{Repository: assets.learnedSkills, Validator: skillvalidation.Validator{}, Evaluator: evaluator, ActivationPolicy: cfg.SkillActivationPolicy, Publisher: publisher}).Process(ctx, skilllifecycle.Candidate{Draft: materialized.Input, Inventory: inventory, Mode: mode, Automatic: true})
		if emit := cfg.LearningMetricsEmitter; emit != nil {
			kind := learning.ActivitySkillStaged
			reason := learning.ReasonStaged
			switch {
			case processErr != nil:
				kind, reason = learning.ActivityFailed, learning.ReasonReflectionFailed
			case receipt.State == learning.SkillActive && receipt.Verdict == learning.EvaluationPass:
				kind, reason = learning.ActivitySkillActivatedEvaluated, learning.ReasonPromoted
			case receipt.State == learning.SkillActive:
				kind, reason = learning.ActivitySkillActivatedValidated, learning.ReasonPromoted
			case receipt.State == learning.SkillRejected:
				kind, reason = learning.ActivitySkillRejected, learning.ReasonConflicted
			}
			emit(learning.Activity{Kind: kind, Reason: reason, Sensitivity: cfg.LearningSensitivity, Count: 1})
		}
		if processErr != nil {
			cfg.diag().Log(ctx, port.LevelWarn, "learned-skill processing failed; candidate remains inspectable", "category", "evaluation", "count", 1)
		}
		return processErr
	}
}
