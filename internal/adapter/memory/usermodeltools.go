package memory

import (
	"github.com/stacklok/mecatl/engine/adapter/memorytools"
	"github.com/stacklok/mecatl/engine/tool"
)

// Catalog names of the user-scoped portable memory tools.
const (
	RememberUserToolName      = "RememberUser"
	RecallUserToolName        = "RecallUser"
	SearchUserModelToolName   = "SearchUserModel"
	InspectUserMemoryToolName = "InspectUserMemory"
	ForgetUserMemoryToolName  = "ForgetUserMemory"
	UndoUserMemoryToolName    = "UndoUserMemory"
)

// NewUserModelTools returns the user-scoped portable family. Lifecycle tools are
// included only when store implements tool.MemoryLifecycleStore.
func NewUserModelTools(store tool.MemoryStore) []tool.Tool {
	return memorytools.UserTools(store)
}

// RegisterUserModel adds the applicable user family to cat.
func RegisterUserModel(cat *tool.Catalog, store tool.MemoryStore) error {
	return memorytools.Register(cat, store, memorytools.ScopeUser)
}
