package session

import (
	"fmt"
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
	if s.brokerCredentialCustody != nil {
		return fmt.Errorf("%w: broker credential custody must be cleared before beginning workspace enrollment", ErrIllegalTransition)
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

// CompleteWorkspaceEnrollment atomically replaces the complete tool set after
// the exact pending enrollment has produced a verified catalogue. Authority is
// cloned from the aggregate so callers cannot supply or alter any non-tool axis.
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
	if !s.authorityBound || !ValidWorkspaceEnrollmentToolNames(exactTools) {
		return fmt.Errorf("%w: invalid workspace enrollment tool set", ErrIllegalTransition)
	}

	exact := s.Authority.Clone()
	exact.CapabilitySet.Tools = append([]string(nil), exactTools...)
	s.Authority = exact
	s.pendingWorkspaceEnrollment = nil
	return nil
}

// CompleteWorkspaceEnrollmentWithBinding atomically installs the fresh broker
// binding and authority produced by one exact pending enrollment.
func (s *Session) CompleteWorkspaceEnrollmentWithBinding(pending PendingWorkspaceEnrollment, binding ExternalBinding, exactTools []string) error {
	if binding == "" {
		return fmt.Errorf("%w: broker binding is required", ErrIllegalTransition)
	}
	if s.pendingWorkspaceEnrollment == nil || s.pendingWorkspaceEnrollment.ID != pending.ID ||
		s.pendingWorkspaceEnrollment.RequiredServices != pending.RequiredServices ||
		!s.pendingWorkspaceEnrollment.ExpiresAt.Equal(pending.ExpiresAt) {
		return fmt.Errorf("session: workspace enrollment %q is not pending", pending.ID)
	}
	if s.State != StateIdle || s.Conversation == nil || len(s.Conversation.Messages) != 0 {
		return fmt.Errorf("%w: workspace enrollment must complete before the first prompt", ErrIllegalTransition)
	}
	if !s.authorityBound || !ValidWorkspaceEnrollmentToolNames(exactTools) {
		return fmt.Errorf("%w: invalid workspace enrollment tool set", ErrIllegalTransition)
	}
	exact := s.Authority.Clone()
	exact.CapabilitySet.Tools = append([]string(nil), exactTools...)
	s.Authority = exact
	s.ExternalBinding = binding
	s.pendingWorkspaceEnrollment = nil
	return nil
}

// AbortWorkspaceEnrollment clears only the enrollment whose exact identifier
// is supplied.
func (s *Session) AbortWorkspaceEnrollment(id WorkspaceEnrollmentID) error {
	if s.pendingWorkspaceEnrollment == nil || s.pendingWorkspaceEnrollment.ID != id {
		return fmt.Errorf("session: workspace enrollment %q is not pending", id)
	}
	if s.brokerCredentialCustody != nil {
		return fmt.Errorf("%w: broker credential custody must be cleared before aborting workspace enrollment", ErrIllegalTransition)
	}
	s.pendingWorkspaceEnrollment = nil
	return nil
}
