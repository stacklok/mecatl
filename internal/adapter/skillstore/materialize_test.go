package skillstore_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/skillstore"
)

func TestMaterializeProposalReconcilesCrashAfterDraft(t *testing.T) {
	ctx := context.Background()
	proposals := memproposal.New()
	record := deferredProcedure(t, proposals)
	skills, err := skillstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	partition := learning.SkillPartition{Principal: record.Partition.Principal, Project: record.Partition.Project}
	bundle := learning.SkillBundle{Name: record.Candidate.Name, Description: record.Candidate.Title, Body: record.Candidate.Body}
	provenance := learning.SkillProvenance{ProposalIDs: []learning.ProposalID{record.ID}, EvidenceRefs: record.Candidate.Evidence, Signals: record.Signals}
	orphan, err := skills.CreateDraft(ctx, partition, "agent-a", bundle, provenance)
	if err != nil {
		t.Fatal(err)
	}

	result, err := skillstore.MaterializeProposal(ctx, proposals, skills, partition, "agent-a", record, nil, learning.Decision{Kind: learning.DecisionApprove, Actor: "operator", Reason: "materialize"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Draft.ID != orphan.ID || result.Draft.Version != orphan.Version || result.Draft.State != learning.SkillDraft || result.Proposal.Status != learning.ProposalSkillMaterialized || result.Proposal.SkillID != orphan.ID {
		t.Fatalf("reconciled=%#v", result)
	}
	page, err := skills.List(ctx, partition, learning.SkillList{})
	if err != nil || len(page.Versions) != 1 {
		t.Fatalf("versions=%#v err=%v", page, err)
	}
}

func TestMaterializeProposalIsIdempotentAcrossConcurrentReconcilers(t *testing.T) {
	ctx := context.Background()
	proposals := memproposal.New()
	record := deferredProcedure(t, proposals)
	skills, _ := skillstore.New(t.TempDir())
	partition := learning.SkillPartition{Principal: record.Partition.Principal, Project: record.Partition.Project}
	results := make(chan skillstore.Materialization, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := skillstore.MaterializeProposal(ctx, proposals, skills, partition, "agent-a", record, nil, learning.Decision{Kind: learning.DecisionApprove, Actor: "operator"})
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id learning.SkillID
	for result := range results {
		if id == "" {
			id = result.Draft.ID
		}
		if result.Draft.ID != id || result.Proposal.SkillID != id {
			t.Fatalf("divergent materialization=%#v", result)
		}
	}
	page, err := skills.List(ctx, partition, learning.SkillList{})
	if err != nil || len(page.Versions) != 1 || len(page.Versions[0].Provenance.ProposalIDs) != 1 {
		t.Fatalf("idempotent page=%#v err=%v", page, err)
	}
}

func deferredProcedure(t *testing.T, repo learning.ProposalRepository) learning.ProposalRecord {
	t.Helper()
	ctx := context.Background()
	partition := learning.ProposalPartition{Principal: "issuer\x00subject", Project: "project"}
	ref := learning.EvidenceRef{SessionID: session.SessionID("session"), Locator: learning.EvidenceMessage, Digest: strings.Repeat("a", 64)}
	candidate := learning.Candidate{Kind: learning.CandidateProcedure, Name: "review-go", Title: "Review Go changes", Body: "Inspect the diff and run focused tests.", Evidence: []learning.EvidenceRef{ref}}
	staged, err := repo.StageBatch(ctx, partition, strings.Repeat("b", 64), []learning.Candidate{candidate}, []learning.Signal{{Kind: learning.SignalExplicitLearnProcedure, Evidence: []learning.EvidenceRef{ref}}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimPromotion(ctx, partition, staged[0].ID, staged[0].Version)
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := repo.Finalize(ctx, partition, claimed.ID, claimed.Version, learning.ProposalDeferredUnsupported, nil, learning.Decision{Kind: learning.DecisionDefer, Actor: "pipeline"})
	if err != nil {
		t.Fatal(err)
	}
	return deferred
}
