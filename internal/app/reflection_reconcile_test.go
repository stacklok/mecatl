package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/reflectionstore"
)

func TestStartupReconcilesPriorProcessPromotion(t *testing.T) {
	for _, mutation := range []string{"current", "excluded", "missing-protocol", "v1", "mixed-protocol"} {
		ctx := context.Background()
		dir := t.TempDir()
		base, err := reflectionstore.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		sources, input, _ := secondRunPublicationInput(t)
		trajectory := input.Trajectory
		ref, err := learning.MessageEvidenceRef(input, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := learning.NewCandidate(input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise", Evidence: []learning.EvidenceRef{ref}})
		if err != nil {
			t.Fatal(err)
		}
		switch mutation {
		case "missing-protocol":
			candidate.Evidence[0].Protocol = ""
		case "v1":
			candidate.Evidence[0].Protocol = "reflection-evidence/v1"
		case "mixed-protocol":
			ref.Protocol = "reflection-evidence/v1"
			ref.Ordinal++
			candidate.Evidence = append(candidate.Evidence, ref)
		}
		part := learning.ProposalPartition{Principal: reflectionPrincipal(trajectory.Principal)}
		staged, err := base.StageBatch(ctx, part, "0123456789abcdef", []learning.Candidate{candidate}, nil)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := base.ClaimPromotion(ctx, part, staged[0].ID, staged[0].Version)
		if err != nil {
			t.Fatal(err)
		}
		memory := memmemory.New()
		writeCtx := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterModel, Origin: tool.MemoryOriginLearning, Source: tool.MemorySource{SessionID: string(trajectory.SessionID), ProposalID: string(claimed.ID)}})
		if _, err = memory.Remember(writeCtx, tool.MemoryEntry{Key: candidate.Key, Value: candidate.Value}, tool.MemoryCurrent{}); err != nil {
			t.Fatal(err)
		}
		// A new repository handle simulates Build after the process that claimed and
		// wrote memory crashed before proposal finalization.
		restarted, err := reflectionstore.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if mutation == "excluded" {
			source, err := sources.Load(ctx, trajectory.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			source.ExcludeFromLearning()
			if err := sources.Save(ctx, source); err != nil {
				t.Fatal(err)
			}
		}
		if err = reconcilePromotingProposals(ctx, Config{learningSourceStore: sources}, restarted, memory, nil); err != nil {
			t.Fatal(err)
		}
		got, found, err := restarted.Get(ctx, part, claimed.ID)
		if mutation != "current" {
			if err != nil || !found || got.Status != learning.ProposalPromoting || got.Receipt != nil {
				t.Fatalf("invalid proposal reconciled (%s): %+v err=%v", mutation, got, err)
			}
		} else if err != nil || !found || got.Status != learning.ProposalPromoted || got.Receipt == nil {
			t.Fatalf("reconciled=%+v found=%v err=%v", got, found, err)
		}
	}
}
