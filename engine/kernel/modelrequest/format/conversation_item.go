package format

import (
	"errors"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ConversationItem is one chronological message. Exactly one branch is required.
// Assignment shares the selected child; Clone detaches it.
//
//dtogen:validate validateLocal
type ConversationItem struct {
	// User is the optional user branch, mutually exclusive with the other branches.
	User *UserMessage `cbor:"u,omitempty"`
	// Assistant is the optional assistant branch, mutually exclusive with the others.
	Assistant *AssistantMessage `cbor:"a,omitempty"`
	// ToolResult is the optional result branch, mutually exclusive with the others.
	ToolResult *ToolResult `cbor:"t,omitempty"`
}

// Validate checks branch exclusivity and the selected message.
func (v ConversationItem) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.User != nil {
		return v.User.Validate()
	}
	if v.Assistant != nil {
		return v.Assistant.Validate()
	}
	return v.ToolResult.Validate()
}

// validateLocal checks only the union after bounded child traversal.
func (v ConversationItem) validateLocal() error {
	n := 0
	if v.User != nil {
		n++
	}
	if v.Assistant != nil {
		n++
	}
	if v.ToolResult != nil {
		n++
	}
	if n != 1 {
		return errors.New("conversation item requires exactly one branch")
	}
	return nil
}

// UserMessage contains prepared user content, without actor or history identity.
// Zero is valid empty content; assignment shares parts and Clone detaches them.
type UserMessage struct {
	// Parts preserves content order and duplicates. Nil and empty are equivalent.
	Parts []sessionformat.ContentPart `cbor:"p"`
}

// Validate checks every typed part.
func (v UserMessage) Validate() error { return ContentBlock(v).Validate() }
