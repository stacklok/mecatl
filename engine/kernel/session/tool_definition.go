package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ToolDefinition retains an advertised definition, not executable behavior.
// ArgumentSchema is exact explicit JSON; assignment shares bytes, Clone detaches.
type ToolDefinition = sessionformat.ToolDefinition

// ToolDefinitionView retains immutable backing storage, including schema bytes.
// Capture and navigation cost O(1); zero has empty fields. ArgumentSchema borrows
// retained backing; Copy detaches only the selected schema.
type ToolDefinitionView struct{ value *sessionformat.ToolDefinition }
