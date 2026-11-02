package session

import "fmt"

// ProviderCallID is the provider's exact tool-call/result pairing token, not an
// executable invocation identity. Occurrence bindings retain it for outbound
// projection even when a provider reuses the token on later turns.
type ProviderCallID string

// ProviderItemID is a provider's exact output-item identity, distinct from its
// call/result pairing token.
type ProviderItemID string

// ProviderReplay carries adapter-owned opaque replay bytes. Only the adapter
// interprets Format, Version, and Data; Data uses JSON's base64 byte encoding.
type ProviderReplay struct {
	Format  string
	Version uint32
	Data    []byte
}

// ToolCallBinding binds one canonical invocation occurrence to provider tokens.
// It preserves exact provider replay without conflating separate executions that
// happen to share a provider pairing token.
type ToolCallBinding struct {
	CallID    ToolCallID
	PairingID ProviderCallID
	ItemID    ProviderItemID
}

// MessageReplay is owned by the same message as its calls. Bindings are engine
// owned; Provider is an adapter-owned envelope that may contain ordered items
// without requiring additional domain messages.
type MessageReplay struct {
	Bindings []ToolCallBinding
	Provider *ProviderReplay
}

// ValidateMessageReplay checks occurrence bindings without interpreting private
// payloads. A nil carrier has no provider bindings to validate; canonical call
// uniqueness and role/pairing validation belong to the history acceptance seam.
// A present carrier must bind every call exactly once, with distinct nonempty
// pairing tokens within this message. Item tokens need not be present.
func ValidateMessageReplay(m Message) error {
	if m.Replay == nil {
		return nil
	}
	if m.Role != RoleAssistant {
		return fmt.Errorf("replay carrier requires an assistant message")
	}
	calls := make(map[ToolCallID]bool, len(m.ToolCalls))
	for _, call := range m.ToolCalls {
		if call.ID == "" || calls[call.ID] {
			return fmt.Errorf("replay carrier has empty or duplicate call ID")
		}
		calls[call.ID] = true
	}
	bound := make(map[ToolCallID]bool, len(m.Replay.Bindings))
	pairings := make(map[ProviderCallID]bool, len(m.Replay.Bindings))
	for _, binding := range m.Replay.Bindings {
		if !calls[binding.CallID] || bound[binding.CallID] {
			return fmt.Errorf("replay carrier has foreign or duplicate binding")
		}
		if binding.PairingID == "" || pairings[binding.PairingID] {
			return fmt.Errorf("replay carrier has empty or duplicate pairing ID")
		}
		bound[binding.CallID] = true
		pairings[binding.PairingID] = true
	}
	if len(bound) != len(calls) {
		return fmt.Errorf("replay carrier must bind every tool call")
	}
	return nil
}
