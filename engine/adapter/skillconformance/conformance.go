// Package skillconformance validates learned SkillRepository implementations.
package skillconformance

//revive:disable:exported // Factory and Run are the package's intentionally tiny test API

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type Factory func(*testing.T) learning.SkillRepository

//nolint:gocyclo // the shared suite keeps the complete lifecycle contract together
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("lifecycle-cas-clone-partition-owner", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		p := partition()
		prov := provenance("a")
		bundle := skill("review-code", "Review code carefully.")
		draft, err := repo.CreateDraft(ctx, p, "agent-a", bundle, prov)
		if err != nil {
			t.Fatal(err)
		}
		again, err := repo.CreateDraft(ctx, p, "agent-a", bundle, provenance("b"))
		if err != nil || again.Version != draft.Version || again.ID != draft.ID || len(again.Provenance.ProposalIDs) != 2 {
			t.Fatalf("duplicate convergence=%#v err=%v", again, err)
		}
		draft.Bundle.Body = "mutated"
		draft.Provenance.ProposalIDs[0] = "mutated"
		stored, found, err := repo.Get(ctx, p, "agent-a", again.ID, again.Version)
		if err != nil || !found || stored.Bundle.Body == "mutated" || stored.Provenance.ProposalIDs[0] == "mutated" {
			t.Fatalf("clone boundary=%#v found=%v err=%v", stored, found, err)
		}
		if _, _, err = repo.Get(ctx, p, "agent-b", again.ID, again.Version); !errors.Is(err, learning.ErrSkillOwnerMismatch) {
			t.Fatalf("wrong owner=%v", err)
		}
		other := p
		other.Project = "other"
		if page, e := repo.List(ctx, other, learning.SkillList{}); e != nil || len(page.Versions) != 0 {
			t.Fatalf("partition leak=%#v %v", page, e)
		}
		eval := evaluation(learning.EvaluationPass)
		evaluated, err := repo.RecordEvaluation(ctx, p, "agent-a", again.ID, again.Version, again.Revision, eval)
		if err != nil || evaluated.State != learning.SkillEvaluated {
			t.Fatalf("evaluation=%#v %v", evaluated, err)
		}
		if _, err = repo.Stage(ctx, p, "agent-a", again.ID, again.Version, again.Revision); !errors.Is(err, learning.ErrSkillConflict) {
			t.Fatalf("stale CAS=%v", err)
		}
		staged, err := repo.Stage(ctx, p, "agent-a", again.ID, again.Version, evaluated.Revision)
		if err != nil {
			t.Fatal(err)
		}
		active, err := repo.Activate(ctx, p, "agent-a", again.ID, again.Version, staged.Revision)
		if err != nil || active.State != learning.SkillActive {
			t.Fatalf("activate=%#v %v", active, err)
		}
		archived, err := repo.Archive(ctx, p, "agent-a", active.ID, active.Version, active.Revision)
		if err != nil || archived.State != learning.SkillArchived {
			t.Fatalf("archive=%#v %v", archived, err)
		}
	})
	t.Run("archive-rejects-never-active-versions", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		p := partition()
		draft, err := repo.CreateDraft(ctx, p, "agent-a", skill("never-active", "Draft workflow."), provenance("draft"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.Archive(ctx, p, "agent-a", draft.ID, draft.Version, draft.Revision); !errors.Is(err, learning.ErrSkillTransition) {
			t.Fatalf("archive draft=%v", err)
		}
		evaluated, err := repo.RecordEvaluation(ctx, p, "agent-a", draft.ID, draft.Version, draft.Revision, evaluation(learning.EvaluationAbstain))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.Archive(ctx, p, "agent-a", evaluated.ID, evaluated.Version, evaluated.Revision); !errors.Is(err, learning.ErrSkillTransition) {
			t.Fatalf("archive evaluated=%v", err)
		}
		staged, err := repo.Stage(ctx, p, "agent-a", evaluated.ID, evaluated.Version, evaluated.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.Archive(ctx, p, "agent-a", staged.ID, staged.Version, staged.Revision); !errors.Is(err, learning.ErrSkillTransition) {
			t.Fatalf("archive staged=%v", err)
		}
		rejectedDraft, err := repo.CreateDraft(ctx, p, "agent-a", skill("never-active-rejected", "Rejected workflow."), provenance("rejected"))
		if err != nil {
			t.Fatal(err)
		}
		rejected, err := repo.Reject(ctx, p, "agent-a", rejectedDraft.ID, rejectedDraft.Version, rejectedDraft.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.Archive(ctx, p, "agent-a", rejected.ID, rejected.Version, rejected.Revision); !errors.Is(err, learning.ErrSkillTransition) {
			t.Fatalf("archive rejected=%v", err)
		}
	})
	t.Run("supersede-activate-rollback-one-active", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		p := partition()
		first := activate(t, repo, p, "One workflow.")
		secondDraft, err := repo.CreateDraft(ctx, p, "agent-a", skill("review-code", "Two workflow."), provenance("two"))
		if err != nil || secondDraft.Supersedes != first.Version {
			t.Fatalf("supersedes=%#v %v", secondDraft, err)
		}
		second := activateVersion(t, repo, p, secondDraft)
		old, found, err := repo.Get(ctx, p, "agent-a", first.ID, first.Version)
		if err != nil || !found || old.State != learning.SkillArchived {
			t.Fatalf("old active=%#v %v", old, err)
		}
		var rolled learning.SkillVersion
		wins := raceCAS(t, func() error {
			result, rollbackErr := repo.Rollback(ctx, p, "agent-a", second.ID, second.Revision, first.Version)
			if rollbackErr == nil {
				rolled = result
			}
			return rollbackErr
		})
		if wins != 1 || rolled.Version != first.Version || rolled.State != learning.SkillActive {
			t.Fatalf("rollback wins=%d result=%#v", wins, rolled)
		}
		newer, _, _ := repo.Get(ctx, p, "agent-a", second.ID, second.Version)
		if newer.State != learning.SkillArchived {
			t.Fatalf("rollback left second active: %#v", newer)
		}
		activeVersion := first.Version
		for i := 0; i < learning.MaxSkillReceipts+5; i++ {
			current, found, getErr := repo.Get(ctx, p, "agent-a", first.ID, activeVersion)
			if getErr != nil || !found {
				t.Fatalf("get rollback source found=%v err=%v", found, getErr)
			}
			target := second.Version
			if activeVersion == second.Version {
				target = first.Version
			}
			rolled, rollbackErr := repo.Rollback(ctx, p, "agent-a", first.ID, current.Revision, target)
			if rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			activeVersion = rolled.Version
		}
		for _, version := range []learning.VersionID{first.Version, second.Version} {
			stored, _, getErr := repo.Get(ctx, p, "agent-a", first.ID, version)
			if getErr != nil || len(stored.Receipts) > learning.MaxSkillReceipts {
				t.Fatalf("receipt history version=%s len=%d err=%v", version, len(stored.Receipts), getErr)
			}
		}
	})
	t.Run("concurrent-cas", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		p := partition()
		d, _ := repo.CreateDraft(ctx, p, "agent-a", skill("race", "Concurrent workflow."), provenance("race"))
		e, _ := repo.RecordEvaluation(ctx, p, "agent-a", d.ID, d.Version, d.Revision, evaluation(learning.EvaluationPass))
		if wins := raceCAS(t, func() error {
			_, err := repo.Stage(ctx, p, "agent-a", e.ID, e.Version, e.Revision)
			return err
		}); wins != 1 {
			t.Fatalf("stage CAS wins=%d", wins)
		}
		staged, found, err := repo.Get(ctx, p, "agent-a", e.ID, e.Version)
		if err != nil || !found {
			t.Fatalf("get staged found=%v err=%v", found, err)
		}
		if wins := raceCAS(t, func() error {
			_, activateErr := repo.Activate(ctx, p, "agent-a", staged.ID, staged.Version, staged.Revision)
			return activateErr
		}); wins != 1 {
			t.Fatalf("activate CAS wins=%d", wins)
		}
		active, found, err := repo.Get(ctx, p, "agent-a", e.ID, e.Version)
		if err != nil || !found {
			t.Fatalf("get active found=%v err=%v", found, err)
		}
		if wins := raceCAS(t, func() error {
			_, archiveErr := repo.Archive(ctx, p, "agent-a", active.ID, active.Version, active.Revision)
			return archiveErr
		}); wins != 1 {
			t.Fatalf("archive CAS wins=%d", wins)
		}
	})
	t.Run("bounds-and-rejection", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		p := partition()
		d, _ := repo.CreateDraft(ctx, p, "agent-a", skill("bounded", "Bounded workflow."), provenance("bound"))
		current := d
		var err error
		for i := 0; i < learning.MaxSkillEvaluations; i++ {
			current, err = repo.RecordEvaluation(ctx, p, "agent-a", d.ID, d.Version, current.Revision, evaluation(learning.EvaluationAbstain))
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err = repo.RecordEvaluation(ctx, p, "agent-a", d.ID, d.Version, current.Revision, evaluation(learning.EvaluationPass)); !errors.Is(err, learning.ErrSkillLimit) {
			t.Fatalf("evaluation bound=%v", err)
		}
		versionRepo := factory(t)
		var latest learning.SkillVersion
		for i := 0; i < learning.MaxSkillVersionsPerSkill; i++ {
			latest, err = versionRepo.CreateDraft(ctx, p, "agent-a", skill("versioned", fmt.Sprintf("Workflow revision %d.", i)), provenance(fmt.Sprintf("version-%d", i)))
			if err != nil {
				t.Fatal(err)
			}
		}
		if latest.Supersedes == "" {
			t.Fatal("bounded version history lost supersedes linkage")
		}
		if _, err = versionRepo.CreateDraft(ctx, p, "agent-a", skill("versioned", "One revision too many."), provenance("overflow")); !errors.Is(err, learning.ErrSkillLimit) {
			t.Fatalf("version bound=%v", err)
		}

		rejectedDraft, _ := repo.CreateDraft(ctx, p, "agent-a", skill("rejected", "Rejected workflow."), provenance("reject"))
		rejected, err := repo.RecordEvaluation(ctx, p, "agent-a", rejectedDraft.ID, rejectedDraft.Version, rejectedDraft.Revision, evaluation(learning.EvaluationFail))
		if err != nil || rejected.State != learning.SkillRejected {
			t.Fatalf("rejected=%#v %v", rejected, err)
		}
		if _, err = repo.Stage(ctx, p, "agent-a", rejected.ID, rejected.Version, rejected.Revision); !errors.Is(err, learning.ErrSkillTransition) {
			t.Fatalf("stage rejected=%v", err)
		}
	})
}

