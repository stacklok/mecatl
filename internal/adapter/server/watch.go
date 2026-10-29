package server

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// The watch delivery phases. They are OPEN STRINGS on the
// wire, not an enum — the same discipline the event `type`/`stop` fields carry —
// so adding a phase is a minor SDK release rather than a wire-compat event, and
// an older client decoding a new value gets a string it can pass through rather
// than a dead branch in an exhaustive switch.
const (
	// WatchPhaseReplay marks a record that was already durable when the watch
	// attached.
	WatchPhaseReplay = "replay"

	// WatchPhaseLive marks a record appended while the watch was following, and
	// the ONE phase-only frame that announces the boundary between the two.
	WatchPhaseLive = "live"

	// WatchPhaseGap marks a position where a durable append is KNOWN to have
	// failed.
	//
	// It is a PHASE rather than an event kind, and that is the whole point:
	// a gap is a fact about DELIVERY, not something that happened
	// in the run. Making it a session.Event would leak it into the event taxonomy,
	// the proto Event message, the kind-parity gate, and every consumer that folds
	// events into a session. TestGapAddsNoEventKind asserts that absence
	// structurally, because "just add an EvGap so clients can render it" is a
	// natural-sounding change that would silently relocate a delivery concern into
	// the domain.
	WatchPhaseGap = "gap"
)

// watchDeliveryBuffer bounds ONE watcher's undelivered envelopes.
//
// This is the required "bounded delivery state". It is
// deliberately generous enough that an ordinarily-busy client rides out a burst
// (a tool-heavy turn emits deltas far faster than a browser renders them) and
// deliberately finite, because the alternative to a bound is an unbounded
// per-watcher queue that a stalled client turns into a memory leak.
// It is a var, not a const, ONLY so the bounded-delivery tests can shrink it —
// proving termination against the production 512 would need 512 events and the
// full grace, which is a slow test that pins nothing extra. Production never
// mutates it.
var watchDeliveryBuffer = 512

// watchDeliveryGrace is how long a full delivery buffer waits for the consumer
// before the watch is terminated.
//
// A merely BACKLOGGED consumer absorbs its tail within the grace; a genuinely
// stuck one is terminated. The grace exists because a zero-tolerance
// non-blocking send would kill a client that was briefly busy, and the cost of a
// false termination (a reconnect and a re-read from the cursor) is paid by the
// client that did nothing wrong.
// A var for the same reason as watchDeliveryBuffer, and never mutated in
// production.
var watchDeliveryGrace = 5 * time.Second

// WatchEnvelope is ONE unit of watch delivery: what happened, where the client
// now is, and which phase of the watch it arrived in.
//
// Event is nil on a PHASE-ONLY frame. There are exactly two: the single
// replay→live transition marker, and every gap. Both are delivery facts with no
// event behind them, which is why the phase — not a synthetic event — carries
// them.
type WatchEnvelope struct {
	// Event is the recorded event, or nil on a phase-only frame.
	Event *session.Event

	// Cursor is the opaque resume token positioned AFTER this envelope.
	//
	// It is SCOPED TO THE run_id IT WAS ISSUED UNDER. A filtered watch advances
	// its internal position over records the filter dropped, so a cursor handed
	// back on a DIFFERENT filter — or on none — resumes past events that filter
	// would have delivered, silently. Resume with the same run_id, or start over
	// from the beginning.
	Cursor port.Cursor

	// Phase is one of the WatchPhase* values, as an open string.
	Phase string
}

// ErrWatchUnsupported means the configured durable EventLog does not implement
// port.CursorEventLog, so it cannot serve a positional resume or a follow.
//
// It is deliberately an ERROR rather than a degrade to "replay the whole
// transcript and stop". A client that asked to resume from a position and was
// handed everything from the beginning is a correctness problem dressed as a
// performance one: it would silently re-process events it had already acted on,
// and never learn that its cursor meant nothing.
var ErrWatchUnsupported = errors.New("server: the configured event log does not support cursors")

