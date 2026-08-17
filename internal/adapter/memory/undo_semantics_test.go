package memory

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

func TestUndoRestoreSourceSkipsUndoAndUndoneRevisions(t *testing.T) {
	const key = "profile/theme"
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.save(persisted{
		Entries: map[string]record{key: {Value: "current"}},
		History: map[string][]persistedRevision{key: {
			{Key: key, Value: "undone", Version: "one", Status: tool.MemoryStatusSuperseded, Origin: tool.MemoryOriginExplicit},
			{Key: key, Value: "undone", Version: "undo-one", Status: tool.MemoryStatusSuperseded, Origin: tool.MemoryOriginUndo, UndoOf: "one"},
			{Key: key, Value: "current", Version: "two", Status: tool.MemoryStatusActive, Origin: tool.MemoryOriginExplicit},
		}},
	}); err != nil {
		t.Fatalf("save fixture: %v", err)
	}

	record, err := store.UndoLatest(context.Background(), key, "two")
	if err != nil {
		t.Fatalf("UndoLatest: %v", err)
	}
	if record.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("UndoLatest restored an already-undone revision: %+v", record.Current)
	}
}
