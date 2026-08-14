package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/reflectionstore"
)

func TestStartupReconcilesPriorProcessPromotion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, err := reflectionstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	trajectory := learning.NewTrajectory("source", "", session.StopEndTurn, session.Usage{}, []session.Message{session.NewUserMessage("Remember concise output")})
	input := learning.NewInput(trajectory, nil, nil, nil)
	ref, err := learning.MessageEvidenceRef(input, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := learning.NewCandidate(input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise", Evidence: []learning.EvidenceRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	part := learning.ProposalPartition{Principal: "principal"}
	staged, err := base.StageBatch(ctx, part, "0123456789abcdef", []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := base.ClaimPromotion(ctx, part, staged[0].ID, staged[0].Version)
	if err != nil {
		t.Fatal(err)
	}
	memory := memmemory.New()
	writeCtx := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterModel, Origin: tool.MemoryOriginLearning, Source: tool.MemorySource{SessionID: "source", ProposalID: string(claimed.ID)}})
	if _, err = memory.RememberIfCurrent(writeCtx, tool.MemoryEntry{Key: candidate.Key, Value: candidate.Value}, tool.MemoryCurrent{}); err != nil {
		t.Fatal(err)
	}
	// A new repository handle simulates Build after the process that claimed and
	// wrote memory crashed before proposal finalization.
	restarted, err := reflectionstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = reconcilePromotingProposals(ctx, Config{}, restarted, memory, nil); err != nil {
		t.Fatal(err)
	}
	got, found, err := restarted.Get(ctx, part, claimed.ID)
	if err != nil || !found || got.Status != learning.ProposalPromoted || got.Receipt == nil {
		t.Fatalf("reconciled=%+v found=%v err=%v", got, found, err)
	}
}
