package skillstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestCrashBoundaryMatrixConvergesAfterEveryAtomicWriteStep(t *testing.T) {
	steps := []string{"body.fsync", "body.rename", "body.dirsync", "manifest.fsync", "manifest.rename", "manifest.dirsync"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			store, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected crash boundary")
			store.fault = func(got string) error {
				if got == step {
					return injected
				}
				return nil
			}
			partition := learning.SkillPartition{Principal: "principal", Project: "/project"}
			bundle := learning.SkillBundle{Name: "recoverable", Description: "Recover a durable skill", Body: "1. Retry safely.\nDone when: stored."}
			provenance := learning.SkillProvenance{ProposalIDs: []learning.ProposalID{"proposal-1"}, EvidenceRefs: []learning.EvidenceRef{{SessionID: session.SessionID("session-1"), Locator: learning.EvidenceMessage, Digest: strings.Repeat("a", 64)}}}
			if _, err = store.CreateDraft(context.Background(), partition, "agent", bundle, provenance); !errors.Is(err, injected) {
				t.Fatalf("CreateDraft error = %v", err)
			}
			reopened, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			page, err := reopened.List(context.Background(), partition, learning.SkillList{})
			if err != nil || len(page.Versions) > 1 {
				t.Fatalf("reopen after %s = %d versions, err=%v", step, len(page.Versions), err)
			}
			draft, err := reopened.CreateDraft(context.Background(), partition, "agent", bundle, provenance)
			if err != nil || draft.State != learning.SkillDraft {
				t.Fatalf("retry after %s = %+v, err=%v", step, draft, err)
			}
		})
	}
}

func TestRollbackCrashMatrixConvergesToOneActiveVersionAndReceipts(t *testing.T) {
	for _, step := range []string{"manifest.fsync", "manifest.rename", "manifest.dirsync"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			store, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			partition := learning.SkillPartition{Principal: "principal", Project: "/project"}
			first := createActiveVersion(t, store, partition, "Rollback one.", "one")
			second := createActiveVersion(t, store, partition, "Rollback two.", "two")
			injected := errors.New("injected rollback crash boundary")
			store.fault = func(got string) error {
				if got == step {
					return injected
				}
				return nil
			}
			if _, err = store.Rollback(ctx, partition, "agent", second.ID, second.Revision, first.Version); !errors.Is(err, injected) {
				t.Fatalf("Rollback error=%v", err)
			}

			reopened, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			firstState, firstFound, firstErr := reopened.Get(ctx, partition, "agent", first.ID, first.Version)
			secondState, secondFound, secondErr := reopened.Get(ctx, partition, "agent", second.ID, second.Version)
			if firstErr != nil || secondErr != nil || !firstFound || !secondFound {
				t.Fatalf("reopen versions: first=%v/%v second=%v/%v", firstFound, firstErr, secondFound, secondErr)
			}
			if secondState.State == learning.SkillActive {
				if _, err = reopened.Rollback(ctx, partition, "agent", secondState.ID, secondState.Revision, first.Version); err != nil {
					t.Fatalf("rerun rollback: %v", err)
				}
				firstState, _, err = reopened.Get(ctx, partition, "agent", first.ID, first.Version)
				if err != nil {
					t.Fatal(err)
				}
				secondState, _, err = reopened.Get(ctx, partition, "agent", second.ID, second.Version)
				if err != nil {
					t.Fatal(err)
				}
			}
			if firstState.State != learning.SkillActive || secondState.State != learning.SkillArchived {
				t.Fatalf("convergence after %s: first=%s second=%s", step, firstState.State, secondState.State)
			}
			from, to := 0, 0
			for _, version := range []learning.SkillVersion{firstState, secondState} {
				for _, receipt := range version.Receipts {
					switch receipt.Operation {
					case "rollback_from":
						from++
					case "rollback_to":
						to++
					}
				}
			}
			receipts, err := reopened.ListSkillReceipts(ctx, partition, learning.SkillReceiptList{Limit: learning.MaxSkillPageSize})
			if err != nil || from != 1 || to != 1 || countRollbackRecords(receipts.Records) != 2 {
				t.Fatalf("durable receipts after %s: from=%d to=%d index=%+v err=%v", step, from, to, receipts.Records, err)
			}
		})
	}
}

func createActiveVersion(t *testing.T, store *Store, partition learning.SkillPartition, body, suffix string) learning.SkillVersion {
	t.Helper()
	ctx := context.Background()
	bundle := learning.SkillBundle{Name: "rollback", Description: "Rollback safely", Body: body}
	provenance := learning.SkillProvenance{ProposalIDs: []learning.ProposalID{learning.ProposalID("proposal-" + suffix)}, EvidenceRefs: []learning.EvidenceRef{{SessionID: session.SessionID("session-" + suffix), Locator: learning.EvidenceMessage, Digest: strings.Repeat("a", 64)}}}
	value, err := store.CreateDraft(ctx, partition, "agent", bundle, provenance)
	if err == nil {
		value, err = store.RecordEvaluation(ctx, partition, "agent", value.ID, value.Version, value.Revision, learning.SkillEvaluation{Verdict: learning.EvaluationPass, FixtureIDs: []string{"fixture"}})
	}
	if err == nil {
		value, err = store.Stage(ctx, partition, "agent", value.ID, value.Version, value.Revision)
	}
	if err == nil {
		value, err = store.Activate(ctx, partition, "agent", value.ID, value.Version, value.Revision)
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func countRollbackRecords(records []learning.SkillReceiptRecord) int {
	count := 0
	for _, record := range records {
		if record.Receipt.Operation == "rollback_from" || record.Receipt.Operation == "rollback_to" {
			count++
		}
	}
	return count
}
