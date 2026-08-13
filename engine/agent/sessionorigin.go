package agent

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

type sessionOriginContextKey struct{}

// withSessionOrigin returns a child context carrying the session id that owns
// schedule side effects created during the run. Deliberately UNEXPORTED: the
// only correct way to acquire an origin is to run under Engine.Run, whose
// startRun stamps it. An out-of-band create has no origin by design (ADR 0075
// decision #1) and goes straight to the schedule manager, never through the
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
