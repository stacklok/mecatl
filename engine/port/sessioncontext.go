package port

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

type sessionIDContextKey struct{}
type runSerialContextKey struct{}
type turnIndexContextKey struct{}

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

// WithRunSerial returns a child context carrying the process-local serial of the
// run that owns a model call. The value is diagnostic correlation only: it is
// bounded to one int64, grants no authority, and is not durable across restarts.
func WithRunSerial(ctx context.Context, serial int64) context.Context {
	return context.WithValue(ctx, runSerialContextKey{}, serial)
}

// RunSerialFromContext returns the process-local run serial carried by ctx.
func RunSerialFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	serial, ok := ctx.Value(runSerialContextKey{}).(int64)
	return serial, ok
}

// WithTurnIndex returns a child context carrying the zero-based turn index of
// the model call within its session. It is diagnostic correlation only.
func WithTurnIndex(ctx context.Context, index int) context.Context {
	return context.WithValue(ctx, turnIndexContextKey{}, index)
}

// TurnIndexFromContext returns the zero-based turn index carried by ctx.
func TurnIndexFromContext(ctx context.Context) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	index, ok := ctx.Value(turnIndexContextKey{}).(int)
	return index, ok
}
