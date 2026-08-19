package port

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

// SessionLiveness protects engine-owned child sessions for their complete
// lifecycle. Register marks id live until the returned idempotent release is
// called. Implementations may also acquire a distributed lease; in that case
// registration fails rather than allowing the child to become runnable without
// exclusion. cancel is invoked if an acquired lease is lost.
//
// Multiple registrations for one id are counted; IsLive remains true until all
// registrations are released. Implementations must bound and join any renewal
// work before release returns.
type SessionLiveness interface {
	Register(ctx context.Context, id session.SessionID, cancel context.CancelFunc) (release func(), err error)
	IsLive(session.SessionID) bool
}
