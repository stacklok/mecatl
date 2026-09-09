package jsonlstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func lineageTestSession(t *testing.T, id session.SessionID, kind session.SessionKind, rel session.SessionRelationship) *session.Session {
	t.Helper()
	s := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := s.RestoreSessionMetadata(kind, rel); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLineageReadOpensOnlySelectedPartitions(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := lineageTestSession(t, "root", session.SessionKindMain, session.SessionRelationship{})
	other := lineageTestSession(t, "other", session.SessionKindMain, session.SessionRelationship{})
	for _, s := range []*session.Session{root, other} {
		if err := st.Create(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	// A corrupt unrelated partition is a deterministic physical-access tripwire:
	// opening it would fail this otherwise valid target query.
	for _, path := range []string{st.lineageRecordPath(other.ID), st.lineageEdgePath(other.ID, other.Incarnation())} {
		if err := os.WriteFile(path, []byte("corrupt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10})
	if err != nil || len(got.Records) != 1 || got.Records[0].ID != root.ID {
		t.Fatalf("selected partition query = %+v, %v", got, err)
	}
}

func TestExactLineageEdgeFailsClosedWhenPartitionMissingOrCorrupt(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*testing.T, string)
	}{
		{name: "missing", damage: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt", damage: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("corrupt\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := lineageTestSession(t, "root", session.SessionKindMain, session.SessionRelationship{})
			child := lineageTestSession(t, "child", session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: root.ID, ParentIncarnation: root.Incarnation(), CallID: "call"})
			for _, s := range []*session.Session{root, child} {
				if err := st.Create(t.Context(), s); err != nil {
					t.Fatal(err)
				}
			}
			test.damage(t, st.lineagePointPath(root.ID, root.Incarnation(), child.ID, string(child.Incarnation())))

			self, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: child.ID, RootIncarnation: child.Incarnation(), Limit: 1})
			if err != nil || len(self.Records) != 1 {
				t.Fatalf("child self record was not retained: %+v, %v", self, err)
			}
			if _, err := st.Load(t.Context(), child.ID); err != nil {
				t.Fatalf("child snapshot was not retained: %v", err)
			}
			if _, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), RecordID: child.ID, RecordIncarnation: child.Incarnation(), Limit: 1}); err == nil {
				t.Fatal("damaged direct-edge partition was accepted")
			}
		})
	}
}

func TestLineageCanonicalOrderAndTruncationSentinel(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := lineageTestSession(t, "root", session.SessionKindMain, session.SessionRelationship{})
	if err := st.Create(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	for _, id := range []session.SessionID{"z-child", "a-child", "m-child"} {
		child := lineageTestSession(t, id, session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: root.ID, ParentIncarnation: root.Incarnation(), CallID: "call"})
		if err := st.Create(t.Context(), child); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	ids := []session.SessionID{got.Records[0].ID, got.Records[1].ID, got.Records[2].ID}
	if want := []session.SessionID{"root", "a-child", "m-child"}; !reflect.DeepEqual(ids, want) || !got.Truncated {
		t.Fatalf("ordered bounded result = %v truncated=%t, want %v true", ids, got.Truncated, want)
	}
}

func TestLineageReadIsPureAndLockFree(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := lineageTestSession(t, "root", session.SessionKindMain, session.SessionRelationship{})
	if err := st.Create(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	before := lineageFiles(t, st)
	fl := flock.New(lineageLockPath(st.lineageRecordPath(root.ID)), flock.SetPermissions(0o600))
	if err := fl.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fl.Close() }()
	if _, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10}); err != nil {
		t.Fatalf("read acquired a writer lock: %v", err)
	}
	after := lineageFiles(t, st)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read mutated lineage files:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestLineageUnrelatedPartitionLockDoesNotBlockWriter(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blocked := lineageTestSession(t, "blocked", session.SessionKindMain, session.SessionRelationship{})
	target := lineageTestSession(t, "target", session.SessionKindMain, session.SessionRelationship{})
	if err := st.Create(t.Context(), blocked); err != nil {
		t.Fatal(err)
	}
	fl := flock.New(lineageLockPath(st.lineageRecordPath(blocked.ID)), flock.SetPermissions(0o600))
	if err := fl.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fl.Close() }()
	if err := st.Create(t.Context(), target); err != nil {
		t.Fatalf("target writer depended on unrelated partition: %v", err)
	}
}

