package jsonlstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionStorageContinuity_Scenario1_AtomicCrashRecovery(t *testing.T) {
	t.Parallel()

	t.Run("temporary replacement shares destination directory", func(t *testing.T) {
		dir := t.TempDir()
		expected := filepath.Join(dir, canonicalDirName)
		ops := defaultSnapshotOps()
		createTemp := ops.createTemp
		ops.createTemp = func(gotDir, pattern string) (*os.File, error) {
			if gotDir != expected {
				return nil, errors.New("temporary replacement is not in the snapshot directory")
			}
			return createTemp(gotDir, pattern)
		}
		st, err := newStoreWithSnapshotOps(dir, ops)
		if err != nil {
			t.Fatalf("newStoreWithSnapshotOps: %v", err)
		}
		if err := st.Save(context.Background(), newSnapshotSession("same-dir", "saved")); err != nil {
			t.Fatalf("Save with same-directory temporary: %v", err)
		}
	})

	for _, tc := range []struct {
		name    string
		inject  func(*snapshotOps)
		wantNew bool
	}{
		{
			name: "short write before file sync",
			inject: func(ops *snapshotOps) {
				ops.write = func(_ *os.File, data []byte) (int, error) {
					return len(data) / 2, nil
				}
			},
		},
		{
			name: "partial write before file sync",
			inject: func(ops *snapshotOps) {
				ops.write = func(f *os.File, data []byte) (int, error) {
					n, _ := f.Write(data[:len(data)/2])
					return n, syscall.EIO
				}
			},
		},
		{
			name: "file sync failure",
			inject: func(ops *snapshotOps) {
				ops.syncFile = func(*os.File) error { return syscall.EIO }
			},
		},
		{
			name: "rename failure",
			inject: func(ops *snapshotOps) {
				ops.rename = func(string, string) error { return syscall.EIO }
			},
		},
		{
			name: "directory open failure after rename",
			inject: func(ops *snapshotOps) {
				ops.openDir = func(string) (*os.File, error) { return nil, syscall.EIO }
			},
			wantNew: true,
		},
		{
			name: "directory sync failure after rename",
			inject: func(ops *snapshotOps) {
				ops.syncDir = func(*os.File) error { return syscall.EIO }
			},
			wantNew: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prior := newSnapshotSession("atomic-crash", "prior")
			st, err := New(dir)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := st.Save(context.Background(), prior); err != nil {
				t.Fatalf("seed Save: %v", err)
			}
			// A stale v1 record must never regain authority after v2 committed.
			v1Path := st.resolver.canonicalPath(prior.ID, kindSnapshot)
			if err := os.WriteFile(v1Path, []byte(`{"id":"atomic-crash","state":"idle","mode":"default","limits":{},"counters":{},"workspace":"/stale","created_at":"2023-11-14T22:13:20Z","messages":[],"title":"stale-v1"}`+"\n"), 0o600); err != nil {
				t.Fatalf("write stale v1: %v", err)
			}

			ops := defaultSnapshotOps()
			tc.inject(&ops)
			failing, err := newStoreWithSnapshotOps(dir, ops)
			if err != nil {
				t.Fatalf("newStoreWithSnapshotOps: %v", err)
			}
			if err := failing.Save(context.Background(), newSnapshotSession(prior.ID, "new")); err == nil {
				t.Fatal("Save with injected failure = nil, want loud error")
			}

			reopened, err := New(dir)
			if err != nil {
				t.Fatalf("reopen New: %v", err)
			}
			got, err := reopened.Load(context.Background(), prior.ID)
			if err != nil {
				t.Fatalf("Load after injected failure: %v", err)
			}
			wantTitle := "prior"
			if tc.wantNew {
				wantTitle = "new"
			}
			if got.Title != wantTitle {
				t.Fatalf("reopened title = %q, want committed %q (never torn, absent, or stale v1)", got.Title, wantTitle)
			}
		})
	}
}

