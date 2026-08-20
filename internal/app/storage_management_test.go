package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
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

func TestBuildWiresStorageManagementAuthorityAndSingleWriter(t *testing.T) {
	manager := &session.Principal{Issuer: "https://idp.example", Subject: "storage-admin", GrantType: session.GrantTypeUser}
	built, err := Build(context.Background(), Config{
		Workspace:                   t.TempDir(),
		Model:                       "mock",
		UseMock:                     true,
		OwnershipEnforced:           true,
		StorageManagementPrincipals: []session.Principal{*manager},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if !built.Service.MaintenanceMutationAvailable() {
		t.Fatal("Build did not wire the private-store single-writer capability")
	}
	if _, err := built.Service.StorageHealth(context.Background()); !errors.Is(err, server.ErrManagementUnauthorized) {
		t.Fatalf("unauthenticated StorageHealth = %v, want ErrManagementUnauthorized", err)
	}
	if _, err := built.Service.StorageHealth(session.WithPrincipal(context.Background(), manager)); err != nil {
		t.Fatalf("authorized StorageHealth = %v", err)
	}
}

func TestLocalStorageManagementAuthorityUsesCrossProcessLease(t *testing.T) {
	storeDir := t.TempDir()
	cfg := Config{StoreDir: storeDir, LocalStorageManagement: true}
	_, _, first, firstOwner, closeFirst, err := buildStoreAndLease(cfg)
	if err != nil {
		t.Fatalf("first buildStoreAndLease: %v", err)
	}
	defer closeFirst()
	_, _, second, secondOwner, closeSecond, err := buildStoreAndLease(cfg)
	if err != nil {
		t.Fatalf("second buildStoreAndLease: %v", err)
	}
	defer closeSecond()
	if first == nil || second == nil {
		t.Fatal("embedded durable stores did not receive a cross-process lease")
	}
	if firstOwner == secondOwner {
		t.Fatal("independent embedded instances share a lease owner")
	}

	id := session.SessionID("maintenance-target")
	lease, err := first.Acquire(context.Background(), id, firstOwner)
	if err != nil {
		t.Fatalf("first lease acquire: %v", err)
	}
	defer func() { _ = first.Release(context.Background(), lease) }()
	if _, err := second.Acquire(context.Background(), id, secondOwner); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("second lease acquire = %v, want ErrLeaseHeld", err)
	}
}

func TestManagementAuthorityIsNotSingleWriterProof(t *testing.T) {
	store, _, closeStore, err := buildStore(Config{StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("buildStore: %v", err)
	}
	defer closeStore()
	if localStorageMaintenanceSingleWriter(store) {
		t.Fatal("shareable local store was classified single-writer")
	}
	if !storageManagementAuthorizer(Config{LocalStorageManagement: true})(context.Background()) {
		t.Fatal("local management authority was not granted independently")
	}
}

func TestRetentionHealthTerminalStatesDoNotClobberOtherMaintenance(t *testing.T) {
	state := &storageMaintenanceState{}
	now := time.Now()
	state.start("migration", "migration-a")
	state.beginSweep()
	state.finishSweep(now, time.Hour)
	state.beginSweep()
	state.failSweep(now.Add(time.Minute), time.Hour)

	got := state.snapshot()
	if got.ActiveJob != "migration" || !got.LastSweepAvailable || !got.LastSweep.Equal(now) ||
		!got.NextSweepAvailable || got.LastFailure != "retention sweep failed" {
		t.Fatalf("failed repeat sweep health = %+v", got)
	}

	state.beginSweep()
	state.disableSweep()
	got = state.snapshot()
	if got.ActiveJob != "migration" || !got.LastSweepAvailable || !got.LastSweep.Equal(now) ||
		got.NextSweepAvailable || got.LastFailure != "retention sweep unavailable" {
		t.Fatalf("disabled sweep health = %+v", got)
	}

	state.beginSweep()
	state.stopSweepSchedule()
	got = state.snapshot()
	if got.ActiveJob != "migration" || got.NextSweepAvailable || got.LastFailure != "retention sweep unavailable" {
		t.Fatalf("cancelled sweep health = %+v", got)
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
