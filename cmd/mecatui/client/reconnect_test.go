package client

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeLiveReplayer is a scripted client.SessionReplayer for the reconnect
// catch-up tests: it returns a *EventStream over a FakeEventStream per call,
// recording each open. The script is reused for every call (a fresh EventStream
// over the SAME script) so a multi-attempt loop can drain the catch-up more than
// once. Mirrors fakeSessionReplayer.
type fakeLiveReplayer struct {
	mu     sync.Mutex
	script []*mecatlv1.Event
	err    error
	opens  int
	lastID string
}

func (f *fakeLiveReplayer) StreamSessionEvents(_ context.Context, id string) (*EventStream, error) {
	f.mu.Lock()
	f.opens++
	f.lastID = id
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return NewEventStream(NewFakeEventStream(f.script...)), nil
}

// fakeLiveStreamerReconnect is a scripted client.LiveStreamer for the reconnect
// tests: it fails the first `failN` StreamSessionLive calls, then succeeds with
// an empty stream. recording opens. This lets a test drive the reconnect loop
// through N failed attempts before a successful reopen.
type fakeLiveStreamerReconnect struct {
	mu      sync.Mutex
	failN   int // number of leading calls that error
	opens   int
	lastID  string
	succeed *FakeEventStream // stream handed back on a successful open (nil ⇒ empty)
}

func (f *fakeLiveStreamerReconnect) StreamSessionLive(_ context.Context, id string) (*EventStream, error) {
	f.mu.Lock()
	f.opens++
	f.lastID = id
	n := f.opens
	failN := f.failN
	f.mu.Unlock()
	if n <= failN {
		return nil, errors.New("live stream unavailable")
	}
	es := f.succeed
	if es == nil {
		es = NewFakeEventStream()
	}
	return NewEventStream(es), nil
}

// drainRecon collects every msg off ch until it closes, with a per-msg timeout
// so a stuck loop fails the test instead of hanging.
func drainRecon(t *testing.T, ch <-chan tea.Msg) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, m)
		case <-time.After(2 * time.Second):
			t.Fatalf("drainRecon timed out after %d msgs", len(out))
		}
	}
}

// deliveryEvent builds a proto user_prompt event carrying a fenced delivery note
// with the given schedule name + fire id, for the catch-up tests.
func deliveryEvent(schedule, fire string) *mecatlv1.Event {
	body := "[scheduled task " + schedule + " (fire " + fire + ") completed with stop reason: end_turn]\nout"
	return &mecatlv1.Event{
		Type:       "user_prompt",
		UserPrompt: &mecatlv1.UserPrompt{Text: fenceDelivery(body)},
	}
}

// restoreBackoff saves the current backoff knobs and returns a restore func;
// tests shrink the knobs (and set jitter to 0 for deterministic timing) and
// defer the restore so the package-level vars are reset for the next test.
func restoreBackoff(t *testing.T) func() {
	t.Helper()
	return RestoreBackoffForTest()
}

// TestReconnectLiveCmd_FiresOnCleanCloseAndError drives the reconnect loop with
// a live streamer that succeeds on the first reopen and a replayer whose catch-up
// is empty. It asserts: exactly one LiveReconnectingMsg (attempt 1) then a
// LiveReconnectedMsg; the live streamer was opened; the replayer catch-up was
// drained (the gap's deliveries recover). Backoff is shrunk to 1ms so the test
// is fast.
func TestReconnectLiveCmd_FiresOnCleanCloseAndError(t *testing.T) {
	defer restoreBackoff(t)()
	liveReconnectBaseBackoff = 1 * time.Millisecond
	liveReconnectJitterFrac = 0

	live := &fakeLiveStreamerReconnect{failN: 0} // succeeds immediately
	replayer := &fakeLiveReplayer{}

	ch, stop := ReconnectLiveCmd(context.Background(), live, replayer, "sess-rc")
	defer stop()
	msgs := drainRecon(t, ch)

	var gotReconnecting, gotReconnected bool
	for _, m := range msgs {
		switch m.(type) {
		case LiveReconnectingMsg:
			gotReconnecting = true
		case LiveReconnectedMsg:
			gotReconnected = true
		}
	}
	if !gotReconnecting {
		t.Errorf("expected a LiveReconnectingMsg, got: %#v", msgs)
	}
	if !gotReconnected {
		t.Errorf("expected a LiveReconnectedMsg, got: %#v", msgs)
	}
	if live.opens < 1 {
		t.Errorf("expected StreamSessionLive to be opened, opens=%d", live.opens)
	}
	if live.lastID != "sess-rc" {
		t.Errorf("live opened for id %q, want sess-rc", live.lastID)
	}
	if replayer.opens < 1 {
		t.Errorf("expected catch-up replay to be drained, opens=%d", replayer.opens)
	}
}

