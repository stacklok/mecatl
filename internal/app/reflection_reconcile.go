package app

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/memorypromotion"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	memoryadapter "github.com/stacklok/mecatl/internal/adapter/memory"
)

type promotingProposalSource interface {
	Promoting(context.Context) ([]learning.ProposalRecord, error)
}

func proposalSourcesEligible(ctx context.Context, sources port.SessionStore, record learning.ProposalRecord) bool {
	if sources == nil || len(record.Candidate.Evidence) == 0 || len(record.Candidate.Evidence) > learning.MaxCandidateEvidence {
		return false
	}
	for _, ref := range record.Candidate.Evidence {
		if ref.Protocol != learning.ReflectionEvidenceV2 {
			return false
		}
		source, err := sources.Load(ctx, ref.SessionID)
		if err != nil || source == nil || source.ID != ref.SessionID || !source.LearningEligible() {
			return false
		}
	}
	return true
}

// reconcilePromotingProposals runs only during Build, before the Service is
// published and before this process can start a promotion worker. A promoting
// record is therefore known to belong to a prior process, unlike a read-side
// List/Get observation.
func reconcilePromotingProposals(ctx context.Context, cfg Config, repository learning.ProposalRepository, operatorMemory, projectMemory tool.MemoryStore) error {
	source, ok := repository.(promotingProposalSource)
	if !ok {
		return nil
	}
	records, err := source.Promoting(ctx)
	if err != nil {
		return fmt.Errorf("list promoting proposals: %w", err)
	}
	for _, record := range records {
		if !proposalSourcesEligible(ctx, cfg.learningSourceStore, record) {
			continue
		}
		store := operatorMemory
		if record.Partition.Project != "" {
			if !projectIngestionAdmittedForRoot(cfg, record.Partition.Project) {
				continue
			}
			store = projectMemory
		}
		if store == nil {
			continue
		}
		promotionCtx := memoryadapter.WithWorkspace(ctx, record.Partition.Project)
		if _, err := (memorypromotion.Promoter{Proposals: repository, Memory: store}).Process(
			promotionCtx, record.Partition, record.ID, record.Version,
			memorypromotion.PolicyInput{Mode: learning.Review, TrustedProject: projectIngestionAdmittedForRoot(cfg, record.Partition.Project), Approved: true},
		); err != nil {
			return fmt.Errorf("reconcile promoting proposal %q: %w", record.ID, err)
		}
	}
	return nil
}
