package skills

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/skilllifecycle"
	"github.com/stacklok/mecatl/engine/adapter/skillvalidation"
	"github.com/stacklok/mecatl/engine/learning"
)

// LifecycleDrafter persists explicit model drafts as versioned, inactive
// agent-owned records. It never writes to or publishes the live catalog.
type LifecycleDrafter struct {
	repository learning.SkillRepository
	partition  learning.SkillPartition
	owner      string
	inventory  []learning.SkillInventoryItem
}

// NewLifecycleDrafter binds explicit model drafts to an inactive lifecycle partition.
func NewLifecycleDrafter(repository learning.SkillRepository, partition learning.SkillPartition, owner string, inventory []learning.SkillInventoryItem) *LifecycleDrafter {
	return &LifecycleDrafter{repository: repository, partition: partition, owner: owner, inventory: append([]learning.SkillInventoryItem(nil), inventory...)}
}

// Draft validates and persists one inactive versioned draft.
func (d *LifecycleDrafter) Draft(ctx context.Context, request DraftRequest) (DraftResult, error) {
	input := learning.SkillDraftInput{Partition: d.partition, OwnerAgent: d.owner, Bundle: learning.SkillBundle{Name: request.Name, Description: request.Description, Body: request.Body}, Provenance: learning.SkillProvenance{Origin: learning.SkillProvenanceLegacyModel}}
	receipt, err := (skilllifecycle.Pipeline{Repository: d.repository, Validator: skillvalidation.Validator{}}).Process(ctx, skilllifecycle.Candidate{Draft: input, Inventory: d.inventory})
	if err != nil {
		return DraftResult{}, err
	}
	return DraftResult{Path: fmt.Sprintf("skill://%s/%s", receipt.SkillID, receipt.Version), Warnings: []string{"draft is versioned and inactive; evidence and evaluation are required before activation"}}, nil
}

var _ Drafter = (*LifecycleDrafter)(nil)
