package jsonlstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestDeleteDurabilityBoundariesAndRetry(t *testing.T) {
	for _, failSync := range []int{1, 2, 3, 4} {
		t.Run(string(rune('0'+failSync)), func(t *testing.T) {
			st := newInternalStore(t)
			id := session.SessionID("delete-durable")
			for _, kind := range sidecarKinds {
				writeBytes(t, st.resolver.canonicalPath(id, kind), []byte("sidecar\n"))
			}
			writeBytes(t, st.resolver.currentSnapshotPath(id), []byte("authority"))

			syncDir := st.snapshot.syncDir
			calls := 0
			st.snapshot.syncDir = func(dir *os.File) error {
				calls++
				if calls == failSync {
					return syscall.EIO
				}
				return syncDir(dir)
			}
			err := st.Delete(context.Background(), id)
			if !errors.Is(err, syscall.EIO) {
				t.Fatalf("Delete error = %v, want EIO", err)
			}
			if failSync <= 2 {
				if _, err := os.Stat(st.resolver.currentSnapshotPath(id)); err != nil {
					t.Fatalf("sidecar-stage failure removed authority: %v", err)
				}
			}

			st.snapshot.syncDir = syncDir
			if err := st.Delete(context.Background(), id); err != nil {
				t.Fatalf("retry Delete: %v", err)
			}
			for _, path := range []string{
				st.resolver.canonicalPath(id, kindTools),
				st.resolver.canonicalPath(id, kindEvents),
				st.resolver.currentSnapshotPath(id),
			} {
				assertMissing(t, path)
			}
		})
	}
}

func TestLegacyPromotionDurabilityBoundariesAndRetry(t *testing.T) {
	for _, failSync := range []int{1, 2, 3, 4} {
		t.Run(string(rune('0'+failSync)), func(t *testing.T) {
			st := newInternalStore(t)
			id := session.SessionID("promotion-retry")
			for _, kind := range familyOrder {
				contents := []byte("sidecar")
				if kind == kindSnapshot {
					contents = append(snapshotLine(t, id, "legacy"), '\n')
				}
				writeBytes(t, st.resolver.legacyPath(id, kind), contents)
			}
			syncDir := st.snapshot.syncDir
			calls := 0
			st.snapshot.syncDir = func(dir *os.File) error {
				calls++
				if calls == failSync {
					return syscall.EIO
				}
				return syncDir(dir)
			}
			if err := st.prepareWrite(id); !errors.Is(err, syscall.EIO) {
				t.Fatalf("prepareWrite error = %v, want EIO", err)
			}
			if failSync <= 2 {
				if _, err := os.Stat(st.resolver.legacyPath(id, kindSnapshot)); err != nil {
					t.Fatalf("sidecar-boundary failure moved authority: %v", err)
				}
			}

			st.snapshot.syncDir = syncDir
			if err := st.prepareWrite(id); err != nil {
				t.Fatalf("retry prepareWrite: %v", err)
			}
			for _, kind := range familyOrder {
				assertMissing(t, st.resolver.legacyPath(id, kind))
				if _, err := os.Stat(st.resolver.canonicalPath(id, kind)); err != nil {
					t.Fatalf("retry did not converge %s: %v", kind, err)
				}
			}
		})
	}
}

func TestDeleteSnapshotMutationFailureLeavesDurableSidecarRemoval(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("delete-before-authority")
	v1 := st.resolver.canonicalPath(id, kindSnapshot)
	current := st.resolver.currentSnapshotPath(id)
	writeBytes(t, v1, append(snapshotLine(t, id, "v1"), '\n'))
	writeBytes(t, current, []byte("current"))
	for _, kind := range sidecarKinds {
		writeBytes(t, st.resolver.canonicalPath(id, kind), []byte("sidecar"))
	}

	remove := st.snapshot.remove
	st.snapshot.remove = func(path string) error {
		if path == v1 {
			return syscall.EIO
		}
		return remove(path)
	}
	var syncs int
	syncDir := st.snapshot.syncDir
	st.snapshot.syncDir = func(dir *os.File) error {
		syncs++
		return syncDir(dir)
	}
	if err := st.Delete(context.Background(), id); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Delete error = %v, want EIO", err)
	}
	for _, kind := range sidecarKinds {
		assertMissing(t, st.resolver.canonicalPath(id, kind))
	}
	for _, path := range []string{v1, current} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("snapshot mutation failure removed %s: %v", path, err)
		}
	}
	if syncs != 2 {
		t.Fatalf("sync calls = %d, want durable root and canonical sidecar boundary", syncs)
	}
}

