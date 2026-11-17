package modelrequest

import (
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// AssistantMessageView retains borrowed immutable interleaved items and optional
// private backing for its lifetime. Zero is empty; navigation is O(1) and does not
// clone sibling payloads.
//
//dtogen:include
type AssistantMessageView struct{ value *format.AssistantMessage }

// AssistantItemView retains borrowed immutable union backing for its lifetime.
// Zero has no selected branch; navigation is O(1) and does not clone siblings.
//
//dtogen:include
type AssistantItemView struct{ value *format.AssistantItem }

// OpaqueProviderDataView retains borrowed immutable envelope backing for its
// lifetime, without interpreting bytes or proving compatibility. Data.Copy detaches
// only its leaf. Zero is empty; navigation is O(1) and does not clone siblings.
//
//dtogen:include
type OpaqueProviderDataView struct {
	value *sessionformat.OpaqueProviderData
}
