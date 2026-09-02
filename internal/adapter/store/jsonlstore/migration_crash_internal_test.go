package jsonlstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func seedMigrationV1(t *testing.T, st *Store, id session.SessionID) string {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0))
	if err := sess.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
		t.Fatal(err)
	}
	payload, err := sessnap.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	path := st.resolver.legacyPath(id, kindSnapshot)
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func acquireMigrationTestContext(t *testing.T, st *Store) (context.Context, func()) {
	t.Helper()
	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, func() {
		t.Helper()
		if err := release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionStorageContinuity_Scenario4_PerFamilyCrashSafety_InjectedFailures(t *testing.T) {
	ctx := context.Background()
	st, err := newStoreWithSnapshotOps(t.TempDir(), defaultSnapshotOps())
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationV1(t, st, "space-failure")
	inspection, err := st.InspectSessionMigration(ctx)
	if err != nil || len(inspection.Families) != 1 {
		t.Fatalf("inspection = %+v, %v", inspection, err)
	}
	original := st.snapshot.write
	st.snapshot.write = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }
	migrationCtx, releaseMigration := acquireMigrationTestContext(t, st)
	reason, err := st.MigrateSessionFamily(migrationCtx, inspection.Families[0])
	releaseMigration()
	if err != nil || reason != "insufficient_space" {
		t.Fatalf("space failure = %q, %v", reason, err)
	}
	if _, err := os.Stat(st.resolver.canonicalPath("space-failure", kindSnapshot)); err != nil {
		t.Fatalf("space failure removed v1: %v", err)
	}
	if _, err := os.Stat(st.resolver.currentSnapshotPath("space-failure")); !os.IsNotExist(err) {
		t.Fatalf("space failure left current v2: %v", err)
	}
	st.snapshot.write = original

	// A host crash at the directory-sync boundary can leave the atomically
	// promoted v2 visible. The migration must not remove v1 on that failed attempt;
	// a retry verifies the existing v2 before removing v1.
	st2, err := newStoreWithSnapshotOps(t.TempDir(), defaultSnapshotOps())
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationV1(t, st2, "post-rename-crash")
	inspection, err = st2.InspectSessionMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	originalSync := st2.snapshot.syncDir
	syncCalls := 0
	st2.snapshot.syncDir = func(dir *os.File) error {
		syncCalls++
		if syncCalls == 5 { // after legacy sidecars and snapshot authority are durable
			return errors.New("injected post-rename crash")
		}
		return originalSync(dir)
	}
	migrationCtx, releaseMigration = acquireMigrationTestContext(t, st2)
	defer releaseMigration()
	reason, err = st2.MigrateSessionFamily(migrationCtx, inspection.Families[0])
	if err == nil || reason != "" {
		t.Fatalf("post-rename failure = %q, %v", reason, err)
	}
	if _, err := os.Stat(st2.resolver.canonicalPath("post-rename-crash", kindSnapshot)); err != nil {
		t.Fatalf("post-rename failure removed v1: %v", err)
	}
	if _, err := st2.Load(ctx, "post-rename-crash"); err != nil {
		t.Fatalf("promoted v2 is not authoritative: %v", err)
	}
	st2.snapshot.syncDir = originalSync
	reason, err = st2.MigrateSessionFamily(migrationCtx, inspection.Families[0])
	if err != nil || reason != "" {
		t.Fatalf("retry = %q, %v", reason, err)
	}
	if _, err := os.Stat(st2.resolver.canonicalPath("post-rename-crash", kindSnapshot)); !os.IsNotExist(err) {
		t.Fatalf("verified retry retained v1: %v", err)
	}
	matches, err := filepath.Glob(st2.resolver.currentSnapshotPath("post-rename-crash") + snapshotTempMarker + "*")
	if err != nil || len(matches) > 1 {
		t.Fatalf("replacement temps = %v, %v", matches, err)
	}
}

func TestMigrateSessionFamilyRestrictsLegacyFamilyWithoutAppending(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("private-direct-migration")
	snapshotPath := seedMigrationV1(t, st, id)
	snapshotBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshotBytes = bytes.TrimSuffix(snapshotBytes, []byte{'\n'})
	legacyEvents := st.resolver.legacyPath(id, kindEvents)
	legacyTools := st.resolver.legacyPath(id, kindTools)
	eventBytes := []byte("legacy event bytes\n")
	toolBytes := []byte("legacy tool bytes\n")
	writeBytes(t, legacyEvents, eventBytes)
	writeBytes(t, legacyTools, toolBytes)
	for _, path := range []string{snapshotPath, legacyEvents, legacyTools} {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("Chmod %s: %v", path, err)
		}
	}

	inspection, err := st.InspectSessionMigration(context.Background())
	if err != nil || len(inspection.Families) != 1 {
		t.Fatalf("InspectSessionMigration = %+v, %v", inspection, err)
	}
	ctx, release := acquireMigrationTestContext(t, st)
	reason, err := st.MigrateSessionFamily(ctx, inspection.Families[0])
	release()
	if err != nil || reason != "" {
		t.Fatalf("MigrateSessionFamily = %q, %v", reason, err)
	}

	canonical := map[string][]byte{
		st.resolver.canonicalPath(id, kindEvents): eventBytes,
		st.resolver.canonicalPath(id, kindTools):  toolBytes,
	}
	for path, want := range canonical {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("migrated %s = %q, %v; want byte-preserved %q", path, got, err, want)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("migrated %s mode = %v, %v; want 0600", path, info.Mode().Perm(), err)
		}
	}
	current := st.resolver.currentSnapshotPath(id)
	currentBytes, err := os.ReadFile(current)
	if err != nil {
		t.Fatalf("Read current snapshot: %v", err)
	}
	decoded, err := decodeCurrentSnapshot(currentBytes)
	if err != nil || !bytes.Equal(decoded.Snapshot, snapshotBytes) {
		t.Fatalf("current snapshot payload preserved = %v, %v", bytes.Equal(decoded.Snapshot, snapshotBytes), err)
	}
	info, err := os.Stat(current)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("current snapshot mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if loaded, err := st.Load(context.Background(), id); err != nil || loaded.ID != id {
		t.Fatalf("Load migrated snapshot = %#v, %v", loaded, err)
	}
}

