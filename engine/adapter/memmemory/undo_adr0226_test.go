package memmemory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// TestADR_0226_UndoNeverRestoresAlreadyUndoneRevision pins ADR 0226's rule
// that restore-source selection must exclude undo revisions and prior targets.
func TestADR_0226_UndoNeverRestoresAlreadyUndoneRevision(t *testing.T) {
	t.Parallel()

	const key = "profile/theme"
	store := New()
	store.records[key] = &record{
		revisions: []tool.MemoryRevision{
			{Key: key, Value: "undone", Version: "one", Status: tool.MemoryStatusSuperseded, Origin: tool.MemoryOriginExplicit},
			{Key: key, Value: "undone", Version: "undo-one", Status: tool.MemoryStatusSuperseded, Origin: tool.MemoryOriginUndo},
			{Key: key, Value: "current", Version: "two", Status: tool.MemoryStatusActive, Origin: tool.MemoryOriginExplicit},
		},
		undone: map[tool.MemoryVersion]bool{"one": true},
	}

	record, err := store.UndoLatest(context.Background(), key, "two")
	if err != nil {
		t.Fatalf("UndoLatest: %v", err)
	}
	if record.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("UndoLatest restored an already-undone revision: %+v", record.Current)
	}

	sequence := New()
	first, err := sequence.RememberVersioned(context.Background(), tool.MemoryEntry{Key: key, Value: "one"}, "")
	if err != nil {
		t.Fatalf("sequence RememberVersioned: %v", err)
	}
	undone, err := sequence.UndoLatest(context.Background(), key, first.Current.Version)
	if err != nil || undone.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("sequence first UndoLatest = (%+v, %v)", undone.Current, err)
	}
	if _, err := sequence.UndoLatest(context.Background(), key, undone.Current.Version); err == nil || !strings.Contains(err.Error(), "no mutation remains to undo") {
		t.Fatalf("sequence second UndoLatest error = %v, want no mutation remains to undo", err)
	}
	current, err := sequence.RememberVersioned(context.Background(), tool.MemoryEntry{Key: key, Value: "two"}, "")
	if err != nil {
		t.Fatalf("sequence second RememberVersioned: %v", err)
	}
	final, err := sequence.UndoLatest(context.Background(), key, current.Current.Version)
	if err != nil || final.Current.Status != tool.MemoryStatusDeleted || final.Current.Value != "" {
		t.Fatalf("sequence final UndoLatest = (%+v, %v), restored an undone value", final.Current, err)
	}
}

// TestADR_0226_RepeatedUndoWalksStrictlyBackward pins ADR 0226's no-redo
// contract for ordinary lifecycle writes.
func TestADR_0226_RepeatedUndoWalksStrictlyBackward(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := New()
	first, err := store.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/theme", Value: "one"}, "")
	if err != nil {
		t.Fatalf("first RememberVersioned: %v", err)
	}
	second, err := store.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/theme", Value: "two"}, first.Current.Version)
	if err != nil {
		t.Fatalf("second RememberVersioned: %v", err)
	}
	one, err := store.UndoLatest(ctx, "profile/theme", second.Current.Version)
	if err != nil || one.Current.Value != "one" {
		t.Fatalf("first UndoLatest = (%+v, %v), want one", one.Current, err)
	}
	deleted, err := store.UndoLatest(ctx, "profile/theme", one.Current.Version)
	if err != nil || deleted.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("second UndoLatest = (%+v, %v), want deleted", deleted.Current, err)
	}
	before := deleted.Current
	_, err = store.UndoLatest(ctx, "profile/theme", before.Version)
	if err == nil || !strings.Contains(err.Error(), "no mutation remains to undo") {
		t.Fatalf("third UndoLatest error = %v, want no mutation remains to undo", err)
	}
	after, found, inspectErr := store.Inspect(ctx, "profile/theme")
	if inspectErr != nil || !found || after.Current != before {
		t.Fatalf("failed undo mutated record: before=%+v after=%+v found=%v inspect=%v", before, after.Current, found, inspectErr)
	}
}

// TestADR_0226_UndoneMapPrunedWithRetainLatest pins the bounded lifetime of
// undo markers when revision retention discards their targets.
func TestADR_0226_UndoneMapPrunedWithRetainLatest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := New()
	for i := range maxRevisionsPerKey + 1 {
		record, err := store.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/theme", Value: fmt.Sprintf("v-%d", i)}, "")
		if err != nil {
			t.Fatalf("RememberVersioned %d: %v", i, err)
		}
		if _, err := store.UndoLatest(ctx, "profile/theme", record.Current.Version); err != nil {
			t.Fatalf("UndoLatest %d: %v", i, err)
		}
	}

	record := store.records["profile/theme"]
	retained := make(map[tool.MemoryVersion]struct{}, len(record.revisions))
	for _, revision := range record.revisions {
		retained[revision.Version] = struct{}{}
	}
	for version := range record.undone {
		if _, ok := retained[version]; !ok {
			t.Fatalf("undone marker %q survived after its revision was truncated", version)
		}
	}
	if len(record.undone) > len(record.revisions) {
		t.Fatalf("undone markers=%d exceed retained revisions=%d", len(record.undone), len(record.revisions))
	}
}
