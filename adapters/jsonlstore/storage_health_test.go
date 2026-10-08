package jsonlstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionStorageContinuity_Scenario6_HealthIsBoundedAndHonest(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, s := range []*session.Session{
		session.New("main", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/secret/main", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0)),
	} {
		if err := st.Save(context.Background(), s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	if _, err := st.PageSessionMetadata(context.Background(), port.SessionMetadataPageRequest{Limit: 1}); err != nil {
		t.Fatalf("build catalog: %v", err)
	}

	work := map[inventoryWorkKind]int{}
	st.inventoryWorkObserver = func(kind inventoryWorkKind) { work[kind]++ }
	health, err := st.SessionStorageHealth(context.Background())
	if err != nil {
		t.Fatalf("SessionStorageHealth: %v", err)
	}
	if !health.Available || !health.CurrentBytesAvailable || health.CurrentBytes == 0 {
		t.Fatalf("current bytes = %+v, want available non-zero measurement", health)
	}
	if health.SessionCount != 1 || health.V2Count != 1 || health.MainCount != 1 || health.FileCount < 1 {
		t.Fatalf("health counts = %+v, want one v2 main session and at least one file", health)
	}
	if health.ReclaimableBytesAvailable {
		t.Fatalf("reclaimable bytes unexpectedly claimed available without a maintenance plan: %+v", health)
	}
	if work[inventoryWorkSnapshotRead] != 0 || work[inventoryWorkRebuild] != 0 {
		t.Fatalf("health traversed authoritative snapshots: work=%v", work)
	}
}

func TestMeasureStorageEntries_ConcurrentRemovalOnly(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := st.resolver.currentSnapshotPath("removed")
	if err := os.WriteFile(path, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(st.resolver.canonicalDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var health port.SessionStorageHealth
	if err := measureStorageEntries(&health, entries); err != nil {
		t.Fatalf("file removed after enumeration: %v", err)
	}
	if health.FileCount != 0 || health.CurrentBytes != 0 || health.V2Count != 0 {
		t.Fatalf("removed snapshot counted: %+v", health)
	}

	var snapshotEntry os.DirEntry
	for _, entry := range entries {
		if entry.Name() == filepath.Base(path) {
			snapshotEntry = entry
			break
		}
	}
	if snapshotEntry == nil {
		t.Fatal("snapshot missing from directory entries")
	}
	if err := measureStorageEntries(&health, []os.DirEntry{faultEntry{DirEntry: snapshotEntry, err: fmt.Errorf("concurrent removal: %w", fs.ErrNotExist)}}); err != nil {
		t.Fatalf("wrapped removal: %v", err)
	}
	if health.FileCount != 0 {
		t.Fatalf("removed file counted: %+v", health)
	}
	if err := measureStorageEntries(&health, []os.DirEntry{faultEntry{DirEntry: snapshotEntry, err: fs.ErrPermission}}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission failure = %v, want error", err)
	}
	if health.FileCount != 0 {
		t.Fatalf("failed stat counted: %+v", health)
	}
}

type faultEntry struct {
	os.DirEntry
	err error
}

func (e faultEntry) Info() (os.FileInfo, error) { return nil, e.err }