func TestMigrateSessionFamilyRequiresActiveAcquisition(t *testing.T) {
	st, err := newStoreWithSnapshotOps(t.TempDir(), defaultSnapshotOps())
	if err != nil {
		t.Fatal(err)
	}
	v1Path := seedMigrationV1(t, st, "ownership-boundary")
	inspection, err := st.InspectSessionMigration(context.Background())
	if err != nil || len(inspection.Families) != 1 {
		t.Fatalf("inspection = %+v, %v", inspection, err)
	}
	family := inspection.Families[0]

	if _, err := st.MigrateSessionFamily(context.Background(), family); err == nil {
		t.Fatal("migration without an acquisition succeeded")
	}
	if _, err := os.Stat(v1Path); err != nil {
		t.Fatalf("absent acquisition mutated v1: %v", err)
	}

	staleCtx, release := acquireMigrationTestContext(t, st)
	release()
	if _, err := st.MigrateSessionFamily(staleCtx, family); err == nil {
		t.Fatal("migration with a released acquisition succeeded")
	}
	if _, err := os.Stat(v1Path); err != nil {
		t.Fatalf("stale acquisition mutated v1: %v", err)
	}

	other, err := newStoreWithSnapshotOps(t.TempDir(), defaultSnapshotOps())
	if err != nil {
		t.Fatal(err)
	}
	foreignCtx, releaseForeign := acquireMigrationTestContext(t, other)
	defer releaseForeign()
	if _, err := st.MigrateSessionFamily(foreignCtx, family); err == nil {
		t.Fatal("migration with another store's acquisition succeeded")
	}
	if _, err := os.Stat(v1Path); err != nil {
		t.Fatalf("foreign acquisition mutated v1: %v", err)
	}
	if _, err := os.Stat(st.resolver.currentSnapshotPath(family.ID)); !os.IsNotExist(err) {
		t.Fatalf("rejected migrations created v2: %v", err)
	}
}
