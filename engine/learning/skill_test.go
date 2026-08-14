package learning_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestHistoricalAndNamedProcedureCandidates(t *testing.T) {
	input := reflectionInput(session.NewUserMessage("save this as a skill"))
	ref := mustMessageEvidenceRef(t, input, 0, "")
	historical := learning.Candidate{Kind: learning.CandidateProcedure, Title: "Review Go", Body: "Inspect the diff and run focused tests.", Evidence: []learning.EvidenceRef{ref}}
	if err := learning.ValidateCandidate(input, historical); err != nil {
		t.Fatalf("historical candidate unreadable: %v", err)
	}
	if err := learning.ValidateSkillCandidate(input, historical); !errors.Is(err, learning.ErrInvalidCandidate) {
		t.Fatalf("unnamed materialization=%v", err)
	}
	named := historical
	named.Name = "review-go"
	if err := learning.ValidateSkillCandidate(input, named); err != nil {
		t.Fatalf("named candidate=%v", err)
	}
}
func TestSkillProvenanceAndEvaluationBounds(t *testing.T) {
	valid := learning.SkillProvenance{ProposalIDs: []learning.ProposalID{"proposal-1"}}
	if err := learning.ValidateSkillProvenance(valid); err != nil {
		t.Fatal(err)
	}
	duplicate := learning.SkillProvenance{ProposalIDs: []learning.ProposalID{"proposal-1", "proposal-1"}}
	if err := learning.ValidateSkillProvenance(duplicate); !errors.Is(err, learning.ErrInvalidSkill) {
		t.Fatalf("duplicate provenance=%v", err)
	}
	badEvent := learning.SkillProvenance{EvidenceRefs: []learning.EvidenceRef{{SessionID: "s", Locator: learning.EvidenceEvent, Digest: strings.Repeat("a", 64)}}}
	if err := learning.ValidateSkillProvenance(badEvent); !errors.Is(err, learning.ErrInvalidSkill) {
		t.Fatalf("event without sequence=%v", err)
	}
	badEvaluation := learning.SkillEvaluation{Verdict: learning.EvaluationPass, FixtureIDs: []string{"same", "same"}}
	if err := learning.ValidateSkillEvaluation(badEvaluation); !errors.Is(err, learning.ErrInvalidSkill) {
		t.Fatalf("duplicate fixtures=%v", err)
	}
	if err := learning.ValidateSkillEvaluation(learning.SkillEvaluation{Verdict: learning.EvaluationAbstain, Reason: "no fixtures"}); err != nil {
		t.Fatalf("fixture-free abstention=%v", err)
	}
	if err := learning.ValidateSkillEvaluation(learning.SkillEvaluation{Verdict: learning.EvaluationPass}); !errors.Is(err, learning.ErrInvalidSkill) {
		t.Fatalf("fixture-free pass=%v", err)
	}
}

func TestExplicitLearnProcedureSignalDoesNotBroadenFactRemember(t *testing.T) {
	input := reflectionInput(session.NewUserMessage("Learn this procedure for future code reviews."))
	signals := learning.DetectSignals(input)
	kinds := map[learning.SignalKind]bool{}
	for _, s := range signals {
		kinds[s.Kind] = true
	}
	if !kinds[learning.SignalExplicitLearnProcedure] {
		t.Fatal("missing explicit procedure signal")
	}
	if kinds[learning.SignalExplicitRemember] {
		t.Fatal("procedure request broadened fact remember")
	}
}

type compileSkillRepository struct{}

func (compileSkillRepository) CreateDraft(context.Context, learning.SkillPartition, string, learning.SkillBundle, learning.SkillProvenance) (learning.SkillVersion, error) {
	return learning.SkillVersion{}, nil
}
func (compileSkillRepository) Get(context.Context, learning.SkillPartition, string, learning.SkillID, learning.VersionID) (learning.SkillVersion, bool, error) {
	return learning.SkillVersion{}, false, nil
}
func (compileSkillRepository) List(context.Context, learning.SkillPartition, learning.SkillList) (learning.SkillPage, error) {
	return learning.SkillPage{}, nil
}
func (compileSkillRepository) RecordEvaluation(context.Context, learning.SkillPartition, string, learning.SkillID, learning.VersionID, learning.Revision, learning.SkillEvaluation) (learning.SkillVersion, error) {
	return learning.SkillVersion{}, nil
}
func (compileSkillRepository) Stage(context.Context, learning.SkillPartition, string, learning.SkillID, learning.VersionID, learning.Revision) (learning.SkillVersion, error) {
	return learning.SkillVersion{}, nil
}
func (compileSkillRepository) Activate(context.Context, learning.SkillPartition, string, learning.SkillID, learning.VersionID, learning.Revision) (learning.SkillVersion, error) {
	return learning.SkillVersion{}, nil
}
func (compileSkillRepository) Reject(context.Context, learning.SkillPartition, string, learning.SkillID, learning.VersionID, learning.Revision) (learning.SkillVersion, error) {
	return learning.SkillVersion{}, nil
}
func (compileSkillRepository) Archive(context.Context, learning.SkillPartition, string, learning.SkillID, learning.VersionID, learning.Revision) (learning.SkillVersion, error) {
	return learning.SkillVersion{}, nil
}
func (compileSkillRepository) Rollback(context.Context, learning.SkillPartition, string, learning.SkillID, learning.Revision, learning.VersionID) (learning.SkillVersion, error) {
	return learning.SkillVersion{}, nil
}
func TestExternalConsumerCanImplementSkillContracts(t *testing.T) {
	var repo learning.SkillRepository = compileSkillRepository{}
	_, err := repo.CreateDraft(context.Background(), learning.SkillPartition{Principal: "p"}, "agent", learning.SkillBundle{Name: "review", Description: "Review code", Body: "Inspect changes."}, learning.SkillProvenance{ProposalIDs: []learning.ProposalID{learning.ProposalID("proposal-" + strings.Repeat("a", 8))}})
	if err != nil {
		t.Fatal(err)
	}
}
