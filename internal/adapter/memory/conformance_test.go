package memory_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memconformance"
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
