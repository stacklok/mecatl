package skillstore

import (
	"context"

	"github.com/stacklok/mecatl/engine/adapter/skillmaterialize"
	"github.com/stacklok/mecatl/engine/learning"
)

// Materialization is the recoverable result of turning a procedure proposal into
// an unevaluated skill draft and linking the proposal to that draft.
type Materialization = skillmaterialize.Result

// MaterializeProposal preserves the root adapter entry point while delegating
// portable validation and reconciliation to the importable engine adapter.
func MaterializeProposal(ctx context.Context, proposals learning.ProposalRepository, skills learning.SkillRepository, partition learning.SkillPartition, owner string, record learning.ProposalRecord, inventory []learning.SkillInventoryItem, decision learning.Decision) (Materialization, error) {
	return skillmaterialize.Materialize(ctx, proposals, skills, partition, owner, record, inventory, decision)
}
