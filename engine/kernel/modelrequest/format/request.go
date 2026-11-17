package format

import (
	"errors"
	"unicode/utf8"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RequestV1 is an owned-by-caller logical model request, not SDK wire bytes.
// It has no envelope, version, route, source identity, recipe, or CID. Nil and
// empty collections are equivalent; optional present-empty leaves remain distinct.
// Assignment shares mutable storage; Clone detaches the complete value.
//
//dtogen:include
//dtogen:validate validateLocal
type RequestV1 struct {
	// API is the required nonempty exact UTF-8 target API label, not credentials.
	API string `cbor:"a"`
	// Model is the required nonempty exact UTF-8 target model label.
	Model string `cbor:"m"`
	// Settings contains explicit prepared controls. Nil controls mean omission,
	// never inheritance or current defaults; consumers must not normalize them.
	Settings sessionformat.ModelSettings `cbor:"s"`
	// RootInstructions preserves prepared root block order and boundaries.
	RootInstructions []ContentBlock `cbor:"r"`
	// UserInstructions preserves independently prepared standing user-guidance blocks
	// in order.
	UserInstructions []ContentBlock `cbor:"u"`
	// Tools preserves advertised order and requires unique exact names.
	Tools []sessionformat.ToolDefinition `cbor:"t"`
	// Conversation preserves chronological effective messages. Every call requires
	// exactly one result, in requested-call order before the next user/assistant
	// message. Provider IDs may be reused by different assistant responses.
	Conversation []ConversationItem `cbor:"c"`
}

// Validate checks the complete logical shape without operational resource limits.
func (v RequestV1) Validate() error {
	if err := v.Settings.Validate(); err != nil {
		return err
	}
	for _, b := range v.RootInstructions {
		if err := b.Validate(); err != nil {
			return err
		}
	}
	for _, b := range v.UserInstructions {
		if err := b.Validate(); err != nil {
			return err
		}
	}
	for _, t := range v.Tools {
		if err := t.Validate(); err != nil {
			return err
		}
	}
	for _, c := range v.Conversation {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return v.validateLocal()
}

// validateLocal checks cross-child invariants only after bounded codec traversal.
func (v RequestV1) validateLocal() error {
	if v.API == "" || v.Model == "" || !utf8.ValidString(v.API) || !utf8.ValidString(v.Model) {
		return errors.New("request requires nonempty UTF-8 API and model")
	}
	seen := make(map[string]struct{}, len(v.Tools))
	for _, t := range v.Tools {
		if _, ok := seen[t.Name]; ok {
			return errors.New("duplicate advertised tool name")
		}
		seen[t.Name] = struct{}{}
	}
	// Retain only the current response's items, without a second call-ID slice.
	var pending []AssistantItem
	for _, c := range v.Conversation {
		if c.ToolResult != nil {
			if len(pending) == 0 || pending[0].Call.ProviderCallID != c.ToolResult.ProviderCallID {
				return errors.New("orphan, duplicate, or out-of-order tool result")
			}
			pending = pending[1:]
		} else {
			if len(pending) != 0 {
				return errors.New("missing tool result")
			}
			if c.Assistant != nil {
				pending = c.Assistant.Items
			}
		}
		for len(pending) > 0 && pending[0].Call == nil {
			pending = pending[1:]
		}
	}
	if len(pending) != 0 {
		return errors.New("missing tool result")
	}
	return nil
}
