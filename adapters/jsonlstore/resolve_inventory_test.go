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

	"github.com/stacklok/mecatl/engine/session"
)

type cachedInfoEntry struct {
	fs.DirEntry
	info fs.FileInfo
}

func (e cachedInfoEntry) Info() (fs.FileInfo, error) { return e.info, nil }

func TestScanCurrentSnapshotEntries_ConcurrentRemovalOnly(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := session.SessionID("removed")
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "current"}, session.Limits{}, time.Unix(1, 0))
	if err := st.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(st.resolver.canonicalDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot fs.DirEntry
	for _, entry := range entries {
		if entry.Name() == filepath.Base(st.resolver.currentSnapshotPath(id)) {
			snapshot = entry
			break
		}
	}
	if snapshot == nil {
		t.Fatal("snapshot missing from directory entries")
	}
	info, err := snapshot.Info()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(st.resolver.currentSnapshotPath(id)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		entry fs.DirEntry
	}{
		{name: "stat", entry: snapshot},
		{name: "open", entry: cachedInfoEntry{DirEntry: snapshot, info: info}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := make(map[session.SessionID]snapshotFile)
			if err := scanCurrentSnapshotEntries(ctx, root, []fs.DirEntry{tc.entry}, rows); err != nil {
				t.Fatalf("snapshot removed after enumeration: %v", err)
			}
			if len(rows) != 0 {
				t.Fatalf("removed snapshot included: %+v", rows)
			}
		})
	}
	if err := scanCurrentSnapshotEntries(ctx, root, []fs.DirEntry{faultEntry{DirEntry: snapshot, err: fmt.Errorf("concurrent removal: %w", fs.ErrNotExist)}}, make(map[session.SessionID]snapshotFile)); err != nil {
		t.Fatalf("wrapped removal: %v", err)
	}
	if err := scanCurrentSnapshotEntries(ctx, root, []fs.DirEntry{faultEntry{DirEntry: snapshot, err: fs.ErrPermission}}, make(map[session.SessionID]snapshotFile)); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission failure = %v, want error", err)
	}
	if err := os.WriteFile(st.resolver.currentSnapshotPath(id), []byte("invalid snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scanCurrentSnapshotEntries(ctx, root, []fs.DirEntry{cachedInfoEntry{DirEntry: snapshot, info: info}}, make(map[session.SessionID]snapshotFile)); err == nil {
		t.Fatal("corrupt replacement hidden as concurrent removal")
	}
	if err := os.Remove(st.resolver.currentSnapshotPath(id)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(st.resolver.dir, "outside"), st.resolver.currentSnapshotPath(id)); err != nil {
		t.Fatal(err)
	}
	if err := scanCurrentSnapshotEntries(ctx, root, []fs.DirEntry{cachedInfoEntry{DirEntry: snapshot, info: info}}, make(map[session.SessionID]snapshotFile)); err == nil {
		t.Fatal("symlink replacing snapshot was hidden as concurrent removal")
	}
}
