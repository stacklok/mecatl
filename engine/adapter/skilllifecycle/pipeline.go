// Package skilllifecycle orchestrates one learned-skill candidate through validation,
// durable lifecycle transitions, evaluation, and live publication.
package skilllifecycle

//revive:disable:exported // pipeline.go declares one compact public orchestration contract

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

const maxReceiptTextBytes = 1024

type Publisher interface{ Publish(context.Context) error }

type Candidate struct {
	Draft     learning.SkillDraftInput
	Inventory []learning.SkillInventoryItem
	Mode      learning.Mode
	Automatic bool
}

type Receipt struct {
	SkillID    learning.SkillID
	Name       string
	Version    learning.VersionID
	State      learning.SkillState
	Verdict    learning.EvaluationVerdict
	Evidence   int
	FixtureIDs []string
	Baseline   string
	Treatment  string
	Inspect    bool
	Undo       bool
}

type Pipeline struct {
	Repository learning.SkillRepository
	Validator  learning.SkillValidator
	Evaluator  learning.SkillEvaluator
	Publisher  Publisher
	Now        func() time.Time
}

// Process drives one candidate synchronously to a bounded receipt.
//
//nolint:gocyclo // lifecycle stages remain visibly ordered around every CAS
func (p Pipeline) Process(ctx context.Context, candidate Candidate) (Receipt, error) {
	if p.Repository == nil || p.Validator == nil {
		return Receipt{}, errors.New("skilllifecycle: repository and validator required")
	}
	if candidate.Automatic && candidate.Mode == learning.Off {
		return Receipt{}, fmt.Errorf("%w: automatic skill generation is disabled", learning.ErrSkillTransition)
	}
	in := candidate.Draft
	if _, err := p.Validator.Validate(ctx, learning.SkillValidationRequest{Partition: in.Partition, OwnerAgent: in.OwnerAgent, Bundle: in.Bundle, Provenance: in.Provenance, Inventory: candidate.Inventory}); err != nil {
		return Receipt{}, err
	}
	draft, err := p.Repository.CreateDraft(ctx, in.Partition, in.OwnerAgent, in.Bundle, in.Provenance)
	if err != nil {
		return Receipt{}, err
	}
	receipt := receiptFor(draft)
	if !candidate.Automatic || len(in.Provenance.EvidenceRefs) == 0 {
		return receipt, nil
	}
	if p.Evaluator == nil {
		return receipt, errors.New("skilllifecycle: evaluator required for an evidence-backed candidate")
	}
	evaluation, err := p.Evaluator.Evaluate(ctx, learning.SkillEvaluationRequest{Partition: in.Partition, OwnerAgent: in.OwnerAgent, Version: draft})
	if err != nil {
		return receipt, err
	}
	if evaluation.At.IsZero() {
		now := p.Now
		if now == nil {
			now = time.Now
		}
		evaluation.At = now().UTC()
	}
	if err := learning.ValidateSkillEvaluation(evaluation); err != nil {
		return receipt, err
	}
	version, err := p.Repository.RecordEvaluation(ctx, in.Partition, in.OwnerAgent, draft.ID, draft.Version, draft.Revision, evaluation)
	if err != nil {
		return receipt, err
	}
	if evaluation.Verdict != learning.EvaluationFail {
		version, err = p.Repository.Stage(ctx, in.Partition, in.OwnerAgent, version.ID, version.Version, version.Revision)
		if err != nil {
			return receipt, err
		}
	}
	if candidate.Mode == learning.Auto && evaluation.Verdict == learning.EvaluationPass {
		version, err = p.Repository.Activate(ctx, in.Partition, in.OwnerAgent, version.ID, version.Version, version.Revision)
		if err != nil {
			return receipt, err
		}
		if p.Publisher != nil {
			if err := p.Publisher.Publish(ctx); err != nil {
				return receiptFor(version), fmt.Errorf("skilllifecycle: publish active catalog: %w", err)
			}
		}
	}
	receipt = receiptFor(version)
	receipt.Verdict = evaluation.Verdict
	receipt.FixtureIDs = append([]string(nil), evaluation.FixtureIDs...)
	receipt.Baseline, receipt.Treatment = bound(evaluation.Baseline), bound(evaluation.Treatment)
	return receipt, nil
}

func receiptFor(v learning.SkillVersion) Receipt {
	return Receipt{SkillID: v.ID, Name: v.Bundle.Name, Version: v.Version, State: v.State, Evidence: len(v.Provenance.EvidenceRefs), Inspect: true, Undo: v.State == learning.SkillActive}
}
func bound(v string) string {
	if len(v) <= maxReceiptTextBytes {
		return v
	}
	return v[:maxReceiptTextBytes]
}
