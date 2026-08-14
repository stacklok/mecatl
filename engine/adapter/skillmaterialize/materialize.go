// Package skillmaterialize converts evidence-backed procedure proposals into
// validated, inactive learned-skill drafts and links them by compare-and-swap.
package skillmaterialize

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/skillvalidation"
	"github.com/stacklok/mecatl/engine/learning"
)

// Result is the recoverable result of draft creation followed by proposal linkage.
type Result struct {
	Input    learning.SkillDraftInput
	Draft    learning.SkillVersion
	Proposal learning.ProposalRecord
}

// Input converts an evidence-backed named procedure to a validated draft input.
// Historical deferred_unsupported records are accepted only when passed explicitly.
func Input(ctx context.Context, partition learning.SkillPartition, owner string, record learning.ProposalRecord, inventory []learning.SkillInventoryItem) (learning.SkillDraftInput, error) {
	if record.Candidate.Kind != learning.CandidateProcedure || !learning.ValidLearnedSkillName(record.Candidate.Name) {
		return learning.SkillDraftInput{}, fmt.Errorf("%w: materialization requires a named procedure", learning.ErrInvalidCandidate)
	}
	if record.Status != learning.ProposalDeferredUnsupported && record.Status != learning.ProposalSkillMaterialized {
		return learning.SkillDraftInput{}, learning.ErrProposalTransition
	}
	if partition.Principal != record.Partition.Principal || partition.Project != record.Partition.Project {
		return learning.SkillDraftInput{}, fmt.Errorf("%w: proposal and skill partitions differ", learning.ErrInvalidSkill)
	}
	if len(record.Candidate.Evidence) == 0 {
		return learning.SkillDraftInput{}, fmt.Errorf("%w: procedure has no evidence", learning.ErrInvalidCandidate)
	}
	input := learning.SkillDraftInput{
		Partition:  partition,
		OwnerAgent: owner,
		Bundle:     learning.SkillBundle{Name: record.Candidate.Name, Description: record.Candidate.Title, Body: record.Candidate.Body},
		Provenance: learning.SkillProvenance{ProposalIDs: []learning.ProposalID{record.ID}, EvidenceRefs: record.Candidate.Evidence, Signals: record.Signals},
	}
	if _, err := (skillvalidation.Validator{}).Validate(ctx, learning.SkillValidationRequest{
		Partition: input.Partition, OwnerAgent: input.OwnerAgent, Bundle: input.Bundle,
		Provenance: input.Provenance, Inventory: inventory,
	}); err != nil {
		return learning.SkillDraftInput{}, err
	}
	return input, nil
}

// Materialize creates the inactive draft, then CAS-links the proposal. A crash
// between stores is reconciled by the content-addressed draft and ProposalID.
func Materialize(ctx context.Context, proposals learning.ProposalRepository, skills learning.SkillRepository, partition learning.SkillPartition, owner string, record learning.ProposalRecord, inventory []learning.SkillInventoryItem, decision learning.Decision) (Result, error) {
	if proposals == nil || skills == nil {
		return Result{}, errors.New("skillmaterialize: proposal and skill repositories required")
	}
	current, found, err := proposals.Get(ctx, record.Partition, record.ID)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, learning.ErrProposalNotFound
	}
	if current.Version != record.Version && current.Status != learning.ProposalSkillMaterialized {
		return Result{}, learning.ErrProposalVersionConflict
	}
	input, err := Input(ctx, partition, owner, current, inventory)
	if err != nil {
		return Result{}, err
	}
	draft, err := skills.CreateDraft(ctx, input.Partition, input.OwnerAgent, input.Bundle, input.Provenance)
	if err != nil {
		return Result{}, err
	}
	if draft.ID == "" || draft.Version == "" {
		return Result{}, errors.New("skillmaterialize: repository returned an invalid version")
	}
	if current.Status == learning.ProposalSkillMaterialized {
		if current.SkillID != draft.ID {
			return Result{}, learning.ErrProposalVersionConflict
		}
		return Result{Input: input, Draft: draft, Proposal: current}, nil
	}
	linked, err := proposals.LinkSkillDraft(ctx, current.Partition, current.ID, current.Version, draft.ID, decision)
	if err == nil {
		return Result{Input: input, Draft: draft, Proposal: linked}, nil
	}
	if !errors.Is(err, learning.ErrProposalVersionConflict) && !errors.Is(err, learning.ErrProposalTransition) {
		return Result{}, err
	}
	linked, found, getErr := proposals.Get(ctx, current.Partition, current.ID)
	if getErr != nil {
		return Result{}, getErr
	}
	if found && linked.Status == learning.ProposalSkillMaterialized && linked.SkillID == draft.ID {
		return Result{Input: input, Draft: draft, Proposal: linked}, nil
	}
	return Result{}, err
}
