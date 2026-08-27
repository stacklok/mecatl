package jsonlstore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

func TestMigrationCheckpointRequiresEveryDurabilityPrimitiveBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		weaken func(*snapshotOps)
	}{
		{name: "atomic replacement", weaken: func(ops *snapshotOps) {
			ops.rename = func(string, string) error { return syscall.ENOTSUP }
		}},
		{name: "file sync", weaken: func(ops *snapshotOps) {
			ops.syncFile = func(*os.File) error { return syscall.ENOTSUP }
		}},
		{name: "directory sync", weaken: func(ops *snapshotOps) {
			ops.syncDir = func(*os.File) error { return syscall.ENOTSUP }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := defaultSnapshotOps()
			tc.weaken(&ops)
			st, err := newStoreWithSnapshotOps(t.TempDir(), ops)
			if err != nil {
				t.Fatalf("newStoreWithSnapshotOps: %v", err)
			}
			id := "0123456789abcdef0123456789abcdef"
			ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), id)
			if err != nil {
				t.Fatalf("AcquireSessionMigrationJob: %v", err)
			}
			path, err := st.migrationJobPath(id)
			if err != nil {
				t.Fatalf("migrationJobPath: %v", err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				job := port.SessionMigrationJob{ID: id, State: port.SessionMigrationRunning, Processed: int64(attempt + 1)}
				if err := st.SaveSessionMigrationJob(ctx, job); err == nil {
					t.Fatalf("SaveSessionMigrationJob attempt %d succeeded", attempt+1)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("checkpoint after attempt %d exists or is unreadable: %v", attempt+1, err)
				}
			}
			if err := release(); err != nil {
				t.Fatalf("release: %v", err)
			}

			dir := t.TempDir()
			strong, err := New(dir)
			if err != nil {
				t.Fatalf("seed New: %v", err)
			}
			seedCtx, seedRelease, err := strong.AcquireSessionMigrationJob(context.Background(), id)
			if err != nil {
				t.Fatalf("seed AcquireSessionMigrationJob: %v", err)
			}
			original := port.SessionMigrationJob{ID: id, State: port.SessionMigrationRunning, Processed: 1}
			if err := strong.SaveSessionMigrationJob(seedCtx, original); err != nil {
				t.Fatalf("seed SaveSessionMigrationJob: %v", err)
			}
			if err := seedRelease(); err != nil {
				t.Fatalf("seed release: %v", err)
			}

			ops = defaultSnapshotOps()
			tc.weaken(&ops)
			weak, err := newStoreWithSnapshotOps(dir, ops)
			if err != nil {
				t.Fatalf("reopen weak store: %v", err)
			}
			weakCtx, weakRelease, err := weak.AcquireSessionMigrationJob(context.Background(), id)
			if err != nil {
				t.Fatalf("weak AcquireSessionMigrationJob: %v", err)
			}
			defer func() {
				if err := weakRelease(); err != nil {
					t.Errorf("weak release: %v", err)
				}
			}()
			for attempt := 0; attempt < 2; attempt++ {
				updated := original
				updated.Processed = int64(attempt + 2)
				if err := weak.SaveSessionMigrationJob(weakCtx, updated); err == nil {
					t.Fatalf("replacement attempt %d succeeded", attempt+1)
				}
				loaded, err := weak.LoadSessionMigrationJob(context.Background(), id)
				if err != nil {
					t.Fatalf("LoadSessionMigrationJob after attempt %d: %v", attempt+1, err)
				}
				if !reflect.DeepEqual(loaded, original) {
					t.Fatalf("checkpoint after attempt %d = %+v, want unchanged %+v", attempt+1, loaded, original)
				}
			}
		})
	}
}

func TestMigrationCheckpointSurvivesReopenAndCanBeRetried(t *testing.T) {
	dir := t.TempDir()
	id := "fedcba9876543210fedcba9876543210"
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, release, err := st.AcquireSessionMigrationJob(context.Background(), id)
	if err != nil {
		t.Fatalf("AcquireSessionMigrationJob: %v", err)
	}
	first := port.SessionMigrationJob{ID: id, State: port.SessionMigrationRunning, Processed: 1}
	if err := st.SaveSessionMigrationJob(ctx, first); err != nil {
		t.Fatalf("SaveSessionMigrationJob: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(dir, canonicalDirName, migrationJobsDir)); err != nil || !info.IsDir() {
		t.Fatalf("reopened migration registry = (%v, %v), want directory", info, err)
	}
	loaded, err := reopened.LoadSessionMigrationJob(context.Background(), id)
	if err != nil {
		t.Fatalf("LoadSessionMigrationJob: %v", err)
	}
	if !reflect.DeepEqual(loaded, first) {
		t.Fatalf("reopened checkpoint = %+v, want %+v", loaded, first)
	}

	retryCtx, retryRelease, err := reopened.AcquireSessionMigrationJob(context.Background(), id)
	if err != nil {
		t.Fatalf("retry AcquireSessionMigrationJob: %v", err)
	}
	defer func() {
		if err := retryRelease(); err != nil {
			t.Errorf("retry release: %v", err)
		}
	}()
	completed := first
	completed.State = port.SessionMigrationCompleted
	completed.Processed = 2
	if err := reopened.SaveSessionMigrationJob(retryCtx, completed); err != nil {
		t.Fatalf("retry SaveSessionMigrationJob: %v", err)
	}
	loaded, err = reopened.LoadSessionMigrationJob(context.Background(), id)
	if err != nil {
		t.Fatalf("LoadSessionMigrationJob after retry: %v", err)
	}
	if !reflect.DeepEqual(loaded, completed) {
		t.Fatalf("retried checkpoint = %+v, want %+v", loaded, completed)
	}
}