// ErrWatchLagging means a watch was terminated because its client fell behind
// the bounded delivery buffer for longer than the grace.
//
// TERMINATING is the point. Service.Subscribe, the
// pre-cursor live registry, DROPS events for a slow subscriber: the stream stays
// open and the client never learns it is missing data. A durable cursor exists
// precisely so that the honest alternative is available — end the stream, and let
// the client resume from the last cursor it received, losing nothing.
//
// It carries NO cursor, deliberately. The server's furthest-queued position is
// NOT the client's: envelopes still sitting in the delivery buffer were never
// received, so resuming from a server-side cursor would SKIP exactly the events
// the termination was supposed to protect. The client's own last-received
// envelope is the only correct resume point, and the client always has it.
var ErrWatchLagging = errors.New("server: watch terminated because the client fell behind the bounded delivery buffer")

// ErrActivityGap means a durable append failed while a watch was attached, so
// the watch's stream is known to be incomplete.
var ErrActivityGap = errors.New("server: a durable event-log append failed; this watch has a delivery gap")

// ActivityGapError is the process-local, GUARANTEED tier of the
// three-tier append-gap guarantee: when an append fails, every watcher in
// the failing process terminates with this error and its cursor never advances.
//
// Like ErrWatchLagging it carries no cursor, for the same reason: the client's
// last-received envelope is the resume point, and the server must not invent one
// that would skip the buffered tail.
//
// The guarantee this belongs to is DELIBERATELY WEAKER than an absolute, and the
// weakness is not an implementation gap. A failed append consumed no position,
// so it leaves nothing for a watcher in ANOTHER process to observe; the
// best-effort durable gap marker (AppendGap) covers the likely case of one
// rejected record, and a total backend outage plus process loss leaves a gap that
// is undetectable by construction. Do not restate this as an absolute.
//
// It carries NO description of the underlying failure, deliberately. The cause is
// a raw backend error — `dial tcp 10.0.0.5:6379: connect: connection refused`, a
// jsonlstore path — and this value reaches the client as a gRPC status message
// and an SSE `error` field. That is the same exposure GetSession already refuses
// under ownership enforcement, on the same reasoning: the store's error routinely
// embeds infrastructure detail a caller has no business reading. The cause is not
// lost — it goes to the durable gap marker (tier 1) and to the append-failure
// WARN the recorder already emits — so the operator keeps every byte of it and
// the client gets the stable `activity_gap` code, which is the whole of what it
// can act on.
type ActivityGapError struct{}

func (*ActivityGapError) Error() string { return ErrActivityGap.Error() }

// Unwrap lets errors.Is(err, ErrActivityGap) classify this through the shared
// error registry, so both transports report it identically.
func (*ActivityGapError) Unwrap() error { return ErrActivityGap }

// watchRegistration is one attached watcher, as the Service sees it.
//
// The Service knows only two things about a watcher: how to stop it, and whether
// a durable append failed underneath it. Everything else — its position, its
// filter, its buffer — belongs to the watch's own goroutine, so a failing
// appender walking this registry does the minimum possible work on the relay
// thread.
type watchRegistration struct {
	// cancel stops the watch's pump. Faulting a watcher cancels it and records
	// WHY, so the pump can turn a cancelled follow into the right terminal error
	// instead of a clean end.
	cancel context.CancelFunc

	mu     sync.Mutex
	gapped bool
}

// fault records that a durable append failed and stops the watch.
//
// It is called from the appending relay thread, so it must not block: a map walk
// plus a context cancel is the entire cost, and a broken log must never
// backpressure the live run it belongs to.
//
// It takes no reason: the cause belongs to the durable marker and the operator
// WARN, not to the client-facing terminal (see ActivityGapError).
func (r *watchRegistration) fault() {
	r.mu.Lock()
	r.gapped = true
	r.mu.Unlock()
	r.cancel()
}

// gap reports whether this watch was faulted.
func (r *watchRegistration) gap() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gapped
}

// registerWatch attaches a watcher for id and returns its registration.
//
// A registration made after Service.Close is born already cancelled: shutdown
// must not leave a pump running past the Service that owns it, and a watch that
// raced the close ends cleanly rather than following a log nobody is writing.
func (s *Service) registerWatch(id session.SessionID, cancel context.CancelFunc) *watchRegistration {
	reg := &watchRegistration{cancel: cancel}
	s.watchMu.Lock()
	if s.watchesClosed {
		s.watchMu.Unlock()
		cancel()
		return reg
	}
	if s.watches == nil {
		s.watches = make(map[session.SessionID]map[*watchRegistration]struct{})
	}
	if s.watches[id] == nil {
		s.watches[id] = make(map[*watchRegistration]struct{})
	}
	s.watches[id][reg] = struct{}{}
	s.watchMu.Unlock()
	return reg
}