func TestLineageWriterTouchesOnlyAffectedPartitions(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := lineageTestSession(t, "target", session.SessionKindMain, session.SessionRelationship{})
	other := lineageTestSession(t, "other", session.SessionKindMain, session.SessionRelationship{})
	for _, s := range []*session.Session{target, other} {
		if err := st.Create(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	var writes []string
	st.lineagePartitionWriteObserver = func(path string) { writes = append(writes, path) }
	if err := st.Save(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	for _, path := range writes {
		if path == st.lineageRecordPath(other.ID) || path == st.lineageEdgePath(other.ID, other.Incarnation()) {
			t.Fatalf("target writer touched unrelated partition %q", path)
		}
	}
	if len(writes) != 1 || writes[0] != st.lineageRecordPath(target.ID) {
		t.Fatalf("target writer partitions = %v, want only target record", writes)
	}
}

func TestLineageLegacyGlobalIndexNeverBecomesAQueryFallback(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.legacyLineageIndexPath(), []byte(`{"v":"session-lineage-json/1","records":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := lineageTestSession(t, "root", session.SessionKindMain, session.SessionRelationship{})
	if err := st.Create(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10}); err == nil {
		t.Fatal("targeted write treated an unmigrated global v1 index as complete")
	}
}

func TestLineageDeleteRecoversPostSnapshotCrashBeforeTombstone(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldParent := lineageTestSession(t, "old-parent", session.SessionKindMain, session.SessionRelationship{})
	newParent := lineageTestSession(t, "new-parent", session.SessionKindMain, session.SessionRelationship{})
	child := lineageTestSession(t, "child", session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: oldParent.ID, ParentIncarnation: oldParent.Incarnation(), CallID: "old-call"})
	for _, s := range []*session.Session{oldParent, newParent, child} {
		if err := st.Create(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	if err := child.RestoreSessionMetadata(session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: newParent.ID, ParentIncarnation: newParent.Incarnation(), CallID: "new-call"}); err != nil {
		t.Fatal(err)
	}
	st.lineagePartitionWriteObserver = func(path string) {
		if path == st.lineageRecordPath(child.ID) {
			panic("injected crash after snapshot publish")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Save did not reach injected crash")
			}
		}()
		_ = st.Save(t.Context(), child)
	}()

	restarted, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Delete(t.Context(), child.ID); err != nil {
		t.Fatalf("Delete after restart: %v", err)
	}
	rows, err := readWholeLineagePartition(restarted.lineageRecordPath(child.ID), lineageRecordFormat)
	if err != nil || len(rows) != 1 {
		t.Fatalf("recovered child records = %+v, %v", rows, err)
	}
	got := rows[0]
	if got.State != port.SessionLineagePruned || got.Incarnation != string(child.Incarnation()) || got.Relationship != child.Relationship || got.DeletedAt.IsZero() {
		t.Fatalf("recovered tombstone = %+v, want exact new-parent lineage for incarnation %q", got, child.Incarnation())
	}
	newFamily, err := restarted.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: newParent.ID, RootIncarnation: newParent.Incarnation(), Limit: 10})
	if err != nil || len(newFamily.Records) != 2 || newFamily.Records[1].ID != child.ID || newFamily.Records[1].State != port.SessionLineagePruned {
		t.Fatalf("new-parent family after delete = %+v, %v", newFamily, err)
	}
	oldFamily, err := restarted.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: oldParent.ID, RootIncarnation: oldParent.Incarnation(), Limit: 10})
	if err != nil || len(oldFamily.Records) != 1 {
		t.Fatalf("old-parent stale edge survived = %+v, %v", oldFamily, err)
	}
}

func TestLineageDeleteFailsClosedWhenDirtySnapshotCannotBeProven(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "unprovable", session.SessionKindMain, session.SessionRelationship{})
	if err := st.Create(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	st.lineagePartitionWriteObserver = func(path string) {
		if path == st.lineageRecordPath(s.ID) {
			panic("injected post-snapshot crash")
		}
	}
	func() {
		defer func() { _ = recover() }()
		_ = st.Save(t.Context(), s)
	}()
	currentPath := st.resolver.currentSnapshotPath(s.ID)
	if err := os.WriteFile(currentPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Delete(t.Context(), s.ID); err == nil {
		t.Fatal("Delete accepted an unprovable dirty snapshot")
	}
	if _, err := os.Stat(currentPath); err != nil {
		t.Fatalf("failed-closed delete removed snapshot: %v", err)
	}
	if _, err := os.Stat(lineageDirtyPath(restarted.lineageRecordPath(s.ID))); err != nil {
		t.Fatalf("failed-closed delete cleared dirty state: %v", err)
	}
}

func TestLineageRecoverySettlesHistoricalAndReparentedPartitions(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	parents := []*session.Session{
		lineageTestSession(t, "parent-a", session.SessionKindMain, session.SessionRelationship{}),
		lineageTestSession(t, "parent-b", session.SessionKindMain, session.SessionRelationship{}),
		lineageTestSession(t, "parent-c", session.SessionKindMain, session.SessionRelationship{}),
	}
	for _, parent := range parents {
		if err := st.Create(t.Context(), parent); err != nil {
			t.Fatal(err)
		}
	}
	first := lineageTestSession(t, "reused-child", session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: parents[0].ID, ParentIncarnation: parents[0].Incarnation(), CallID: "first"})
	if err := st.Create(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	second := lineageTestSession(t, first.ID, session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: parents[1].ID, ParentIncarnation: parents[1].Incarnation(), CallID: "second"})
	if err := st.Create(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := second.RestoreSessionMetadata(session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: parents[2].ID, ParentIncarnation: parents[2].Incarnation(), CallID: "reparented"}); err != nil {
		t.Fatal(err)
	}
	writes := 0
	st.lineagePartitionWriteObserver = func(string) {
		writes++
		if writes == 2 {
			panic("injected crash during edge publish")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Save did not reach injected crash")
			}
		}()
		_ = st.Save(t.Context(), second)
	}()

	restarted, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Save(t.Context(), second); err != nil {
		t.Fatalf("recovery Save: %v", err)
	}
	for i, parent := range parents {
		got, err := restarted.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: parent.ID, RootIncarnation: parent.Incarnation(), Limit: 10})
		if err != nil {
			t.Fatalf("parent %d read: %v", i, err)
		}
		switch i {
		case 0:
			if len(got.Records) != 2 || got.Records[1].Incarnation != string(first.Incarnation()) || got.Records[1].State != port.SessionLineagePruned {
				t.Fatalf("historical partition = %+v", got)
			}
		case 1:
			if len(got.Records) != 1 {
				t.Fatalf("old retained edge not removed: %+v", got)
			}
		case 2:
			if len(got.Records) != 2 || got.Records[1].Incarnation != string(second.Incarnation()) || got.Records[1].State != port.SessionLineageRetained {
				t.Fatalf("new retained edge = %+v", got)
			}
		}
	}
	entries, err := os.ReadDir(restarted.inventoryCatalogDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".dirty" {
			t.Fatalf("recovery left dirty partition %q", entry.Name())
		}
	}
}

func TestMigrateLegacyLineagePublishesCompleteTargetPartitions(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := lineageTestSession(t, "legacy-root", session.SessionKindMain, session.SessionRelationship{})
	child := lineageTestSession(t, "legacy-child", session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: root.ID, ParentIncarnation: root.Incarnation(), CallID: "legacy-call"})
	data, err := json.Marshal(legacyLineageIndex{Format: legacyLineageIndexFormat, Records: []port.SessionLineageRecord{retainedLineageRecord(root), retainedLineageRecord(child)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.legacyLineageIndexPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	query := port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10}
	if _, err := st.ReadSessionLineage(t.Context(), query); err == nil {
		t.Fatal("unmigrated legacy lineage unexpectedly readable")
	}
	if err := st.MigrateLegacyLineage(t.Context(), 1); err == nil {
		t.Fatal("bounded migration accepted insufficient max work")
	}
	if _, err := os.Stat(st.legacyLineageIndexPath()); err != nil {
		t.Fatalf("failed bounded migration removed legacy authority: %v", err)
	}
	if err := st.MigrateLegacyLineage(t.Context(), 100); err != nil {
		t.Fatalf("MigrateLegacyLineage: %v", err)
	}
	got, err := st.ReadSessionLineage(t.Context(), query)
	if err != nil || len(got.Records) != 2 || got.Records[0].ID != root.ID || got.Records[1].ID != child.ID {
		t.Fatalf("migrated target = %+v, %v", got, err)
	}
	if _, err := os.Stat(st.legacyLineageIndexPath()); !os.IsNotExist(err) {
		t.Fatalf("legacy index retained after complete migration: %v", err)
	}
}

func TestMigrateLegacyLineageRetriesAfterInterruptedMarker(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := lineageTestSession(t, "retry-root", session.SessionKindMain, session.SessionRelationship{})
	child := lineageTestSession(t, "retry-child", session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: root.ID, ParentIncarnation: root.Incarnation(), CallID: "retry-call"})
	data, err := json.Marshal(legacyLineageIndex{Format: legacyLineageIndexFormat, Records: []port.SessionLineageRecord{retainedLineageRecord(root), retainedLineageRecord(child)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.legacyLineageIndexPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.lineageMigrationPath(), []byte("incomplete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	query := port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10}
	if _, err := st.ReadSessionLineage(t.Context(), query); err == nil {
		t.Fatal("interrupted migration marker did not fail reads closed")
	}
	if err := st.MigrateLegacyLineage(t.Context(), 100); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	got, err := st.ReadSessionLineage(t.Context(), query)
	if err != nil || len(got.Records) != 2 || got.Records[0].ID != root.ID || got.Records[1].ID != child.ID {
		t.Fatalf("retry did not converge: %+v, %v", got, err)
	}
	for _, path := range []string{st.lineageMigrationPath(), st.legacyLineageIndexPath()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("migration authority %q remained after convergence: %v", path, err)
		}
	}
}

func TestLineageDirtyPartitionFailsClosedWithoutAffectingOthers(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dirty := lineageTestSession(t, "dirty", session.SessionKindMain, session.SessionRelationship{})
	clean := lineageTestSession(t, "clean", session.SessionKindMain, session.SessionRelationship{})
	for _, s := range []*session.Session{dirty, clean} {
		if err := st.Create(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.writeInventoryFileWithPattern(lineageDirtyPath(st.lineageEdgePath(dirty.ID, dirty.Incarnation())), []byte("crash\n"), ".lineage-crash-*"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: dirty.ID, RootIncarnation: dirty.Incarnation(), Limit: 10}); err == nil {
		t.Fatal("dirty target partition was accepted")
	}
	got, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: clean.ID, RootIncarnation: clean.Incarnation(), Limit: 10})
	if err != nil || len(got.Records) != 1 {
		t.Fatalf("unrelated clean partition = %+v, %v", got, err)
	}
}

func TestLineageTombstoneContainsNoPrincipalPII(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := lineageTestSession(t, "owned", session.SessionKindMain, session.SessionRelationship{})
	s.Owner = &session.Principal{Issuer: "secret-issuer.example", Subject: "private-subject", GrantType: session.GrantTypeUser, Name: "Private Person"}
	if err := st.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(st.lineageRecordPath(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{s.Owner.Issuer, s.Owner.Subject, s.Owner.Name} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("lineage tombstone retained Principal PII %q: %s", forbidden, body)
		}
	}
}

func lineageFiles(t *testing.T, st *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(st.inventoryCatalogDir())
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		if !stringsHasLineagePrefix(entry.Name()) || stringsHasLockSuffix(entry.Name()) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(st.inventoryCatalogDir(), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, entry.Name()+"|"+info.ModTime().UTC().Format(time.RFC3339Nano)+"|"+string(body))
	}
	sort.Strings(files)
	return files
}

func stringsHasLineagePrefix(name string) bool {
	return len(name) >= len(".session-lineage-") && name[:len(".session-lineage-")] == ".session-lineage-"
}
func stringsHasLockSuffix(name string) bool {
	return len(name) >= len(".lock") && name[len(name)-len(".lock"):] == ".lock"
}
