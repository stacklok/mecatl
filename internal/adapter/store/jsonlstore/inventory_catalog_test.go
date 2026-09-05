package jsonlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionStorageContinuity_Scenario2_CatalogRebuildAndExternalChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatalf("New first Store: %v", err)
	}

	const transcriptMarker = "TRANSCRIPT-AUTHORITY-MUST-NOT-ENTER-CATALOG"
	current := session.New("current", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())
	current.SetTitle("initial title")
	if err := current.RecordUserPrompt(transcriptMarker, nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := first.Save(ctx, current); err != nil {
		t.Fatalf("Save current: %v", err)
	}

	rows, err := first.discoveryMetaList(ctx)
	if err != nil {
		t.Fatalf("initial discoveryMetaList: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != current.ID || rows[0].Title != "initial title" {
		t.Fatalf("initial catalog rows = %+v", rows)
	}
	catalogPath := first.inventoryCatalogPath()
	catalogBytes, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("ReadFile catalog: %v", err)
	}
	if strings.Contains(string(catalogBytes), transcriptMarker) {
		t.Fatalf("derivative catalog contains transcript content: %s", catalogBytes)
	}

	// Rebuilding a current-format row stops at the bounded metadata header. A
	// damaged conversation payload remains authoritative (Load fails) but is not
	// decoded merely to reconstruct inventory metadata.
	snapshotPath := first.resolver.currentSnapshotPath(current.ID)
	original, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("ReadFile current snapshot: %v", err)
	}
	originalInfo, err := os.Stat(snapshotPath)
	if err != nil {
		t.Fatalf("Stat current snapshot: %v", err)
	}
	snapshotKey := []byte(`"snapshot":`)
	payloadAt := bytes.Index(original, snapshotKey)
	if payloadAt < 0 {
		t.Fatal("current snapshot has no snapshot payload key")
	}
	broken := append(append([]byte(nil), original[:payloadAt+len(snapshotKey)]...), []byte(`{broken}`)...)
	if err := os.Remove(catalogPath); err != nil {
		t.Fatalf("Remove catalog before header rebuild: %v", err)
	}
	if err := os.WriteFile(snapshotPath, broken, 0o600); err != nil {
		t.Fatalf("write broken snapshot payload: %v", err)
	}
	rows, err = first.discoveryMetaList(ctx)
	if err != nil || len(rows) != 1 || rows[0].Title != "initial title" {
		t.Fatalf("header-only rebuild = %+v, %v", rows, err)
	}
	if _, err := first.Load(ctx, current.ID); err == nil {
		t.Fatal("broken authoritative snapshot unexpectedly loaded")
	}
	if err := os.WriteFile(snapshotPath, original, 0o600); err != nil {
		t.Fatalf("restore current snapshot: %v", err)
	}
	if err := os.Chtimes(snapshotPath, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatalf("restore current snapshot timestamp: %v", err)
	}

	// Missing and corrupt catalog files are both rebuildable metadata failures,
	// never failures of the authoritative snapshot family.
	if err := os.Remove(catalogPath); err != nil {
		t.Fatalf("Remove catalog: %v", err)
	}
	if _, err := first.discoveryMetaList(ctx); err != nil {
		t.Fatalf("rebuild missing catalog: %v", err)
	}
	if err := os.WriteFile(catalogPath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("corrupt catalog: %v", err)
	}
	if _, err := first.discoveryMetaList(ctx); err != nil {
		t.Fatalf("rebuild corrupt catalog: %v", err)
	}
	repaired, err := os.ReadFile(catalogPath)
	if err != nil || !json.Valid(repaired) {
		t.Fatalf("catalog was not repaired: valid=%v err=%v bytes=%q", json.Valid(repaired), err, repaired)
	}

	// A second Store and a directly-planted historical v1 family model the
	// shared-directory changes that an unchecked process-local cache would hide.
	second, err := New(dir)
	if err != nil {
		t.Fatalf("New second Store: %v", err)
	}
	loaded, err := second.Load(ctx, current.ID)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if err := loaded.RenameTitle("changed externally"); err != nil {
		t.Fatalf("RenameTitle: %v", err)
	}
	if err := second.Save(ctx, loaded); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	legacy := session.New("legacy", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/legacy", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_600_000_000, 0).UTC())
	legacy.SetTitle("bounded v1 tail")
	if err := legacy.RecordUserPrompt(strings.Repeat("historical-transcript-", 20_000), nil); err != nil {
		t.Fatalf("legacy RecordUserPrompt: %v", err)
	}
	legacyLine, err := sessnap.Marshal(legacy)
	if err != nil {
		t.Fatalf("Marshal legacy: %v", err)
	}
	legacyPath := first.resolver.canonicalPath(legacy.ID, kindSnapshot)
	if err := os.WriteFile(legacyPath, append(legacyLine, '\n'), 0o600); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}

	rows, err = first.discoveryMetaList(ctx)
	if err != nil {
		t.Fatalf("discoveryMetaList after external changes: %v", err)
	}
	got := make(map[session.SessionID]string, len(rows))
	for _, row := range rows {
		got[row.ID] = row.Title
	}
	if got[current.ID] != "changed externally" || got[legacy.ID] != "bounded v1 tail" {
		t.Fatalf("external changes hidden by catalog: %+v", got)
	}

	// An historical v1 writer appends in place: the directory entry is unchanged,
	// so catalog validation must reconcile the recorded v1 file metadata.
	if err := legacy.RenameTitle("newer appended v1 title"); err != nil {
		t.Fatalf("RenameTitle newer legacy: %v", err)
	}
	newerLegacyLine, err := sessnap.Marshal(legacy)
	if err != nil {
		t.Fatalf("Marshal newer legacy: %v", err)
	}
	legacyDirBefore, err := os.Stat(first.resolver.canonicalDir())
	if err != nil {
		t.Fatalf("Stat legacy directory before append: %v", err)
	}
	legacyFile, err := os.OpenFile(legacyPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open legacy snapshot for append: %v", err)
	}
	if _, err := legacyFile.Write(append(newerLegacyLine, '\n')); err != nil {
		_ = legacyFile.Close()
		t.Fatalf("append newer legacy snapshot: %v", err)
	}
	if err := legacyFile.Close(); err != nil {
		t.Fatalf("close appended legacy snapshot: %v", err)
	}
	legacyDirAfter, err := os.Stat(first.resolver.canonicalDir())
	if err != nil {
		t.Fatalf("Stat legacy directory after append: %v", err)
	}
	if !legacyDirAfter.ModTime().Equal(legacyDirBefore.ModTime()) || legacyDirAfter.Size() != legacyDirBefore.Size() {
		t.Fatalf("in-place append unexpectedly changed directory metadata: before=%+v after=%+v", legacyDirBefore, legacyDirAfter)
	}

	rows, err = first.discoveryMetaList(ctx)
	if err != nil {
		t.Fatalf("discoveryMetaList after in-place v1 append: %v", err)
	}
	for _, row := range rows {
		if row.ID == legacy.ID {
			if row.Title != "newer appended v1 title" {
				t.Fatalf("in-place v1 append served stale catalog row: %+v", row)
			}
			return
		}
	}
	t.Fatalf("appended v1 session missing from inventory: %+v", rows)
}

