package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// blockingEventStream is a client.EventRecver that BLOCKS on Recv until its ctx
// is cancelled, then returns ctx.Err(). It stands in for a live session event
// stream that stays open (a connected push feed) so the reconnect tests' re-armed
// live feed does NOT immediately close and re-trigger a reconnect loop. The
// reconnect loop's connectivity probe ALSO uses it: a blocking successful open
// never returns (the loop emits LiveReconnectedMsg and returns without reading
// it, cancelling its ctx on the ui's stop).
type blockingEventStream struct {
	ctx context.Context
}

func (b *blockingEventStream) Recv() (*mecatlv1.Event, error) {
	<-b.ctx.Done()
	return nil, b.ctx.Err()
}

var _ client.EventRecver = (*blockingEventStream)(nil)

// fencedDeliveryText builds the canonical fenced-untrusted delivery note for the
// given schedule name + fire id, mirroring renderFireDelivery's output shape (the
// client's deliverNoteFrom detects the note via this exact fenced shape). Used
// instead of the hardcoded deliveryProvenanceText so the reconnect tests can
// vary the fire id for dedup assertions.
func fencedDeliveryText(schedule, fire string) string {
	body := "[scheduled task " + schedule + " (fire " + fire + ") completed with stop reason: end_turn]\nfire result text"
	return "<<<UNTRUSTED\n" + body + "\n<<<UNTRUSTED\n"
}

// deliveryEv builds a proto user_prompt event carrying the fenced delivery note
// for the given schedule + fire, for the catch-up / live delivery tests.
func deliveryEv(schedule, fire string) *mecatlv1.Event {
	return &mecatlv1.Event{
		Type:       "user_prompt",
		UserPrompt: &mecatlv1.UserPrompt{Text: fencedDeliveryText(schedule, fire)},
	}
}

// reconnectLiveStreamer is a scripted client.LiveStreamer for the reconnect
// tests. The FIRST StreamSessionLive call (the ui's initial arm) returns an
// empty stream that closes immediately (clean EOF) so the live feed drops and
// triggers the reconnect loop. Subsequent calls (the reconnect probe + the
// re-arm) fail the first `failN` of them, then succeed with a BLOCKING stream
// (a live feed that stays open so the re-arm does not immediately re-drop). It
// records opens + last id.
type reconnectLiveStreamer struct {
	mu      sync.Mutex
	failN   int
	opens   atomic.Int32
	lastID  string
	succCtx context.Context // ctx bound to the blocking successful stream
}

func (f *reconnectLiveStreamer) StreamSessionLive(_ context.Context, id string) (*client.EventStream, error) {
	f.mu.Lock()
	f.lastID = id
	f.mu.Unlock()
	n := f.opens.Add(1)
	if n == 1 {
		// The initial arm: an empty stream that closes immediately (clean EOF)
		// → triggers the reconnect loop.
		return client.NewEventStream(client.NewFakeEventStream()), nil
	}
	// Probe / re-arm opens: fail the first `failN` of these, then block.
	if int(n-1) <= f.failN {
		return nil, errors.New("live stream unavailable")
	}
	return client.NewEventStream(&blockingEventStream{ctx: f.succCtx}), nil
}

// newReconnectModel builds a connected idle Model with a fake LiveStreamer (the
// first arm opens a stream that closes immediately to trigger the reconnect)
// AND a fake SessionReplayer (the catch-up). The deps.Ctx is cancellable so the
// test can tear down the blocking streams at the end (no goroutine leak). The
// live feed is armed; the caller drains the close → reconnect → reconnected via
// feedReconnect.
//
//nolint:revive // context-as-argument: t *testing.T must come first per Go convention
func newReconnectModel(t *testing.T, ctx context.Context, fl client.LiveStreamer, fr client.SessionReplayer) Model {
	t.Helper()
	conv := &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: client.Capabilities{}}
	deps := Deps{
		Session:     conv,
		Conv:        conv,
		LiveStream:  fl,
		Replayer:    fr,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         ctx,
		NoAltScreen: true,
	}
	m := newTestModelFromDeps(deps)
	m = applyAll(
		m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-rc-0001", Capabilities: client.Capabilities{}},
	)
	// Arm the live feed so liveCh/liveGen/liveArmed are populated. Do NOT feed
	// the returned waitLiveCmd here — feedReconnect drives the close → reconnect
	// → reconnected flow with bounded timeouts (the re-arm's waitLiveCmd blocks
	// forever on the blocking successful stream).
	m.armLiveFeed()
	return m
}

// runCmdTimeout runs a tea.Cmd with a deadline and returns its msg (nil on
// timeout). The reconnect loop's reads block on channels fed by goroutines, so
// a 2s bound is ample; the final re-arm's waitLiveCmd blocks forever (a blocking
// stream) — feedReconnect never feeds that one.
func runCmdTimeout(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	type result struct{ msg tea.Msg }
	done := make(chan result, 1)
	go func() { done <- result{cmd()} }()
	select {
	case r := <-done:
		return r.msg
	case <-time.After(2 * time.Second):
		t.Fatalf("runCmdTimeout: cmd blocked past 2s")
		return nil
	}
}

// joinReconnectForCleanup returns a deferable that disarms the reconnect loop +
// live feed, JOINING the reconnect goroutine (ReconnectLiveCmd's stop joins).
// Tests defer it so the reconnect loop — which reads the package-level backoff
// knobs — is fully stopped before the deferred backoff-knob restore writes them
// (no data race under -race). Safe to call when nothing is armed (idempotent).
func joinReconnectForCleanup(m *Model) func() {
	return func() {
		// Save the reconnect channel before disarming (disarmReconnect nils it).
		ch := m.liveReconCh
		(*m).disarmReconnect()
		(*m).disarmLiveFeed()
		// Join the reconnect goroutine: drain the channel until closed so the
		// goroutine is fully done before the caller restores the package-level
		// backoff knobs (no data race under -race). The goroutine exits on ctx
		// cancel (from disarmReconnect's stop) and closes the channel.
		if ch != nil {
			for range ch {
			}
		}
	}
}