func TestDeleteSyncsPartialAuthorityMutationBeforeReturningError(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("delete-partial-authority")
	v1 := st.resolver.canonicalPath(id, kindSnapshot)
	current := st.resolver.currentSnapshotPath(id)
	writeBytes(t, v1, append(snapshotLine(t, id, "v1"), '\n'))
	writeBytes(t, current, []byte("current"))

	remove := st.snapshot.remove
	st.snapshot.remove = func(path string) error {
		if path == current {
			return syscall.EIO
		}
		return remove(path)
	}
	var synced []string
	syncDir := st.snapshot.syncDir
	st.snapshot.syncDir = func(dir *os.File) error {
		synced = append(synced, filepath.Clean(dir.Name()))
		return syncDir(dir)
	}
	if err := st.Delete(context.Background(), id); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Delete error = %v, want EIO", err)
	}
	assertMissing(t, v1)
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("later failed authority removal changed current: %v", err)
	}
	if len(synced) != 4 {
		t.Fatalf("delete boundaries synced %v, want both affected directories before and after partial authority mutation", synced)
	}
}

func TestDestructiveOperationsRequireDirectorySyncBeforeMutation(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		st := newInternalStore(t)
		id := session.SessionID("unsupported-delete")
		paths := []string{
			st.resolver.canonicalPath(id, kindEvents),
			st.resolver.currentSnapshotPath(id),
		}
		for _, path := range paths {
			writeBytes(t, path, []byte("present"))
		}
		st.durability.DirectorySync = false
		if err := st.Delete(context.Background(), id); err == nil {
			t.Fatal("Delete without directory sync succeeded")
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("unsupported Delete mutated %s: %v", path, err)
			}
		}
	})

	t.Run("legacy promotion", func(t *testing.T) {
		st := newInternalStore(t)
		id := session.SessionID("unsupported-promotion")
		for _, kind := range familyOrder {
			contents := []byte("sidecar")
			if kind == kindSnapshot {
				contents = append(snapshotLine(t, id, "legacy"), '\n')
			}
			writeBytes(t, st.resolver.legacyPath(id, kind), contents)
		}
		st.durability.DirectorySync = false
		if err := st.prepareWrite(id); err == nil {
			t.Fatal("legacy promotion without directory sync succeeded")
		}
		for _, kind := range familyOrder {
			if _, err := os.Stat(st.resolver.legacyPath(id, kind)); err != nil {
				t.Fatalf("unsupported promotion mutated legacy %s: %v", kind, err)
			}
			assertMissing(t, st.resolver.canonicalPath(id, kind))
		}
	})
}

func TestLegacyPromotionMoveFailuresSyncPartialProgressAndRetry(t *testing.T) {
	for _, failedKind := range []sessionKind{kindEvents, kindSnapshot} {
		t.Run(string(failedKind), func(t *testing.T) {
			st := newInternalStore(t)
			id := session.SessionID("promotion-move-failure")
			for _, kind := range familyOrder {
				contents := []byte("sidecar")
				if kind == kindSnapshot {
					contents = append(snapshotLine(t, id, "legacy"), '\n')
				}
				writeBytes(t, st.resolver.legacyPath(id, kind), contents)
			}
			moveRoot := st.snapshot.moveRoot
			st.snapshot.moveRoot = func(root *os.Root, src, dst string) error {
				if filepath.Ext(src) != "" && filepath.Base(src) == filepath.Base(st.resolver.legacyPath(id, failedKind)) {
					return syscall.EIO
				}
				return moveRoot(root, src, dst)
			}
			var syncs int
			syncDir := st.snapshot.syncDir
			st.snapshot.syncDir = func(dir *os.File) error {
				syncs++
				return syncDir(dir)
			}
			if err := st.prepareWrite(id); !errors.Is(err, syscall.EIO) {
				t.Fatalf("prepareWrite error = %v, want EIO", err)
			}
			if syncs < 2 {
				t.Fatalf("partial move progress was not synced: %d calls", syncs)
			}
			if _, err := os.Stat(st.resolver.legacyPath(id, kindSnapshot)); err != nil {
				t.Fatalf("move failure lost legacy authority: %v", err)
			}

			st.snapshot.moveRoot = moveRoot
			if err := st.prepareWrite(id); err != nil {
				t.Fatalf("retry prepareWrite: %v", err)
			}
			for _, kind := range familyOrder {
				assertMissing(t, st.resolver.legacyPath(id, kind))
				if _, err := os.Stat(st.resolver.canonicalPath(id, kind)); err != nil {
					t.Fatalf("retry did not converge %s: %v", kind, err)
				}
			}
		})
	}
}

