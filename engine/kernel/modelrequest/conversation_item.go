package modelrequest

import "github.com/stacklok/mecatl/engine/kernel/modelrequest/format"

// ConversationItemView retains borrowed immutable union backing for its lifetime.
// Zero has no selected branch; navigation is O(1) and does not clone siblings.
//
//dtogen:include
type ConversationItemView struct{ value *format.ConversationItem }

// UserMessageView retains borrowed immutable user parts for its lifetime.
// Zero is empty; navigation is O(1) and does not clone sibling payloads.
//
//dtogen:include
type UserMessageView struct{ value *format.UserMessage }
