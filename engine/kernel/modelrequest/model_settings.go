package modelrequest

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ModelSettingsView retains borrowed immutable explicit controls for its lifetime.
// Zero omits both controls; navigation is O(1) and does not clone sibling payloads.
//
//dtogen:include
type ModelSettingsView struct{ value *sessionformat.ModelSettings }
