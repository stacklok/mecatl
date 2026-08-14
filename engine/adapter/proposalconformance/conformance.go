// Package proposalconformance validates ProposalRepository implementations.
package proposalconformance

//revive:disable:exported // Factory and Run are the package's intentionally tiny test API

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type Factory func(*testing.T) learning.ProposalRepository

//nolint:gocyclo // the shared suite keeps repository lifecycle cases together for every adapter
func Run(t *testing.T, f Factory) {
	t.Helper()
	t.Run("stage-list-partition-cas", func(t *testing.T) {
		r := f(t)
		p, c := fixture("one")
		c.Description = ""
		first, e := r.StageBatch(context.Background(), p, strings.Repeat("a", 64), []learning.Candidate{c}, nil)
		if e != nil {
			t.Fatal(e)
		}
		again, e := r.StageBatch(context.Background(), p, strings.Repeat("a", 64), []learning.Candidate{c}, nil)
		if e != nil || first[0].ID != again[0].ID || first[0].Version != again[0].Version {
			t.Fatalf("idempotency: %#v %v", again, e)
		}
		_, c2 := fixture("two")
		if _, e = r.StageBatch(context.Background(), p, strings.Repeat("b", 64), []learning.Candidate{c2}, nil); e != nil {
			t.Fatal(e)
		}
		page, e := r.List(context.Background(), p, learning.ProposalList{Limit: 1})
		if e != nil || len(page.Records) != 1 || page.Next == "" {
			t.Fatalf("page %#v %v", page, e)
		}
		other := p
		other.Project = "other"
		empty, _ := r.List(context.Background(), other, learning.ProposalList{})
		if len(empty.Records) != 0 {
			t.Fatal("project partition leak")
		}
		other = p
		other.Principal = "other-principal"
		if _, found, e := r.Get(context.Background(), other, first[0].ID); e != nil || found {
			t.Fatalf("principal partition leak: found=%v err=%v", found, e)
		}
		collisionA := learning.ProposalPartition{Principal: "a", Project: "b\x00c"}
		collisionB := learning.ProposalPartition{Principal: "a\x00b", Project: "c"}
		if _, e = r.StageBatch(context.Background(), collisionA, strings.Repeat("f", 64), []learning.Candidate{c}, nil); e != nil {
			t.Fatal(e)
		}
		collisionPage, e := r.List(context.Background(), collisionB, learning.ProposalList{})
		if e != nil || len(collisionPage.Records) != 0 {
			t.Fatalf("delimiter collision leaked partition: %#v %v", collisionPage, e)
		}
		var wg sync.WaitGroup
		ch := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := r.ClaimPromotion(context.Background(), p, first[0].ID, first[0].Version)
				ch <- e
			}()
		}
		wg.Wait()
		close(ch)
		wins := 0
		for e := range ch {
			if e == nil {
				wins++
			} else if !errors.Is(e, learning.ErrProposalVersionConflict) {
				t.Fatal(e)
			}
		}
		if wins != 1 {
			t.Fatalf("wins=%d", wins)
		}
	})
	t.Run("bounded-decisions-and-terminal-cas", func(t *testing.T) {
		r := f(t)
		p, candidate := fixture("history")
		records, err := r.StageBatch(context.Background(), p, strings.Repeat("c", 64), []learning.Candidate{candidate}, nil)
		if err != nil {
			t.Fatal(err)
		}
		current := records[0]
		for i := 0; i < learning.MaxProposalDecisions+3; i++ {
			current, err = r.ClaimDecision(context.Background(), p, current.ID, current.Version, learning.Decision{Kind: learning.DecisionDefer, Actor: "reviewer", Reason: "needs review"})
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(current.Decisions) != learning.MaxProposalDecisions {
			t.Fatalf("decision history len=%d", len(current.Decisions))
		}
		claimed, err := r.ClaimPromotion(context.Background(), p, current.ID, current.Version)
		if err != nil {
			t.Fatal(err)
		}
		decision := learning.Decision{Kind: learning.DecisionDefer, Actor: "test", Reason: "concurrent value"}
		type finalizeResult struct {
			record learning.ProposalRecord
			err    error
		}
		start := make(chan struct{})
		results := make(chan finalizeResult, 2)
		for range 2 {
			go func() {
				<-start
				record, finalizeErr := r.Finalize(context.Background(), p, claimed.ID, claimed.Version, learning.ProposalConflicted, nil, decision)
				results <- finalizeResult{record: record, err: finalizeErr}
			}()
		}
		close(start)
		firstResult, secondResult := <-results, <-results
		for _, result := range []finalizeResult{firstResult, secondResult} {
			if result.err != nil || result.record.Status != learning.ProposalConflicted {
				t.Fatalf("concurrent finalize=%#v err=%v", result.record, result.err)
			}
		}
		if firstResult.record.Version != secondResult.record.Version || len(firstResult.record.Decisions) != len(secondResult.record.Decisions) {
			t.Fatalf("identical finalizers diverged: first=%#v second=%#v", firstResult.record, secondResult.record)
		}
		if _, err = r.Finalize(context.Background(), p, claimed.ID, claimed.Version, learning.ProposalRejected, nil, learning.Decision{}); !errors.Is(err, learning.ErrProposalVersionConflict) {
			t.Fatalf("stale changed finalize error=%v", err)
		}
	})
	t.Run("returned-records-do-not-alias-store", func(t *testing.T) {
		r := f(t)
		p, candidate := fixture("clone")
		records, err := r.StageBatch(context.Background(), p, strings.Repeat("e", 64), []learning.Candidate{candidate}, []learning.Signal{{Kind: learning.SignalContradiction, Evidence: candidate.Evidence}})
		if err != nil {
			t.Fatal(err)
		}
		records[0].Candidate.Value = "mutated"
		records[0].Candidate.Evidence[0].Digest = strings.Repeat("0", 64)
		records[0].Signals[0].Evidence[0].Digest = strings.Repeat("0", 64)
		stored, found, err := r.Get(context.Background(), p, records[0].ID)
		if err != nil || !found {
			t.Fatalf("get found=%v err=%v", found, err)
		}
		if stored.Candidate.Value == "mutated" || stored.Candidate.Evidence[0].Digest == strings.Repeat("0", 64) || stored.Signals[0].Evidence[0].Digest == strings.Repeat("0", 64) {
			t.Fatal("returned record aliases repository state")
		}
	})
	t.Run("historical-procedure-materialization", func(t *testing.T) {
		r := f(t)
		p, candidate := fixture("procedure")
		candidate.Kind, candidate.Key, candidate.Value, candidate.Description = learning.CandidateProcedure, "", "", ""
		candidate.Title, candidate.Body = "Review Go changes", "Inspect the diff and run focused tests."
		records, err := r.StageBatch(context.Background(), p, strings.Repeat("9", 64), []learning.Candidate{candidate}, nil)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := r.ClaimPromotion(context.Background(), p, records[0].ID, records[0].Version)
		if err != nil {
			t.Fatal(err)
		}
		deferred, err := r.Finalize(context.Background(), p, claimed.ID, claimed.Version, learning.ProposalDeferredUnsupported, nil, learning.Decision{Kind: learning.DecisionDefer, Actor: "legacy", Reason: "skills were unsupported"})
		if err != nil {
			t.Fatal(err)
		}
		linked, err := r.LinkSkillDraft(context.Background(), p, deferred.ID, deferred.Version, "skill-review-go", learning.Decision{Kind: learning.DecisionApprove, Actor: "operator", Reason: "explicit materialization"})
		if err != nil || linked.Status != learning.ProposalSkillMaterialized || linked.SkillID != "skill-review-go" || linked.Receipt != nil {
			t.Fatalf("linked=%#v err=%v", linked, err)
		}
		if _, err = r.LinkSkillDraft(context.Background(), p, deferred.ID, deferred.Version, "skill-other", learning.Decision{Kind: learning.DecisionApprove}); !errors.Is(err, learning.ErrProposalVersionConflict) {
			t.Fatalf("stale linkage=%v", err)
		}
	})
}
func fixture(x string) (learning.ProposalPartition, learning.Candidate) {
	return learning.ProposalPartition{Principal: "issuer\x00subject", Project: "project"}, learning.Candidate{Kind: learning.CandidateProjectFact, Key: "project/" + x, Value: "Use task test.", Description: "Test command", Evidence: []learning.EvidenceRef{{SessionID: session.SessionID("s-" + x), Locator: learning.EvidenceMessage, Ordinal: 0, Digest: strings.Repeat("d", 64)}}}
}
