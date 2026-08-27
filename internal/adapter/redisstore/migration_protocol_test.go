package redisstore

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
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
	mr.Set(migrationLockKeyBase+strings.Repeat("a", 32), "successor")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("renewal loss did not cancel bound operation context")
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
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ownership loss did not cancel bound operation context: %v", ctx.Err())
	}
	if err := st.SaveSessionMigrationJob(ctx, port.SessionMigrationJob{ID: id}); err == nil {
		t.Fatal("checkpoint after ownership loss succeeded")
	}
}

func TestMigrationFamilyCASRejectsLossBetweenPrecheckAndLuaWithoutSideEffects(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	sess := session.New("legacy-race", session.ModeAccept, "/work", session.Limits{}, time.Unix(1, 0))
	blob, err := sessnap.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	mr.HSet(sessionKey(sess.ID), fieldBlob, string(blob), fieldMtime, "1")
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), strings.Repeat("e", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	inspection, err := st.InspectSessionMigration(ctx)
	if err != nil || len(inspection.Families) != 1 {
		t.Fatalf("inspection = %+v, %v", inspection, err)
	}
	if err := st.CheckSessionMigrationJobOwnership(ctx); err != nil {
		t.Fatal(err)
	}
	st.migrationMutationObserver = func() { mr.Set(migrationLockKeyBase+strings.Repeat("e", 32), "successor") }
	if _, err := st.MigrateSessionFamily(ctx, inspection.Families[0]); err == nil {
		t.Fatal("stale family mutation succeeded")
	}
	if got := mr.HGet(sessionKey(sess.ID), fieldMetadataEntry); got != "" {
		t.Fatalf("stale mutation installed metadata %q", got)
	}
	if got := st.testClient().ZCard(context.Background(), metadataGlobalIndexKey).Val(); got != 0 {
		t.Fatalf("stale mutation changed global index cardinality to %d", got)
	}
}

func TestMigrationFinalizeCASRejectsStaleHolderWithoutSideEffects(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	mr.Set(metadataRebuildGenerationKey, "7")
	mr.Set(metadataIndexStateKey, metadataIndexStale)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	id := strings.Repeat("f", 32)
	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	if err := st.CheckSessionMigrationJobOwnership(ctx); err != nil {
		t.Fatal(err)
	}
	st.migrationMutationObserver = func() { mr.Set(migrationLockKeyBase+id, "successor") }
	if _, err := st.FinalizeSessionMigrationCoverage(ctx, "7", 0); err == nil {
		t.Fatal("stale finalization succeeded")
	}
	if got, _ := mr.Get(metadataIndexStateKey); got != metadataIndexStale {
		t.Fatalf("stale finalization changed readiness to %q", got)
	}
}

func TestInspectionRepairsMissingCoverageDespiteEqualOrphanCardinality(t *testing.T) {
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
	sess := session.New("covered-session", session.ModeAccept, "/work", session.Limits{}, time.Unix(1, 0))
	if err := st.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	member := mr.HGet(sessionKey(sess.ID), fieldMetadataEntry)
	mr.ZRem(metadataGlobalIndexKey, member)
	mr.ZAdd(metadataGlobalIndexKey, 0, "orphan-row")
	mr.Set(metadataIndexStateKey, metadataIndexStale)

	inspection, err := st.InspectSessionMigration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.V2Families != 1 || len(inspection.Families) != 1 || st.testClient().ZCard(context.Background(), metadataGlobalIndexKey).Val() != 1 {
		t.Fatalf("equal-cardinality drift was not a repair candidate: %+v", inspection)
	}
	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), strings.Repeat("1", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	if reason, err := st.MigrateSessionFamily(ctx, inspection.Families[0]); err != nil || reason != "" {
		t.Fatalf("repair = %q, %v", reason, err)
	}
	published, err := st.FinalizeSessionMigrationCoverage(ctx, inspection.Generation, 1)
	if !errors.Is(err, errMetadataIndexCoverage) {
		t.Fatalf("orphan coverage error = %v, want exact-coverage mismatch", err)
	}
	if published {
		t.Fatal("orphan row offset missing coverage and published readiness")
	}
	if got, _ := mr.Get(metadataIndexStateKey); got != metadataIndexStale {
		t.Fatalf("incomplete coverage changed readiness to %q", got)
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

func TestFinalizeSessionMigrationUsesBoundedCoverageProofAndConstantWorkCAS(t *testing.T) {
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
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "owner"}
	for i := range 250 {
		sess := session.New(session.SessionID("covered-"+strconv.Itoa(i)), session.ModeAccept, "/work", session.Limits{}, time.Unix(1, 0))
		if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
			t.Fatal(err)
		}
		if err := st.Save(context.Background(), sess); err != nil {
			t.Fatal(err)
		}
	}
	generation, err := st.rebuildGeneration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mr.Set(metadataIndexStateKey, metadataIndexStale)
	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), strings.Repeat("d", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	spy := &redisCommandSpy{}
	mr.Server().SetPreHook(spy.hook)
	spy.reset()

	published, err := st.FinalizeSessionMigrationCoverage(ctx, strconv.FormatInt(generation, 10), 250)
	if err != nil || !published {
		t.Fatalf("complete coverage publication = %v, %v", published, err)
	}
	var sawSnapshotScan, sawOwnerScan, sawPublication bool
	for _, command := range spy.snapshot() {
		switch command.name {
		case "SCAN":
			sawSnapshotScan = true
		case "ZSCAN":
			sawOwnerScan = true
		case "EVALSHA":
			if len(command.args) > 0 && command.args[0] == publishMetadataReadyScript.Hash() {
				sawPublication = true
				if len(command.args) != 10 {
					t.Fatalf("publication script is not constant-work: %#v", command.args)
				}
			}
		}
	}
	if !sawSnapshotScan || !sawOwnerScan || !sawPublication {
		t.Fatalf("coverage protocol commands missing: snapshot_scan=%v owner_scan=%v publication=%v", sawSnapshotScan, sawOwnerScan, sawPublication)
	}
}

