package app

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// reconcileStaleFireMsgClaim is the terminal StopError Err the reconciler
// records for a fire lost between Claim and session creation (crash sub-case
// 1: LastFireSessionID == port.PendingFireSessionID). It is the honest,
// operator-readable reason an operator reading the fire record sees.
const reconcileStaleFireMsgClaim = "fire lost: process crashed between claim and session creation"

// reconcileStaleFireMsgRun is the terminal StopError Err the reconciler
// records for a fire lost during its run (crash sub-case 2: a real in-flight
// LastFireSessionID whose lease lapsed). It is the honest, operator-readable
// reason an operator reading the fire record sees.
const reconcileStaleFireMsgRun = "fire lost: process crashed during run"

// makeReconcileStaleFire builds the composition-injected
// scheduler.ReconcileStaleFire callback over the assembled *server.Service +
// ScheduleStore (issue #386 Phase 4b, acceptance criterion #7). It is the
// SETTLE half of the stale-fire reconciler: the scheduler package does the
// DETECTION (store + the leader-lease/isPriorFireLive seam it already has) and
// hands a detected stale schedule to this callback, which settles the
// terminal state a crashed process left behind.
//
// Two crash cases the detector hands it (see scheduler.shouldReconcileStaleFire):
//
//  1. Crash after Claim, before session creation: LastFireSessionID ==
//     port.PendingFireSessionID (the sentinel). No session exists. The
//     callback mints a fire id, RecordFire-ing a terminal StopError fire
//     (reconcileStaleFireMsgClaim) — RecordFire clears the in-flight
//     ScheduleState fields (LastFireStartedAt/LastFireProgressAt/FireDeadline)
//     and overwrites LastFireSessionID with the minted id. The at-most-once
//     Claim already advanced NextFireAt, so the slot is gone; the reconciled
//     fire is NOT retried (the same posture as a failed fire).
//
//  2. Crash after session creation, before RecordFire: LastFireSessionID is a
//     real "sched--" id, the fire is still in-flight. The callback settles the
//     terminal session snapshot (cancelled, Interrupt-recoverable) and
//     RecordFire-ing a terminal StopError fire (reconcileStaleFireMsgRun) so
//     the schedule's in-flight state is cleared and a caller's ListFires sees
//     the honest outcome (not a dangling in-flight record forever).
//
// Idempotency: RecordFire is idempotent per fire id, so a fire already terminal
// (a prior reconcile or a late RecordFire from the crashed process's
// last-gasp save) is a no-op. The settle persists the cancelled snapshot
// directly via the SessionStore (the same store the Service uses) — a
// restarted process has no in-memory run registered with Service.Persist, so
// Persist would no-op; the store is the durable writer. The snapshot lands
// recoverable (cancelled, NOT running) regardless of the loop's save race.
//
// It is nil-safe-by-construction on the store (a nil store — the
// byte-identical no-schedule path — is a no-op) and best-effort throughout: a
// settle/RecordFire failure WARNs and never panics (the detector re-runs on
// the next tick). It does NOT fail the tick.
// NOTE (issue #475 Step 4): this reconciler is "sched--"-fire-specific and
// only ever inspects the MOST RECENT fire per schedule (sched.State), so it
// is not the general mechanism for a crash-orphaned StateRunning session — a
// future reader should not mistake it for one. The generic case (any other
// id, including subagent-*/parallel-*/team-* children, and any "sched--" fire
// session OLDER than the latest one) is handled by session_reconcile.go's
// startStaleSessionReconcile sweep and by Step 3's run-entry funnel repair. An
// older orphaned "sched--" session beyond the latest fire is reconciled by
// NEITHER mechanism — session_reconcile.go deliberately excludes the
// "sched--" family (this reconciler owns it) — a known residual documented
// more fully in Step 5's ADR update.
func makeReconcileStaleFire(svc *server.Service, store port.ScheduleStore, sessionStore port.SessionStore) func(ctx context.Context, sched port.Schedule) {
	return func(ctx context.Context, sched port.Schedule) {
		if store == nil {
			return // byte-identical no-schedule path.
		}
		diag := svc.Diagnostics()
		if diag == nil {
			diag = port.NopDiagnostics{}
		}
		name := sched.Spec.Name
		now := time.Now()

		// Crash sub-case 1: pending sentinel (crash after Claim, before session
		// creation). No session exists; mint a fire id and RecordFire a terminal
		// StopError fire. RecordFire clears the in-flight ScheduleState fields
		// and overwrites LastFireSessionID with the minted id. The at-most-once
		// Claim already advanced NextFireAt, so this fire is NOT retried.
		if sched.State.LastFireSessionID == port.PendingFireSessionID {
			anchor := sched.State.LastFireAt
			if anchor.IsZero() {
				// Defensive: a pending sentinel with no LastFireAt is not a real
				// crashed fire (the detector skips it); mint against now so the
				// record has a sane FiredAt.
				anchor = now
			}
			id := newFireID(name, anchor)
			fire := port.ScheduleFire{
				ID:           id,
				ScheduleName: name,
				// SessionID stays empty — no session was ever created. RecordFire
				// overwrites LastFireSessionID with it (clearing the pending
				// sentinel); an empty real id is honest (the fire never ran).
				SessionID: "",
				FiredAt:   anchor,
				Stop:      session.StopError,
				Err:       reconcileStaleFireMsgClaim,
			}
			if err := store.RecordFire(ctx, fire); err != nil {
				// ErrScheduleNotFound: the schedule was deleted between detection
				// and settle — the fence worked, not an error.
				if !errors.Is(err, port.ErrScheduleNotFound) {
					diag.Log(ctx, port.LevelWarn, "scheduler: reconcile stale fire (claim) RecordFire failed",
						"schedule", name, "fire", id, "err", err.Error())
				}
				return
			}
			diag.Log(ctx, port.LevelInfo, "scheduler: reconciled stale fire (crash after claim)",
				"schedule", name, "fire", id)
			return
		}

		// Crash sub-case 2: a real in-flight fire (crash after session
		// creation, before RecordFire). Settle the terminal session snapshot
		// (cancelled, Interrupt-recoverable) and RecordFire a terminal
		// StopError fire so the schedule's in-flight state is cleared.
		sessID := sched.State.LastFireSessionID
		if sessID == "" {
			return // no prior fire at all — not a reconcile candidate.
		}
		// Settle the session snapshot: a crashed process left the session
		// StateRunning (the loop never persisted terminal). Cancel it to
		// StateCancelled (Interrupt-recoverable) and persist directly via the
		// SessionStore. GetSession loads the raw stored snapshot (NOT
		// LoadSession — that would Recover it to idle, which is wrong for a
		// fire we are about to record as lost; we want the terminal cancelled
		// snapshot a later resume can Interrupt). Service.Persist only writes
		// runs registered in the Service's in-memory registry, and a
		// restarted process has no such run, so it would no-op; the store is
		// the durable writer.
		if sessionStore != nil {
			sess, err := sessionStore.Load(ctx, sessID)
			if err != nil {
				// The session was never persisted (crash before the loop's
				// first save, or the store lost it). The fire record still
				// needs to settle: RecordFire a terminal StopError fire with
				// the real session id so the schedule's in-flight state is
				// cleared. Degrade honestly — a missing session is not an
				// error to fail the tick on.
				diag.Log(ctx, port.LevelWarn, "scheduler: reconcile stale fire (run) session load failed; settling fire record only",
					"schedule", name, "session", sessID, "err", err.Error())
			} else if !sess.State.IsTerminal() {
				// The snapshot is non-terminal (StateRunning, the crashed
				// process's in-flight state). Cancel it to StateCancelled
				// (Interrupt-recoverable) and persist so a later
				// loadAndReopen recovers it.
				if cerr := sess.Cancel(); cerr != nil {
					// Cancel refuses a terminal state — but we gated on
					// !IsTerminal above, so this is an unexpected transition
					// refusal. Degrade: leave the snapshot as-is and settle
					// the fire record. Best-effort WARN.
					diag.Log(ctx, port.LevelWarn, "scheduler: reconcile stale fire (run) session cancel refused",
						"schedule", name, "session", sessID, "state", string(sess.State), "err", cerr.Error())
				} else if serr := sessionStore.Save(ctx, sess); serr != nil {
					// A Save failure WARNs and never fails the reconcile —
					// the fire record still settles; the snapshot may be
					// recovered by a later tick or the loop's own save.
					diag.Log(ctx, port.LevelWarn, "scheduler: reconcile stale fire (run) session persist failed",
						"schedule", name, "session", sessID, "err", serr.Error())
				}
			}
		}

		// RecordFire a terminal StopError fire with the real session id so
		// the schedule's in-flight state (LastFireStartedAt/LastFireProgressAt/
		// FireDeadline) is cleared and LastFireSessionID points at the
		// settled session. Idempotent per fire id: a fire already terminal (a
		// late RecordFire from the crashed process) is a no-op.
		fireID := string(sessID) // the fire id IS the session id (decision #7).
		fire := port.ScheduleFire{
			ID:           fireID,
			ScheduleName: name,
			SessionID:    sessID,
			FiredAt:      sched.State.LastFireAt,
			StartedAt:    sched.State.LastFireStartedAt,
			Deadline:     sched.State.FireDeadline,
			Stop:         session.StopError,
			Err:          reconcileStaleFireMsgRun,
		}
		if fire.FiredAt.IsZero() {
			anchor := sched.State.LastFireStartedAt
			if anchor.IsZero() {
				anchor = now
			}
			fire.FiredAt = anchor
		}
		if err := store.RecordFire(ctx, fire); err != nil {
			if !errors.Is(err, port.ErrScheduleNotFound) {
				diag.Log(ctx, port.LevelWarn, "scheduler: reconcile stale fire (run) RecordFire failed",
					"schedule", name, "fire", fireID, "err", err.Error())
			}
			return
		}
		diag.Log(ctx, port.LevelInfo, "scheduler: reconciled stale fire (crash during run)",
			"schedule", name, "fire", fireID, "session", sessID)
	}
}
