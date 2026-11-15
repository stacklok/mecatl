package modelrequest

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ToolDefinitionView retains borrowed immutable advertised definition backing for
// its lifetime, not executable code. ArgumentSchema.Copy detaches only schema
// bytes. Zero is empty; navigation is O(1) and does not clone sibling payloads.
//
//dtogen:include
type ToolDefinitionView struct{ value *sessionformat.ToolDefinition }
