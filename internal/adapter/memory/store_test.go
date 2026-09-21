package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestStorePersistsVersionedRecordAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Remember(context.Background(), tool.MemoryEntry{Key: "profile/editor", Value: "helix", Description: "editor"}, tool.MemoryCurrent{})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := reopened.Inspect(context.Background(), "profile/editor")
	if err != nil || !found || record.Current.Version != created.Current.Version || record.Current.Value != "helix" {
		t.Fatalf("reopened record = (%+v, %v, %v)", record, found, err)
	}
}

func TestStoreCreateOnlyAndExactUpdateCAS(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "vim"}, tool.MemoryCurrent{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "overwrite"}, tool.MemoryCurrent{}); err == nil {
		t.Fatal("create-only Remember overwrote existing memory")
	}
	second, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "helix"}, tool.MemoryCurrent{Exists: true, Version: first.Current.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "stale"}, tool.MemoryCurrent{Exists: true, Version: first.Current.Version}); err == nil {
		t.Fatal("stale update succeeded")
	}
	entry, _, _ := store.Recall(ctx, "profile/editor")
	if entry.Value != "helix" || second.Current.Version == first.Current.Version {
		t.Fatalf("current entry=%+v versions=%q/%q", entry, first.Current.Version, second.Current.Version)
	}
}

func TestStoreTombstoneAndUndoCAS(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/theme", Value: "dark"}, tool.MemoryCurrent{})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Forget(ctx, "profile/theme", created.Current.Version)
	if err != nil || deleted.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("Forget = (%+v, %v)", deleted, err)
	}
	if _, err := store.Undo(ctx, "profile/theme", created.Current.Version); err == nil {
		t.Fatal("stale Undo succeeded")
	}
	restored, err := store.Undo(ctx, "profile/theme", deleted.Current.Version)
	if err != nil || restored.Current.Status != tool.MemoryStatusActive || restored.Current.Value != "dark" {
		t.Fatalf("Undo = (%+v, %v)", restored, err)
	}
}

func TestStoreConcurrentCASHasSingleWinnerAcrossHandles(t *testing.T) {
	dir := t.TempDir()
	firstStore, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	initial, err := firstStore.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "vim"}, tool.MemoryCurrent{})
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{firstStore, secondStore}
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = stores[i].Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: string(rune('a' + i))}, tool.MemoryCurrent{Exists: true, Version: initial.Current.Version})
		}()
	}
	close(start)
	wg.Wait()
	successes, conflicts := 0, 0
	for _, err := range errs {
		if err == nil {
			successes++
			continue
		}
		var conflict *tool.MemoryVersionConflictError
		if errors.As(err, &conflict) {
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d errors=%v", successes, conflicts, errs)
	}
}

func TestStoreIgnoresUnversionedLegacyDocument(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "memory.json")
	old := []byte(`{"entries":{"profile/editor":{"value":"vim","updated_at":"2026-01-01T00:00:00Z"}}}`)
	if err := os.WriteFile(oldPath, old, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Recall(context.Background(), "profile/editor"); err != nil || found {
		t.Fatalf("legacy memory became visible: found=%v err=%v", found, err)
	}
	if _, err := store.Remember(context.Background(), tool.MemoryEntry{Key: "profile/editor", Value: "helix"}, tool.MemoryCurrent{}); err != nil {
		t.Fatalf("create current memory: %v", err)
	}
	got, err := os.ReadFile(oldPath)
	if err != nil || string(got) != string(old) {
		t.Fatalf("old memory changed: bytes=%q err=%v", got, err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := reopened.Recall(context.Background(), "profile/editor")
	if err != nil || !found || entry.Value != "helix" {
		t.Fatalf("current memory after reopen = (%+v, %v, %v)", entry, found, err)
	}
}

func TestStoreRejectsMalformedCurrentDocument(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, memoryFileName), []byte(`{"entries":`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Recall(context.Background(), "profile/editor"); err == nil {
		t.Fatal("malformed current memory document was accepted")
	}
}

func TestStoreRejectsCurrentActiveEntryWithoutHistory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, memoryFileName), []byte(`{"entries":{"profile/editor":{"value":"vim","updated_at":"2026-01-01T00:00:00Z"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Recall(context.Background(), "profile/editor"); err == nil {
		t.Fatal("current active entry without version history was accepted")
	}
}

func TestStoreFailedAtomicSaveDoesNotCommitMutation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "vim"}, tool.MemoryCurrent{})
	if err != nil {
		t.Fatal(err)
	}
	store.rename = func(string, string) error { return errors.New("rename failed") }
	if _, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "helix"}, tool.MemoryCurrent{Exists: true, Version: created.Current.Version}); err == nil {
		t.Fatal("failed save reported success")
	}
	entry, found, err := store.Recall(ctx, "profile/editor")
	if err != nil || !found || entry.Value != "vim" {
		t.Fatalf("failed save changed durable value: (%+v, %v, %v)", entry, found, err)
	}
}
