package app

import (
	"context"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// TestBuildCatalogRegistersMemorySearchWhenEnabled proves that the REAL wiring
// (buildCatalog, the same function buildEngine calls) registers all three memory
// tools — Remember, Recall, AND SearchMemory — into the catalog exactly when a
// per-project memory directory is configured (cfg.MemoryDir set), and registers
// NONE of them when memory is disabled (cfg.MemoryDir == ""). This guards the
// "memory enabled ⇒ SearchMemory is available" claim through the composition
// layer, not just the adapter's own Register helper.
func TestBuildCatalogRegistersMemorySearchWhenEnabled(t *testing.T) {
	ctx := context.Background()
	provider := mockllm.New(mockllm.TextTurn("x"))
	hooks := hookexec.New(nil)

	memoryToolNames := []string{
		memory.SearchMemoryToolName,
		memory.RecallToolName,
		memory.RememberToolName,
		memory.InspectMemoryToolName,
		memory.ForgetMemoryToolName,
		memory.UndoMemoryToolName,
	}

	t.Run("enabled", func(t *testing.T) {
		cfg := Config{MemoryDir: t.TempDir()}
		cat, _, _, _, closeFn, err := buildCatalog(ctx, isolateConfig(t, cfg), regForTest(provider, providerMock, cfg.Model), provider, hooks, agents.NewRegistry(nil), memstore.New(), nil)
		if err != nil {
			t.Fatalf("buildCatalog: %v", err)
		}
		defer closeFn()

		for _, name := range memoryToolNames {
			if _, ok := cat.Lookup(name); !ok {
				t.Errorf("memory enabled (MemoryDir set): catalog is missing %q", name)
			}
		}
	})

	t.Run("disabled", func(t *testing.T) {
		cfg := Config{MemoryDir: ""}
		cat, _, _, _, closeFn, err := buildCatalog(ctx, isolateConfig(t, cfg), regForTest(provider, providerMock, cfg.Model), provider, hooks, agents.NewRegistry(nil), memstore.New(), nil)
		if err != nil {
			t.Fatalf("buildCatalog: %v", err)
		}
		defer closeFn()

		for _, name := range memoryToolNames {
			if _, ok := cat.Lookup(name); ok {
				t.Errorf("memory disabled (MemoryDir empty): catalog should NOT contain %q", name)
			}
		}
	})
}