// unregisterWatch detaches a watcher, dropping the session's bucket when it
// empties so a long-lived server does not accumulate one per session ever
// watched.
func (s *Service) unregisterWatch(id session.SessionID, reg *watchRegistration) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	bucket := s.watches[id]
	if bucket == nil {
		return
	}
	delete(bucket, reg)
	if len(bucket) == 0 {
		delete(s.watches, id)
	}
}

// faultWatchers terminates every watcher attached to id with a delivery gap.
//
// This is the append-gap guarantee's guaranteed, process-local tier. It runs on the
// appending thread and is therefore deliberately cheap.
func (s *Service) faultWatchers(id session.SessionID) {
	s.watchMu.Lock()
	regs := make([]*watchRegistration, 0, len(s.watches[id]))
	for reg := range s.watches[id] {
		regs = append(regs, reg)
	}
	s.watchMu.Unlock()
	// Faulting is a fan-out: EVERY watcher on the session terminates, not just the
	// one that happened to be first. The snapshot above is taken under the lock and
	// walked outside it, so a watcher unregistering mid-walk is faulted harmlessly
	// rather than deadlocking against watchMu.
	for _, reg := range regs {
		reg.fault()
	}
}

// closeWatches cancels every attached watcher at shutdown and refuses later
// attachments, so no pump goroutine outlives the Service.
//
// A shutdown cancel is a CLEAN end, not a gap: no append failed, so claiming one
// would be a lie the client would act on. The client's stream simply ends and it
// reconnects with its cursor.
func (s *Service) closeWatches() {
	s.watchMu.Lock()
	s.watchesClosed = true
	var regs []*watchRegistration
	for _, bucket := range s.watches {
		for reg := range bucket {
			regs = append(regs, reg)
		}
	}
	s.watches = make(map[session.SessionID]map[*watchRegistration]struct{})
	s.watchMu.Unlock()
	for _, reg := range regs {
		reg.cancel()
	}
}

// WatchSessionEvents is the DURABLE replay-then-follow read: it replays a
// session's log from after the given cursor, announces the transition, and
// follows the tail until the caller stops (issue #821).
//
// It is ONE operation on purpose. The two existing read paths cannot be composed
// into it without a hole: port.EventLog.Read is a complete durable replay with no
// position and no follow, and Service.Subscribe is a live in-memory registry with
// no history and no durability — so "read everything, then subscribe" silently
// loses whatever was appended between the two steps. A cursor closes that window,
// because the follow resumes from exactly where the replay stopped.
//
// Both transports (gRPC WatchSessionEvents, SSE GET /v1/sessions/{id}/watch)
// consume THIS method, which is what makes their envelope sequences identical
// rather than merely similar (AC7.3). Put transport framing in the handlers and
// delivery semantics here.
//
// The returned iterator yields at most one error, as its last item, per the
// port.EventLog convention. A caller that breaks out early releases the watch.
func (s *Service) WatchSessionEvents(ctx context.Context, id session.SessionID, after port.Cursor, runID string) (iter.Seq2[WatchEnvelope, error], error) {
	log, err := s.watchLog(ctx, id)
	if err != nil {
		return nil, err
	}
	return func(yield func(WatchEnvelope, error) bool) {
		// The pump's context is a child of the caller's, so a client disconnect
		// stops the follow, AND faultWatchers can stop it independently.
		pumpCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		reg := s.registerWatch(id, cancel)
		defer s.unregisterWatch(id, reg)

		// The bounded delivery buffer. Decoupling the log read from the wire write
		// is what keeps a slow client from holding a backend follow slot open (for
		// Redis, a pooled connection) and what makes "too slow" observable at all
		// — a synchronous write inside the read would just block, invisibly.
		out := make(chan WatchEnvelope, watchDeliveryBuffer)
		// term is SEPARATE from out and written BEFORE it closes, so a terminal
		// error is never itself queued behind the backlog it is reporting.
		term := make(chan error, 1)
		go pumpWatch(pumpCtx, log, id, after, runID, reg, out, term)

		for env := range out {
			if !yield(env, nil) {
				return
			}
		}
		// out is closed, so every buffered envelope has been delivered; only now
		// can a terminal error be surfaced without reordering it ahead of them.
		select {
		case err := <-term:
			yield(WatchEnvelope{}, err)
		default:
		}
	}, nil
}

