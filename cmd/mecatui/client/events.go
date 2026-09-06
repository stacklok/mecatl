package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The durable-event-log replay surface (issue #245 Phase 2, cloud-native Phase
// 3a read-back): the read-only EventStream wrapper over a server-streaming
// StreamSessionEvents RPC, its ReadLoop entry, and the tea.Cmd constructor the
// ui's transcript viewer calls. As with the rest of this package, NO proto type
// leaks past this file — the ui drains tea.Msgs from the returned channel, the
// SAME fan-in (WaitForMsg) the live Converse stream uses.

// EventStream wraps one open StreamSessionEvents replay: a receive side ONLY
// (read-only, no Send side, unlike the bidi Converse Stream). The generated
// grpc.ServerStreamingClient[mecatlv1.Event] satisfies the EventRecver in
// production; tests supply a scripted fake.
type EventStream struct {
	recv EventRecver
	// bearerBacked is transport provenance, not credential material. Replay streams
	// intentionally leave it false so replay failures can never open auth recovery.
	bearerBacked bool
	// classifyAuth distinguishes live/Converse streams from durable replay. A
	// false bearer value means anonymous live transport when this is true, but
	// replay must not classify receive failures at all.
	classifyAuth bool
}

// NewEventStream wraps an EventRecver in an EventStream. Pass the generated
// grpc.ServerStreamingClient[mecatlv1.Event] from StreamSessionEvents in
// production; pass a fake EventRecver in tests.
func NewEventStream(recv EventRecver) *EventStream {
	return &EventStream{recv: recv}
}

func newAuthenticatedEventStream(recv EventRecver, bearerBacked bool) *EventStream {
	return newLiveEventStream(recv, bearerBacked)
}

func newLiveEventStream(recv EventRecver, bearerBacked bool) *EventStream {
	return &EventStream{recv: recv, bearerBacked: bearerBacked, classifyAuth: true}
}

// ReadLoop runs the receive loop on its OWN goroutine over the replay stream:
// it delegates to readEventLoop (the shared translation path), so the replay
// and a live Converse run project identically for the same event sequence. It
// pushes translated tea.Msgs onto out, then closes out when the replay ends. A
// clean EOF yields StreamClosedMsg; any other error yields StreamErrMsg. Run it
// off the Bubble Tea update goroutine; pass a cancellable context so the ui can
// tear it down when it leaves the transcript view.
func (s *EventStream) ReadLoop(ctx context.Context, out chan<- tea.Msg) {
	readEventLoop(ctx, s.recv.Recv, out, s.bearerBacked, s.classifyAuth)
}

// StreamSessionLive opens the LIVE per-session event stream (ADR 0075
// Scenario 5): the server pushes events including the three log-only kinds
// (approval/user_prompt/compaction.archive) as they occur — principally
// fire-result delivery notes for the active session. It wraps the returned
// server stream in an EventStream. The SAME projection path (EventToMsg →
// readEventLoop) means a live delivery and a replay produce the SAME
// DeliveryNoteMsg. A server with no live subscription bridge returns gRPC
// UNIMPLEMENTED → StreamErrMsg.
func (c *Client) StreamSessionLive(ctx context.Context, id string) (*EventStream, error) {
	stream, err := c.svc.StreamSessionLive(withSessionAffinity(ctx, id), &mecatlv1.StreamSessionLiveRequest{SessionId: id})
	if err != nil {
		return nil, fmt.Errorf("stream session live: %w", err)
	}
	return newAuthenticatedEventStream(stream, c.bearerBacked), nil
}

// LiveStreamCmd opens the live session event stream for session id synchronously,
// then runs ReadLoop on a goroutine. It returns the message channel and an
// idempotent teardown function.
func LiveStreamCmd(ctx context.Context, live LiveStreamer, id string) (ch chan tea.Msg, stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	ch = make(chan tea.Msg, 64)
	var once sync.Once
	stop = func() { once.Do(cancel) }

	es, err := live.StreamSessionLive(ctx, id)
	if err != nil {
		// Open errors are classified only for a live stream with authenticated
		// transport provenance; durable replay never opens auth recovery.
		reason, classified := AuthFailure(err, liveBearerBacked(live))
		go func() {
			defer close(ch)
			emit(ctx, ch, StreamErrMsg{Err: err, AuthReason: reason, Transient: !classified && TransientStreamErr(err)})
		}()
		return ch, stop
	}
	if p, ok := live.(liveAuthProvenance); ok {
		es.bearerBacked = p.BearerBackedStream()
	}
	es.classifyAuth = true
	go es.ReadLoop(ctx, ch)
	return ch, stop
}

// StreamSessionEvents opens the durable-event-log replay (cloud-native Phase 3a
// read-back) for session id and wraps the returned server stream in an
// EventStream. The replay yields *mecatlv1.Event directly (NO ConverseResponse
// envelope), and INCLUDES the three log-only kinds (approval/user_prompt/
// compaction.archive) — a transcript viewer wants the verdicts and user prompts;
// metadata-only by construction (gauntlet #7). An unknown id yields an EMPTY
// stream (absence is data) → a single StreamClosedMsg; a server with no durable
// EventLog returns gRPC UNIMPLEMENTED → a StreamErrMsg.
func (c *Client) StreamSessionEvents(ctx context.Context, id string) (*EventStream, error) {
	stream, err := c.svc.StreamSessionEvents(withSessionAffinity(ctx, id), &mecatlv1.StreamSessionEventsRequest{SessionId: id})
	if err != nil {
		return nil, fmt.Errorf("stream session events: %w", err)
	}
	return NewEventStream(stream), nil
}

// Live-feed reconnect + catch-up (issue #387). When the LIVE session event feed
// drops (a clean StreamClosedMsg or a StreamErrMsg on the live reader), the ui
// drives ReconnectLiveCmd: a bounded-exponential-backoff loop that, per attempt,
// (a) drains the durable catch-up via StreamSessionEvents (the SAME full replay —
// no new from_seq/log_seq; exactly-once is the ui's FireID-dedup job, NOT a
// server cursor) to recover delivery notes emitted during the gap, then (b)
// re-opens StreamSessionLive. The loop emits LiveReconnectingMsg{Attempt, Err} at
// the top of each attempt and LiveReconnectedMsg{} once the live stream reopens;
// the catch-up events (DeliveryNoteMsg/…) arrive as ordinary event msgs on the
// SAME channel. The loop stops when ctx is done (session switch / TUI exit) — the
// ui ties it to m.deps.Ctx and the live generation. The backoff itself lives in
// backoff.go (package-level vars so tests can shrink it).

// catchUpReplay drains the durable-event-log replay ONCE for session id via
// replayer. The durable log is consulted ONLY to recover DELIVERY NOTES emitted
// during the gap — the visible conversation (user prompts, assistant text, tool
// calls) is already on screen, so replaying those msg types into the live
// conversation would re-append the whole prior transcript on every reconnect
// (C1). Only DeliveryNoteMsg events are forwarded onto out (deduped by the ui's
// seenFireIDs); every other projected event is dropped here. The replay's own
// terminal StreamClosedMsg/StreamErrMsg is SWALLOWED — the reconnect loop owns
// its lifecycle markers (LiveReconnectingMsg/LiveReconnectedMsg), so a catch-up
// EOF must not be mistaken for the live feed closing. Returns the replay's
// terminal error (nil = clean EOF) so the loop can surface a catch-up failure
// distinctly from a live-reopen failure. Honours ctx: a cancelled ctx aborts
// the in-flight ReadLoop (its emit honours ctx) and this drain.
func catchUpReplay(ctx context.Context, replayer SessionReplayer, id string, out chan<- tea.Msg) error {
	es, err := replayer.StreamSessionEvents(ctx, id)
	if err != nil {
		return err
	}
	tmp := make(chan tea.Msg, 64)
	go es.ReadLoop(ctx, tmp)
	for m := range tmp {
		switch m.(type) {
		case StreamClosedMsg, StreamErrMsg:
			// Swallow the replay's terminal marker; the reconnect owns its own.
			continue
		case DeliveryNoteMsg, ResultMsg:
			// Delivery notes recover gap output. ResultMsg is forwarded only so the UI
			// can recover the latest durable failed-step retry eligibility; it must not replay
			// transcript cards or trigger automatic retry.
			if !emit(ctx, out, m) {
				return ctx.Err()
			}
		default:
			// Not a delivery note (a user prompt / assistant text / tool call /
			// turn marker from the already-visible transcript): drop it so the
			// full-log replay never re-appends the prior conversation.
			continue
		}
	}
	return nil
}

type liveAuthProvenance interface {
	BearerBackedStream() bool
}

func liveBearerBacked(live LiveStreamer) bool {
	p, ok := live.(liveAuthProvenance)
	return ok && p.BearerBackedStream()
}

// BearerBackedStream reports whether this client sends bearer credentials.
func (c *Client) BearerBackedStream() bool { return c != nil && c.bearerBacked }

// liveReconnectAttemptTimeout bounds one StreamSessionLive reopening attempt. It
// prevents a wedged gRPC transport from holding the reconnect loop forever. Tests
// temporarily shrink it to exercise the timeout path.
var liveReconnectAttemptTimeout = 10 * time.Second

// reconnectLiveLoop is the body of ReconnectLiveCmd: the bounded-backoff
// reconnect+catch-up loop. It emits LiveReconnectingMsg at the top of each
// attempt, drains the durable catch-up (forwarding its event msgs onto out), then
// re-opens StreamSessionLive as the connectivity probe. On a successful reopen
// it emits LiveReconnectedMsg and returns (the ui re-arms a FRESH live channel);
// on failure it records the error and backs off. The probe stream's ctx is the
// loop's ctx, so the ui's stop (cancel) cleans it up once the ui has re-armed.
func reconnectLiveLoop(ctx context.Context, live LiveStreamer, replayer SessionReplayer, id string, out chan<- tea.Msg, priorAttempt int) {
	defer close(out)
	attempt := priorAttempt
	var lastErr error
	for {
		attempt++
		// Backoff BEFORE each attempt after the first (H1): the first retry is
		// immediate (a single fast retry on a transient close is correct), but
		// subsequent attempts are gated so a feed that re-opens then instantly
		// closes cannot cycle re-arm→close→reopen with zero delay. A non-positive
		// base (reconnect disabled) stops the loop after the first attempt rather
		// than hot-spinning.
		if attempt > 1 {
			d := liveReconnectDelay(attempt)
			if d <= 0 {
				return
			}
			timer := time.NewTimer(d)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if !emit(ctx, out, LiveReconnectingMsg{Attempt: attempt, Err: lastErr}) {
			return
		}
		// (a) drain the durable catch-up (recover delivery notes from the gap).
		// Best-effort: a catch-up failure does not block the live reopen.
		if replayer != nil {
			_ = catchUpReplay(ctx, replayer, id, out)
		}
		// (b) Re-open the live feed as a bounded connectivity probe. The stream is
		// discarded because the UI re-arms a fresh live channel on success.
		attemptCtx, cancel := context.WithTimeout(ctx, liveReconnectAttemptTimeout)
		es, err := live.StreamSessionLive(attemptCtx, id)
		cancel()
		if err == nil {
			_ = es
			emit(ctx, out, LiveReconnectedMsg{})
			return
		}
		if reason, classified := AuthFailure(err, liveBearerBacked(live)); classified {
			// An authenticated feed cannot recover by retrying: hand the closed,
			// typed reason to the reducer and stop. Replay remains unclassified.
			emit(ctx, out, StreamErrMsg{Err: err, AuthReason: reason})
			return
		}
		lastErr = err
	}
}

// ReconnectLiveCmd opens the live-feed reconnect+catch-up loop for session id:
// it runs reconnectLiveLoop on a goroutine, pushing tea.Msgs (LiveReconnectingMsg
// / LiveReconnectedMsg / the catch-up event msgs) onto a buffered (64) channel the
// ui drains via WaitForMsg (the SAME fan-in the live and replay streams use). The
// loop stops when ctx is done. Returns the channel + an idempotent teardown that
// cancels the loop's ctx AND JOINS the loop goroutine, so a caller that mutates
// state the loop reads (e.g. a test shrinking the package-level backoff vars)
// cannot race the loop's final backoff read after stop. Mirrors
// LiveStreamCmd needs no join because its ReadLoop goroutine only reads the
// injected stream, not package-level test knobs. The ui
// stores the channel + stop, tags reads with the live-reconnect generation, and
// re-arms the live reader on LiveReconnectedMsg.
func ReconnectLiveCmd(ctx context.Context, live LiveStreamer, replayer SessionReplayer, id string) (ch chan tea.Msg, stop func()) {
	return reconnectLiveCmd(ctx, live, replayer, id, 0)
}

// ReconnectLiveCmdFromAttempt continues the per-session continuity attempt
// sequence after a successful probe/re-arm. The first-ever failure remains
// immediate; a later reader failure keeps its attempt number and backoff.
func ReconnectLiveCmdFromAttempt(ctx context.Context, live LiveStreamer, replayer SessionReplayer, id string, priorAttempt int) (ch chan tea.Msg, stop func()) {
	return reconnectLiveCmd(ctx, live, replayer, id, priorAttempt)
}

func reconnectLiveCmd(ctx context.Context, live LiveStreamer, replayer SessionReplayer, id string, priorAttempt int) (ch chan tea.Msg, stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	ch = make(chan tea.Msg, 64)
	done := make(chan struct{})
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done // join the loop goroutine so its final backoff read can't race a test restore
		})
	}
	go func() {
		defer close(done)
		reconnectLiveLoop(ctx, live, replayer, id, ch, priorAttempt)
	}()
	return ch, stop
}
