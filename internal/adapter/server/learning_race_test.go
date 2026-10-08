package server

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memorypromotion"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type blockedConvergenceStore struct {
	*memmemory.Store
	started chan struct{}
	release chan struct{}
}

func (s *blockedConvergenceStore) Remember(ctx context.Context, entry tool.MemoryEntry, current tool.MemoryCurrent) (tool.MemoryRecord, error) {
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return tool.MemoryRecord{}, ctx.Err()
	}
	return s.Store.Remember(ctx, entry, current)
}

func TestLearningReadsNeverReconcileActivePromotion(t *testing.T) {
	repo := memproposal.New()
	part, record := stagedProposalFixture(t, repo)
	memory := &blockedConvergenceStore{Store: memmemory.New(), started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan learning.ProposalRecord, 1)
	go func() {
		promoted, _ := (memorypromotion.Promoter{Proposals: repo, Memory: memory}).Process(context.Background(), part, record.ID, record.Version, memorypromotion.PolicyInput{Mode: learning.Review, Approved: true})
		done <- promoted
	}()
	select {
	case <-memory.started:
	case <-time.After(time.Second):
		t.Fatal("promotion did not reach blocked memory write")
	}

	svc := &Service{cfg: Config{
		Store: memstore.New(), Proposals: repo, ProposalPrincipal: func(*session.Principal) string { return part.Principal },
		PromoteProposal: func(context.Context, learning.ProposalPartition, learning.ProposalID, learning.ProposalVersion, bool) (learning.ProposalRecord, error) {
			t.Fatal("List/Get attempted read-side reconciliation")
			return learning.ProposalRecord{}, nil
		},
	}}
	page, err := svc.ListLearningProposals(context.Background(), "", "", 10, "")
	if err != nil || len(page.GetProposals()) != 1 || page.GetProposals()[0].GetStatus() != string(learning.ProposalPromoting) {
		t.Fatalf("list during promotion=%+v err=%v", page, err)
	}
	got, err := svc.GetLearningProposal(context.Background(), string(record.ID), "")
	if err != nil || got.GetStatus() != string(learning.ProposalPromoting) {
		t.Fatalf("get during promotion=%+v err=%v", got, err)
	}

	close(memory.release)
	promoted := <-done
	if promoted.Status != learning.ProposalPromoted {
		t.Fatalf("promotion status=%s", promoted.Status)
	}
	stored, found, err := memory.Recall(context.Background(), record.Candidate.Key)
	if err != nil || !found || stored.Value != record.Candidate.Value {
		t.Fatalf("memory=%+v found=%v err=%v", stored, found, err)
	}
	final, err := svc.GetLearningProposal(context.Background(), string(record.ID), "")
	if err != nil || final.GetStatus() != string(learning.ProposalPromoted) {
		t.Fatalf("final proposal=%+v err=%v", final, err)
	}
}