// feedReconnect drives the live-feed close → reconnect → reconnected flow,
// stopping once LiveReconnectedMsg is observed (it does NOT feed the re-arm's
// blocking waitLiveCmd). It returns the resulting Model and whether reconnected
// was observed. It first drains the live feed's initial close/error (the first
// waitLiveCmd), which triggers startReconnect, then drains the reconnect loop.
func feedReconnect(t *testing.T, m Model) (Model, bool) {
	t.Helper()
	var reconnected bool
	// First, drain the live feed's initial close/error (the arm's waitLiveCmd).
	if cmd := m.waitLiveCmd(); cmd != nil {
		msg := runCmdTimeout(t, cmd)
		if msg == nil {
			return m, false
		}
		mm, next := m.Update(msg)
		m = mm.(Model)
		if next != nil {
			// next is the startReconnect waitReconnectCmd; begin draining it below
			// by feeding it as the first reconnect cmd.
			msg = runCmdTimeout(t, next)
			if msg == nil {
				return m, false
			}
			mm, next = m.Update(msg)
			m = mm.(Model)
			if rm, ok := msg.(reconnectMsg); ok {
				if _, ok := rm.msg.(client.LiveReconnectedMsg); ok {
					reconnected = true
				}
			}
			// If reconnected, the next cmd is the re-arm (blocking) — drop it.
			if reconnected {
				return m, true
			}
			// Continue draining the reconnect loop.
			cmd = next
			for cmd != nil {
				msg = runCmdTimeout(t, cmd)
				if msg == nil {
					break
				}
				mm, next = m.Update(msg)
				m = mm.(Model)
				if rm, ok := msg.(reconnectMsg); ok {
					if _, ok := rm.msg.(client.LiveReconnectedMsg); ok {
						reconnected = true
					}
				}
				if reconnected {
					return m, true // drop the blocking re-arm cmd
				}
				cmd = next
			}
		}
	}
	return m, reconnected
}

// TestReconnectUI_TriggerOnStreamCloseAndError asserts the ui drives the
// reconnect loop when the live feed drops (a clean StreamClosedMsg here — the
// fakeLiveStreamer's empty stream EOFs immediately), recovers a delivery note
// from the durable catch-up, clears the degraded state on LiveReconnectedMsg,
// and re-arms the live feed. The catch-up delivery renders exactly once.
func TestReconnectUI_ReplayedAuthorizationRestoresActionableCard(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fl := &reconnectLiveStreamer{succCtx: ctx}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream(&mecatlv1.Event{Type: "authorization.required", Authorization: &mecatlv1.Authorization{AuthorizationId: "authorization:replayed", CallId: "call-1", Status: "pending", DisplayName: "GitHub"}})}
	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	var reconnected bool
	m, reconnected = feedReconnect(t, m)
	if !reconnected {
		t.Fatal("reconnect did not complete")
	}
	if m.phase != phaseAuthorizing || m.authorization.authorizationID != "authorization:replayed" {
		t.Fatalf("reconnect reducer did not restore authorization card: phase=%v state=%+v", m.phase, m.authorization)
	}
	view := stripANSIstr(m.View().Content)
	for _, action := range []string{"Complete connection", "Copy Link", "checked automatically", "Cancel"} {
		if !strings.Contains(view, action) {
			t.Fatalf("replayed authorization card missing %q: %s", action, view)
		}
	}
}

func TestReconnectUI_CompletedAuthorizationLifecycleStaysIdle(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fl := &reconnectLiveStreamer{succCtx: ctx}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream(
		&mecatlv1.Event{Type: "authorization.required", Authorization: &mecatlv1.Authorization{AuthorizationId: "authorization:complete", CallId: "call-1", Status: "pending", DisplayName: "GitHub"}},
		&mecatlv1.Event{Type: "authorization.resolved", Authorization: &mecatlv1.Authorization{AuthorizationId: "authorization:complete", CallId: "call-1", Status: "cancelled", DisplayName: "GitHub"}},
	)}
	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	m, reconnected := feedReconnect(t, m)
	if !reconnected {
		t.Fatal("reconnect did not complete")
	}
	if m.phase != phaseIdle || m.authorization.authorizationID != "" {
		t.Fatalf("completed replay left phantom authorization: phase=%v state=%+v", m.phase, m.authorization)
	}
	if strings.Contains(stripANSIstr(m.View().Content), "MCP authorization required") {
		t.Fatal("completed replay rendered an authorization card")
	}
}

