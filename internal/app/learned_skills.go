package app

import (
	"context"
	"fmt"
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
	if assets.liveSkills == nil || assets.learnedSkills == nil || len(partitions) == 0 {
		return partitions
	}
	publisher := learnedSkillPublisher{repository: assets.learnedSkills, partitions: partitions, catalog: assets.liveSkills, serial: assets.skillPublication}
	if err := publisher.Publish(ctx); err != nil {
		cfg.diag().Log(ctx, port.LevelWarn, "learned-skill hydration failed; caller partition quarantined", "err", err)
	}
	return partitions
}

func listActiveLearnedSkills(ctx context.Context, repository learning.SkillRepository, partition learning.SkillPartition, owner string) ([]learning.SkillVersion, error) {
	var out []learning.SkillVersion
	var after learning.SkillID
	for {
		page, err := repository.List(ctx, partition, learning.SkillList{After: after, Limit: learning.MaxSkillPageSize, State: learning.SkillActive, OwnerAgent: owner})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Versions...)
		if page.Next == "" {
			return out, nil
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

func (p learnedSkillPublisher) Quarantine(name string) {
	if p.catalog != nil && len(p.partitions) > 0 {
		p.catalog.RevokePartition(p.partitions[len(p.partitions)-1], name)
	}
}

func (p learnedSkillPublisher) Publish(ctx context.Context) error {
	if p.serial != nil {
		p.serial.mu.Lock()
		defer p.serial.mu.Unlock()
	}
	var active []learning.SkillVersion
	for _, partition := range p.partitions {
		versions, err := listActiveLearnedSkills(ctx, p.repository, partition, p.owner)
		if err != nil {
			p.catalog.ClearPartitions(p.partitions...)
			return err
		}
		active = append(active, versions...)
	}
	p.catalog.RefreshPartitions(p.partitions, active)
	return nil
}

type abstainingSkillEvaluator struct{}

func (abstainingSkillEvaluator) Evaluate(_ context.Context, request learning.SkillEvaluationRequest) (learning.SkillEvaluation, error) {
	fixtures := make([]string, 0, len(request.Version.Provenance.EvidenceRefs))
	for i, ref := range request.Version.Provenance.EvidenceRefs {
		fixtures = append(fixtures, fmt.Sprintf("evidence-%d-%s", i+1, ref.Digest[:min(len(ref.Digest), 16)]))
	}
	if len(fixtures) == 0 {
		fixtures = []string{"no-evidence"}
	}
	return learning.SkillEvaluation{Verdict: learning.EvaluationAbstain, FixtureIDs: fixtures, Reason: "no skill evaluator configured", At: time.Now().UTC()}, nil
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
		if evaluator == nil {
			evaluator = abstainingSkillEvaluator{}
		}
		var publisher skilllifecycle.Publisher
		publishable := partition.Principal == assets.skillPartition.Principal && (partition.Project == "" || partition.Project == cfg.Workspace && projectIngestionAdmitted(cfg))
		if publishable && assets.liveSkills != nil {
			partitions := []learning.SkillPartition{assets.skillPartition}
			if partition != assets.skillPartition {
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
		_, err = (skilllifecycle.Pipeline{Repository: assets.learnedSkills, Validator: skillvalidation.Validator{}, Evaluator: evaluator, Publisher: publisher}).Process(ctx, skilllifecycle.Candidate{Draft: materialized.Input, Inventory: inventory, Mode: mode, Automatic: true})
		return err
	}
}
