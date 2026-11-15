package format

import (
	"errors"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// AssistantMessage preserves one response's interleaved items and optional whole
// private envelope. Zero is valid empty content. Assignment shares storage; Clone
// detaches it. Provider call IDs must be unique only within this message.
//
//dtogen:validate validateLocal
type AssistantMessage struct {
	// Items preserves output/refusal/call order. Nil and empty are equivalent.
	Items []AssistantItem `cbor:"i"`
	// Private is an optional whole opaque envelope. Nil omits it; a present envelope
	// with empty Data is distinct. Its metadata does not prove replay compatibility.
	Private *sessionformat.OpaqueProviderData `cbor:"p,omitempty"`
}

// Validate checks children and response-local provider-ID uniqueness.
func (v AssistantMessage) Validate() error {
	for _, item := range v.Items {
		if err := item.Validate(); err != nil {
			return err
		}
	}
	if v.Private != nil {
		if err := v.Private.Validate(); err != nil {
			return err
		}
	}
	return v.validateLocal()
}

// validateLocal allocates the response-local set only after bounded child checks.
func (v AssistantMessage) validateLocal() error {
	seen := make(map[string]struct{})
	for _, item := range v.Items {
		if item.Call == nil {
			continue
		}
		if _, ok := seen[item.Call.ProviderCallID]; ok {
			return errors.New("duplicate response provider call ID")
		}
		seen[item.Call.ProviderCallID] = struct{}{}
	}
	return nil
}

// AssistantItem requires exactly one typed output, refusal, or requested call.
// Assignment shares the child; Clone detaches it. Refusal is explicit, not inferred.
//
//dtogen:validate validateLocal
type AssistantItem struct {
	// Output is optional ordinary output, exclusive with Refusal and Call.
	Output *ContentBlock `cbor:"o,omitempty"`
	// Refusal is optional provider-typed refusal content, exclusive with other branches.
	Refusal *ContentBlock `cbor:"r,omitempty"`
	// Call is an optional original requested call, exclusive with content branches.
	Call *ToolCall `cbor:"c,omitempty"`
}

// Validate checks exclusivity and the selected child.
func (v AssistantItem) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Output != nil {
		return v.Output.Validate()
	}
	if v.Refusal != nil {
		return v.Refusal.Validate()
	}
	return v.Call.Validate()
}

// validateLocal checks the union after bounded child traversal.
func (v AssistantItem) validateLocal() error {
	n := 0
	if v.Output != nil {
		n++
	}
	if v.Refusal != nil {
		n++
	}
	if v.Call != nil {
		n++
	}
	if n != 1 {
		return errors.New("assistant item requires exactly one branch")
	}
	return nil
}
