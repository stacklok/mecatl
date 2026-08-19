package redisstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
)

func TestMigrationLockRenewsPastOriginalExpiry(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	oldTTL, oldInterval := migrationLockTTL, migrationRenewInterval
	migrationLockTTL, migrationRenewInterval = 80*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { migrationLockTTL, migrationRenewInterval = oldTTL, oldInterval })

	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	for range 4 {
		time.Sleep(20 * time.Millisecond)
		mr.FastForward(30 * time.Millisecond)
	}
	if err := st.CheckSessionMigrationJobOwnership(ctx); err != nil {
		t.Fatalf("renewed acquisition lost before release: %v", err)
	}
}

func TestMigrationAcquisitionFencesStaleCheckpointAndRelease(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	id := strings.Repeat("b", 32)
	staleCtx, staleRelease, err := st.AcquireSessionMigrationJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	mr.Del(migrationLockKeyBase + id)
	successorCtx, successorRelease, err := st.AcquireSessionMigrationJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = successorRelease() })

	stale := port.SessionMigrationJob{ID: id, State: port.SessionMigrationCancelled}
	if err := st.SaveSessionMigrationJob(staleCtx, stale); err == nil {
		t.Fatal("stale acquisition checkpoint succeeded")
	}
	if err := staleRelease(); err == nil {
		t.Fatal("stale acquisition release did not report lost ownership")
	}
	if err := st.CheckSessionMigrationJobOwnership(successorCtx); err != nil {
		t.Fatalf("stale release removed successor: %v", err)
	}
	successor := port.SessionMigrationJob{ID: id, State: port.SessionMigrationRunning}
	if err := st.SaveSessionMigrationJob(successorCtx, successor); err != nil {
		t.Fatalf("successor checkpoint: %v", err)
	}
}

func TestMigrationOwnershipLossFailsClosed(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	id := strings.Repeat("c", 32)
	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	mr.Set(migrationLockKeyBase+id, "successor")
	if err := st.CheckSessionMigrationJobOwnership(ctx); err == nil {
		t.Fatal("ownership replacement was not detected")
	}
	if err := st.SaveSessionMigrationJob(ctx, port.SessionMigrationJob{ID: id}); err == nil {
		t.Fatal("checkpoint after ownership loss succeeded")
	}
}

func TestInspectSessionMigrationReturnsExplicitRestartAfterGenerationDrift(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	mr.HSet(sessionKeyPrefix+"invalid", fieldBlob, "not-json", fieldMtime, "1")
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	st.migrationInspectionObserver = func() { mr.Incr(metadataRebuildGenerationKey, 1) }

	inspection, err := st.InspectSessionMigration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Available || inspection.UnavailableReason != "inventory_changed_restart" {
		t.Fatalf("drifting inspection = %+v, want explicit restart", inspection)
	}
	if inspection.V1Families != 0 || inspection.V2Families != 0 || inspection.InvalidFamilies != 0 || len(inspection.Families) != 0 {
		t.Fatalf("drifting inspection exposed mixed plan: %+v", inspection)
	}
}

func TestFinalizeSessionMigrationUsesConstantWorkCoverageCAS(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	for i := range 250 {
		mr.ZAdd(metadataGlobalIndexKey, float64(i), string(rune(0x1000+i)))
	}
	mr.Set(metadataRebuildGenerationKey, "7")
	mr.Set(metadataIndexStateKey, metadataIndexStale)
	spy := &redisCommandSpy{}
	mr.Server().SetPreHook(spy.hook)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	published, err := st.FinalizeSessionMigrationCoverage(context.Background(), "7", 251)
	if err != nil || published {
		t.Fatalf("incomplete coverage publication = %v, %v", published, err)
	}
	spy.reset()
	published, err = st.FinalizeSessionMigrationCoverage(context.Background(), "7", 250)
	if err != nil || !published {
		t.Fatalf("complete coverage publication = %v, %v", published, err)
	}
	for _, command := range spy.snapshot() {
		switch command.name {
		case "EVALSHA":
			if len(command.args) == 0 || command.args[0] != publishMetadataReadyScript.Hash() || len(command.args) != 8 {
				t.Fatalf("publication script is not constant-work: %#v", command.args)
			}
		case "GET", "ZCARD", "SET":
		default:
			t.Fatalf("finalization performed unbounded work: %#v", command)
		}
	}
}
