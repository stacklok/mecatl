package port

import "github.com/stacklok/mecatl/engine/session"

// SessionLiveness tracks process-local session activity that is not represented
// by a Service-owned top-level run. Register marks id live until the returned
// idempotent release is called. Multiple registrations for one id are counted;
// IsLive remains true until all registrations are released.
//
// This seam is process-local only. Cross-process exclusion remains the
// responsibility of SessionLease.
type SessionLiveness interface {
	Register(session.SessionID) (release func())
	IsLive(session.SessionID) bool
}
