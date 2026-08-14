package app

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/adapter/skilllifecycle"
	"github.com/stacklok/mecatl/engine/adapter/skillmaterialize"
	"github.com/stacklok/mecatl/engine/adapter/skillvalidation"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/tool"
)

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

type learnedSkillPublisher struct {
	repository learning.SkillRepository
	partitions []learning.SkillPartition
	owner      string
	catalog    *skillfs.AtomicCatalog
	external   []tool.SkillMeta
	source     tool.SkillSource
}

func (p learnedSkillPublisher) Publish(ctx context.Context) error {
	var active []learning.SkillVersion
	for _, partition := range p.partitions {
		versions, err := listActiveLearnedSkills(ctx, p.repository, partition, p.owner)
		if err != nil {
			return err
		}
		active = append(active, versions...)
	}
	p.catalog.Refresh(p.external, p.source, active)
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

func buildProcedureProcessor(cfg Config, assets catalogAssets) func(context.Context, learning.ProposalRecord, learning.Mode) error {
	if cfg.LearningMode == learning.Off || assets.learnedSkills == nil || assets.reflectionRepository == nil {
		return nil
	}
	return func(ctx context.Context, record learning.ProposalRecord, mode learning.Mode) error {
		partition := learning.SkillPartition{Principal: record.Partition.Principal, Project: record.Partition.Project}
		inventory := make([]learning.SkillInventoryItem, 0, len(assets.skills))
		for _, meta := range assets.skills {
			inventory = append(inventory, learning.SkillInventoryItem{Name: meta.Name})
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
			publisher = learnedSkillPublisher{repository: assets.learnedSkills, partitions: partitions, owner: assets.skillOwner, catalog: assets.liveSkills, external: assets.skills, source: assets.skillSource}
		} else if mode == learning.Auto {
			// A shared process catalog cannot safely expose another caller/project
			// partition. Keep it staged until a partition-bound catalog is available.
			mode = learning.Review
		}
		_, err = (skilllifecycle.Pipeline{Repository: assets.learnedSkills, Validator: skillvalidation.Validator{}, Evaluator: evaluator, Publisher: publisher}).Process(ctx, skilllifecycle.Candidate{Draft: materialized.Input, Inventory: inventory, Mode: mode, Automatic: true})
		return err
	}
}
