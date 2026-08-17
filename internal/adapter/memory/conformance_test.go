package memory_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memconformance"
	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// TestFlockStoreConformance runs the shared MemoryStore conformance table
// against the flock-file reference adapter.
func TestFlockStoreConformance(t *testing.T) {
	memconformance.Run(t, func(t *testing.T) tool.MemoryStore {
		st, err := memory.New(t.TempDir())
		if err != nil {
			t.Fatalf("memory.New: %v", err)
		}
		return st
	})
}

func TestFlockStoreLifecycleConformance(t *testing.T) {
	memconformance.RunLifecycle(t, func(t *testing.T) (tool.MemoryStore, tool.MemoryLifecycleStore) {
		st, err := memory.New(t.TempDir())
		if err != nil {
			t.Fatalf("memory.New: %v", err)
		}
		return st, st
	})
}

// TestADR_0226_MemoryRecordExposesTruncation pins the lifecycle contract that
// callers can distinguish a retained history tail from complete history.
func TestADR_0226_MemoryRecordExposesTruncation(t *testing.T) {
	stores := map[string]func(t *testing.T) tool.MemoryLifecycleStore{
		"in-memory reference": func(*testing.T) tool.MemoryLifecycleStore { return memmemory.New() },
		"local file": func(t *testing.T) tool.MemoryLifecycleStore {
			st, err := memory.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return st
		},
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			for i := range 65 {
				if _, err := store.RememberVersioned(context.Background(), tool.MemoryEntry{Key: "profile/history", Value: fmt.Sprintf("v-%d", i)}, ""); err != nil {
					t.Fatalf("RememberVersioned(%d): %v", i, err)
				}
			}
			record, found, err := store.Inspect(context.Background(), "profile/history")
			if err != nil || !found {
				t.Fatalf("Inspect: found=%v err=%v", found, err)
			}
			if !record.Truncated {
				t.Fatalf("Inspect Truncated = false after retention cap; record=%+v", record)
			}
		})
	}
}