// TestReconnectLiveCmd_RetriesUntilSuccess asserts the loop retries with backoff
// until the live stream reopens: the streamer fails the first 3 opens, succeeds
// on the 4th. The loop emits 4 LiveReconnectingMsgs (attempts 1..4) then a
// LiveReconnectedMsg. The first LiveReconnectingMsg carries a nil Err (no prior
// failure); each subsequent one carries the prior attempt's error.
func TestReconnectLiveCmd_RetriesUntilSuccess(t *testing.T) {
	defer restoreBackoff(t)()
	liveReconnectBaseBackoff = 1 * time.Millisecond
	liveReconnectJitterFrac = 0

	live := &fakeLiveStreamerReconnect{failN: 3}
	replayer := &fakeLiveReplayer{}

	ch, stop := ReconnectLiveCmd(context.Background(), live, replayer, "sess-retry")
	defer stop()
	msgs := drainRecon(t, ch)

	var reconnecting []LiveReconnectingMsg
	var reconnected bool
	for _, m := range msgs {
		switch v := m.(type) {
		case LiveReconnectingMsg:
			reconnecting = append(reconnecting, v)
		case LiveReconnectedMsg:
			reconnected = true
		}
	}
	if !reconnected {
		t.Fatalf("expected LiveReconnectedMsg, got: %#v", msgs)
	}
	if len(reconnecting) != 4 {
		t.Fatalf("expected 4 LiveReconnectingMsgs (1 per attempt), got %d: %+v", len(reconnecting), reconnecting)
	}
	for i, r := range reconnecting {
		if r.Attempt != i+1 {
			t.Errorf("attempt[%d].Attempt = %d, want %d", i, r.Attempt, i+1)
		}
	}
	// First attempt: no prior error. Subsequent attempts: carry the prior error.
	if reconnecting[0].Err != nil {
		t.Errorf("first attempt Err = %v, want nil", reconnecting[0].Err)
	}
	for i := 1; i < len(reconnecting); i++ {
		if reconnecting[i].Err == nil {
			t.Errorf("attempt[%d] Err = nil, want the prior failure", i)
		}
	}
	if live.opens != 4 {
		t.Errorf("live opens = %d, want 4", live.opens)
	}
}

// TestReconnectLiveCmd_StopsOnCtxCancel asserts the loop stops when ctx is
// cancelled (session switch / TUI exit): the streamer always errors and the
// backoff is non-trivial, so the loop would otherwise keep retrying; cancelling
// ctx mid-backoff stops it and the channel closes without a LiveReconnectedMsg.
func TestReconnectLiveCmd_StopsOnCtxCancel(t *testing.T) {
	defer restoreBackoff(t)()
	liveReconnectBaseBackoff = 50 * time.Millisecond
	liveReconnectMaxBackoff = 50 * time.Millisecond
	liveReconnectJitterFrac = 0

	ctx, cancel := context.WithCancel(context.Background())
	live := &fakeLiveStreamerReconnect{failN: 1000} // always fails
	replayer := &fakeLiveReplayer{}

	ch, stop := ReconnectLiveCmd(ctx, live, replayer, "sess-cancel")
	// Let one attempt fail + backoff start, then cancel.
	<-time.After(20 * time.Millisecond)
	cancel()
	stop()

	// The channel must close; no LiveReconnectedMsg should arrive.
	var gotReconnected bool
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				if gotReconnected {
					t.Errorf("got an unexpected LiveReconnectedMsg after ctx cancel")
				}
				return
			}
			if _, ok := m.(LiveReconnectedMsg); ok {
				gotReconnected = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("channel did not close after ctx cancel")
		}
	}
}