// watchLog resolves the cursor-capable log for a watch, applying every
// precondition EAGERLY so a caller learns about a missing feature or a session it
// may not read as a plain error rather than as an empty stream.
func (s *Service) watchLog(ctx context.Context, id session.SessionID) (port.CursorEventLog, error) {
	if s.cfg.EventLog == nil {
		return nil, ErrNoEventLog
	}
	// A delegation child's transcript is not a caller-addressable stream. This is
	// safe TODAY by absence of data — every NewRunEventRecorder site passes a
	// top-level relay id, so a child has no durable log and a watch on one would
	// replay nothing — but "safe because the data happens not to exist" stops being
	// true the moment a per-child-observability feature records under child ids,
	// and then this becomes a direct child-transcript read with nothing in the way
	// (gauntlet #7). The guard makes the invariant enforced rather than emergent.
	if isDelegationChildSessionID(id) {
		return nil, fmt.Errorf("%w: %s is a delegation child session", ErrInvalidArgument, id)
	}
	log := s.cursorLog
	if log == nil {
		return nil, ErrWatchUnsupported
	}
	// The same ownership question GetSession answers, asked the same way — a
	// caller who may not read the session may not watch it either. The durable log
	// holds the whole transcript, so a watch is at least as revealing as a read.
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSession(ctx, id); err != nil {
			return nil, err
		}
	}
	return log, nil
}

// pumpWatch drains the durable log into the bounded delivery buffer.
//
// It reads in TWO phases rather than one Follow read, which is what makes the
// replay→live boundary exact. port.LogRecord.Live marks records that arrived
// after a read caught up, but only once such a record ARRIVES — on an idle
// session none ever does, and a client would wait forever to learn it was caught
// up. Splitting the read gives a boundary that does not depend on the run
// emitting anything, and it loses nothing: the second read resumes from the exact
// cursor the first stopped at, so an append landing between them is delivered by
// the follow rather than skipped.
func pumpWatch(
	ctx context.Context,
	log port.CursorEventLog,
	id session.SessionID,
	after port.Cursor,
	runID string,
	reg *watchRegistration,
	out chan<- WatchEnvelope,
	term chan<- error,
) {
	var terminal error
	// resumeFrom is the INTERNAL continuation position — the furthest record read
	// from the log, whether or not the run filter delivered it. It is NOT the
	// client's cursor (which only ever advances on an envelope the client
	// received), and it must advance over filtered-out records so the follow does
	// not re-read them.
	resumeFrom := after

	defer func() {
		if err := watchTerminal(reg.gap(), terminal); err != nil {
			select {
			case term <- err:
			default:
			}
		}
		close(out)
	}()

	deliver := func(env WatchEnvelope) bool {
		// Try-send first: while the buffer has room this is deterministic and
		// allocation-free, and the timer below is never armed.
		select {
		case out <- env:
			return true
		default:
		}
		t := time.NewTimer(watchDeliveryGrace)
		defer t.Stop()
		select {
		case out <- env:
			return true
		case <-ctx.Done():
			return false
		case <-t.C:
			terminal = ErrWatchLagging
			return false
		}
	}

	// PHASE 1 — replay everything already durable.
	for rec, err := range log.ReadAfter(ctx, id, after, port.ReadOptions{}) {
		if err != nil {
			terminal = err
			return
		}
		resumeFrom = rec.Cursor
		if env, ok := watchEnvelopeFor(rec, WatchPhaseReplay, runID); ok {
			if !deliver(env) {
				return
			}
		}
	}

	// The boundary. A phase-only frame, so a client can render the transcript and
	// switch to a live view without waiting for an event that may never arrive.
	if !deliver(WatchEnvelope{Cursor: resumeFrom, Phase: WatchPhaseLive}) {
		return
	}

	// PHASE 2 — follow the tail. A follow that ends because ctx was cancelled
	// yields no error (port.ReadOptions.Follow's contract): a clean detach must
	// not look like a fault in the logs.
	//
	// resumeFrom is deliberately NOT advanced in this loop: the follow's own
	// iterator owns the position from here, and nothing after this reads it. The
	// client's position is on the envelopes it received, which is the only cursor
	// that matters once the boundary has passed.
	for rec, err := range log.ReadAfter(ctx, id, resumeFrom, port.ReadOptions{Follow: true}) {
		if err != nil {
			terminal = err
			return
		}
		if env, ok := watchEnvelopeFor(rec, WatchPhaseLive, runID); ok {
			if !deliver(env) {
				return
			}
		}
	}
}

