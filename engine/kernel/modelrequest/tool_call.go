package modelrequest

import "github.com/stacklok/mecatl/engine/kernel/modelrequest/format"

// ToolCallView retains borrowed immutable original call data for its lifetime.
// Arguments.Copy detaches only argument bytes. Zero is empty; navigation is O(1)
// and does not clone sibling payloads.
//
//dtogen:include
type ToolCallView struct{ value *format.ToolCall }