// TestReconnectLiveCmd_CatchUpForwardsDeliveryEvents asserts the catch-up drains
// the durable replay and forwards its projected event msgs (DeliveryNoteMsg)
// onto the reconnect channel — so a delivery note emitted during the gap is
// recovered. The catch-up's terminal StreamClosedMsg is SWALLOWED (not
// forwarded): the reconnect owns its own lifecycle markers.
func TestReconnectLiveCmd_CatchUpForwardsDeliveryEvents(t *testing.T) {
	defer restoreBackoff(t)()
	liveReconnectBaseBackoff = 1 * time.Millisecond
	liveReconnectJitterFrac = 0

	catchUpEvents := []*mecatlv1.Event{deliveryEvent("nightly-sync", "sched--fire-gap")}
	live := &fakeLiveStreamerReconnect{failN: 0}
	replayer := &fakeLiveReplayer{script: catchUpEvents}

	ch, stop := ReconnectLiveCmd(context.Background(), live, replayer, "sess-catchup")
	defer stop()
	msgs := drainRecon(t, ch)

	var deliveryCount int
	var reconnecting, reconnected int
	var streamClosed, streamErr int
	for _, m := range msgs {
		switch v := m.(type) {
		case DeliveryNoteMsg:
			deliveryCount++
			if v.FireID != "sched--fire-gap" {
				t.Errorf("delivery FireID = %q, want sched--fire-gap", v.FireID)
			}
		case LiveReconnectingMsg:
			reconnecting++
		case LiveReconnectedMsg:
			reconnected++
		case StreamClosedMsg:
			streamClosed++
		case StreamErrMsg:
			streamErr++
		}
	}
	if deliveryCount != 1 {
		t.Errorf("expected 1 catch-up DeliveryNoteMsg, got %d", deliveryCount)
	}
	if reconnecting != 1 {
		t.Errorf("expected 1 LiveReconnectingMsg, got %d", reconnecting)
	}
	if reconnected != 1 {
		t.Errorf("expected 1 LiveReconnectedMsg, got %d", reconnected)
	}
	// The catch-up's terminal markers must be swallowed.
	if streamClosed != 0 || streamErr != 0 {
		t.Errorf("catch-up terminal leaked: StreamClosed=%d StreamErr=%d", streamClosed, streamErr)
	}
}

// TestReconnectLiveCmd_CatchUpOpenErrorStillReconnects asserts a catch-up RPC
// failure (no durable log / UNIMPLEMENTED) does NOT abort the live-reopen: the
// loop still re-opens the live stream and emits LiveReconnectedMsg. The catch-up
// is best-effort.
func TestReconnectLiveCmd_CatchUpOpenErrorStillReconnects(t *testing.T) {
	defer restoreBackoff(t)()
	liveReconnectBaseBackoff = 1 * time.Millisecond
	liveReconnectJitterFrac = 0

	live := &fakeLiveStreamerReconnect{failN: 0}
	replayer := &fakeLiveReplayer{err: errors.New("durable log unavailable")}

	ch, stop := ReconnectLiveCmd(context.Background(), live, replayer, "sess-noreplay")
	defer stop()
	msgs := drainRecon(t, ch)

	var reconnected bool
	for _, m := range msgs {
		if _, ok := m.(LiveReconnectedMsg); ok {
			reconnected = true
		}
	}
	if !reconnected {
		t.Errorf("expected LiveReconnectedMsg despite catch-up failure, got: %#v", msgs)
	}
}

