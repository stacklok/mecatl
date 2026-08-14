package skillmaterialize_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skillmaterialize"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestInputRequiresEvidenceAndExactPartition(t *testing.T) {
	record := learning.ProposalRecord{
		ID: "proposal-1", Status: learning.ProposalDeferredUnsupported,
		Partition: learning.ProposalPartition{Principal: "principal", Project: "project"},
		Candidate: learning.Candidate{Kind: learning.CandidateProcedure, Name: "review-go", Title: "Review Go", Body: "Inspect changes and run tests."},
	}
	partition := learning.SkillPartition{Principal: "principal", Project: "project"}
	if _, err := skillmaterialize.Input(context.Background(), partition, "agent", record, nil); !errors.Is(err, learning.ErrInvalidCandidate) {
		t.Fatalf("unevidenced input=%v", err)
	}
	record.Candidate.Evidence = []learning.EvidenceRef{{SessionID: session.SessionID("session"), Locator: learning.EvidenceMessage, Digest: strings.Repeat("a", 64)}}
	other := partition
	other.Project = "other"
	if _, err := skillmaterialize.Input(context.Background(), other, "agent", record, nil); !errors.Is(err, learning.ErrInvalidSkill) {
		t.Fatalf("cross-partition input=%v", err)
	}
	input, err := skillmaterialize.Input(context.Background(), partition, "agent", record, nil)
	if err != nil || input.Bundle.Name != "review-go" || len(input.Provenance.EvidenceRefs) != 1 {
		t.Fatalf("input=%#v err=%v", input, err)
	}
}

func TestMaterializeHistoricalDeferredRecordAndRetry(t *testing.T) {
	ctx := context.Background()
	proposals := memproposal.New()
	partition := learning.ProposalPartition{Principal: "principal", Project: "project"}
	ref := learning.EvidenceRef{SessionID: session.SessionID("session"), Locator: learning.EvidenceMessage, Digest: strings.Repeat("a", 64)}
	staged, err := proposals.StageBatch(ctx, partition, strings.Repeat("b", 64), []learning.Candidate{{Kind: learning.CandidateProcedure, Name: "review-go", Title: "Review Go", Body: "Inspect changes and run tests.", Evidence: []learning.EvidenceRef{ref}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := proposals.ClaimPromotion(ctx, partition, staged[0].ID, staged[0].Version)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := proposals.Finalize(ctx, partition, claimed.ID, claimed.Version, learning.ProposalDeferredUnsupported, nil, learning.Decision{Kind: learning.DecisionDefer, Actor: "pipeline"})
	if err != nil {
		t.Fatal(err)
	}
	skills := memskill.New()
	skillPartition := learning.SkillPartition(partition)
	decision := learning.Decision{Kind: learning.DecisionApprove, Actor: "operator"}
	first, err := skillmaterialize.Materialize(ctx, proposals, skills, skillPartition, "agent", historical, nil, decision)
	if err != nil {
		t.Fatal(err)
	}
	second, err := skillmaterialize.Materialize(ctx, proposals, skills, skillPartition, "agent", historical, nil, decision)
	if err != nil {
		t.Fatal(err)
	}
	if first.Draft.ID != second.Draft.ID || second.Proposal.SkillID != first.Draft.ID || second.Draft.State != learning.SkillDraft {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}