func TestReconnectUI_TriggerOnStreamCloseAndError(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The first live open (arm) yields an empty stream → StreamClosedMsg →
	// reconnect. The reconnect loop's probe open fails once (failN=1) then
	// succeeds with a blocking stream; the re-arm ALSO opens a blocking stream
	// (stays open, no second reconnect).
	fl := &reconnectLiveStreamer{failN: 1, succCtx: ctx}

	// Catch-up carries one delivery note emitted during the gap.
	catchUpEvents := []*mecatlv1.Event{deliveryEv("nightly-sync", "sched--fire-gap")}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream(catchUpEvents...)}

	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	// Drain the live feed's immediate close → startReconnect → reconnect loop.
	m, reconnected := feedReconnect(t, m)
	if !reconnected {
		t.Fatal("expected LiveReconnectedMsg")
	}

	// The catch-up delivery renders exactly once.
	if len(m.conv.testBlocks()) != 1 {
		t.Fatalf("expected 1 delivery block, got %d: %+v", len(m.conv.testBlocks()), m.conv.testBlocks())
	}
	if m.conv.testBlocks()[0].kind != blockDelivery {
		t.Errorf("block kind = %v, want blockDelivery", m.conv.testBlocks()[0].kind)
	}
	if m.conv.testBlocks()[0].deliveryFireID != "sched--fire-gap" {
		t.Errorf("fire id = %q, want sched--fire-gap", m.conv.testBlocks()[0].deliveryFireID)
	}

	// Degraded state cleared.
	if m.liveReconnecting {
		t.Errorf("liveReconnecting should be cleared after LiveReconnectedMsg")
	}

	// seenFireIDs recorded the catch-up delivery.
	if _, ok := m.seenFireIDs["sched--fire-gap"]; !ok {
		t.Errorf("seenFireIDs missing sched--fire-gap: %+v", m.seenFireIDs)
	}

	// Live feed re-armed for the session.
	if m.liveArmed != "sess-rc-0001" || m.liveCh == nil {
		t.Errorf("live feed should be re-armed, liveArmed=%q liveCh=%v", m.liveArmed, m.liveCh)
	}
	// Reconnect channel torn down.
	if m.liveReconCh != nil {
		t.Errorf("reconnect channel should be torn down after LiveReconnectedMsg")
	}

	// The live streamer was opened: once for the initial arm, once for the
	// reconnect probe (failed), once for the probe (succeeded), once for the
	// re-arm. At least 3 opens (arm + 1 fail + 1 success); the re-arm adds 1.
	opens := int(fl.opens.Load())
	if opens < 3 {
		t.Errorf("live opens = %d, want >= 3 (arm + probe fail + probe success)", opens)
	}
}

// TestReconnectUI_DedupAcrossCatchUpAndLive asserts exactly-once: a delivery
// note that arrives BOTH via the durable catch-up AND the reopened live feed
// renders EXACTLY ONCE (deduped by FireID). The live feed delivers the same
// fire id; addDelivery is skipped on the second arrival.
func TestReconnectUI_DedupAcrossCatchUpAndLive(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fl := &reconnectLiveStreamer{failN: 0, succCtx: ctx} // probe succeeds first try

	// Catch-up carries one delivery note (fire-dup).
	catchUpEvents := []*mecatlv1.Event{deliveryEv("nightly-sync", "fire-dup")}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream(catchUpEvents...)}

	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	m, reconnected := feedReconnect(t, m)
	if !reconnected {
		t.Fatal("expected LiveReconnectedMsg")
	}

	// The catch-up delivery rendered once.
	if len(m.conv.testBlocks()) != 1 {
		t.Fatalf("expected 1 delivery block after catch-up, got %d", len(m.conv.testBlocks()))
	}

	// Now simulate the SAME delivery arriving on the reopened LIVE feed.
	dupMsg := client.DeliveryNoteMsg{ScheduleName: "nightly-sync", FireID: "fire-dup", Text: fencedDeliveryText("nightly-sync", "fire-dup")}
	mm, _ := m.updateLiveMsg(liveMsg{gen: m.liveGen, msg: dupMsg})
	m = mm.(Model)

	// Exactly-once: still 1 block (the live delivery was deduped by FireID).
	if len(m.conv.testBlocks()) != 1 {
		t.Errorf("expected 1 delivery block after live dup, got %d (FireID dedup failed)", len(m.conv.testBlocks()))
	}
	if _, ok := m.seenFireIDs["fire-dup"]; !ok {
		t.Errorf("seenFireIDs missing fire-dup: %+v", m.seenFireIDs)
	}
}

// TestReconnectUI_DifferentFireIDsBothRender asserts two deliveries with
// DIFFERENT fire ids both render (the dedup is per-fire-id, not a blanket
// single-delivery gate).
func TestReconnectUI_DifferentFireIDsBothRender(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fl := &reconnectLiveStreamer{failN: 0, succCtx: ctx}
	catchUp := []*mecatlv1.Event{
		deliveryEv("nightly-sync", "fire-A"),
		deliveryEv("nightly-sync", "fire-B"),
	}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream(catchUp...)}

	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	m, reconnected := feedReconnect(t, m)
	if !reconnected {
		t.Fatal("expected LiveReconnectedMsg")
	}
	if len(m.conv.testBlocks()) != 2 {
		t.Errorf("expected 2 delivery blocks (fire-A + fire-B), got %d", len(m.conv.testBlocks()))
	}
}

