package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// subagentDefaultMaxTurns / subagentDefaultMaxToolCalls are the conservative
// PER-FIRE caps applied when a schedule carries no Limits of its own. They
// mirror the subagent posture: a scheduled fire is an unattended delegation,
// so it is bounded exactly as a read-leaning subagent is (decision #6). A
// schedule that pins its own Limits keeps them verbatim.
const (
	subagentDefaultMaxTurns       = 50
	subagentDefaultMaxToolCalls   = 200
	subagentDefaultMaxConsecFails = 5
)

// makeFireFunc builds the composition-supplied scheduler.FireFunc over the
// assembled *server.Service. Each fire mints a FRESH top-level "sched--" session
// (decision #7 — a schedule fire is its own conversation, never a continuation
// of a prior fire's session) via Service.CreateSessionWithProfile, drives it to
// a terminal EvResult via Service.StartRunContent, and returns the fire record
// carrying the stop reason + any error. It applies subagent-grade defaults
// (bounded MaxTurns/MaxToolCalls when the schedule carries none) and maps the
// port.ScheduleSpec's neutral selector/profile onto the server adapter's
// ProviderSelector/SessionProfile. Fail-closed: an error at create or run-start
// surfaces as a ScheduleFire with Stop=StopError (the at-most-once Claim already
// advanced NextFireAt, so a failed fire is NOT retried). Model pinning is
// fail-closed at the provider call — there is NO pre-flight ListModels check (a
// live network call, deferred); an unknown model surfaces as StopError.
func makeFireFunc(svc *server.Service) scheduler.FireFunc {
	return func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
		sel := server.ProviderSelector{
			ProviderID: sched.Spec.Selector.ProviderID,
			ModelID:    sched.Spec.Selector.ModelID,
		}
		profile := server.ProfileDefault
		if sched.Spec.Profile == string(server.ProfileNoFS) {
			profile = server.ProfileNoFS
		}
		// Read-leaning default (decision #3): a schedule that does NOT opt into
		// mutating (Mutating=false) runs in plan mode (read-only toolset) — the
		// conservative posture for unattended runs. A schedule that opts into
		// mutating (Mutating=true) honors its explicit Mode (or default if unset).
		// This is the fire-time enforcement; the create-seam (Phase 2) will
		// additionally reject Mutating=false with a write-capable Mode at save time.
		mode := sched.Spec.Mode
		if mode == "" {
			mode = session.ModeDefault
		}
		if !sched.Spec.Mutating {
			mode = session.ModePlan
		}
		limits := sched.Spec.Limits
		if limits.MaxTurns == 0 {
			limits.MaxTurns = subagentDefaultMaxTurns
		}
		if limits.MaxToolCalls == 0 {
			limits.MaxToolCalls = subagentDefaultMaxToolCalls
		}
		if limits.MaxConsecutiveFailures == 0 {
			limits.MaxConsecutiveFailures = subagentDefaultMaxConsecFails
		}

		sess, err := svc.CreateSessionWithProfile(ctx, sched.Spec.Workspace, mode, limits, sel, profile)
		if err != nil {
			return fireFailed(sched, now, "", err), err
		}
		// Release the fire session's process-scoped resources — its cross-process
		// session lease + renewer goroutine and its per-session engine — once the
		// fire returns, while KEEPING the durable snapshot for pull-only result
		// delivery (decision #8; CloseSession does NOT delete the persisted session,
		// so ScheduleStore.LoadFire + SessionStore.Load still serve the outcome).
		//
		// Without this, run-entry leases are held for the whole process lifetime
		// (released ONLY by CloseSession/shutdown, never per-run — service.go), so on
		// a lease-backed deployment every fire would leak a held lease + renewer AND
		// break Singleton: the next fire's trial-acquire on this still-renewed lease
		// would return ErrLeaseHeld and skip forever (review #189).
		defer svc.CloseSession(sess.ID)

		run, err := svc.StartRunContent(ctx, sess.ID, sched.Spec.Prompt, sched.Spec.Parts)
		if err != nil {
			return fireFailed(sched, now, string(sess.ID), err), err
		}

		// Drive the run to its terminal EvResult. The session is persisted by the
		// relay (the same path a wire client takes); the fire record carries only
		// the stop reason + error pointer to the session id.
		var stop session.StopReason
		var runErr string
		for ev := range run.Events() {
			if ev.Type == session.EvResult && ev.Result != nil {
				stop = ev.Result.Stop
				runErr = ev.Result.Error
				break
			}
		}
		// Decision #7: the fire ID IS the session id (the session id is the
		// discoverability key — LastFireSessionID, which RecordFire sets to
		// f.SessionID, is what a caller hands LoadFire). CreateSessionWithProfile
		// mints the session id (via the Service's NewID); the fire adopts it.
		return port.ScheduleFire{
			ID:           string(sess.ID),
			ScheduleName: sched.Spec.Name,
			SessionID:    sess.ID,
			FiredAt:      now,
			Stop:         stop,
			Err:          runErr,
		}, nil
	}
}

// newFireID mints a per-fire identifier: "sched--<name>-<UTC compact>-<randhex>".
// It is used ONLY on the create-FAILURE fallback path (fireFailed), so a fire
// that never minted a session still has a non-empty, unique RecordFire key. On
// the SUCCESS path the fire id IS the session id minted by
// CreateSessionWithProfile (decision #7), which in Phase 1 is an ordinary random
// id — the "sched--" session-id prefix awaits a session-id override on
// CreateSessionWithProfile (Phase 2). The random suffix keeps two failed fires of
// the same schedule in the same second distinct.
func newFireID(name string, now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("sched--%s-%s-%s", name, now.UTC().Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// fireFailed builds a ScheduleFire for a create/run-start failure: StopError +
// the error string, fail-closed. The session id is "" when create failed (no
// session exists) or the partial id when run-start failed after create. The fire
// id is the session id (decision #7); a create-time failure has no session yet,
// so the fire id falls back to a "sched--<name>-<ts>-<rand>" mint so RecordFire
// has a non-empty, unique key (the at-most-once Claim already advanced
// NextFireAt, so this fire is never re-fired).
func fireFailed(sched port.Schedule, now time.Time, sessID string, err error) port.ScheduleFire {
	id := sessID
	if id == "" {
		id = newFireID(sched.Spec.Name, now)
	}
	return port.ScheduleFire{
		ID:           id,
		ScheduleName: sched.Spec.Name,
		SessionID:    session.SessionID(sessID),
		FiredAt:      now,
		Stop:         session.StopError,
		Err:          err.Error(),
	}
}
