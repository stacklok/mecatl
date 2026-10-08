package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeLiveStreamer is a spy client.LiveStreamer for the live-delivery tests: it
// returns a scripted *client.EventStream over a client.FakeEventStream, recording
// the id it was called with so the armLiveFeed handoff can be asserted. It mirrors
// the fakeSessionReplayer pattern from sessions_test.go.
type fakeLiveStreamer struct {
	stream *client.FakeEventStream
	err    error
	calls  int
	lastID string
}

func (f *fakeLiveStreamer) StreamSessionLive(_ context.Context, id string) (*client.EventStream, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return nil, f.err
	}
	return client.NewEventStream(f.stream), nil
}

// newLiveDeliveryModel builds a connected idle Model with a fake LiveStreamer
// wired and the live feed armed. This is the setup for Scenario 5.1 — the TUI is
// connected, the live subscription bridge is open, and a fire-result delivery
// event can arrive at any time with no operator input.
func newLiveDeliveryModel(t *testing.T, fl *fakeLiveStreamer) Model {
	t.Helper()
	conv := &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: client.Capabilities{}}
	deps := Deps{
		Session:     conv,
		Conv:        conv,
		LiveStream:  fl,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	}
	m := newTestModelFromDeps(deps)
	m = applyAll(
		m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{}},
	)
	// Arm the live feed — the applySessionReady path fires armLiveFeed, but the
	// tea.Cmd it returns needs to be fed. Feed it so the liveCh/liveGen/liveArmed
	// are populated.
	cmd := m.armLiveFeed()
	if cmd != nil {
		m = feedCmd(t, m, cmd)
	}
	return m
}

// deliveryUserPromptEvent builds a proto Event for a fire-result delivery note.
// The schedule name and fire id are embedded in the text via the canonical
// renderFireDelivery provenance header; the function signature calls them out
// so callers are explicit about the delivery contract even though only text
// is used directly.
func deliveryUserPromptEvent(_, _, text string) *mecatlv1.Event {
	return &mecatlv1.Event{
		Type: "user_prompt",
		UserPrompt: &mecatlv1.UserPrompt{
			Text: text,
		},
	}
}

// deliveryProvenanceBody is the canonical delivery note BODY from the
// scheduler (the renderFireDelivery header + final-text line, BEFORE the
// untrusted-fence wrapping). deliveryProvenanceText is the SAME body wrapped
// in the fenced-untrusted block renderFireDelivery produces (the
// "<<<UNTRUSTED\n" opener + body + trailing "<<<UNTRUSTED\n"). The ui package
// may not import engine/governance, so this is the literal mirror of
// governance.WriteUntrustedBlock's bytes; the client's deliverNoteFrom detects the
// note via this exact fenced shape.
const deliveryProvenanceBody = "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"

// deliveryProvenanceText is the canonical FENCED delivery note from the
// scheduler (renderFireDelivery's actual output). The live-wire relay + the TUI
// client detect the note via the "<<<UNTRUSTED\n[scheduled task " opener shape.
const deliveryProvenanceText = "<<<UNTRUSTED\n" + deliveryProvenanceBody + "\n<<<UNTRUSTED\n"