// TestReconnectUI_CatchUpDoesNotDuplicateTranscript is the regression for the
// spec-review finding: the catch-up replay is a FULL historical scan (no
// cursor), so it replays turn starts, deltas, tool calls, results, user prompts
// — everything — not just delivery notes. updateReconnectMsg must reduce ONLY
// DeliveryNoteMsg from the replay and DROP every other event, or a reconnect
// would re-render the entire transcript into the LIVE conversation (m.conv),
// duplicating turns the operator already saw. This test seeds the catch-up with
// a turn_start + assistant delta + a plain user prompt + a result alongside one
// delivery note, and asserts only the delivery note lands in m.conv.
func TestReconnectUI_CatchUpDoesNotDuplicateTranscript(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fl := &reconnectLiveStreamer{failN: 0, succCtx: ctx}
	// A full-history replay: turn/delta/tool/prompt/result events PLUS one delivery
	// note. The Event struct is flat (Type + scalar Turn/Text + payload pointers);
	// EventToMsg keys off the dot-separated Type strings.
	catchUp := []*mecatlv1.Event{
		{Type: "turn.start", Turn: 1},
		{Type: "message.delta", Turn: 1, Text: "earlier answer that must not re-render"},
		{Type: "tool.call", ToolCall: &mecatlv1.ToolCall{Name: "Read"}},
		{Type: "user_prompt", UserPrompt: &mecatlv1.UserPrompt{Text: "a genuine earlier user prompt"}},
		{Type: "result", Result: &mecatlv1.Result{Stop: "end_turn"}},
		deliveryEv("nightly-sync", "fire-only-me"),
	}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream(catchUp...)}

	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	m, reconnected := feedReconnect(t, m)
	if !reconnected {
		t.Fatal("expected LiveReconnectedMsg")
	}

	// Only the delivery note landed; the replayed turn/delta/prompt/result events
	// were dropped, NOT re-rendered into the live conversation.
	if len(m.conv.testBlocks()) != 1 {
		t.Fatalf("expected exactly 1 block (the delivery note) — the replay re-rendered history; got %d blocks: %+v", len(m.conv.testBlocks()), m.conv.testBlocks())
	}
	if m.conv.testBlocks()[0].kind != blockDelivery || m.conv.testBlocks()[0].deliveryFireID != "fire-only-me" {
		t.Errorf("block = kind %v fire %q, want the delivery note fire-only-me", m.conv.testBlocks()[0].kind, m.conv.testBlocks()[0].deliveryFireID)
	}
}

// triggerReconnect drains the live feed's initial close/error (the arm's
// waitLiveCmd, which yields StreamErrMsg/StreamClosedMsg and triggers
// startReconnect), then feeds ONE reconnect msg (the first LiveReconnectingMsg)
// so the model enters the degraded state. It returns the resulting Model. Used
// by the tests that assert the degraded state / session-switch teardown without
// driving the loop to a LiveReconnectedMsg.
func triggerReconnect(t *testing.T, m Model) Model {
	t.Helper()
	// Drain the live feed's close/error → startReconnect.
	if cmd := m.waitLiveCmd(); cmd != nil {
		msg := runCmdTimeout(t, cmd)
		if msg != nil {
			mm, _ := m.Update(msg)
			m = mm.(Model)
		}
	}
	// Drain one reconnect msg (the first LiveReconnectingMsg). startReconnect
	// returned waitReconnectCmd as the Update's next cmd; re-derive it.
	if cmd := m.waitReconnectCmd(); cmd != nil {
		msg := runCmdTimeout(t, cmd)
		if msg != nil {
			mm, _ := m.Update(msg)
			m = mm.(Model)
		}
	}
	return m
}

// TestReconnectUI_StopsOnSessionSwitch asserts the reconnect loop stops when
// the session switches: a stale reconnect msg (gen mismatch) is dropped WITHOUT
// triggering a reconnect for the old session, and disarmReconnect clears the
// reconnect state.
func TestADR_0096_StaleReconnectAfterSessionSwitchCannotRearmOldSession(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Live streamer: the initial arm fails (so the live feed errors → reconnect),
	// then the probe always fails (the loop keeps retrying). The session switch
	// mid-reconnect must stop it.
	fl := &reconnectLiveStreamer{failN: 1000, succCtx: ctx}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}

	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	m = triggerReconnect(t, m)
	if !m.liveReconnecting {
		t.Fatal("expected liveReconnecting after the live feed dropped")
	}
	reconGen := m.liveReconGen
	if m.liveReconCh == nil {
		t.Fatal("expected reconnect channel to be armed")
	}

	// Simulate a session switch: resetSession (the single seam for session-
	// derived teardown) tears down BOTH the live feed AND the reconnect loop,
	// clears the degraded state, and bumps liveReconGen.
	m = m.resetSession()
	if m.liveReconCh != nil {
		t.Errorf("reconnect channel should be torn down on session switch")
	}
	if m.liveReconnecting {
		t.Errorf("liveReconnecting should clear on session switch (resetSession clears it)")
	}
	// A real session replacement arms a fresh reader. A stale reconnect success
	// from the old generation must not re-arm or mutate that replacement.
	m.sessionID = "sess-fresh-0002"
	if cmd := m.armLiveFeed(); cmd == nil || m.liveArmed != "sess-fresh-0002" {
		t.Fatalf("fresh session live reader was not armed: armed=%q cmd=%v", m.liveArmed, cmd)
	}
	opensBefore := fl.opens.Load()
	stale := reconnectMsg{gen: reconGen, msg: client.LiveReconnectedMsg{}}
	mm, c := m.updateReconnectMsg(stale)
	m = mm.(Model)
	if c != nil || m.sessionID != "sess-fresh-0002" || m.liveArmed != "sess-fresh-0002" || fl.opens.Load() != opensBefore {
		t.Fatalf("stale reconnect mutated/rearmed fresh session: cmd=%v session=%q armed=%q opens=%d want=%d", c, m.sessionID, m.liveArmed, fl.opens.Load(), opensBefore)
	}
}

// TestReconnectUI_NoDuplicateConcurrentReconnect asserts the no-duplicate-
// concurrent-subscriptions invariant: if a reconnect is already in flight,
// startReconnect is a no-op (returns nil, does not open a second loop).
func TestReconnectUI_NoDuplicateConcurrentReconnect(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fl := &reconnectLiveStreamer{failN: 1000, succCtx: ctx}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}

	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	m = triggerReconnect(t, m)
	if m.liveReconCh == nil {
		t.Fatal("expected a reconnect loop to be armed")
	}
	firstGen := m.liveReconGen
	firstCh := m.liveReconCh

	// A second startReconnect (e.g. from a second live-feed close) must be a
	// no-op: same channel, same gen.
	cmd2 := (&m).startReconnect(nil)
	if cmd2 != nil {
		t.Errorf("second startReconnect should be a no-op (reconnect in flight), got cmd=%v", cmd2)
	}
	if m.liveReconGen != firstGen || m.liveReconCh != firstCh {
		t.Errorf("second startReconnect opened a duplicate reconnect loop")
	}
}

