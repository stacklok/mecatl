package session

import (
	"fmt"
	"sort"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxWorkspaceEnrollmentIDBytes        = 256
	maxWorkspaceEnrollmentTools          = 256
	maxWorkspaceEnrollmentToolNameBytes  = 256
	maxWorkspaceEnrollmentToolNamesBytes = 32 * 1024
)

// WorkspaceEnrollmentID is an opaque correlation identifier for one
// workspace enrollment.
type WorkspaceEnrollmentID string

// Valid reports whether id is non-empty, bounded, valid UTF-8, and contains no
// whitespace or control characters.
func (id WorkspaceEnrollmentID) Valid() bool {
	value := string(id)
	if value == "" || len(value) > maxWorkspaceEnrollmentIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// PendingWorkspaceEnrollment contains only durable, presentation-safe
// correlation. Runtime endpoints, credentials, grants, and discovered services
// do not belong in the session aggregate.
type PendingWorkspaceEnrollment struct {
	ID               WorkspaceEnrollmentID
	RequiredServices uint32
	ExpiresAt        time.Time
}

func (s *Session) rejectWhileWorkspaceEnrollmentPending(operation string) error {
	if s.pendingWorkspaceEnrollment != nil {
		return fmt.Errorf("%w: %s while workspace enrollment is pending", ErrIllegalTransition, operation)
	}
	return nil
}

// BeginWorkspaceEnrollment records one enrollment from a stable idle session. It
// does not alter the agent-loop lifecycle or either per-call pending state.
func (s *Session) BeginWorkspaceEnrollment(pending PendingWorkspaceEnrollment) error {
	if s.State != StateIdle || s.Conversation == nil ||
		s.pending != nil || s.pendingAuthorization != nil {
		return fmt.Errorf("%w: workspace enrollment requires an idle session without a conflicting control", ErrIllegalTransition)
	}
	if !s.authorityBound {
		return fmt.Errorf("%w: workspace enrollment requires bound authority", ErrIllegalTransition)
	}
	if s.pendingWorkspaceEnrollment != nil {
		return fmt.Errorf("%w: workspace enrollment already pending", ErrIllegalTransition)
	}
	if !pending.ID.Valid() || pending.RequiredServices == 0 || pending.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: invalid workspace enrollment", ErrIllegalTransition)
	}
	pendingCopy := pending
	s.pendingWorkspaceEnrollment = &pendingCopy
	return nil
}

// PendingWorkspaceEnrollment returns an independent copy of the pending safe
// correlation value.
func (s *Session) PendingWorkspaceEnrollment() (PendingWorkspaceEnrollment, bool) {
	if s.pendingWorkspaceEnrollment == nil {
		return PendingWorkspaceEnrollment{}, false
	}
	return *s.pendingWorkspaceEnrollment, true
}

