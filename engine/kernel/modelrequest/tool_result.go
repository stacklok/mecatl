package modelrequest

import "github.com/stacklok/mecatl/engine/kernel/modelrequest/format"

// ToolResultView retains borrowed immutable correlated result backing for its
// lifetime. Zero is empty; navigation is O(1) and does not clone sibling payloads.
//
//dtogen:include
type ToolResultView struct{ value *format.ToolResult }