func TestSessionStorageContinuity_Scenario1_DurabilityCapabilityTruth(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ops := defaultSnapshotOps()
	ops.atomicRename = true
	ops.fileSync = true
	ops.syncDir = func(*os.File) error { return syscall.ENOTSUP }
	st, err := newStoreWithSnapshotOps(dir, ops)
	if err != nil {
		t.Fatalf("New with unsupported directory sync: %v", err)
	}
	capability := st.SnapshotDurability()
	if !capability.AtomicReplace || !capability.FileSync || capability.DirectorySync || capability.HostCrashSafe() {
		t.Fatalf("unsupported directory sync overclaimed: %+v", capability)
	}
	if err := st.Save(context.Background(), newSnapshotSession("weak-durability", "saved")); err != nil {
		t.Fatalf("Save under explicit weaker durability: %v", err)
	}

	noFileSyncOps := defaultSnapshotOps()
	noFileSyncOps.syncFile = func(*os.File) error { return syscall.ENOTSUP }
	noFileSync, err := newStoreWithSnapshotOps(filepath.Join(dir, "no-file-sync"), noFileSyncOps)
	if err != nil {
		t.Fatalf("New with unsupported file sync: %v", err)
	}
	if got := noFileSync.SnapshotDurability(); got.FileSync || got.HostCrashSafe() {
		t.Fatalf("unsupported file sync overclaimed: %+v", got)
	}
	if err := noFileSync.Save(context.Background(), newSnapshotSession("weak-file-sync", "saved")); err != nil {
		t.Fatalf("Save under weaker file-sync durability: %v", err)
	}

	noRenameOps := defaultSnapshotOps()
	noRenameOps.rename = func(string, string) error { return syscall.ENOTSUP }
	noRename, err := newStoreWithSnapshotOps(filepath.Join(dir, "no-rename"), noRenameOps)
	if err != nil {
		t.Fatalf("New with unsupported atomic replacement: %v", err)
	}
	if got := noRename.SnapshotDurability(); got.AtomicReplace || got.HostCrashSafe() {
		t.Fatalf("unsupported atomic replacement overclaimed: %+v", got)
	}

	supportedOps := defaultSnapshotOps()
	supportedOps.atomicRename = true
	supportedOps.fileSync = true
	supportedOps.syncDir = func(*os.File) error { return nil }
	fullyDurable, err := newStoreWithSnapshotOps(filepath.Join(dir, "supported"), supportedOps)
	if err != nil {
		t.Fatalf("New with directory sync: %v", err)
	}
	if got := fullyDurable.SnapshotDurability(); !got.HostCrashSafe() {
		t.Fatalf("supported durability underreported: %+v", got)
	}
}

func TestSessionStorageContinuity_Scenario1_DiskFullPreservesCommittedSnapshot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		inject func(*snapshotOps)
	}{
		{
			name: "write",
			inject: func(ops *snapshotOps) {
				ops.write = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }
			},
		},
		{
			name: "file sync",
			inject: func(ops *snapshotOps) {
				ops.syncFile = func(*os.File) error { return syscall.ENOSPC }
			},
		},
		{
			name: "rename",
			inject: func(ops *snapshotOps) {
				ops.rename = func(string, string) error { return syscall.ENOSPC }
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			id := session.SessionID("disk-full")
			st, err := New(dir)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := st.Save(context.Background(), newSnapshotSession(id, "prior")); err != nil {
				t.Fatalf("seed Save: %v", err)
			}
			toolPath := st.resolver.canonicalPath(id, kindTools)
			toolData := []byte("committed sidecar\n")
			if err := os.WriteFile(toolPath, toolData, 0o600); err != nil {
				t.Fatalf("write sidecar: %v", err)
			}

			ops := defaultSnapshotOps()
			tc.inject(&ops)
			failing, err := newStoreWithSnapshotOps(dir, ops)
			if err != nil {
				t.Fatalf("newStoreWithSnapshotOps: %v", err)
			}
			err = failing.Save(context.Background(), newSnapshotSession(id, "new"))
			if err == nil || !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("Save error = %v, want loud ENOSPC", err)
			}
			got, err := failing.Load(context.Background(), id)
			if err != nil || got.Title != "prior" {
				t.Fatalf("committed snapshot after failure = title %q, err %v; want prior", got.Title, err)
			}
			gotSidecar, err := os.ReadFile(toolPath)
			if err != nil || string(gotSidecar) != string(toolData) {
				t.Fatalf("committed sidecar changed: %q, %v", gotSidecar, err)
			}
		})
	}
}

func newSnapshotSession(id session.SessionID, title string) *session.Session {
	s := session.New(id, session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1700000000, 0).UTC())
	s.SetTitle(title)
	return s
}