func validToolAuthorityName(name string) bool {
	if name == "" || len(name) > maxWorkspaceEnrollmentToolNameBytes || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidWorkspaceEnrollmentToolNames reports whether names is an exact,
// duplicate-free enrollment tool set. Names are opaque catalogue identifiers:
// the boundary imposes only framing and size safety, not a provider-specific
// function-name grammar.
func ValidWorkspaceEnrollmentToolNames(names []string) bool {
	if len(names) > maxWorkspaceEnrollmentTools {
		return false
	}
	seen := make(map[string]struct{}, len(names))
	totalBytes := 0
	for _, name := range names {
		if !validToolAuthorityName(name) {
			return false
		}
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		totalBytes += len(name)
		if totalBytes > maxWorkspaceEnrollmentToolNamesBytes {
			return false
		}
		seen[name] = struct{}{}
	}
	return true
}

// WorkspaceEnrollmentBrokerKeys returns an independent copy of the exact broker
// registration-key ledger and whether that provenance is present. Absent
// provenance is unrepaired legacy state, not a known empty ledger.
func (s *Session) WorkspaceEnrollmentBrokerKeys() ([]string, bool) {
	return append([]string(nil), s.workspaceEnrollmentBrokerKeys...), s.workspaceEnrollmentBrokerKeysPresent
}

// RestoreWorkspaceEnrollmentBrokerKeys restores copied workspace-enrollment
// provenance exactly once during root creation, snapshot restore, or successor
// construction. A present empty ledger is valid; absent provenance must have no
// keys.
func (s *Session) RestoreWorkspaceEnrollmentBrokerKeys(keys []string, present bool) error {
	if s.workspaceEnrollmentBrokerKeysRestored {
		return fmt.Errorf("%w: workspace enrollment broker keys already restored", ErrIllegalTransition)
	}
	if s.State != StateIdle || s.pending != nil || s.pendingAuthorization != nil || s.pendingWorkspaceEnrollment != nil {
		return fmt.Errorf("%w: restore workspace enrollment broker keys requires an idle session", ErrIllegalTransition)
	}
	if (!present && len(keys) != 0) || !ValidWorkspaceEnrollmentToolNames(keys) {
		return fmt.Errorf("%w: invalid workspace enrollment broker-key ledger", ErrIllegalTransition)
	}
	s.workspaceEnrollmentBrokerKeys = append([]string(nil), keys...)
	s.workspaceEnrollmentBrokerKeysPresent = present
	s.workspaceEnrollmentBrokerKeysRestored = true
	return nil
}

// CompleteWorkspaceEnrollment atomically replaces only the prior broker
// registration-key bundle after the exact pending enrollment has produced a
// verified catalogue. Authority is cloned from the aggregate so callers cannot
// supply or alter any non-tool axis. Previously recorded broker keys absent from
// carried authority remain excluded; keys newly introduced by this bundle remain
// eligible for authority.
func (s *Session) CompleteWorkspaceEnrollment(pending PendingWorkspaceEnrollment, exactTools []string) error {
	if s.pendingWorkspaceEnrollment == nil ||
		s.pendingWorkspaceEnrollment.ID != pending.ID ||
		s.pendingWorkspaceEnrollment.RequiredServices != pending.RequiredServices ||
		!s.pendingWorkspaceEnrollment.ExpiresAt.Equal(pending.ExpiresAt) {
		return fmt.Errorf("session: workspace enrollment %q is not pending", pending.ID)
	}
	if s.State != StateIdle || s.Conversation == nil {
		return fmt.Errorf("%w: workspace enrollment requires an idle session", ErrIllegalTransition)
	}
	if !s.authorityBound || !s.workspaceEnrollmentBrokerKeysPresent || !ValidWorkspaceEnrollmentToolNames(exactTools) {
		return fmt.Errorf("%w: invalid workspace enrollment tool set", ErrIllegalTransition)
	}

	completedAuthority, ledger, err := completeWorkspaceEnrollmentAuthority(
		s.Authority, s.workspaceEnrollmentBrokerKeys, exactTools,
	)
	if err != nil {
		return err
	}
	s.Authority = completedAuthority
	s.workspaceEnrollmentBrokerKeys = ledger
	s.workspaceEnrollmentBrokerKeysPresent = true
	s.pendingWorkspaceEnrollment = nil
	return nil
}

func completeWorkspaceEnrollmentAuthority(
	authority Authority, priorBrokerKeys, exactTools []string,
) (Authority, []string, error) {
	priorBroker := make(map[string]struct{}, len(priorBrokerKeys))
	for _, name := range priorBrokerKeys {
		priorBroker[name] = struct{}{}
	}
	priorAuthority := make(map[string]struct{}, len(authority.CapabilitySet.Tools))
	for _, name := range authority.CapabilitySet.Tools {
		priorAuthority[name] = struct{}{}
	}
	excluded := make(map[string]struct{}, len(priorBroker))
	for name := range priorBroker {
		if _, included := priorAuthority[name]; !included {
			excluded[name] = struct{}{}
		}
	}

	completed := authority.Clone()
	replacement := make([]string, 0, len(completed.CapabilitySet.Tools)+len(exactTools))
	replacementSet := make(map[string]struct{}, len(completed.CapabilitySet.Tools)+len(exactTools))
	for _, name := range completed.CapabilitySet.Tools {
		if _, broker := priorBroker[name]; !broker {
			replacement = append(replacement, name)
			replacementSet[name] = struct{}{}
		}
	}
	for _, name := range exactTools {
		if _, excluded := excluded[name]; !excluded {
			if _, alreadyPresent := replacementSet[name]; !alreadyPresent {
				replacement = append(replacement, name)
				replacementSet[name] = struct{}{}
			}
		}
	}
	ledger := append([]string(nil), exactTools...)
	ledgerSet := make(map[string]struct{}, len(exactTools))
	for _, name := range exactTools {
		ledgerSet[name] = struct{}{}
	}
	appendedExcluded := make([]string, 0, len(excluded))
	for name := range excluded {
		if _, included := ledgerSet[name]; !included {
			appendedExcluded = append(appendedExcluded, name)
		}
	}
	sort.Strings(appendedExcluded)
	ledger = append(ledger, appendedExcluded...)
	if !ValidWorkspaceEnrollmentToolNames(ledger) {
		return Authority{}, nil, fmt.Errorf("%w: invalid workspace enrollment broker-key ledger", ErrIllegalTransition)
	}

	completed.CapabilitySet.Tools = replacement
	return completed, ledger, nil
}

// AbortWorkspaceEnrollment clears only the enrollment whose exact identifier
// is supplied.
func (s *Session) AbortWorkspaceEnrollment(id WorkspaceEnrollmentID) error {
	if s.pendingWorkspaceEnrollment == nil || s.pendingWorkspaceEnrollment.ID != id {
		return fmt.Errorf("session: workspace enrollment %q is not pending", id)
	}
	s.pendingWorkspaceEnrollment = nil
	return nil
}
