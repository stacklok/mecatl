package app

import (
	"context"
	"errors"
	"strings"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// deliverFireStarted is the composition-injected "started" notice driver (issue
// #386, Phase 4a): right after RecordFireStart, route a fenced-untrusted harness
// note carrying ONLY the schedule name + fire/session id back into the fire's
// origin conversation. It mirrors deliverFireResult: it renders the start note
// (renderFireStarted — NO model-authored content, none exists at start) and
// drives it through the SAME state-aware enqueue+drive path, so the start note
// and the terminal note are distinct ledger entries (Enqueue mints a fresh seq
// per call) and exactly-once rides the SAME delivery-queue ledger. A nil queue
// is the byte-identical no-start-notice path.
func deliverFireStarted(svc *server.Service, queue port.DeliveryQueue) func(ctx context.Context, sched port.Schedule, fire port.ScheduleFire) {
	return func(ctx context.Context, sched port.Schedule, fire port.ScheduleFire) {
		origin := sched.Spec.OriginSessionID
		if origin == "" {
			return // no origin → no start notice (mirrors deliverFireResult)
		}
		// A nil queue is the byte-identical no-start-notice path and needs no
		// owner lookup.
		if queue == nil {
			return
		}
		diag := svc.Diagnostics()
		if diag == nil {
			diag = port.NopDiagnostics{}
		}
		var enq port.DeliveryNote
		var note string
		ownerCtx, originSess, ok, err := authorizeScheduleDeliveryOrigin(ctx, svc, sched, diag, func(_ context.Context, _ *session.Session) error {
			note = renderFireStarted(server.PresentScheduleName(sched), fire.ID)
			var enqueueErr error
			enq, enqueueErr = queue.Enqueue(ctx, origin, note)
			return enqueueErr
		})
		if !ok {
			return
		}
		if errors.Is(err, errScheduleDeliveryOriginMissing) {
			diag.Log(ctx, port.LevelWarn, "delivery: origin session not found (degrading to pull-only)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "kind", "fire started", "err", err.Error())
			return
		}
		if err != nil {
			diag.Log(ctx, port.LevelWarn, "delivery: enqueue failed (start notice stays pull-able)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "kind", "fire started", "err", err.Error())
			return
		}

		// State-aware delivery. The origin was loaded and authorized before the
		// enqueue; use that snapshot only to select enqueue-only versus drive.
		if isNonDeliverableOrigin(origin) {
			diag.Log(ctx, port.LevelWarn, "delivery: origin is a child/sched-- session (degrading to pull-only)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "kind", "fire started")
			return
		}
		if svc.IsLive(origin) || originSess.State == session.StateAwaiting {
			// Enqueued; the loop drains it. Done.
			return
		}
		// idle/completed/cancelled/failed: drive a delivery run.
		run, err := svc.StartRunContent(ownerCtx, origin, note, nil)
		if err != nil {
			diag.Log(ctx, port.LevelWarn, "delivery: drive run failed (start notice stays pull-able)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "kind", "fire started", "err", err.Error())
			return
		}
		for ev := range run.Events() {
			svc.PublishSessionEvent(origin, ev)
		}
		svc.FinishRun(origin, run)
		if err := queue.MarkDelivered(ctx, origin, enq.Seq); err != nil {
			diag.Log(ctx, port.LevelWarn, "delivery: mark-delivered after drive failed (note may double-drain)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "kind", "fire started", "seq", enq.Seq, "err", err.Error())
		}
	}
}

// deliverFireResult is the composition-injected ADR-0075 fire-result delivery
// driver: after a fire of a schedule with a non-empty OriginSessionID reaches
// its terminal EvResult AND RecordFire has persisted the fire record, it renders
// the fire's outcome as a fenced-untrusted harness note (renderFireDelivery),
// enqueues it to the durable per-session DeliveryQueue, and drives a delivery
// run into an idle/completed/cancelled/failed origin via the EXISTING
// StartRunContent → loadAndReopen funnel (reopens-if-completed /
// interrupts-if-cancelled / recovers-if-failed). Delivery is DECOUPLED from the
// fire's success: a delivery error WARNs and NEVER fails the fire (the fire is
// already recorded; the result stays pull-able via ListFires).
//
// State-aware handling (ADR 0075 decision #3):
//   - idle/completed/cancelled/failed origin: enqueue + drive StartRunContent
//     (the note is the prompt; loadAndReopen recovers the terminal state). The
//     enqueued note is marked delivered BEFORE the drive so the loop's Step 2a
//     drain does not re-record it (the drive's recordPrompt is the SOLE
//     recording). A drive failure WARNs (the note is lost from the queue —
//     best-effort; the fire result stays pull-able).
//   - awaiting origin: NOT driven past its pending ask — enqueue only; the note
//     is drained at the resumeFromAwaiting boundary (the next run-entry drains
//     it at Step 2a).
//   - busy origin (run in flight): enqueue only; the loop drains it at the
//     origin's next turn boundary (Step 2a, BEFORE BeginTurn), exactly-once via
//     the session-scoped ledger.
//   - deleted / collected-child / `sched--` origin: degrade to pull-only with a
//     WARN; never fails the fire, never a delivery loop; the result stays
//     pull-able via ListFires.
//
// Trust: the note is fenced-untrusted (renderFireDelivery); delivery into an
// origin with a STRICTER posture does not loosen it (claims of approval inside
// the untrusted fence are void — the origin's own policy still applies).
func deliverFireResult(svc *server.Service, queue port.DeliveryQueue) func(ctx context.Context, sched port.Schedule, fire port.ScheduleFire) {
	return func(ctx context.Context, sched port.Schedule, fire port.ScheduleFire) {
		origin := sched.Spec.OriginSessionID
		if origin == "" {
			return // no delivery — the pre-ADR-0075 pull-only posture
		}
		// A nil queue is the byte-identical no-delivery path and needs no owner
		// lookup.
		if queue == nil {
			return
		}
		diag := svc.Diagnostics()
		if diag == nil {
			diag = port.NopDiagnostics{}
		}
		var enq port.DeliveryNote
		var note string
		ownerCtx, originSess, ok, err := authorizeScheduleDeliveryOrigin(ctx, svc, sched, diag, func(ownerCtx context.Context, _ *session.Session) error {
			// Render only after the origin's authoritative under-lock authorization.
			finalText := fireFinalText(ownerCtx, svc, fire)
			note = renderFireDelivery(server.PresentScheduleName(sched), fire.ID, fire.Stop, finalText)
			var enqueueErr error
			enq, enqueueErr = queue.Enqueue(ctx, origin, note)
			return enqueueErr
		})
		if !ok {
			return
		}
		if errors.Is(err, errScheduleDeliveryOriginMissing) {
			diag.Log(ctx, port.LevelWarn, "delivery: origin session not found (degrading to pull-only)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "err", err.Error())
			return
		}
		if err != nil {
			diag.Log(ctx, port.LevelWarn, "delivery: enqueue failed (fire result stays pull-able)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "err", err.Error())
			return
		}

		// State-aware delivery uses the snapshot loaded and authorized before the
		// enqueue (read-only load — NOT loadAndReopen, which would drive a terminal
		// state to idle prematurely for the awaiting/busy cases).
		// A child/`sched--` origin is short-lived and reaped by GC, OR is another
		// fire's session. Delivering into it would be a delivery loop (a fire
		// delivering into another fire's chat). Degrade to pull-only with a WARN.
		if isNonDeliverableOrigin(origin) {
			diag.Log(ctx, port.LevelWarn, "delivery: origin is a child/sched-- session (degrading to pull-only)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin))
			return
		}
		// A BUSY origin (run in flight) or an AWAITING origin must NOT be driven
		// past its pending ask / collide with its in-flight run. The note is
		// already enqueued; the loop's Step 2a drain records it at the origin's
		// next turn boundary (busy) or the resumeFromAwaiting boundary (awaiting).
		if svc.IsLive(origin) || originSess.State == session.StateAwaiting {
			// Enqueued; the loop drains it. Done.
			return
		}
		// idle/completed/cancelled/failed: drive a delivery run via the EXISTING
		// StartRunContent → loadAndReopen funnel. The note IS the prompt (recorded
		// via recordPrompt as ordinary harness-framed user history). loadAndReopen
		// reopens-if-completed / interrupts-if-cancelled / recovers-if-failed.
		//
		// The note is NOT pre-marked-delivered: it stays PENDING in the queue while
		// the drive runs, and the drive's own recordPrompt does NOT consult the
		// queue (it is a user prompt, not the Step 2a drain), so there is exactly
		// ONE recording — the drive's. MarkDelivered runs AFTER a successful drive
		// (below), closing the TOCTOU where a concurrent user-initiated run's Step
		// 2a drain could record the still-pending note AND the drive then record it
		// again. If that race fires (a user run slips between IsLive and the drive's
		// runEntryMu acquire), the user run's drain records the note and the drive's
		// StartRunContent then blocks on runEntryMu; when the user run completes the
		// drive proceeds and records the note — a possible double-record, bounded
		// and benign (both records are fenced data; the origin policy gates both).
		// Not pre-marking keeps the common path single-recorded. A drive failure
		// WARNs and never fails the fire; the note stays pending and drains on a
		// later run-entry (the durable queue is the recovery).
		run, err := svc.StartRunContent(ownerCtx, origin, note, nil)
		if err != nil {
			diag.Log(ctx, port.LevelWarn, "delivery: drive run failed (fire result stays pull-able)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "err", err.Error())
			return
		}
		// Drain the delivery run to channel-close (not first EvResult) so the
		// drain blocks until the run goroutine's deferred close(r.events) fires
		// — which is after body() returns, after save() has persisted the
		// terminal session snapshot. Stopping at EvResult races the save() call
		// that follows emitResult in terminateComplete (engine/agent/loop.go
		// ~2202-2203), so a caller's immediate GetSession can see a stale,
		// pre-persist snapshot. Draining to close guarantees the snapshot is
		// settled. Publish each event to the origin session's live subscription
		// (ADR 0075 decision #5) so a connected embedded mecatui renders the
		// delivery card live (Wave 2); the durable log records the tail regardless.
		for ev := range run.Events() {
			svc.PublishSessionEvent(origin, ev)
		}
		svc.FinishRun(origin, run)
		// The drive recorded the note (recordPrompt). Mark it delivered so the
		// loop's Step 2a drain (on THIS or a later run) does not re-record it —
		// the session-scoped exactly-once ledger. A mark failure WARNs (the worst
		// case is a bounded double-record on a later drain); it never fails the fire.
		if err := queue.MarkDelivered(ctx, origin, enq.Seq); err != nil {
			diag.Log(ctx, port.LevelWarn, "delivery: mark-delivered after drive failed (note may double-drain)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "seq", enq.Seq, "err", err.Error())
		}
	}
}

var errScheduleDeliveryOriginMissing = errors.New("schedule delivery origin missing")

// authorizeScheduleDeliveryOrigin reconstructs the schedule owner's caller
// context and proves that the stored origin belongs to that owner immediately
// before effect runs under the origin's run-entry lock. Failure is deliberately
// silent: missing, ownerless, and foreign origins are indistinguishable at this
// asynchronous boundary. The outer scheduler context remains in use for queue
// bookkeeping and diagnostics; ownerCtx is used only for caller-owned session
// operations.
func authorizeScheduleDeliveryOrigin(
	ctx context.Context,
	svc *server.Service,
	sched port.Schedule,
	diag port.Diagnostics,
	effect func(context.Context, *session.Session) error,
) (context.Context, *session.Session, bool, error) {
	// Preserve the no-verifier path byte-for-byte: render and enqueue first, then
	// inspect the origin for state-aware delivery. The stronger ordering below is
	// required only when ownership is active.
	if !svc.OwnershipEnforced() {
		ownerCtx := schedulerOwnerContext(ctx, sched.Spec.Owner)
		if err := effect(ownerCtx, nil); err != nil {
			return ownerCtx, nil, true, err
		}
		originSess, err := svc.GetSession(ownerCtx, sched.Spec.OriginSessionID)
		if err != nil {
			return ownerCtx, nil, true, errors.Join(errScheduleDeliveryOriginMissing, err)
		}
		return ownerCtx, originSess, true, nil
	}
	// An ownerless persisted schedule must not inherit the outer scheduler system
	// identity when ownership is active. That is an OIDC-cutover casualty rather
	// than an attack, and it is permanent for that schedule, so the operator is
	// told why it stopped delivering.
	if sched.Spec.Owner == nil {
		// Target withheld: naming the schedule or origin would correlate a record a
		// caller may not know exists. An operator enumerates ownerless schedules by
		// name through GetStorageHealth's cutover inventory (AC4.6), which is
		// management-authorized; this line only has to make the RATE visible.
		diag.Log(ctx, port.LevelWarn, "delivery: ownerless schedule cannot deliver under ownership enforcement (target withheld; see the ownerless cutover inventory)")
		return nil, nil, false, nil
	}
	ownerCtx := schedulerOwnerContext(ctx, sched.Spec.Owner)
	authorized := false
	var originSess *session.Session
	_, err := svc.WithAuthorizedSession(ownerCtx, sched.Spec.OriginSessionID, func(sess *session.Session) error {
		authorized = true
		originSess = sess
		return effect(ownerCtx, sess)
	})
	if !authorized {
		// Missing, ownerless, and foreign origins stay ONE answer to the caller.
		// The operator still gets a line: unlike GetSession there is no untrusted
		// caller at this boundary — the scheduler is the caller, and the origin id
		// came from the schedule's own spec, which validateScheduleOrigin
		// owner-checked at create time. Silence here made a deleted origin
		// indistinguishable from a healthy schedule that simply never fired.
		// Target withheld for the same reason: a foreign origin would correlate one
		// owner's schedule with another owner's session id.
		diag.Log(ctx, port.LevelWarn, "delivery: origin not resolvable for the schedule owner (degrading to pull-only; target withheld)")
		return nil, nil, false, nil
	}
	return ownerCtx, originSess, true, err
}

// fireFinalText extracts the fire's last meaningful assistant text from its
// persisted session's conversation. A fire with no assistant text (an empty
// terminal — StopBudget/StopNoProgress/etc.) returns "" so renderFireDelivery
// renders a note carrying the stop reason (never a silent blank). A load
// failure returns "" (degrade to the stop-reason-only note).
func fireFinalText(ctx context.Context, svc *server.Service, fire port.ScheduleFire) string {
	if fire.SessionID == "" {
		return ""
	}
	sess, err := svc.GetSession(ctx, fire.SessionID)
	if err != nil || sess == nil || sess.Conversation == nil {
		return ""
	}
	msgs := sess.Conversation.Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == session.RoleAssistant && strings.TrimSpace(m.Text) != "" {
			return m.Text
		}
	}
	return ""
}

// isNonDeliverableOrigin reports whether the origin session id is a child
// (subagent-/parallel-/team-) or a `sched--` fire session — short-lived / reaped
// by GC, or another fire's chat. Delivering into such an origin would be a
// delivery loop or a dead-end, so the fire path degrades to pull-only with a
// WARN. The prefixes are the single source (agent.SubagentSessionPrefix etc.);
// the `sched--` prefix is the fire-session family (newFireID).
func isNonDeliverableOrigin(id session.SessionID) bool {
	s := string(id)
	if s == "" {
		return true
	}
	return strings.HasPrefix(s, agent.SubagentSessionPrefix) ||
		strings.HasPrefix(s, agent.ParallelSessionPrefix) ||
		strings.HasPrefix(s, agent.TeamSessionPrefix) ||
		strings.HasPrefix(s, "sched--")
}