func TestSessionStorageContinuity_PageRebuildRetriesConcurrentMutation(t *testing.T) {
	ctx := context.Background()
	first, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New first Store: %v", err)
	}
	sess := session.New("concurrent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())
	if err := first.Save(ctx, sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := first.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1}); err != nil {
		t.Fatalf("prime inventory page: %v", err)
	}
	if err := os.Remove(first.inventoryCatalogPath()); err != nil {
		t.Fatalf("remove inventory catalog: %v", err)
	}
	second, err := New(first.resolver.dir)
	if err != nil {
		t.Fatalf("New second Store: %v", err)
	}

	rebuilt := make(chan struct{})
	release := make(chan struct{})
	first.inventoryCatalogReadyObserver = func() {
		first.inventoryCatalogReadyObserver = nil
		close(rebuilt)
		<-release
	}
	pageDone := make(chan error, 1)
	go func() {
		_, pageErr := first.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
		pageDone <- pageErr
	}()
	awaitSignal(t, rebuilt, "inventory catalog was not rebuilt")
	loaded, err := second.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load concurrent session: %v", err)
	}
	if err := loaded.RenameTitle("changed while page prepared"); err != nil {
		t.Fatalf("rename concurrent session: %v", err)
	}
	if err := second.Save(ctx, loaded); err != nil {
		t.Fatalf("save concurrent session: %v", err)
	}
	close(release)
	if err := awaitError(t, pageDone, "inventory page did not finish"); err != nil {
		t.Fatalf("PageSessionMetadata after concurrent mutation: %v", err)
	}
}
