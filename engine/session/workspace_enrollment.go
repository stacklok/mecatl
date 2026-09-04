package session

import (
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxWorkspaceEnrollmentIDBytes = 256

// WorkspaceEnrollmentID is an opaque correlation identifier for one
// pre-prompt workspace enrollment.
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

// BeginWorkspaceEnrollment records one enrollment before the first prompt. It
// does not alter the agent-loop lifecycle or either per-call pending state.
func (s *Session) BeginWorkspaceEnrollment(pending PendingWorkspaceEnrollment) error {
	if s.State != StateIdle || s.Conversation == nil || len(s.Conversation.Messages) != 0 ||
		s.pending != nil || s.pendingAuthorization != nil {
		return fmt.Errorf("%w: workspace enrollment must precede the first prompt", ErrIllegalTransition)
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

// AbortWorkspaceEnrollment clears only the enrollment whose exact identifier
// is supplied.
func (s *Session) AbortWorkspaceEnrollment(id WorkspaceEnrollmentID) error {
	if s.pendingWorkspaceEnrollment == nil || s.pendingWorkspaceEnrollment.ID != id {
		return fmt.Errorf("session: workspace enrollment %q is not pending", id)
	}
	s.pendingWorkspaceEnrollment = nil
	return nil
}