// TestReconnectUI_DegradedFooter asserts the footer shows the degraded state
// while liveReconnecting and clears it after reconnect.
func TestReconnectUI_DegradedFooter(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fl := &reconnectLiveStreamer{failN: 1000, succCtx: ctx} // always fails → stays reconnecting
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}

	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	m = triggerReconnect(t, m)
	if !m.liveReconnecting {
		t.Fatal("expected liveReconnecting")
	}
	footer := stripANSIstr(m.renderFooter())
	if !containsStr(footer, "live feed reconnecting") {
		t.Errorf("footer should show degraded state, got: %q", footer)
	}
	if !containsStr(footer, "attempt 1") {
		t.Errorf("footer should show attempt 1, got: %q", footer)
	}
}

// TestReconnectUI_RegressionUnfencedPromptNotDelivery asserts an ordinary
// un-fenced "[scheduled task …" user prompt in the catch-up is NOT rendered as
// a delivery card (the fenced discriminator holds in the reconnect catch-up
// path too). It renders as a user prompt block.
func TestReconnectUI_RegressionUnfencedPromptNotDelivery(t *testing.T) {
	defer restoreBackoffClient(t)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fl := &reconnectLiveStreamer{failN: 0, succCtx: ctx}
	// An un-fenced prompt that merely starts with the delivery header prefix.
	unfenced := &mecatlv1.Event{
		Type:       "user_prompt",
		UserPrompt: &mecatlv1.UserPrompt{Text: "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nplain user prompt"},
	}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream(unfenced)}

	m := newReconnectModel(t, ctx, fl, fr)
	defer joinReconnectForCleanup(&m)()
	m, reconnected := feedReconnect(t, m)
	if !reconnected {
		t.Fatal("expected LiveReconnectedMsg")
	}
	// No delivery block; the prompt was NOT misclassified.
	for _, b := range m.conv.testBlocks() {
		if b.kind == blockDelivery {
			t.Errorf("un-fenced prompt misclassified as a delivery block")
		}
	}
}

