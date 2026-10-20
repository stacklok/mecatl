package session

import (
	"fmt"
	"slices"
)

// LegacyCallIdentity is verification-only provenance, never executable lookup.
// Slot is the immutable call slot in the original source message.
type LegacyCallIdentity struct {
	CallID     ToolCallID
	Slot       int
	ProviderID string
	ItemID     string
}

// LegacyMessageIdentity preserves snapshot-local coordinates for old evidence.
// Unsupported contains the original message DTO only when normal pairing could
// not be proven. It is private historical data, not a runnable tool result.
type LegacyMessageIdentity struct {
	Incarnation      IncarnationID
	Namespace        string
	Ordinal          int
	Calls            []LegacyCallIdentity
	ResultProviderID string
	ResultOrdinal    int
	ResultSlot       int
	ReasoningItemID  string
	Unsupported      []byte
}

func (p *LegacyMessageIdentity) clone() *LegacyMessageIdentity {
	if p == nil {
		return nil
	}
	out := *p
	out.Calls = slices.Clone(p.Calls)
	out.Unsupported = slices.Clone(p.Unsupported)
	return &out
}

// ValidateIdentityPersistence validates canonical history and continuation authority
// before persistence.
func (s *Session) ValidateIdentityPersistence() error {
	if _, err := validateToolHistory(s.Conversation.Messages); err != nil {
		return fmt.Errorf("session: invalid persisted canonical history: %w", err)
	}
	return s.ValidateAuthorizationState()
}
