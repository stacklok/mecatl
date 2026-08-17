// Package skilllifecycle orchestrates one learned-skill candidate through validation,
// durable lifecycle transitions, evaluation, and live publication.
package skilllifecycle

//revive:disable:exported // pipeline.go declares one compact public orchestration contract

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

const (
	maxReceiptTextBytes = 1024
	publicationTimeout  = 5 * time.Second
)

type Publisher interface{ Publish(context.Context) error }
type Quarantiner interface{ Quarantine(string) }

type Candidate struct {
	Draft     learning.SkillDraftInput
	Inventory []learning.SkillInventoryItem
	Mode      learning.Mode
	Automatic bool
}

type Receipt struct {
	SkillID          learning.SkillID
	Name             string
	Version          learning.VersionID
	State            learning.SkillState
	Verdict          learning.EvaluationVerdict
	Evidence         int
	FixtureIDs       []string
	Baseline         string
	Treatment        string
	Inspect          bool
	Undo             bool
	Published        bool
	PublicationError string
}

type Pipeline struct {
	Repository learning.SkillRepository
	Validator  learning.SkillValidator
	// Evaluator is trusted admission-control code supplied by the host. It must
	// evaluate host-issued immutable fixture IDs, keep baseline and treatment
	// independent, expose no tools/shell/network, fence candidate content, and
	// enforce deterministic time/token/output limits. Nil deliberately records
	// ABSTAIN. A returned error is durably marked ERROR and can never activate;
	// the engine deliberately provides no production keyword or model judge.
	Evaluator learning.SkillEvaluator
	// ActivationPolicy is the assurance required for Auto activation. The zero
	// value resolves to evaluated for source compatibility with existing embedders.
	ActivationPolicy learning.SkillActivationPolicy
	Publisher        Publisher
	Now              func() time.Time
}

// Process idempotently resumes one candidate from its durable repository state.
// Publication follows committed activation with a cancel-detached bounded context;
// a publication failure is reported on the receipt without disguising the commit.
func (p Pipeline) Process(ctx context.Context, candidate Candidate) (Receipt, error) { //nolint:gocyclo
	if p.Repository == nil || p.Validator == nil {
		return Receipt{}, errors.New("skilllifecycle: repository and validator required")
	}
	if candidate.Automatic && candidate.Mode == learning.Off {
		return Receipt{}, learning.ErrSkillTransition
	}
	in := candidate.Draft
	validation, err := p.Validator.Validate(ctx, learning.SkillValidationRequest{Partition: in.Partition, OwnerAgent: in.OwnerAgent, Bundle: in.Bundle, Provenance: in.Provenance, Inventory: candidate.Inventory})
	if err != nil {
		return Receipt{}, err
	}
	in.Provenance.ValidationDisposition = validation.Disposition
	version, err := p.Repository.CreateDraft(ctx, in.Partition, in.OwnerAgent, in.Bundle, in.Provenance)
	if err != nil {
		return Receipt{}, err
	}
	if !candidate.Automatic || len(in.Provenance.EvidenceRefs) == 0 {
		return receiptFor(version), nil
	}

	var evaluatorErr error
	if version.State == learning.SkillDraft {
		evaluation := learning.SkillEvaluation{Verdict: learning.EvaluationAbstain, Reason: "no trusted skill evaluator configured", At: p.now()}
		if p.Evaluator != nil {
			evaluation, evaluatorErr = p.Evaluator.Evaluate(ctx, learning.SkillEvaluationRequest{Partition: in.Partition, OwnerAgent: in.OwnerAgent, Version: version})
			if evaluatorErr != nil {
				// Persist only a closed, non-activatable marker while returning the
				// original infrastructure error to the caller.
				evaluation = learning.SkillEvaluation{Verdict: learning.EvaluationError, Reason: "trusted skill evaluator unavailable", At: p.now()}
			} else if evaluation.At.IsZero() {
				evaluation.At = p.now()
			}
		}
		if err = learning.ValidateSkillEvaluation(evaluation); err != nil {
			return receiptFor(version), err
		}
		version, err = p.Repository.RecordEvaluation(ctx, in.Partition, in.OwnerAgent, version.ID, version.Version, version.Revision, evaluation)
		if err != nil {
			return receiptFor(version), err
		}
	}

	evaluation := lastEvaluation(version)
	if version.State == learning.SkillEvaluated && evaluation.Verdict != learning.EvaluationFail {
		version, err = p.Repository.Stage(ctx, in.Partition, in.OwnerAgent, version.ID, version.Version, version.Revision)
		if err != nil {
			return receiptFor(version), err
		}
	}
	if evaluatorErr == nil && candidate.Mode == learning.Auto && version.State == learning.SkillStaged && version.Disposition != learning.ValidationSimilarStageHint && p.Publisher != nil {
		switch {
		case evaluation.Verdict == learning.EvaluationPass:
			version, err = p.Repository.Activate(ctx, in.Partition, in.OwnerAgent, version.ID, version.Version, version.Revision)
		case evaluation.Verdict == learning.EvaluationAbstain && p.ActivationPolicy.Effective() == learning.SkillActivationValidated:
			if activator, ok := p.Repository.(learning.ValidatedSkillActivator); ok {
				version, err = activator.ActivateValidated(ctx, in.Partition, in.OwnerAgent, version.ID, version.Version, version.Revision)
			}
		}
		if err != nil {
			return receiptFor(version), err
		}
	}
	receipt := receiptFor(version)
	receipt.Verdict = evaluation.Verdict
	receipt.FixtureIDs = append([]string(nil), evaluation.FixtureIDs...)
	receipt.Baseline, receipt.Treatment = bound(evaluation.Baseline), bound(evaluation.Treatment)
	if version.State == learning.SkillActive && p.Publisher != nil {
		publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publicationTimeout)
		err = p.Publisher.Publish(publishCtx)
		cancel()
		if err != nil {
			if quarantiner, ok := p.Publisher.(Quarantiner); ok {
				quarantiner.Quarantine(version.Bundle.Name)
			}
			receipt.PublicationError = bound(err.Error())
		} else {
			receipt.Published = true
		}
	}
	return receipt, evaluatorErr
}

func (p Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func lastEvaluation(v learning.SkillVersion) learning.SkillEvaluation {
	if len(v.Evaluations) == 0 {
		return learning.SkillEvaluation{}
	}
	return v.Evaluations[len(v.Evaluations)-1]
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
