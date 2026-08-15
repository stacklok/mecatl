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
			verified, found, err := reopened.Get(context.Background(), partition, "agent", draft.ID, draft.Version)
			if err != nil || !found || verified.Bundle != bundle {
				t.Fatalf("verified after %s = %+v found=%v err=%v", step, verified, found, err)
			}
		})
	}
}
