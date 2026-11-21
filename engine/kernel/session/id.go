package session

import sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"

// SessionID identifies a Session and the generic history stored under that same
// resource key. Its zero value is invalid; it is comparable and immutable.
type SessionID = sessionid.SessionID

// NewSessionID generates a Session identity without reserving it in storage.
// It panics if secure randomness is unavailable.
func NewSessionID() SessionID { return sessionid.NewSessionID() }

// ParseSessionID validates exact Session-kind text without normalization.
// Failure returns the zero identity.
func ParseSessionID(s string) (SessionID, error) { return sessionid.ParseSessionID(s) }
