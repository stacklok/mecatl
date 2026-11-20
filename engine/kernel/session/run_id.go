package session

import (
	"errors"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// RunID identifies a run occurrence, not a successful execution or content CID.
// It is comparable and contains no mutable storage. Zero is invalid. Obtain IDs
// from session allocation or inspection; their allocation order is not a public contract.
type RunID struct {
	// sessionID is the Session in which the run occurs.
	sessionID sessionid.SessionID
	// ordinal is the one-based run ordinal, independently of input ordinals.
	ordinal uint64
}

// SessionID returns the enclosing Session identity, or zero for zero.
func (v RunID) SessionID() sessionid.SessionID { return v.sessionID }

// SessionRelative returns this run's nonzero component within its enclosing
// Session, or zero for a zero RunID. The component alone does not prove the
// run belongs to any Session or was allocated there.
func (v RunID) SessionRelative() uint64 { return v.ordinal }

// NewRunID reconstructs a structurally valid run identity from its enclosing
// Session and nonzero local component. It does not allocate a run, establish
// Session membership, or imply an allocation order.
func NewRunID(sessionID sessionid.SessionID, component uint64) (RunID, error) {
	id := RunID{sessionID: sessionID, ordinal: component}
	if err := id.Validate(); err != nil {
		return RunID{}, err
	}
	return id, nil
}

// validateOrdinal checks the local occurrence ordinal.
func (v RunID) validateOrdinal() error {
	if v.ordinal == 0 {
		return errors.New("run ordinal must be positive")
	}
	return nil
}

// Validate checks local identity validity without mutation or Session lookup.
// It does not establish acceptance or monotonic allocation.
func (v RunID) Validate() error {
	if _, err := sessionid.ParseSessionID(v.sessionID.String()); err != nil {
		return errors.New("invalid run session ID")
	}
	return v.validateOrdinal()
}

// Equal reports whether both Session-scoped occurrences are identical.
func (v RunID) Equal(other RunID) bool { return v == other }