// TestFireDelivery_Scenario5_ConnectedTUIRendersDeliveryLive verifies AC5.1: a
// fire-result delivery EvUserPrompt arriving via the live subscription bridge
// (updateLiveMsg) renders as a delivery card (blockDelivery) in the conversation
// with NO operator input. The live feed is armed at session-ready time, the event
// arrives pushed from the server, and the TUI renders it as the distinct
// scheduled-task delivery card.
func TestFireDelivery_Scenario5_ConnectedTUIRendersDeliveryLive(t *testing.T) {
	fl := &fakeLiveStreamer{stream: client.NewFakeEventStream()}
	m := newLiveDeliveryModel(t, fl)

	if fl.calls != 1 || fl.lastID != "sess-test-0001" {
		t.Fatalf("LiveStreamer calls=%d lastID=%q, want 1/sess-test-0001", fl.calls, fl.lastID)
	}
	if m.liveCh == nil {
		t.Fatal("liveCh should be armed after newLiveDeliveryModel")
	}

	// Sanity: the conversation starts empty.
	if !m.conv.isEmpty() {
		t.Fatalf("conversation should be empty before the delivery, got %d blocks", len(m.conv.testBlocks()))
	}

	// Push a delivery user_prompt event through the live feed fan-in path.
	deliveryEv := deliveryUserPromptEvent("nightly-sync", "sched--fire1", deliveryProvenanceText)

	// The live stream's ReadLoop calls EventToMsg on the proto event, which
	// produces a DeliveryNoteMsg. That msg is then pushed onto the liveCh and
	// pulled via waitLiveCmd → liveMsg → updateLiveMsg.
	//
	// Simulate this: EventToMsg the proto event, then feed it through
	// updateLiveMsg with the current liveGen.
	msg := client.EventToMsg(deliveryEv)
	if msg == nil {
		t.Fatal("EventToMsg returned nil for a delivery user_prompt")
	}
	_, ok := msg.(client.DeliveryNoteMsg)
	if !ok {
		t.Fatalf("EventToMsg returned %T, want DeliveryNoteMsg", msg)
	}

	mm, cmd := m.updateLiveMsg(liveMsg{gen: m.liveGen, msg: msg})
	m = mm.(Model)
	// The live feed re-arms itself, so cmd should include waitLiveCmd.
	if cmd == nil {
		t.Error("updateLiveMsg should return a re-arm command for the live feed")
	}

	// The conversation should now contain a delivery card block.
	if m.conv.isEmpty() {
		t.Fatal("conversation should be non-empty after a live delivery")
	}
	blocks := m.conv.testBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (the delivery card), got %d: %+v", len(blocks), blocks)
	}
	delivery, ok := blocks[0].Payload.(scrollback.DeliveryCardSnapshot)
	if !ok {
		t.Fatalf("card payload = %T, want delivery", blocks[0].Payload)
	}
	if delivery.ScheduleName != "nightly-sync" {
		t.Errorf("delivery card schedule name = %q, want nightly-sync", delivery.ScheduleName)
	}
	if delivery.Text != deliveryProvenanceText {
		t.Errorf("delivery card text = %q, want %q", delivery.Text, deliveryProvenanceText)
	}
}

// TestFireDelivery_Scenario6_TransportProjectionParity verifies AC6.4: the SAME
// delivery EvUserPrompt event produces the SAME DeliveryNoteMsg/render whether it
// arrives via the live stream (LiveStreamCmd / updateLiveMsg) or replay processing
// (StreamSessionEvents / applyReplayEvent). One EventToMsg projection, two transports
// — both paths reduce the delivery to an equivalent delivery-card block.
func TestFireDelivery_Scenario6_TransportProjectionParity(t *testing.T) {
	deliveryEv := deliveryUserPromptEvent("nightly-sync", "sched--fire1", deliveryProvenanceText)

	// ── Live path ──────────────────────────────────────────────────────
	lm := newLiveDeliveryModel(t, &fakeLiveStreamer{stream: client.NewFakeEventStream()})
	lmEvtMsg := client.EventToMsg(deliveryEv)
	if lmEvtMsg == nil {
		t.Fatal("live path: EventToMsg returned nil")
	}
	liveDN, ok := lmEvtMsg.(client.DeliveryNoteMsg)
	if !ok {
		t.Fatalf("live path: EventToMsg returned %T, want DeliveryNoteMsg", lmEvtMsg)
	}
	mm, _ := lm.updateLiveMsg(liveMsg{gen: lm.liveGen, msg: lmEvtMsg})
	lm = mm.(Model)

	// ── Replay path ────────────────────────────────────────────────────
	// Build a fresh transcript conversation and drive applyReplayEvent with
	// the SAME DeliveryNoteMsg (the msg EventToMsg returns is what the replay
	// ReadLoop pushes).
	rs := sessionsState{}
	rs.applyReplayEvent(liveDN)

	// ── Assertions ─────────────────────────────────────────────────────
	liveBlocks := lm.conv.testBlocks()
	if len(liveBlocks) != 1 {
		t.Fatalf("live path: expected 1 block, got %d", len(liveBlocks))
	}
	replayBlocks := rs.transcript.testBlocks()
	if len(replayBlocks) != 1 {
		t.Fatalf("replay path: expected 1 block, got %d", len(replayBlocks))
	}

	lb, liveOK := liveBlocks[0].Payload.(scrollback.DeliveryCardSnapshot)
	rb, replayOK := replayBlocks[0].Payload.(scrollback.DeliveryCardSnapshot)
	if !liveOK || !replayOK {
		t.Fatalf("delivery payloads = %T/%T", liveBlocks[0].Payload, replayBlocks[0].Payload)
	}
	if lb.ScheduleName != rb.ScheduleName || lb.ScheduleName != "nightly-sync" {
		t.Errorf("schedule names = %q/%q, want nightly-sync", lb.ScheduleName, rb.ScheduleName)
	}
	if lb.Text != rb.Text || lb.Text != deliveryProvenanceText {
		t.Errorf("delivery text mismatch: live=%q replay=%q", lb.Text, rb.Text)
	}
}