// watchTerminal decides a watch's terminal error from the two facts that can end
// it: whether a durable append failed underneath it, and whatever the pump itself
// recorded.
//
// A GAP OUTRANKS EVERY OTHER OUTCOME — a clean end and a lagging termination
// alike. The two are not interchangeable and the ordering is not a tie-break:
// after a lagging termination a client reconnects from its cursor and carries on
// believing its transcript is whole, which is exactly the false belief a KNOWN
// gap has to destroy. Lagging is recoverable and says so; a gap is not, and must
// not be masked merely because the delivery grace happened to expire first.
//
// It is a function rather than an inline defer because the interesting state —
// gapped AND already lagging — is reachable only through a race between the
// delivery grace expiring and the fault landing, so no end-to-end test can
// produce it reliably. Extracted, the precedence is exhaustively testable, which
// is the difference between a documented claim and an enforced one.
func watchTerminal(gapped bool, recorded error) error {
	if gapped {
		return &ActivityGapError{}
	}
	return recorded
}

// watchEnvelopeFor projects one durable log record into a delivery envelope,
// reporting whether the run filter admits it.
func watchEnvelopeFor(rec port.LogRecord, phase, runID string) (WatchEnvelope, bool) {
	if rec.Kind == port.LogRecordGap {
		// A gap is delivered WHATEVER the run filter says. A failed append left no
		// record behind, so there is nothing to attribute to a run — filtering it
		// would silently hide a real gap from exactly the client that asked to be
		// told about its run.
		return WatchEnvelope{Cursor: rec.Cursor, Phase: WatchPhaseGap}, true
	}
	if runID != "" && rec.Event.RunID != runID {
		return WatchEnvelope{}, false
	}
	ev := rec.Event
	return WatchEnvelope{Event: &ev, Cursor: rec.Cursor, Phase: phase}, true
}

// noteAppendGap runs the append-gap guarantee's two reachable tiers after a durable
// append failed, in order of reach.
//
// It is called from appendEvent — the one persistence chokepoint — and it never
// changes the caller's outcome: appendEvent still returns the original append
// error, its caller still WARNs once per run, and the run continues. A broken log
// must not break a live run.
//
// It adds NO diagnostics line of its own. The append failure is already reported
// (RunEventRecorder's sticky WARN; the manual-compaction WARN), a landed gap
// marker is owned by the gap PHASE its watchers observe, and the residual —
// a marker that could not land either — is an accepted residual rather than
// logged twice per incident.
func (s *Service) noteAppendGap(ctx context.Context, id session.SessionID, log port.CursorEventLog, cause error) {
	// The reason is a backend error string bound for a DURABLE RECORD, and it stops
	// there: ActivityGapError deliberately carries no prose, so this text never
	// reaches a client. Run it through the same UTF-8 repair every
	// producer-influenced string crossing into proto gets, because a gap marker
	// read back through ReadAfter is still bound for a protobuf string field.
	reason := valid(cause.Error())

	// TIER 1 — best-effort, CROSS-process. One gap marker, one attempt. If it
	// lands, every watcher everywhere learns of the gap deterministically; if it
	// does not, tier 2 still covers this process. Deliberately not retried: the
	// append it is reporting was not retried either.
	_, _ = log.AppendGap(ctx, id, reason)

	// TIER 2 — guaranteed, PROCESS-local.
	s.faultWatchers(id)
}
