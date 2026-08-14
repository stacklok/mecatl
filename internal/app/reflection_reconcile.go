package app

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/memorypromotion"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/tool"
	memoryadapter "github.com/stacklok/mecatl/internal/adapter/memory"
)

type promotingProposalSource interface {
	Promoting(context.Context) ([]learning.ProposalRecord, error)
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
		store := operatorMemory
		if record.Partition.Project != "" {
			if !projectIngestionAdmittedForRoot(cfg, record.Partition.Project) {
				continue
			}
			store = projectMemory
		}
		if _, ok := store.(tool.MemoryConvergenceStore); !ok {
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
