package jsonlstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestOpenReaderExistingStoreDoesNotMutate(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "read-only", session.SessionKindMain, session.SessionRelationship{})
	if err := writer.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(ctx, s.ID, session.Event{Type: session.EvResult, Seq: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.MetaList(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	before := storeFiles(t, dir)
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(r).(port.SessionStore); ok {
		t.Fatal("reader exposes writes")
	}
	if _, ok := any(r).(port.EventLog); ok {
		t.Fatal("reader exposes event writes")
	}
	if _, ok := any(r).(port.CursorEventLog); ok {
		t.Fatal("reader exposes cursor writes")
	}
	if sessions, err := r.List(ctx); err != nil || len(sessions) != 1 || sessions[0].ID != s.ID {
		t.Fatalf("List: %v, %v", sessions, err)
	}
	if loaded, err := r.Load(ctx, s.ID); err != nil || loaded.ID != s.ID {
		t.Fatalf("Load: %v, %v", loaded, err)
	}
	if rows, err := r.MetaList(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("MetaList: %v, %v", rows, err)
	}
	if page, err := r.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1}); err != nil || page.TotalCount != 1 {
		t.Fatalf("Page: %+v, %v", page, err)
	}
	if rows, err := r.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: s.ID, RootIncarnation: s.Incarnation(), Limit: 1}); err != nil || len(rows.Records) != 1 {
		t.Fatalf("lineage: %+v, %v", rows, err)
	}
	count := 0
	for _, err := range r.Read(ctx, s.ID) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("Read returned %d events", count)
	}
	count = 0
	for rec, err := range r.ReadAfter(ctx, s.ID, "", port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Cursor == "" {
			t.Fatal("missing cursor")
		}
		count++
	}
	if count != 1 {
		t.Fatalf("ReadAfter returned %d events", count)
	}
	if after := storeFiles(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("read mutated store: before=%v after=%v", before, after)
	}
}

func storeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(path, dir)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestOpenReaderRejectsUnpreparedAndStaleCatalog(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenReader(missing); err == nil {
		t.Fatal("created missing root")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("root appeared: %v", err)
	}
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "stale", session.SessionKindMain, session.SessionRelationship{})
	if err := writer.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.MetaList(t.Context()); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unprepared catalog: %v", err)
	}
	if _, err := r.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 1}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unprepared page: %v", err)
	}
	if _, err := writer.MetaList(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MetaList(t.Context()); err != nil {
		t.Fatalf("prepared catalog: %v", err)
	}
	if err := writer.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 1}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("stale catalog: %v", err)
	}
	if _, err := r.Load(t.Context(), s.ID); err != nil {
		t.Fatalf("load during stale catalog: %v", err)
	}
}

func TestOpenReaderDeniedWrites(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "denied", session.SessionKindMain, session.SessionRelationship{})
	if err := writer.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(t.Context(), s.ID, session.Event{Type: session.EvResult, Seq: 7}); err != nil {
		t.Fatal(err)
	}
	// The catalog fingerprint includes the snapshot directory's mode.
	if err := os.Chmod(writer.resolver.canonicalDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.MetaList(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	before := storeFiles(t, dir)
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o500)
		}
		return os.Chmod(path, 0o400)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return os.Chmod(path, 0o600)
		}); err != nil {
			t.Error(err)
		}
	})
	// Prove the fixture really rejects mutation, including when the test
	// runner is privileged: setpriv drops root's DAC bypass capabilities.
	args := []string{os.Args[0], "-test.run=^TestOpenReaderDeniedWritesHelper$"}
	if os.Geteuid() == 0 {
		binary, err := exec.LookPath("setpriv")
		if err != nil {
			t.Skip("setpriv is required for a privileged denied-write proof")
		}
		args = append([]string{binary, "--bounding-set=-dac_override,-dac_read_search", "--"}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // test binary and fixed setpriv invocation
	cmd.Env = append(os.Environ(), "MECATL_READER_DENIED_DIR="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("denied-write subprocess: %v: %s", err, output)
	}
	if after := storeFiles(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("read mutated store: before=%v after=%v", before, after)
	}
}

func TestOpenReaderDeniedWritesHelper(t *testing.T) {
	dir := os.Getenv("MECATL_READER_DENIED_DIR")
	if dir == "" {
		return
	}
	if f, err := os.OpenFile(filepath.Join(dir, "probe"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
		_ = f.Close()
		t.Fatal("fixture still allows writes")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("write probe: %v", err)
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	id := session.SessionID("denied")
	path := r.store.resolver.currentSnapshotPath(id)
	for _, flag := range []int{os.O_WRONLY, os.O_WRONLY | os.O_TRUNC} {
		f, err := os.OpenFile(path, flag, 0)
		if err == nil {
			_ = f.Close()
			t.Fatal("fixture allows existing-file mutation")
		}
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("existing-file probe: %v", err)
		}
	}
	for name, err := range map[string]error{
		"truncate": os.Truncate(path, 0),
		"rename":   os.Rename(path, path+".renamed"),
		"unlink":   os.Remove(path),
	} {
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("%s probe: %v", name, err)
		}
	}
	if _, err := r.Load(ctx, "no-lock"); err == nil {
		t.Fatal("missing family lock accepted")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	_, listErr := r.List(ctx)
	_, loadErr := r.Load(ctx, id)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(listErr, os.ErrPermission) || !errors.Is(loadErr, os.ErrPermission) {
		t.Fatalf("unreadable snapshot: %v, %v", listErr, loadErr)
	}
	manifest := r.store.inventoryCatalogPath()
	if err := os.Chmod(manifest, 0); err != nil {
		t.Fatal(err)
	}
	_, metaErr := r.MetaList(ctx)
	_, pageErr := r.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
	if err := os.Chmod(manifest, 0o400); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(metaErr, os.ErrPermission) || !errors.Is(pageErr, os.ErrPermission) {
		t.Fatalf("unreadable manifest: %v, %v", metaErr, pageErr)
	}
	if _, err := r.List(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MetaList(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	loaded, err := r.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: id, RootIncarnation: loaded.Incarnation(), Limit: 1}); err != nil {
		t.Fatal(err)
	}
	for _, err := range r.Read(ctx, id) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range r.ReadAfter(ctx, id, "", port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	fmt.Fprintln(os.Stdout, "read-only success")
}

func TestOpenReaderLineageExactAndDirty(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := lineageTestSession(t, "root", session.SessionKindMain, session.SessionRelationship{})
	child := lineageTestSession(t, "child", session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: root.ID, ParentIncarnation: root.Incarnation(), CallID: "call"})
	for _, s := range []*session.Session{root, child} {
		if err := writer.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	query := port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), RecordID: child.ID, RecordIncarnation: child.Incarnation(), Limit: 1}
	if result, err := r.ReadSessionLineage(t.Context(), query); err != nil || len(result.Records) != 1 || result.Records[0].ID != child.ID {
		t.Fatalf("exact lineage: %+v, %v", result, err)
	}
	path := lineageDirtyPath(writer.lineagePointPath(root.ID, root.Incarnation(), child.ID, string(child.Incarnation())))
	if err := os.WriteFile(path, []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadSessionLineage(t.Context(), query); !errors.Is(err, errLineagePartitionIncomplete) {
		t.Fatalf("dirty lineage: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	point := writer.lineagePointPath(root.ID, root.Incarnation(), child.ID, string(child.Incarnation()))
	if err := os.Remove(point); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), point); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadSessionLineage(t.Context(), query); err == nil {
		t.Fatal("symlinked lineage partition was accepted")
	}
}

func TestOpenReaderPagesPreparedCatalogWithoutRebuild(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []session.SessionID{"one", "two", "three"} {
		if err := writer.Save(t.Context(), lineageTestSession(t, id, session.SessionKindMain, session.SessionRelationship{})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 2}); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 2})
	if err != nil || first.TotalCount != 3 || len(first.Sessions) != 2 || first.NextCursor == nil {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	second, err := r.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Sessions) != 1 || second.NextCursor != nil {
		t.Fatalf("second page: %+v, %v", second, err)
	}
}

func TestOpenReaderEmptyCatalogScopeIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Save(t.Context(), lineageTestSession(t, "catalog-row", session.SessionKindMain, session.SessionRelationship{})); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.MetaList(t.Context()); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := writer.inventoryFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	catalog, ok := writer.readInventoryManifest(fingerprint)
	if !ok {
		t.Fatal("missing prepared manifest")
	}
	path := filepath.Join(writer.inventoryCatalogDir(), catalog.Scopes[inventoryGlobalScope].File)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.MetaList(t.Context()); err == nil {
		t.Fatal("empty catalog scope accepted by MetaList")
	}
	if _, err := r.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 1}); err == nil {
		t.Fatal("empty catalog scope accepted by pager")
	}
}

func TestOpenReaderRejectsSymlinkedInventory(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "symlink", session.SessionKindMain, session.SessionRelationship{})
	if err := writer.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.MetaList(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := writer.inventoryGenerationMarkerPath()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), path); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.MetaList(t.Context()); err == nil {
		t.Fatal("symlinked generation marker was accepted")
	}
}

func TestOpenReaderFollowAndCursor(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "follow", session.SessionKindMain, session.SessionRelationship{})
	if err := writer.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(t.Context(), s.ID, session.Event{Type: session.EvResult, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	var cursor port.Cursor
	for rec, err := range r.ReadAfter(t.Context(), s.ID, "", port.ReadOptions{Limit: 1}) {
		if err != nil {
			t.Fatal(err)
		}
		cursor = rec.Cursor
	}
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		for rec, err := range r.ReadAfter(ctx, s.ID, cursor, port.ReadOptions{Follow: true, Limit: 1}) {
			if err != nil {
				result <- err
				return
			}
			if rec.Cursor == cursor || !rec.Live {
				result <- fmt.Errorf("unexpected follow record: %+v", rec)
				return
			}
			result <- nil
		}
	}()
	time.Sleep(150 * time.Millisecond)
	if err := writer.Append(t.Context(), s.ID, session.Event{Type: session.EvResult, Seq: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestOpenReaderMissingFamilyLockFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "missing-lock", session.SessionKindMain, session.SessionRelationship{})
	if err := writer.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(snapshotFamilyLockPath(writer.resolver.currentSnapshotPath(s.ID))); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(t.Context(), s.ID); err == nil {
		t.Fatal("Load succeeded without family lock")
	}
}

func TestOpenReaderExistingLockWaitHonorsContext(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "locked", session.SessionKindMain, session.SessionRelationship{})
	if err := writer.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(snapshotFamilyLockPath(writer.resolver.currentSnapshotPath(s.ID)))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if _, err := r.Load(ctx, s.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Load under exclusive lock: %v", err)
	}
}