// restoreBackoffClient saves/restores the client package's backoff knobs around
// a ui test that shrinks them. The ui test shrinks them indirectly via the
// client package's package-level vars (the reconnect loop reads them).
func restoreBackoffClient(t *testing.T) func() {
	t.Helper()
	return client.RestoreBackoffForTest()
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// rearmedReaderAuthRejectedStreamer closes the initial reader, lets the first
// reconnect probe succeed, then rejects the first Recv on the freshly rearmed
// reader. It proves the auth terminal also covers the rearm boundary.
type rearmedReaderAuthRejectedStreamer struct{ opens atomic.Int32 }

func (s *rearmedReaderAuthRejectedStreamer) StreamSessionLive(context.Context, string) (*client.EventStream, error) {
	switch s.opens.Add(1) {
	case 1:
		return client.NewEventStream(client.NewFakeEventStream()), nil
	case 2:
		return client.NewEventStream(client.NewFakeEventStream()), nil
	case 3:
		return client.NewEventStream(client.NewFakeEventStream().WithEndErr(status.Error(codes.Unauthenticated, "rejected"))), nil
	default:
		return nil, errors.New("rejected bearer retried")
	}
}

func (*rearmedReaderAuthRejectedStreamer) BearerBackedStream() bool { return true }

// reconnectProbeAuthRejectedStreamer closes its initial reader, then rejects the
// reconnect loop's actual StreamSessionLive open with bearer provenance.
type reconnectProbeAuthRejectedStreamer struct{ opens atomic.Int32 }

func (s *reconnectProbeAuthRejectedStreamer) StreamSessionLive(context.Context, string) (*client.EventStream, error) {
	if s.opens.Add(1) == 1 {
		return client.NewEventStream(client.NewFakeEventStream()), nil
	}
	return nil, status.Error(codes.Unauthenticated, "rejected")
}

func (*reconnectProbeAuthRejectedStreamer) BearerBackedStream() bool { return true }

func TestADR_0096_BearerLiveReaderAuthRejectedPreservesHandoffAndStopsRetry(t *testing.T) {
	live := &rearmedReaderAuthRejectedStreamer{}
	m := New(Deps{
		Connect:     fakeConnect{targets: []ConnectTarget{{Target: "remote.example:443"}}},
		LiveStream:  live,
		Server:      "remote.example:443",
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m.sessionID = "sess-rejected"
	m.phase = phaseIdle

	// Drive initial close, successful probe, and fresh rearm through the actual
	// reader commands. The final message is the rearmed reader's first Recv.
	cmd := m.armLiveFeed()
	initialClose := runCmdTimeout(t, cmd)
	mm, waitReconnect := m.Update(initialClose)
	m = mm.(Model)
	attempt := runCmdTimeout(t, waitReconnect)
	mm, waitSuccess := m.Update(attempt)
	m = mm.(Model)
	success := runCmdTimeout(t, waitSuccess)
	mm, rearmedReader := m.Update(success)
	m = mm.(Model)
	rejected := runCmdTimeout(t, rearmedReader)
	liveMsg, ok := rejected.(liveMsg)
	if !ok {
		t.Fatalf("rearmed reader handoff = %T, want liveMsg", rejected)
	}
	if streamErr, ok := liveMsg.msg.(client.StreamErrMsg); !ok || streamErr.AuthReason != client.AuthRejected {
		t.Fatalf("rearmed first Recv = %#v, want AuthRejected StreamErrMsg", liveMsg.msg)
	}
	mm, _ = m.Update(rejected)
	m = mm.(Model)

	if !m.connect.open || m.connect.reason != client.AuthRejected {
		t.Fatalf("auth recovery = %#v, want open AuthRejected /connect recovery", m.connect)
	}
	if m.sessionID != "sess-rejected" || m.connect.failedTarget != "remote.example:443" || m.connect.resumeSessionID != "sess-rejected" {
		t.Fatalf("auth recovery lost target/session: session=%q connect=%#v", m.sessionID, m.connect)
	}
	if m.liveCh != nil || m.liveReconCh != nil || m.liveStop != nil || m.liveReconStop != nil || m.liveArmed != "" {
		t.Fatalf("auth recovery did not tear down both readers: live=%v recon=%v armed=%q", m.liveCh, m.liveReconCh, m.liveArmed)
	}
	if live.opens.Load() != 3 {
		t.Fatalf("live opens = %d, want exactly initial reader + probe + rearmed reader; rejected bearer must not retry", live.opens.Load())
	}

	// A rejection returned by the reconnect probe OPEN must travel through the
	// same connected reducer recovery, rather than ending only inside the client
	// reconnect loop. The initial reader's clean close starts that real loop.
	probeLive := &reconnectProbeAuthRejectedStreamer{}
	probe := New(Deps{
		Connect:     fakeConnect{targets: []ConnectTarget{{Target: "remote.example:443"}}},
		LiveStream:  probeLive,
		Server:      "remote.example:443",
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	probe.sessionID = "sess-probe-rejected"
	probe.phase = phaseIdle
	probeCmd := probe.armLiveFeed()
	probeClose := runCmdTimeout(t, probeCmd)
	mm, waitProbe := probe.Update(probeClose)
	probe = mm.(Model)
	probeAttempt := runCmdTimeout(t, waitProbe)
	marker, ok := probeAttempt.(reconnectMsg)
	if !ok {
		t.Fatalf("probe reconnect marker handoff = %T, want reconnectMsg", probeAttempt)
	}
	if reconnecting, ok := marker.msg.(client.LiveReconnectingMsg); !ok || reconnecting.Attempt != 1 {
		t.Fatalf("probe reconnect marker = %#v, want attempt 1", marker.msg)
	}
	mm, waitProbe = probe.Update(probeAttempt)
	probe = mm.(Model)
	probeRejected := runCmdTimeout(t, waitProbe)
	reconnect, ok := probeRejected.(reconnectMsg)
	if !ok {
		t.Fatalf("probe rejection handoff = %T, want reconnectMsg", probeRejected)
	}
	streamErr, ok := reconnect.msg.(client.StreamErrMsg)
	if !ok || streamErr.AuthReason != client.AuthRejected {
		t.Fatalf("probe open classification = %#v, want AuthRejected StreamErrMsg", reconnect.msg)
	}
	mm, _ = probe.Update(probeRejected)
	probe = mm.(Model)
	if !probe.connect.open || probe.connect.reason != client.AuthRejected {
		t.Fatalf("probe auth recovery = %#v, want open AuthRejected /connect recovery", probe.connect)
	}
	if probe.sessionID != "sess-probe-rejected" || probe.connect.failedTarget != "remote.example:443" || probe.connect.resumeSessionID != "sess-probe-rejected" {
		t.Fatalf("probe auth recovery lost target/session: session=%q connect=%#v", probe.sessionID, probe.connect)
	}
	if probe.liveCh != nil || probe.liveReconCh != nil || probe.liveStop != nil || probe.liveReconStop != nil || probe.liveArmed != "" {
		t.Fatalf("probe auth recovery did not tear down both readers: live=%v recon=%v armed=%q", probe.liveCh, probe.liveReconCh, probe.liveArmed)
	}
	if probeLive.opens.Load() != 2 {
		t.Fatalf("probe live opens = %d, want initial reader + rejected probe; rejected bearer must not retry", probeLive.opens.Load())
	}
}

// reconnectSequenceStreamer models the exact close → probe → rearm → close
// sequence. Probe streams are discarded by reconnectLiveLoop; reader streams are
// consumed by LiveStreamCmd, so the closes below travel through the actual handoff.
type reconnectSequenceStreamer struct{ opens atomic.Int32 }

func (s *reconnectSequenceStreamer) StreamSessionLive(context.Context, string) (*client.EventStream, error) {
	switch s.opens.Add(1) {
	case 1, 3: // initial reader, then freshly rearmed reader
		return client.NewEventStream(client.NewFakeEventStream()), nil
	case 2: // first reconnect probe succeeds
		return client.NewEventStream(client.NewFakeEventStream()), nil
	case 4: // attempt-2 probe follows the client-loop backoff
		return client.NewEventStream(client.NewFakeEventStream()), nil
	default:
		return nil, errors.New("unexpected extra live open")
	}
}

func TestADR_0096_ImmediateRearmedCloseUsesAttemptTwoBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := &reconnectSequenceStreamer{}
	m := New(Deps{LiveStream: live, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: ctx, NoAltScreen: true})
	m.sessionID = "sess-continuity"

	// Initial reader close starts attempt 1; its successful probe re-arms a fresh
	// live reader. Neither handoff is injected as a preclassified lifecycle msg.
	cmd := m.armLiveFeed()
	firstClose := runCmdTimeout(t, cmd)
	mm, reconnect1 := m.Update(firstClose)
	m = mm.(Model)
	firstAttempt := runCmdTimeout(t, reconnect1)
	rm, ok := firstAttempt.(reconnectMsg)
	if !ok {
		t.Fatalf("first reconnect handoff = %T, want reconnectMsg", firstAttempt)
	}
	if marker, ok := rm.msg.(client.LiveReconnectingMsg); !ok || marker.Attempt != 1 {
		t.Fatalf("first reconnect marker = %#v, want attempt 1", rm.msg)
	}
	mm, nextReconnect := m.Update(firstAttempt)
	m = mm.(Model)
	reconnected := runCmdTimeout(t, nextReconnect)
	rm, ok = reconnected.(reconnectMsg)
	if !ok {
		t.Fatalf("successful probe handoff = %T, want reconnectMsg", reconnected)
	}
	if _, ok := rm.msg.(client.LiveReconnectedMsg); !ok {
		t.Fatalf("successful probe message = %#v, want LiveReconnectedMsg", rm.msg)
	}
	mm, rearmedReader := m.Update(reconnected)
	m = mm.(Model)
	secondClose := runCmdTimeout(t, rearmedReader)
	if _, ok := secondClose.(liveMsg); !ok {
		t.Fatalf("rearmed reader handoff = %T, want liveMsg", secondClose)
	}
	mm, reconnect2 := m.Update(secondClose)
	m = mm.(Model)

	secondAttempt := runCmdTimeout(t, reconnect2)
	rm, ok = secondAttempt.(reconnectMsg)
	if !ok {
		t.Fatalf("second reconnect handoff = %T, want reconnectMsg", secondAttempt)
	}
	if marker, ok := rm.msg.(client.LiveReconnectingMsg); !ok || marker.Attempt != 2 {
		t.Fatalf("second reconnect marker = %#v, want attempt 2", rm.msg)
	}
	// The attempt-2 marker is emitted on the loop's buffered channel BEFORE the
	// loop calls live.StreamSessionLive for the attempt-2 probe (case 4 below),
	// so receiving it here gives no happens-before guarantee that opens is
	// already 4 — a buffered send does not block on the receiver, and the loop
	// goroutine may not yet have reached the StreamSessionLive call. Draining
	// the loop's NEXT message (LiveReconnectedMsg, emitted only after that call
	// returns) establishes the real synchronization: Go's channel semantics
	// guarantee this receive happens after the corresponding send, which in
	// turn happens after the StreamSessionLive call in the same goroutine.
	mm, nextReconnect2 := m.Update(secondAttempt)
	m = mm.(Model)
	secondProbe := runCmdTimeout(t, nextReconnect2)
	rm, ok = secondProbe.(reconnectMsg)
	if !ok {
		t.Fatalf("attempt-2 probe handoff = %T, want reconnectMsg", secondProbe)
	}
	if _, ok := rm.msg.(client.LiveReconnectedMsg); !ok {
		t.Fatalf("attempt-2 probe message = %#v, want LiveReconnectedMsg", rm.msg)
	}
	if opens := live.opens.Load(); opens != 4 {
		t.Fatalf("live opens = %d, want initial reader + probe + rearmed reader + attempt-2 probe", opens)
	}
	joinReconnectForCleanup(&m)()
}

type continuityResetStreamer struct{ opens atomic.Int32 }

func (s *continuityResetStreamer) StreamSessionLive(context.Context, string) (*client.EventStream, error) {
	switch s.opens.Add(1) {
	case 1: // initial reader closes
		return client.NewEventStream(client.NewFakeEventStream()), nil
	case 2: // reconnect probe succeeds
		return client.NewEventStream(client.NewFakeEventStream()), nil
	case 3: // rearmed reader emits a real event, then closes
		return client.NewEventStream(client.NewFakeEventStream(deliveryEv("nightly", "fire-live"))), nil
	case 4: // post-reset reconnect probe succeeds
		return client.NewEventStream(client.NewFakeEventStream()), nil
	default:
		return nil, errors.New("unexpected extra live open")
	}
}

func TestADR_0096_OnlyCurrentLiveEventResetsContinuity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := &continuityResetStreamer{}
	replayer := &fakeSessionReplayer{stream: client.NewFakeEventStream(deliveryEv("nightly", "fire-catchup"))}
	m := New(Deps{
		LiveStream:  live,
		Replayer:    replayer,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         ctx,
		NoAltScreen: true,
	})
	m.sessionID = "sess-continuity"

	// Drive the initial reader close through the arm/fan-in seam. It starts the
	// first continuity sequence and the real reconnect loop.
	initialReader := m.armLiveFeed()
	initialClose := runCmdTimeout(t, initialReader)
	mm, reconnect := m.Update(initialClose)
	m = mm.(Model)
	if m.liveContinuityAttempt != 1 {
		t.Fatalf("initial close set continuity to %d, want 1", m.liveContinuityAttempt)
	}

	// The loop's marker, catch-up event, and successful probe all arrive through
	// its fan-in and are not evidence from the current live reader.
	markerMsg := runCmdTimeout(t, reconnect)
	marker, ok := markerMsg.(reconnectMsg)
	if !ok {
		t.Fatalf("reconnect marker handoff = %T, want reconnectMsg", markerMsg)
	}
	if reconnecting, ok := marker.msg.(client.LiveReconnectingMsg); !ok || reconnecting.Attempt != 1 {
		t.Fatalf("reconnect marker = %#v, want attempt 1", marker.msg)
	}
	mm, _ = m.Update(markerMsg)
	m = mm.(Model)
	if m.liveContinuityAttempt != 1 {
		t.Fatalf("reconnect marker reset continuity to %d, want 1", m.liveContinuityAttempt)
	}

	catchUpMsg := runCmdTimeout(t, m.waitReconnectCmd())
	catchUp, ok := catchUpMsg.(reconnectMsg)
	if !ok {
		t.Fatalf("catch-up handoff = %T, want reconnectMsg", catchUpMsg)
	}
	if _, ok := catchUp.msg.(client.DeliveryNoteMsg); !ok {
		t.Fatalf("catch-up message = %#v, want DeliveryNoteMsg", catchUp.msg)
	}
	mm, _ = m.Update(catchUpMsg)
	m = mm.(Model)
	if m.liveContinuityAttempt != 1 {
		t.Fatalf("catch-up event reset continuity to %d, want 1", m.liveContinuityAttempt)
	}

	probeMsg := runCmdTimeout(t, m.waitReconnectCmd())
	probe, ok := probeMsg.(reconnectMsg)
	if !ok {
		t.Fatalf("probe handoff = %T, want reconnectMsg", probeMsg)
	}
	if _, ok := probe.msg.(client.LiveReconnectedMsg); !ok {
		t.Fatalf("probe message = %#v, want LiveReconnectedMsg", probe.msg)
	}
	mm, rearmedReader := m.Update(probeMsg)
	m = mm.(Model)
	if m.liveContinuityAttempt != 1 {
		t.Fatalf("successful probe reset continuity to %d, want 1", m.liveContinuityAttempt)
	}

	// The freshly rearmed reader's actual event is the sole reset signal.
	currentEvent := runCmdTimeout(t, rearmedReader)
	liveEvent, ok := currentEvent.(liveMsg)
	if !ok {
		t.Fatalf("current-reader handoff = %T, want liveMsg", currentEvent)
	}
	if _, ok := liveEvent.msg.(client.DeliveryNoteMsg); !ok {
		t.Fatalf("current-reader message = %#v, want DeliveryNoteMsg", liveEvent.msg)
	}
	mm, _ = m.Update(currentEvent)
	m = mm.(Model)
	if m.liveContinuityAttempt != 0 {
		t.Fatalf("current-reader event left continuity at %d, want reset", m.liveContinuityAttempt)
	}

	currentClose := runCmdTimeout(t, m.waitLiveCmd())
	mm, reconnect = m.Update(currentClose)
	m = mm.(Model)
	defer joinReconnectForCleanup(&m)()
	next := runCmdTimeout(t, reconnect)
	rm, ok := next.(reconnectMsg)
	if !ok {
		t.Fatalf("post-event reconnect handoff = %T, want reconnectMsg", next)
	}
	if marker, ok := rm.msg.(client.LiveReconnectingMsg); !ok || marker.Attempt != 1 {
		t.Fatalf("post-event reconnect marker = %#v, want attempt 1", rm.msg)
	}
}

// readerAuthRejectedLiveStreamer returns an actual EventStream whose first Recv
// fails with Unauthenticated. Its bearer provenance exercises LiveStreamCmd's
// reader classification rather than preclassifying a StreamErrMsg in the UI.
type readerAuthRejectedLiveStreamer struct{ opens atomic.Int32 }

func (s *readerAuthRejectedLiveStreamer) StreamSessionLive(context.Context, string) (*client.EventStream, error) {
	s.opens.Add(1)
	return client.NewEventStream(client.NewFakeEventStream().WithEndErr(status.Error(codes.Unauthenticated, "rejected"))), nil
}

func (*readerAuthRejectedLiveStreamer) BearerBackedStream() bool { return true }

func TestADR_0096_BearerLiveReaderRecvAuthRejectedRoutesToConnectRecovery(t *testing.T) {
	live := &readerAuthRejectedLiveStreamer{}
	m := New(Deps{
		Connect:     fakeConnect{targets: []ConnectTarget{{Target: "remote.example:443"}}},
		LiveStream:  live,
		Server:      "remote.example:443",
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m.sessionID = "sess-rejected"
	m.phase = phaseIdle

	cmd := m.armLiveFeed()
	if cmd == nil {
		t.Fatal("initial live reader was not armed")
	}
	msg := runCmdTimeout(t, cmd)
	liveMsg, ok := msg.(liveMsg)
	if !ok {
		t.Fatalf("live reader handoff = %T, want liveMsg", msg)
	}
	streamErr, ok := liveMsg.msg.(client.StreamErrMsg)
	if !ok || streamErr.AuthReason != client.AuthRejected {
		t.Fatalf("first Recv classification = %#v, want AuthRejected StreamErrMsg", liveMsg.msg)
	}
	mm, _ := m.Update(msg)
	m = mm.(Model)

	if !m.connect.open || m.connect.reason != client.AuthRejected {
		t.Fatalf("connect recovery = %#v, want open AuthRejected recovery", m.connect)
	}
	if m.connect.failedTarget != "remote.example:443" || m.connect.resumeSessionID != "sess-rejected" || m.sessionID != "sess-rejected" {
		t.Fatalf("target/session not preserved: failed=%q resume=%q session=%q", m.connect.failedTarget, m.connect.resumeSessionID, m.sessionID)
	}
	if m.liveCh != nil || m.liveReconCh != nil || m.liveReconnecting || m.liveArmed != "" {
		t.Fatalf("auth recovery left live/reconnect state armed: live=%v recon=%v reconnecting=%v armed=%q", m.liveCh, m.liveReconCh, m.liveReconnecting, m.liveArmed)
	}
	if footer := stripANSIstr(m.renderFooter()); containsStr(footer, "live feed reconnecting") {
		t.Fatalf("stale reconnect footer remained after auth recovery: %q", footer)
	}
	overlay := stripANSIstr(m.View().Content)
	for _, guidance := range []string{"Re-login is disabled", "issuer, audience, and CA"} {
		if !containsStr(overlay, guidance) {
			t.Fatalf("auth recovery overlay missing %q: %q", guidance, overlay)
		}
	}
	if live.opens.Load() != 1 {
		t.Fatalf("live opens = %d, want 1; rejected bearer must not retry", live.opens.Load())
	}
}
