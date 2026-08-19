package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
)

// SteerOutcome is the closed-enum result of a steer inbox transition (enqueue
// or cancel). The engine is AUTHORITATIVE: the client cannot observe the exact
// drain moment across stream latency, so the outcome reports what actually
// happened to the steer rather than letting the client guess. It is an ENUM,
// not stacked booleans — AGENTS.md's "a third provenance ⇒ extract an enum"
// discipline (a bool pair cannot express accepted-vs-appended-vs-too_late).
type SteerOutcome string

const (
	// SteerAccepted means the steer was parked in the single pending slot (the
	// slot was empty). It will drain at the next turn boundary unless retracted
	// first.
	SteerAccepted SteerOutcome = "accepted"
	// SteerAppended means the steer found the pending slot OCCUPIED and was
	// MERGED into it: the pending bundle's text grew by "\n\n"+text (it still
	// drains as ONE bundle). Append is the DEFAULT for a second steer on the
	// occupied slot (round-3 rework; its outcome distinguished from accepted —
	// the new pending bundle — so the client can render "merged onto pending"
	// honestly). Replacing the pending bundle is the explicit cancel path
	// (steer_cancel, then a fresh steer); append and replace are distinct.
	SteerAppended SteerOutcome = "appended"
	// SteerRetracted means a cancel found a pending steer and retracted it; the
	// run drains nothing for it and the next turn sees no injected message.
	SteerRetracted SteerOutcome = "retracted"
	// SteerNonePending means a cancel found the slot EMPTY — there was nothing
	// to retract (no steer was pending, or the run is terminal).
	SteerNonePending SteerOutcome = "none_pending"
	// SteerTooLate means the steer arrived after the run went terminal (the
	// inbox is closed) — it is never parked and never silently drained into a
	// finished turn. The service layer promotes a too_late steer to a fresh
	// follow-up run; the engine only reports the outcome.
	SteerTooLate SteerOutcome = "too_late"
)

// steerInbox is a Run-scoped, in-memory, best-effort pending-steer slot (issue
// #512, steer-while-running). A STEER is an operator-supplied instruction
// injected mid-run that the loop drains at the next turn boundary and records
// as an ordinary harness-authored user continuation (recordContinuation), so it
// replays to the model like any user turn and flows through compaction /
// session.ValidateToolPairing / ADR-0038 rehydration unchanged.
//
// The slot is SINGLE and append-default: at most one steer BUNDLE is pending at
// a time. A second enqueue on the occupied slot APPENDS to the pending bundle
// (SteerAppended — its text merges in with "\n\n", and it still drains as ONE
// bundle); a cancel RETRACTS the pending one; the boundary drain COMMITS it.
// Every transition reports a SteerOutcome (the enum) and is safe under
// concurrent access — the run goroutine drains while a wire handler enqueues.
// (Reject-on-full and supersede were tried and dropped — append is the default.)
//
// The inbox is IN-MEMORY and best-effort: a pending (un-drained) steer lives
// only on the Run and is LOST with it — on a crash, a Run.Cancel, or a session
// Abandon. It is never persisted to the SessionStore / snapshot, so it does NOT
// survive a restart (the honest contract: only a steer that reached a turn
// boundary and was recorded into durable history survives, as ordinary
// conversation). This mirrors the background-notice / delivery-drain posture:
// the loop records, the relay persists; the loop itself stays storage-agnostic.
//
// The state is a mutex-guarded {closed, pending, has} triple: EVERY transition
// is ONE critical section over the whole triple, so the non-atomic
// observe-then-replace window (a receive between two selects, a done-check
// torn from the send) is impossible by construction — there is no second step
// to interleave a drain or a close into. Drain does NOT close the inbox — a
// steer enqueued after one boundary's drain but before the next is accepted
// for the following turn; only run-terminal closes it.
type steerInbox struct {
	mu sync.Mutex
	// closed flips at run terminal; once closed, enqueue is too_late and
	// cancel is none_pending.
	closed bool
	// pending parks the single pending steer (valid only when has is true).
	pending string
	// has records whether pending is occupied (the single-slot bit).
	has bool
}

// newSteerInbox mints an armed (open, empty-slot) inbox.
func newSteerInbox() *steerInbox {
	return &steerInbox{}
}

