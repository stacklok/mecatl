package port

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

type sessionIDContextKey struct{}

// WithSessionID returns a child context carrying the exact identity of the
// session that owns model calls made with that context. It grants no authority.
func WithSessionID(ctx context.Context, id session.SessionID) context.Context {
	return context.WithValue(ctx, sessionIDContextKey{}, id)
}

// SessionIDFromContext returns the session identity carried by ctx.
func SessionIDFromContext(ctx context.Context) (session.SessionID, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(sessionIDContextKey{}).(session.SessionID)
	return id, ok
}