// TestLiveReconnectDelay_BoundedAndIncreasing asserts the backoff grows
// monotonically up to the cap when jitter is disabled (deterministic), and that
// it never exceeds the cap. With jitter enabled it stays within [base, max].
func TestLiveReconnectDelay_BoundedAndIncreasing(t *testing.T) {
	defer restoreBackoff(t)()
	liveReconnectBaseBackoff = 10 * time.Millisecond
	liveReconnectMaxBackoff = 320 * time.Millisecond
	liveReconnectJitterFrac = 0

	// Deterministic (no jitter): strictly increasing up to the cap, then flat.
	var prev time.Duration
	for attempt := 1; attempt <= 20; attempt++ {
		d := liveReconnectDelay(attempt)
		if d <= 0 {
			t.Fatalf("attempt %d: non-positive delay %v", attempt, d)
		}
		if d > liveReconnectMaxBackoff {
			t.Errorf("attempt %d: delay %v exceeds cap %v", attempt, d, liveReconnectMaxBackoff)
		}
		if d < prev {
			t.Errorf("attempt %d: delay %v < prev %v (not monotonic)", attempt, d, prev)
		}
		prev = d
	}
	// Capped at max once saturated.
	if got := liveReconnectDelay(100); got != liveReconnectMaxBackoff {
		t.Errorf("saturated delay = %v, want cap %v", got, liveReconnectMaxBackoff)
	}

	// With jitter: every delay stays within [base, max].
	liveReconnectJitterFrac = 0.20
	for attempt := 1; attempt <= 50; attempt++ {
		d := liveReconnectDelay(attempt)
		if d < liveReconnectBaseBackoff {
			t.Errorf("jittered attempt %d: delay %v < base %v", attempt, d, liveReconnectBaseBackoff)
		}
		if d > liveReconnectMaxBackoff {
			t.Errorf("jittered attempt %d: delay %v > cap %v", attempt, d, liveReconnectMaxBackoff)
		}
	}
}

// TestLiveReconnectDelay_DisabledWhenBaseZero asserts a non-positive base
// disables the backoff (returns 0) — the loop then runs no sleep and relies on
// the ctx check to stop.
func TestLiveReconnectDelay_DisabledWhenBaseZero(t *testing.T) {
	defer restoreBackoff(t)()
	liveReconnectBaseBackoff = 0
	if d := liveReconnectDelay(1); d != 0 {
		t.Errorf("disabled backoff = %v, want 0", d)
	}
}

// TestReconnectLiveCmd_RegressionUnfencedPromptNotMisclassified is the regression
// guard: an ordinary un-fenced "[scheduled task …" user prompt in the catch-up
// replay is NOT misclassified as a delivery note (the fenced discriminator
// holds). It arrives as a UserPromptMsg on the reconnect channel.
func TestReconnectLiveCmd_RegressionUnfencedPromptNotMisclassified(t *testing.T) {
	defer restoreBackoff(t)()
	liveReconnectBaseBackoff = 1 * time.Millisecond
	liveReconnectJitterFrac = 0

	// An un-fenced prompt that merely starts with the delivery header prefix.
	unfenced := &mecatlv1.Event{
		Type:       "user_prompt",
		UserPrompt: &mecatlv1.UserPrompt{Text: "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nthis is a plain user prompt, not a delivery"},
	}
	live := &fakeLiveStreamerReconnect{failN: 0}
	replayer := &fakeLiveReplayer{script: []*mecatlv1.Event{unfenced}}

	ch, stop := ReconnectLiveCmd(context.Background(), live, replayer, "sess-regress")
	defer stop()
	msgs := drainRecon(t, ch)

	var delivery int
	var userPrompt int
	for _, m := range msgs {
		switch m.(type) {
		case DeliveryNoteMsg:
			delivery++
		case UserPromptMsg:
			userPrompt++
		}
	}
	if delivery != 0 {
		t.Errorf("un-fenced prompt misclassified as a delivery note: delivery=%d", delivery)
	}
	if userPrompt != 1 {
		t.Errorf("expected 1 UserPromptMsg, got %d", userPrompt)
	}
}