func raceCAS(t *testing.T, operation func() error) int {
	t.Helper()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- operation()
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, learning.ErrSkillConflict) {
			t.Fatal(err)
		}
	}
	return wins
}

func partition() learning.SkillPartition {
	return learning.SkillPartition{Principal: "issuer\x00subject", Project: "project"}
}
func provenance(s string) learning.SkillProvenance {
	return learning.SkillProvenance{ProposalIDs: []learning.ProposalID{learning.ProposalID("proposal-" + s)}, EvidenceRefs: []learning.EvidenceRef{{SessionID: session.SessionID("s-" + s), Locator: learning.EvidenceMessage, Digest: strings.Repeat("a", 64)}}}
}
func skill(name, body string) learning.SkillBundle {
	return learning.SkillBundle{Name: name, Description: "Reusable " + name + " instructions", Body: body}
}
func evaluation(v learning.EvaluationVerdict) learning.SkillEvaluation {
	return learning.SkillEvaluation{Verdict: v, FixtureIDs: []string{"fixture-1"}, Baseline: "baseline", Treatment: "treatment", Reason: "verified"}
}
func activate(t *testing.T, r learning.SkillRepository, p learning.SkillPartition, body string) learning.SkillVersion {
	t.Helper()
	d, e := r.CreateDraft(context.Background(), p, "agent-a", skill("review-code", body), provenance(fmt.Sprint(len(body))))
	if e != nil {
		t.Fatal(e)
	}
	return activateVersion(t, r, p, d)
}
func activateVersion(t *testing.T, r learning.SkillRepository, p learning.SkillPartition, d learning.SkillVersion) learning.SkillVersion {
	t.Helper()
	e, err := r.RecordEvaluation(context.Background(), p, "agent-a", d.ID, d.Version, d.Revision, evaluation(learning.EvaluationPass))
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Stage(context.Background(), p, "agent-a", e.ID, e.Version, e.Revision)
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Activate(context.Background(), p, "agent-a", s.ID, s.Version, s.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