func TestFinalizeRejectsUnmatchedOwnerMembershipsAndPublishesOnlyHealthyPaging(t *testing.T) {
	for _, tc := range []struct {
		name   string
		orphan func(*miniredis.Miniredis, string)
	}{
		{name: "malformed", orphan: func(mr *miniredis.Miniredis, ownerKey string) { mr.ZAdd(ownerKey, 0, "malformed-owner-row") }},
		{name: "wrong-owner", orphan: func(mr *miniredis.Miniredis, ownerKey string) {
			mr.ZAdd(ownerKey, 0, mr.HGet(sessionKey("bob"), fieldMetadataEntry))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
			bob := &session.Principal{Issuer: "https://issuer.example", Subject: "bob"}
			for _, fixture := range []struct {
				id    session.SessionID
				owner *session.Principal
			}{{"alice", alice}, {"bob", bob}} {
				sess := session.New(fixture.id, session.ModeAccept, "/work", session.Limits{}, time.Unix(1, 0))
				if err := sess.RestoreLabels(fixture.owner, session.Authority{}); err != nil {
					t.Fatal(err)
				}
				if err := st.Save(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			}
			generation, err := st.rebuildGeneration(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ownerKey := metadataOwnerIndexBase + metadataOwnerScope(alice)
			tc.orphan(mr, ownerKey)
			if got := st.testClient().ZCard(context.Background(), metadataGlobalIndexKey).Val(); got != 2 {
				t.Fatalf("global cardinality = %d, want equal snapshot count despite owner orphan", got)
			}
			mr.Set(metadataIndexStateKey, metadataIndexStale)
			ownerCount := st.testClient().ZCard(context.Background(), ownerKey).Val()
			inspection, err := st.InspectSessionMigration(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(inspection.Families) != 0 || st.testClient().ZCard(context.Background(), ownerKey).Val() != ownerCount {
				t.Fatalf("read-only inspection mutated or misclassified owner orphan: %+v", inspection)
			}
			ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), strings.Repeat("9", 32))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = release() })

			published, err := st.FinalizeSessionMigrationCoverage(ctx, strconv.FormatInt(generation, 10), 2)
			if published || !errors.Is(err, errMetadataIndexCoverage) {
				t.Fatalf("owner orphan publication = %v, %v", published, err)
			}
			if _, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10, OwnershipEnforced: true, Owner: alice}); !errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
				t.Fatalf("unready owner paging error = %v", err)
			}

			mr.ZRem(ownerKey, "malformed-owner-row")
			mr.ZRem(ownerKey, mr.HGet(sessionKey("bob"), fieldMetadataEntry))
			published, err = st.FinalizeSessionMigrationCoverage(ctx, strconv.FormatInt(generation, 10), 2)
			if err != nil || !published {
				t.Fatalf("repaired owner coverage publication = %v, %v", published, err)
			}
			page, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10, OwnershipEnforced: true, Owner: alice})
			if err != nil {
				t.Fatal(err)
			}
			assertOwnerPage(t, page, alice, 1, 1)
		})
	}
}

func TestFinalizeRejectsGenerationDriftAfterExactCoverageProof(t *testing.T) {
	st, mr := newMetadataTestStore(t)
	sess := session.New("drift", session.ModeAccept, "/work", session.Limits{}, time.Unix(1, 0))
	if err := st.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	generation, err := st.rebuildGeneration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mr.Set(metadataIndexStateKey, metadataIndexStale)
	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), strings.Repeat("8", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	st.migrationMutationObserver = func() { mr.Incr(metadataRebuildGenerationKey, 1) }

	published, err := st.FinalizeSessionMigrationCoverage(ctx, strconv.FormatInt(generation, 10), 1)
	if err != nil || published {
		t.Fatalf("drifted publication = %v, %v", published, err)
	}
	if got, _ := mr.Get(metadataIndexStateKey); got != metadataIndexStale {
		t.Fatalf("drifted publication changed readiness to %q", got)
	}
}
