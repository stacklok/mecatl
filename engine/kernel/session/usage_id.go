package session

import (
	"errors"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// UsageID identifies one accounting occurrence within a Session, not a model
// invocation, content identity, or verified charge. It is comparable and owns no
// mutable storage. Zero is invalid; allocation order is not a public contract.
type UsageID struct {
	// sessionID is the enclosing Session, independent of any correlated run.
	sessionID sessionid.SessionID
	// usageOrdinal distinguishes this private shape from other occurrence IDs.
	usageOrdinal uint64
}

// SessionID returns the enclosing Session identity, or zero for zero.
func (v UsageID) SessionID() sessionid.SessionID { return v.sessionID }

// SessionRelative returns the positive Session-relative component, or zero for
// zero. The component alone establishes neither allocation nor membership.
func (v UsageID) SessionRelative() uint64 { return v.usageOrdinal }

// NewUsageID reconstructs a structurally valid scoped identity. It does not
// allocate usage, prove membership or authority, or imply an allocation order.
// Failure returns the zero identity.
func NewUsageID(sessionID sessionid.SessionID, component uint64) (UsageID, error) {
	id := UsageID{sessionID: sessionID, usageOrdinal: component}
	if err := id.Validate(); err != nil {
		return UsageID{}, err
	}
	return id, nil
}

// Validate checks structural validity without mutation or Session lookup.
func (v UsageID) Validate() error {
	if _, err := sessionid.ParseSessionID(v.sessionID.String()); err != nil {
		return errors.New("invalid usage session ID")
	}
	if v.usageOrdinal == 0 {
		return errors.New("usage ordinal must be positive")
	}
	return nil
}

// Equal reports whether both Session-scoped occurrences are identical.
func (v UsageID) Equal(other UsageID) bool { return v == other }
