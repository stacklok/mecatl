package server_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
)

func TestStaleMaintenanceBoundaryEnumeratesOwnedCandidatesOnly(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	owned := crashOrphanedSession(t, "owned-running")
	if err := owned.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	ownerless := crashOrphanedSession(t, "legacy-ownerless")
	scheduled := crashOrphanedSession(t, "sched--maintenance-owned")
	scheduled.Kind = session.SessionKindScheduled
	scheduled.Relationship = session.SessionRelationship{ScheduleName: "maintenance"}
	if err := scheduled.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	for _, sess := range []*session.Session{owned, ownerless, scheduled} {
		if err := store.Save(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := newPlacementTestService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:             store,
		OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.StaleRunningCandidates(ctx); !errors.Is(err, server.ErrManagementUnauthorized) {
		t.Fatalf("unauthorized StaleRunningCandidates = %v, want ErrManagementUnauthorized", err)
	}
	if settled, err := svc.SettleIfStale(ctx, owned.ID); settled || !errors.Is(err, server.ErrManagementUnauthorized) {
		t.Fatalf("unauthorized SettleIfStale = (%v, %v), want refusal", settled, err)
	}

	maintenanceCtx := syscaller.Context(ctx, syscaller.RootStaleSessionReconcile)
	candidates, err := svc.StaleRunningCandidates(maintenanceCtx)
	if err != nil {
		t.Fatalf("StaleRunningCandidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != owned.ID {
		t.Fatalf("stale candidates = %+v, want only owned running session", candidates)
	}
	if settled, err := svc.SettleIfStale(maintenanceCtx, ownerless.ID); err != nil || settled {
		t.Fatalf("ownerless SettleIfStale = (%v, %v), want no-op", settled, err)
	}
	if settled, err := svc.SettleIfStale(maintenanceCtx, scheduled.ID); err != nil || settled {
		t.Fatalf("scheduled SettleIfStale = (%v, %v), want no-op", settled, err)
	}
	reloaded, err := store.Load(ctx, ownerless.ID)
	if err != nil || reloaded.State != session.StateRunning || reloaded.Owner != nil {
		t.Fatalf("ownerless snapshot changed = (%+v, %v)", reloaded, err)
	}
}
