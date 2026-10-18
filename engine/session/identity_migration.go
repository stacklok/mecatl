package session

import (
	"errors"
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

// IdentityMigrationHold is private codec-owned continuation data, installed when
// legacy evidence cannot prove which invocation may resume. Inspection remains
// available, but Payload is opaque to the aggregate and must never be projected,
// logged, or executed.
type IdentityMigrationHold struct {
	Reason  string
	Payload []byte
}

// ErrMigrationHeld denotes unresolved legacy invocation identity. It grants no
// execution, external claim, or continuation-consumption authority.
var ErrMigrationHeld = errors.New("session: identity migration held")

const maxIdentityHoldBytes = 16 << 20

// CheckIdentityReady is the pre-effect guard for identity-dependent operations.
func (s *Session) CheckIdentityReady() error {
	if s.identityHold != nil {
		return ErrMigrationHeld
	}
	return nil
}

// IdentityHoldSummary exposes only a fixed safe reason, never private bytes.
func (s *Session) IdentityHoldSummary() (string, bool) {
	if s.identityHold == nil {
		return "", false
	}
	return s.identityHold.Reason, true
}

// IdentityHoldSnapshot returns an independently owned private persistence view.
func (s *Session) IdentityHoldSnapshot() *IdentityMigrationHold {
	if s.identityHold == nil {
		return nil
	}
	return &IdentityMigrationHold{Reason: s.identityHold.Reason, Payload: slices.Clone(s.identityHold.Payload)}
}

// RestoreIdentityMigrationHold restores an inspection-only lifecycle overlay on
// a fresh idle aggregate. It deliberately does not restore runnable pending data.
func (s *Session) RestoreIdentityMigrationHold(state State, hold IdentityMigrationHold) error {
	if s.State != StateIdle || s.pending != nil || s.pendingAuthorization != nil || s.identityHold != nil {
		return fmt.Errorf("%w: restore migration hold", ErrIllegalTransition)
	}
	if state != StateAwaiting && state != StateAuthorizing {
		return errors.New("session: invalid migration hold lifecycle")
	}
	if hold.Reason != "legacy-continuation-unproven" || len(hold.Payload) == 0 || len(hold.Payload) > maxIdentityHoldBytes {
		return errors.New("session: invalid migration hold payload")
	}
	s.identityHold = &IdentityMigrationHold{Reason: hold.Reason, Payload: slices.Clone(hold.Payload)}
	s.State = state
	return nil
}

// ValidateIdentityPersistence separates held persistence from runnable state
// validation. A held continuation must not acquire canonical pending authority.
func (s *Session) ValidateIdentityPersistence() error {
	if _, err := validateToolHistory(s.Conversation.Messages); err != nil {
		return fmt.Errorf("session: invalid persisted canonical history: %w", err)
	}
	if s.identityHold == nil {
		return s.ValidateAuthorizationState()
	}
	if (s.State != StateAwaiting && s.State != StateAuthorizing) || s.pending != nil || s.pendingAuthorization != nil ||
		s.identityHold.Reason != "legacy-continuation-unproven" || len(s.identityHold.Payload) == 0 || len(s.identityHold.Payload) > maxIdentityHoldBytes {
		return errors.New("session: invalid persisted migration hold")
	}
	return nil
}