// EnqueueSteer is the wire-facing steer entry point: a live run's Service
// routes an operator steer here. It reports the authoritative SteerOutcome
// (accepted/appended/too_late) — never an error for the ordinary too-late
// race (that outcome is what the Service promotes on).
func (r *Run) EnqueueSteer(text string) (SteerOutcome, error) {
	return r.enqueueSteer(text)
}

// CancelSteer is the wire-facing steer-cancel entry point: a live run's
// Service routes an operator steer_cancel here. It retracts the PENDING
// (un-drained) steer and reports the authoritative SteerOutcome
// (retracted/none_pending) — a steer that already drained at a turn boundary
// is ordinary recorded history and cannot be retracted (the cancel reports
// none_pending then: the drain won).
func (r *Run) CancelSteer() (SteerOutcome, error) {
	return r.cancelSteer()
}

// enqueueSteer parks a steer for draining at the next turn boundary. It is
// safe to call from any goroutine while the run is in-flight (the wire-facing
// caller runs on a different goroutine than the loop). It returns the
// authoritative SteerOutcome: SteerAccepted (empty slot), SteerAppended (the
// slot is occupied — merge into the pending bundle), or SteerTooLate (the run
// is terminal / steer not live). A nil inbox (EnableSteer off — the byte-
// identical no-steer posture) reports SteerTooLate.
//
// The text is repaired to valid UTF-8 BEFORE it enters the inbox (the
// two-layer UTF-8 rule's semantic-repair layer, issue #402): steer text is
// operator prose, always safe to repair (no byte-exact exception), and
// repairing at ingress keeps recorded history == EvSteer echo == model-view
// byte-identical downstream. ONE critical section: the closed/full check and
// the park are indivisible.
func (r *Run) enqueueSteer(text string) (SteerOutcome, error) {
	if r.steer == nil {
		return SteerTooLate, nil
	}
	repaired := session.ToValidUTF8(text)
	r.steer.mu.Lock()
	defer r.steer.mu.Unlock()
	if r.steer.closed {
		return SteerTooLate, nil
	}
	if r.steer.has {
		// Append-default (round-3): merge into the pending bundle with a blank-line
		// separator; it still drains as ONE bundle. (Was reject-on-full; dropped.)
		r.steer.pending = r.steer.pending + "\n\n" + repaired
		return SteerAppended, nil
	}
	r.steer.pending, r.steer.has = repaired, true
	return SteerAccepted, nil
}

// cancelSteer retracts the pending steer (if any). It is safe to call from any
// goroutine. It returns SteerRetracted when a pending steer was retracted, or
// SteerNonePending when the slot was empty / the run is terminal (nothing to
// retract). A nil inbox reports SteerNonePending. ONE critical section:
// take+clear.
func (r *Run) cancelSteer() (SteerOutcome, error) {
	if r.steer == nil {
		return SteerNonePending, nil
	}
	r.steer.mu.Lock()
	defer r.steer.mu.Unlock()
	if !r.steer.has {
		return SteerNonePending, nil
	}
	r.steer.pending, r.steer.has = "", false
	return SteerRetracted, nil
}

// drainSteer atomically takes the pending steer (if any), returning its text
// and whether one was parked. The inbox stays OPEN after a drain — a steer
// enqueued after this boundary's drain but before the next is accepted for the
// following turn (only run-terminal closes the inbox). A nil inbox is the
// no-op (steer disabled). ONE critical section: take+clear.
func (r *Run) drainSteer() (string, bool) {
	if r.steer == nil {
		return "", false
	}
	r.steer.mu.Lock()
	defer r.steer.mu.Unlock()
	if !r.steer.has {
		return "", false
	}
	text := r.steer.pending
	r.steer.pending, r.steer.has = "", false
	return text, true
}

// hasSteer reports whether a steer is currently PARKED in the inbox. The
// clean-exit defer (finishTurnNoTools) consults it before a clean terminal, so
// a parked steer defers the exit until it drains (the run extends; never-drop
// holds engine-internally). A nil inbox reports false.
func (r *Run) hasSteer() bool {
	if r.steer == nil {
		return false
	}
	r.steer.mu.Lock()
	defer r.steer.mu.Unlock()
	return r.steer.has
}

// closeSteer marks the inbox closed at run terminal, so a post-terminal
// enqueue reports too_late and a post-terminal cancel reports none_pending. It
// is idempotent and does NOT drain: the terminate paths drain-then-close (a
// parked steer is recorded into durable history first), so closeSteer itself
// never swallows a parked steer.
func (r *Run) closeSteer() {
	if r.steer == nil {
		return
	}
	r.steer.mu.Lock()
	defer r.steer.mu.Unlock()
	r.steer.closed = true
}

