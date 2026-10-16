package agent

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

type sessionOriginContextKey struct{}

// withSessionOrigin returns a child context carrying the session id that owns
// schedule side effects created during the run. Deliberately UNEXPORTED: the
// only correct way to acquire an origin is to run under Engine.Run, whose
// startRun stamps it. An out-of-band create has no origin by design
// and goes straight to the schedule manager, never through the
// Schedule tool — so an exported writer would serve no composition this harness
// performs, while letting an embedder name any session as the origin. Exporting
// later is Added (a minor bump); un-exporting later would be breaking, so the
// option is kept open.
//
// It lives here rather than beside session.WithPrincipal on purpose: the
// principal has writers outside the engine core (the server authn edge, the
// syscaller registry), which forces its writer to be exported from the domain
// leaf. This one has exactly one writer (startRun) and one reader
// (ScheduleTool.create), both in this package, and staying unexported IS the
// security argument above.
func withSessionOrigin(ctx context.Context, id session.SessionID) context.Context {
	return context.WithValue(ctx, sessionOriginContextKey{}, id)
}

// sessionOriginFromContext reads the run's origin session id, returning the
// empty id when the context did not come from startRun. The empty id is the
// honest "no origin" value every consumer fails closed on — it is never
// substituted for a fallback session. Its one reader is ScheduleTool.create,
// which sets port.ScheduleSpec.OriginSessionID from it and nothing else.
func sessionOriginFromContext(ctx context.Context) session.SessionID {
	id, _ := ctx.Value(sessionOriginContextKey{}).(session.SessionID)
	return id
}

type sessionModeContextKey struct{}

// withSessionMode returns a child context carrying the executing session's
// permission mode at the moment a tool call executes. Its one writer is
// timeExecute, which stamps sess.Mode per call (not once per run) because the
// mode can change between dispatches; its one reader is ScheduleTool.create,
// which records it as a mutating schedule's fire mode. Unexported for the same
// reason as withSessionOrigin: the mode must come from the run, never from an
// embedder or from model arguments, or a schedule could name a mode its
// creating session never held.
func withSessionMode(ctx context.Context, mode session.PermissionMode) context.Context {
	return context.WithValue(ctx, sessionModeContextKey{}, mode)
}

// sessionModeFromContext reads the executing session's mode, returning the
// empty mode when the context did not come from timeExecute.
func sessionModeFromContext(ctx context.Context) session.PermissionMode {
	mode, _ := ctx.Value(sessionModeContextKey{}).(session.PermissionMode)
	return mode
}
