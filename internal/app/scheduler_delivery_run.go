package app

import (
	"context"
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
		diag := svc.Diagnostics()
		if diag == nil {
			diag = port.NopDiagnostics{}
		}
		note := renderFireStarted(sched.Spec.Name, fire.ID)

		// Enqueue to the durable per-session queue (the session-scoped exactly-once
		// ledger). A nil queue (the no-delivery posture) is a no-op drop.
		if queue == nil {
			return
		}
		enq, err := queue.Enqueue(ctx, origin, note)
		if err != nil {
			diag.Log(ctx, port.LevelWarn, "delivery: enqueue failed (start notice stays pull-able)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "kind", "fire started", "err", err.Error())
			return
		}

		// State-aware delivery. Determine the origin's state via GetSession.
		originSess, err := svc.GetSession(ctx, origin)
		if err != nil {
			diag.Log(ctx, port.LevelWarn, "delivery: origin session not found (degrading to pull-only)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "kind", "fire started", "err", err.Error())
			return
		}
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
		run, err := svc.StartRunContent(ctx, origin, note, nil)
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
		diag := svc.Diagnostics()
		if diag == nil {
			diag = port.NopDiagnostics{}
		}
		// Render the note. The final text is extracted from the fire session's
		// conversation (the last assistant text); a fire with no assistant text
		// (an empty terminal) still renders a note carrying the stop reason.
		finalText := fireFinalText(svc, fire)
		note := renderFireDelivery(sched.Spec.Name, fire.ID, fire.Stop, finalText)

		// Enqueue to the durable per-session queue (the session-scoped exactly-once
		// ledger). A nil queue (the no-delivery posture) is a no-op drop — the
		// fire path treats a nil-error Enqueue as "recorded" and proceeds.
		if queue == nil {
			return // no delivery queue wired — the byte-identical no-delivery path
		}
		enq, err := queue.Enqueue(ctx, origin, note)
		if err != nil {
			// Enqueue is best-effort: a failure WARNs and never fails the fire.
			// The note is not recorded; the fire result stays pull-able.
			diag.Log(ctx, port.LevelWarn, "delivery: enqueue failed (fire result stays pull-able)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "err", err.Error())
			return
		}

		// State-aware delivery. Determine the origin's state via GetSession
		// (read-only snapshot load — NOT loadAndReopen, which would drive a
		// terminal state to idle prematurely for the awaiting/busy cases).
		originSess, err := svc.GetSession(ctx, origin)
		if err != nil {
			// Origin not found (deleted) — degrade to pull-only with a WARN. The
			// note stays pending in the queue (it will drain if the origin is ever
			// re-created with the same id, which is rare; otherwise it is bounded by
			// the backlog cap). The fire result stays pull-able.
			diag.Log(ctx, port.LevelWarn, "delivery: origin session not found (degrading to pull-only)",
				"schedule", sched.Spec.Name, "fire", fire.ID, "origin", string(origin), "err", err.Error())
			return
		}
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
		run, err := svc.StartRunContent(ctx, origin, note, nil)
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

// fireFinalText extracts the fire's last meaningful assistant text from its
// persisted session's conversation. A fire with no assistant text (an empty
// terminal — StopBudget/StopNoProgress/etc.) returns "" so renderFireDelivery
// renders a note carrying the stop reason (never a silent blank). A load
// failure returns "" (degrade to the stop-reason-only note).
func fireFinalText(svc *server.Service, fire port.ScheduleFire) string {
	if fire.SessionID == "" {
		return ""
	}
	sess, err := svc.GetSession(context.Background(), fire.SessionID)
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
