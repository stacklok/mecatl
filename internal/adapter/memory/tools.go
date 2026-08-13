package memory

import (
	"github.com/stacklok/mecatl/engine/adapter/memorytools"
	"github.com/stacklok/mecatl/engine/tool"
)

// Tool names are kept here for compatibility with root composition callers.
const (
	RememberToolName      = "Remember"
	RecallToolName        = "Recall"
	SearchMemoryToolName  = "SearchMemory"
	InspectMemoryToolName = "InspectMemory"
	ForgetMemoryToolName  = "ForgetMemory"
	UndoMemoryToolName    = "UndoMemory"
)

// NewRememberTool returns the portable project-scoped Remember implementation.
func NewRememberTool(store tool.MemoryStore) tool.Tool {
	return projectTool(store, RememberToolName)
}

// NewRecallTool returns the portable project-scoped Recall implementation.
func NewRecallTool(store tool.MemoryStore) tool.Tool {
	return projectTool(store, RecallToolName)
}

// NewSearchMemoryTool returns the portable project-scoped Search implementation.
func NewSearchMemoryTool(store tool.MemoryStore) tool.Tool {
	return projectTool(store, SearchMemoryToolName)
}

func projectTool(store tool.MemoryStore, name string) tool.Tool {
	if store == nil {
		panic("memory: tool constructor requires a non-nil MemoryStore")
	}
	for _, candidate := range memorytools.ProjectTools(store) {
		if candidate.Spec().Name == name {
			return candidate
		}
	}
	panic("memory: portable tool missing " + name)
}

// Tools returns the project family. Lifecycle tools are included only when the
// store advertises tool.MemoryLifecycleStore.
func Tools(store tool.MemoryStore) []tool.Tool { return memorytools.ProjectTools(store) }

// Register adds the applicable project family to cat.
func Register(cat *tool.Catalog, store tool.MemoryStore) error {
	return memorytools.Register(cat, store, memorytools.ScopeProject)
}
