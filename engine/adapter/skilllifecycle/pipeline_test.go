package skilllifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skilllifecycle"
	"github.com/stacklok/mecatl/engine/adapter/skillvalidation"
	"github.com/stacklok/mecatl/engine/learning"
)

type evaluator struct{ verdict learning.EvaluationVerdict }

func (e evaluator) Evaluate(context.Context, learning.SkillEvaluationRequest) (learning.SkillEvaluation, error) {
	return learning.SkillEvaluation{Verdict: e.verdict, FixtureIDs: []string{"fixture-1"}, Baseline: "before", Treatment: "after", At: time.Unix(1, 0)}, nil
}

type publisher struct {
	calls       int
	err         error
	quarantined string
}

func (p *publisher) Publish(context.Context) error { p.calls++; return p.err }
func (p *publisher) Quarantine(name string)        { p.quarantined = name }

func TestPipelineModesAndVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      learning.Mode
		verdict   learning.EvaluationVerdict
		state     learning.SkillState
		published int
	}{
		{"review-pass", learning.Review, learning.EvaluationPass, learning.SkillStaged, 0},
		{"review-abstain", learning.Review, learning.EvaluationAbstain, learning.SkillStaged, 0},
		{"auto-pass", learning.Auto, learning.EvaluationPass, learning.SkillActive, 1},
		{"auto-fail", learning.Auto, learning.EvaluationFail, learning.SkillRejected, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub := &publisher{}
			pipeline := skilllifecycle.Pipeline{Repository: memskill.New(), Validator: skillvalidation.Validator{}, Evaluator: evaluator{tc.verdict}, Publisher: pub}
			receipt, err := pipeline.Process(context.Background(), skilllifecycle.Candidate{Draft: evidenceDraft(), Mode: tc.mode, Automatic: true})
			if err != nil {
				t.Fatal(err)
			}
			if receipt.State != tc.state || pub.calls != tc.published {
				t.Fatalf("state=%s publishes=%d", receipt.State, pub.calls)
			}
		})
	}
}

func TestPipelineResumeDispositionAndPublicationStatus(t *testing.T) {
	t.Run("idempotent active resume republishes", func(t *testing.T) {
		pub := &publisher{}
		pipeline := skilllifecycle.Pipeline{Repository: memskill.New(), Validator: skillvalidation.Validator{}, Evaluator: evaluator{learning.EvaluationPass}, Publisher: pub}
		candidate := skilllifecycle.Candidate{Draft: evidenceDraft(), Mode: learning.Auto, Automatic: true}
		first, err := pipeline.Process(context.Background(), candidate)
		if err != nil || first.State != learning.SkillActive || !first.Published {
			t.Fatalf("first=%+v err=%v", first, err)
		}
		second, err := pipeline.Process(context.Background(), candidate)
		if err != nil || second.State != learning.SkillActive || !second.Published || pub.calls != 2 {
			t.Fatalf("resume=%+v calls=%d err=%v", second, pub.calls, err)
		}
	})
	t.Run("similar candidate stays staged in auto", func(t *testing.T) {
		pub := &publisher{}
		repository := memskill.New()
		candidate := skilllifecycle.Candidate{Draft: evidenceDraft(), Mode: learning.Auto, Automatic: true, Inventory: []learning.SkillInventoryItem{{Name: "review-codes", Bundle: learning.SkillBundle{Body: "Inspect the change and run focused tests."}}}}
		pipeline := skilllifecycle.Pipeline{Repository: repository, Validator: skillvalidation.Validator{}, Evaluator: evaluator{learning.EvaluationPass}, Publisher: pub}
		receipt, err := pipeline.Process(context.Background(), candidate)
		if err != nil || receipt.State != learning.SkillStaged || pub.calls != 0 {
			t.Fatalf("receipt=%+v calls=%d err=%v", receipt, pub.calls, err)
		}
		candidate.Inventory = nil
		retried, err := pipeline.Process(context.Background(), candidate)
		if err != nil || retried.State != learning.SkillStaged || pub.calls != 0 {
			t.Fatalf("retry erased durable similarity: receipt=%+v calls=%d err=%v", retried, pub.calls, err)
		}
	})
	t.Run("nil evaluator abstains", func(t *testing.T) {
		receipt, err := (skilllifecycle.Pipeline{Repository: memskill.New(), Validator: skillvalidation.Validator{}}).Process(context.Background(), skilllifecycle.Candidate{Draft: evidenceDraft(), Mode: learning.Auto, Automatic: true})
		if err != nil || receipt.State != learning.SkillStaged || receipt.Verdict != learning.EvaluationAbstain {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	})
	t.Run("committed activation reports publication failure", func(t *testing.T) {
		pub := &publisher{err: errors.New("offline")}
		receipt, err := (skilllifecycle.Pipeline{Repository: memskill.New(), Validator: skillvalidation.Validator{}, Evaluator: evaluator{learning.EvaluationPass}, Publisher: pub}).Process(context.Background(), skilllifecycle.Candidate{Draft: evidenceDraft(), Mode: learning.Auto, Automatic: true})
		if err != nil || receipt.State != learning.SkillActive || receipt.PublicationError == "" || receipt.Published || pub.quarantined != receipt.Name {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	})
}

func TestPipelineOffRejectsAutomaticButKeepsExplicitDraft(t *testing.T) {
	pipeline := skilllifecycle.Pipeline{Repository: memskill.New(), Validator: skillvalidation.Validator{}}
	if _, err := pipeline.Process(context.Background(), skilllifecycle.Candidate{Draft: evidenceDraft(), Automatic: true}); err == nil {
		t.Fatal("automatic off accepted")
	}
	receipt, err := pipeline.Process(context.Background(), skilllifecycle.Candidate{Draft: evidenceDraft()})
	if err != nil || receipt.State != learning.SkillDraft {
		t.Fatalf("explicit draft: %+v %v", receipt, err)
	}
}

func evidenceDraft() learning.SkillDraftInput {
	seq := int64(1)
	return learning.SkillDraftInput{Partition: learning.SkillPartition{Principal: "p"}, OwnerAgent: "reflection", Bundle: learning.SkillBundle{Name: "review-code", Description: "Review code safely", Body: "Inspect the change and run focused tests."}, Provenance: learning.SkillProvenance{ProposalIDs: []learning.ProposalID{"proposal-1"}, EvidenceRefs: []learning.EvidenceRef{{SessionID: "s", Locator: learning.EvidenceEvent, EventSeq: &seq, Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}
}
