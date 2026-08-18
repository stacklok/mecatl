package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestStorageManagementAuthorityIsExplicit(t *testing.T) {
	manager := &session.Principal{Issuer: "https://idp.example", Subject: "storage-admin", GrantType: session.GrantTypeUser}
	tenant := &session.Principal{Issuer: manager.Issuer, Subject: "tenant", GrantType: session.GrantTypeUser}
	system := &session.Principal{Issuer: "mecatl://system", Subject: "scheduler", GrantType: session.GrantTypeSystem}

	authorize := storageManagementAuthorizer(Config{
		OwnershipEnforced:           true,
		StorageManagementPrincipals: []session.Principal{*manager},
	})
	if authorize == nil || !authorize(session.WithPrincipal(context.Background(), manager)) {
		t.Fatal("explicit storage manager was denied")
	}
	for name, ctx := range map[string]context.Context{
		"ordinary tenant":  session.WithPrincipal(context.Background(), tenant),
		"system principal": session.WithPrincipal(context.Background(), system),
		"anonymous":        context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			if authorize(ctx) {
				t.Fatal("request without explicit management authority was allowed")
			}
		})
	}
	if got := storageManagementAuthorizer(Config{OwnershipEnforced: true}); got != nil {
		t.Fatal("OIDC deployment advertised hidden default management authority")
	}

	local := storageManagementAuthorizer(Config{LocalStorageManagement: true})
	if local == nil || !local(context.Background()) {
		t.Fatal("explicit local embedded operator was denied")
	}
	if local(session.WithPrincipal(context.Background(), tenant)) {
		t.Fatal("local embedded authority accepted a request principal")
	}
}

func TestSessionStorageContinuity_Scenario6_HealthIsBoundedAndHonest(t *testing.T) {
	state := &storageMaintenanceState{}
	state.beginSweep()
	state.start("migration", "migration-a")
	state.start("cleanup", "cleanup-a")
	state.start("migration", "migration-b")

	got := state.snapshot()
	if got.ActiveJob != "cleanup,migration(2),retention_sweep" {
		t.Fatalf("active jobs = %q", got.ActiveJob)
	}

	state.finish("migration", "migration-a", "storage maintenance failed")
	got = state.snapshot()
	if got.ActiveJob != "cleanup,migration,retention_sweep" || got.LastFailure != "storage maintenance failed" {
		t.Fatalf("partial terminal state = %+v", got)
	}
	state.finish("cleanup", "cleanup-a", "")
	state.finish("migration", "migration-b", "")
	got = state.snapshot()
	if got.ActiveJob != "retention_sweep" || got.LastFailure != "storage maintenance failed" {
		t.Fatalf("completion/failure retention = %+v", got)
	}
}
