package modelrequest

import (
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ContentBlockView retains borrowed immutable block backing for its lifetime.
// Zero is empty; navigation is O(1) and does not clone sibling payloads.
//
//dtogen:include
type ContentBlockView struct{ value *format.ContentBlock }

// ContentPartView retains borrowed immutable typed content for its lifetime.
// Zero has no selected branch; navigation is O(1) and does not clone siblings.
//
//dtogen:include
type ContentPartView struct{ value *sessionformat.ContentPart }

// MediaView retains borrowed immutable media backing for its lifetime, without
// dereferencing locators. InlineData.Copy detaches only its leaf. Zero is empty;
// navigation is O(1) and does not clone siblings.
//
//dtogen:include
type MediaView struct{ value *sessionformat.Media }
