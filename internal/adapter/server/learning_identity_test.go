package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestExcludedCurrentSourceCannotRematerialize(t *testing.T) {
	fixture := newProposalManifestFixture(t)
	svc := fixture.service()
	if _, _, err := svc.rematerializeProposal(t.Context(), fixture.part, fixture.record); err != nil {
		t.Fatalf("current source unavailable: %v", err)
	}
	source, err := fixture.store.Load(t.Context(), fixture.manifest.Source.Value)
	if err != nil {
		t.Fatal(err)
	}
	source.ExcludeFromLearning()
	if err := fixture.store.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.rematerializeProposal(t.Context(), fixture.part, fixture.record); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("excluded source rematerialized: %v", err)
	}
}

func TestExcludedSessionCannotReflectOrSupplyEvidence(t *testing.T) {
	ctx := t.Context()
	source, err := sessnap.Unmarshal([]byte(`{"id":"legacy-source","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"user","text":"remember concise output"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc := &Service{cfg: Config{Store: store, ReflectSession: func(_ context.Context, _ *session.Session) (ReflectionReceipt, error) {
		calls++
		return ReflectionReceipt{}, nil
	}}}
	if _, err := svc.ReflectSession(ctx, source.ID); !errors.Is(err, ErrFailedPrecondition) || calls != 0 {
		t.Fatalf("excluded reflection: %v calls=%d", err, calls)
	}
	for _, protocol := range []learning.EvidenceProtocol{"", "reflection-evidence/v1", learning.ReflectionEvidenceV2} {
		ref := learning.EvidenceRef{Protocol: protocol, SessionID: source.ID, Locator: learning.EvidenceMessage, Ordinal: 0}
		if _, status := svc.learningEvidenceCitation(ctx, ref); status == evidenceAvailabilityAvailable {
			t.Fatalf("excluded evidence available: %q", protocol)
		}
	}
	current := session.New("current", session.ModeDefault, source.EnvironmentRef, session.Limits{}, time.Unix(1, 0))
	if err := current.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := current.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReflectSession(ctx, current.ID); err != nil || calls != 1 {
		t.Fatalf("current reflection: %v calls=%d", err, calls)
	}
}
