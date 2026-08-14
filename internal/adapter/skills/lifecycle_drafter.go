package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/skilllifecycle"
	"github.com/stacklok/mecatl/engine/adapter/skillvalidation"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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
	return d.draft(ctx, d.partition, d.owner, request)
}

// DraftIn derives model-facing draft ownership from the verified caller and
// exact live workspace. Missing identity or workspace disables drafting rather
// than writing to a process-global ownerless partition.
func (d *LifecycleDrafter) DraftIn(ctx context.Context, env tool.Environment, request DraftRequest) (DraftResult, error) {
	principal := session.PrincipalFromContext(ctx)
	if principal == nil || principal.Issuer == "" || principal.Subject == "" || env.Workspace() == nil || env.Workspace().Root() == "" {
		return DraftResult{}, fmt.Errorf("SkillDraft unavailable: verified caller identity and workspace are required")
	}
	sum := sha256.Sum256([]byte(principal.Issuer + "\x00" + principal.Subject))
	return d.draft(ctx, learning.SkillPartition{Principal: hex.EncodeToString(sum[:]), Project: env.Workspace().Root()}, "main", request)
}

func (d *LifecycleDrafter) draft(ctx context.Context, partition learning.SkillPartition, owner string, request DraftRequest) (DraftResult, error) {
	input := learning.SkillDraftInput{Partition: partition, OwnerAgent: owner, Bundle: learning.SkillBundle{Name: request.Name, Description: request.Description, Body: request.Body}, Provenance: learning.SkillProvenance{Origin: learning.SkillProvenanceLegacyModel}}
	receipt, err := (skilllifecycle.Pipeline{Repository: d.repository, Validator: skillvalidation.Validator{}}).Process(ctx, skilllifecycle.Candidate{Draft: input, Inventory: d.inventory})
	if err != nil {
		return DraftResult{}, err
	}
	return DraftResult{Path: fmt.Sprintf("skill://%s/%s", receipt.SkillID, receipt.Version), Warnings: []string{"draft is versioned and inactive; evidence and evaluation are required before activation"}}, nil
}

var _ Drafter = (*LifecycleDrafter)(nil)