// drainPendingSteer is the Step 2a steer-drain sibling of injectBackgroundNotice
// / drainPendingDelivery: it takes any pending operator steer off the run's
// inbox and records it as an ordinary harness-authored user continuation
// (recordContinuation — provider-legal at a turn boundary, where history always
// ends on a user prompt / tool result / nudge, never inside a tool_use pair),
// then persists (e.save) so the recorded steer is durable across resume/reload
// independent of how the run later ends.
//
// It runs at the SAME seam as the background notice and delivery drain (Step
// 2a, BEFORE BeginTurn and BEFORE the preTurnTerminal stop checks), so a steer
// drained at a boundary where the turn-limit / token-budget brake also trips is
// STILL recorded into durable history (the run terminates StopMaxTurns /
// StopBudget normally, and the recorded steer is addressed by the NEXT run) —
// it is never silently dropped and never bypasses BeginTurn's bound. The drain
// is ordered with the other Step 2a seams so the steer lands after any settled
// tool results and after the harness notices, keeping history provider-legal.
//
// On a successful record it emits EvSteer carrying the COMMITTED text — the
// authoritative echo the client renders (the engine is the sole authority on
// which version won the slot). The echo is sequenced BEFORE the turn it
// feeds (it emits ahead of BeginTurn's EvTurnStart), so a client renders the
// committed steer ahead of the model turn that consumed it.
//
// A nil inbox (EnableSteer off — the composition knob) is the strict no-op:
// an empty/disabled inbox changes nothing. An empty pending slot (steer
// enabled but unused) is likewise a no-op — the common case costs one mutex'd
// read. A record fault ends the run StopError, mirroring the delivery drain
// (a recorded-then-lost steer must not silently vanish; the operator sees the
// fault).
func (e *Engine) drainPendingSteer(ctx context.Context, r *Run, sess *session.Session) error {
	text, ok := r.drainSteer()
	if !ok {
		return nil
	}
	return e.commitSteer(ctx, r, sess, text)
}

// closeSteerDrained is the terminate-path steer hook: it drains any PARKED
// steer into durable history FIRST (recording it exactly like the Step 2a
// drain, so the never-drop contract holds engine-internally), THEN closes the
// inbox. A parked steer is never closed-unconsumed on ANY terminate path
// (clean or otherwise) — a steer captured here is addressed by the next run.
// The record is provider-legal at a terminal: history there always ends on a
// settled tool result / user message, never inside a tool_use pair. A record
// fault terminates the run StopError (the recorded-then-lost steer must not
// silently vanish). A nil inbox / empty slot is the strict no-op.
func (e *Engine) closeSteerDrained(ctx context.Context, r *Run, sess *session.Session) {
	if r.steer == nil {
		return
	}
	text, ok := r.drainSteer()
	if ok {
		if err := e.commitSteer(ctx, r, sess, text); err != nil {
			e.terminate(ctx, r, sess, session.StopError, "", session.Usage{},
				fmt.Errorf("agent: record steer on terminal close: %w", err), false)
			return
		}
	}
	r.closeSteer()
}

// commitSteer records one drained steer text as an ordinary harness-authored
// user continuation and emits the authoritative EvSteer echo + persists. It is
// the shared commit of the Step 2a drain and the terminal close-drain — the
// two paths can never drift on record/echo/save ordering.
//
// Recorded at a turn boundary, before the upcoming BeginTurn (or at the
// terminal), so it belongs to the turn about to start (Counters.Turns is the
// 0-based index of it). recordContinuation records it AND emits the log-only
// EvUserPrompt so the durable log / eventsource.Fold captures this user turn
// like every other. The EvSteer echo is emitted AFTER the record succeeds (a
// recorded-then-unrecorded steer must not echo) and BEFORE the upcoming
// turn.start, so the echo precedes the turn it feeds on the wire.
func (e *Engine) commitSteer(ctx context.Context, r *Run, sess *session.Session, text string) error {
	if err := e.recordContinuation(r, sess, sess.Counters.Turns, text); err != nil {
		return fmt.Errorf("agent: record steer: %w", err)
	}
	e.emit(r, session.Event{Type: session.EvSteer, Turn: sess.Counters.Turns,
		Steer: &session.SteerPayload{Text: text}})
	e.save(ctx, r, sess)
	return nil
}