func TestExplicitMigrationFinalRemovalIsSyncedAndRetryable(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("explicit-final-sync")
	seedMigrationV1(t, st, id)
	inspection, err := st.InspectSessionMigration(context.Background())
	if err != nil || len(inspection.Families) != 1 {
		t.Fatalf("InspectSessionMigration = %+v, %v", inspection, err)
	}
	ctx, release := acquireMigrationTestContext(t, st)
	defer release()

	syncDir := st.snapshot.syncDir
	calls := 0
	st.snapshot.syncDir = func(dir *os.File) error {
		calls++
		if calls == 6 { // final canonical sync after v1 authority removal
			return syscall.EIO
		}
		return syncDir(dir)
	}
	reason, err := st.MigrateSessionFamily(ctx, inspection.Families[0])
	if err != nil || reason != "backend_failure" {
		t.Fatalf("MigrateSessionFamily = %q, %v", reason, err)
	}
	assertMissing(t, st.resolver.canonicalPath(id, kindSnapshot))
	if _, err := os.Stat(st.resolver.currentSnapshotPath(id)); err != nil {
		t.Fatalf("current authority missing after final-sync failure: %v", err)
	}

	st.snapshot.syncDir = syncDir
	reason, err = st.MigrateSessionFamily(ctx, inspection.Families[0])
	if err != nil || reason != "" {
		t.Fatalf("retry MigrateSessionFamily = %q, %v", reason, err)
	}
}

func TestExplicitMigrationSyncsExistingV2BeforeRemovingV1(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("explicit-existing-v2-sync")
	v1Path := seedMigrationV1(t, st, id)
	inspection, err := st.InspectSessionMigration(context.Background())
	if err != nil || len(inspection.Families) != 1 {
		t.Fatalf("InspectSessionMigration = %+v, %v", inspection, err)
	}
	info, err := os.Stat(v1Path)
	if err != nil {
		t.Fatalf("stat v1: %v", err)
	}
	line, err := os.ReadFile(v1Path)
	if err != nil {
		t.Fatalf("read v1: %v", err)
	}
	line = []byte(strings.TrimSpace(string(line)))
	sess, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("decode v1: %v", err)
	}
	current, err := json.Marshal(currentSnapshot{
		Format: currentSnapshotFormat, ModifiedAt: info.ModTime(), Metadata: metaSnapshotFromSession(sess), Snapshot: line,
	})
	if err != nil {
		t.Fatalf("marshal v2: %v", err)
	}
	writeBytes(t, st.resolver.currentSnapshotPath(id), current) // rename visible; directory sync was interrupted

	ctx, release := acquireMigrationTestContext(t, st)
	defer release()
	syncDir := st.snapshot.syncDir
	st.snapshot.syncDir = func(*os.File) error { return syscall.EIO }
	reason, err := st.MigrateSessionFamily(ctx, inspection.Families[0])
	if err != nil || reason != "backend_failure" {
		t.Fatalf("MigrateSessionFamily = %q, %v", reason, err)
	}
	if _, err := os.Stat(v1Path); err != nil {
		t.Fatalf("migration removed v1 before v2 directory durability: %v", err)
	}

	st.snapshot.syncDir = syncDir
	reason, err = st.MigrateSessionFamily(ctx, inspection.Families[0])
	if err != nil || reason != "" {
		t.Fatalf("retry MigrateSessionFamily = %q, %v", reason, err)
	}
	assertMissing(t, v1Path)
}

func TestLegacyPromotionSyncsRootAndCanonicalAtEachBoundary(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("cross-directory-promotion")
	for _, kind := range familyOrder {
		contents := []byte("sidecar")
		if kind == kindSnapshot {
			contents = append(snapshotLine(t, id, "legacy"), '\n')
		}
		writeBytes(t, st.resolver.legacyPath(id, kind), contents)
	}
	var synced []string
	syncDir := st.snapshot.syncDir
	st.snapshot.syncDir = func(dir *os.File) error {
		synced = append(synced, filepath.Clean(dir.Name()))
		return syncDir(dir)
	}
	if err := st.prepareWrite(id); err != nil {
		t.Fatalf("prepareWrite: %v", err)
	}
	want := []string{st.resolver.dir, st.resolver.canonicalDir(), st.resolver.dir, st.resolver.canonicalDir()}
	if !reflect.DeepEqual(synced, want) {
		t.Fatalf("synced directories = %v, want %v", synced, want)
	}
}
