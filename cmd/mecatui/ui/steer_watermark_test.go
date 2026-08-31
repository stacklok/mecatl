package ui

import (
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// Scenario 6 of docs/acceptance/steer-while-running.md (the mecatui half) — the
// queue-split contract live-tested in session 00a10fc8 previously failed: the
// first ack was dropped because it did not match the live watermark id (the
// newest send). applySteerOutcome must split the ordered queue on ANY queued
// id (the prefix renders sent, the suffix stays pending), advancing the
// watermark to the new tail.

// TestSteer_WatermarkSplitOnQueuedAck is R5-1 (the m1-in-flight, m2-queued,
// m3-queued, first-echo-arrives case): the DRAIN ECHO owns the queue split —
// the ack only advances the lifecycle phase. Splitting on the watermark id
// drops the drained prefix and keeps the suffix pending as the new bundle.
func TestSteer_WatermarkSplitOnQueuedAck(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "initial")

	m = enqueueSteer(t, m, "first")
	m = enqueueSteer(t, m, "second")
	m = enqueueSteer(t, m, "third")

	// The queue holds three sends; the watermark is the newest.
	if m.steer == nil || m.steer.watermarkID() != "steer-0003" || len(m.steer.Sends) != 3 {
		t.Fatalf("pre-drain state = %+v, want one bundle with three sends watermarked at steer-0003", m.steer)
	}

	// The FIRST ack (for steer-0001) advances the lifecycle to steerSent; the
	// queue carries it unsplit (only the echo drains a prefix).
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "first", MessageID: "steer-0001"})
	m = mm.(Model)
	if m.steer == nil || m.steer.watermarkID() != "steer-0003" || len(m.steer.Sends) != 3 || m.steer.Phase != steerSent {
		t.Fatalf("the first ack must advance the phase (not split the queue), got %+v", m.steer)
	}

	// The drain echo for the TAIL (watermark) splits the prefix and clears it;
	// the whole bundle drained (nothing left to scope).
	mm2, _ := m.Update(client.SteerEchoMsg{Text: "first\n\nsecond\n\nthird", MessageID: "steer-0003"})
	m = mm2.(Model)
	if m.steer != nil {
		t.Fatalf("the drain echo must clear the whole bundle, got %+v", m.steer)
	}
}

func TestSteer_WatermarkPrefixDrainPreservesSuffixMediaForEditBack(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "initial")
	m.steer = &steerState{Phase: steerSent, Sends: []steerQueuedSend{
		{ID: "steer-0001", Text: "first", Draft: "first"},
		{ID: "steer-0002", Text: "second", Draft: "second [Image #2]", Staged: map[string]stagedAttachment{"[Image #2]": {mime: "image/png", data: []byte("suffix")}}},
	}}
	m.steer.Text = joinSteerSends(m.steer.Sends)
	mm, _ := m.Update(client.SteerEchoMsg{Text: "first", MessageID: "steer-0001"})
	m = mm.(Model)
	if m.steer == nil || len(m.steer.Sends) != 1 || m.steer.Sends[0].ID != "steer-0002" {
		t.Fatalf("watermark did not retain suffix: %#v", m.steer)
	}
	mm, cmd := m.editBackQueue()
	m = mm.(Model)
	runBatchLeaves(cmd)
	if m.prompt.Value() != "second [Image #2]" || string(m.stagedMedia["[Image #2]"].data) != "suffix" {
		t.Fatalf("suffix media did not survive edit-back: draft=%q staged=%#v", m.prompt.Value(), m.stagedMedia)
	}
}

// TestSteer_AppendAckAdvancesPhase is R5-2: an "appended" ack scopes any
// queued id and only advances the lifecycle phase (the queue itself only
// splits on the echo, never on an ack).
func TestSteer_AppendAckAdvancesPhase(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "initial")

	m = enqueueSteer(t, m, "one")
	m = enqueueSteer(t, m, "two")
	m = enqueueSteer(t, m, "three")

	// The middle-id ack advances the phase; the queue itself stays whole.
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAppended, Text: "one\n\ntwo", MessageID: "steer-0002"})
	m = mm.(Model)
	if m.steer == nil || m.steer.watermarkID() != "steer-0003" || len(m.steer.Sends) != 3 || m.steer.Phase != steerSent {
		t.Fatalf("the appended ack must advance the phase (not split), got %+v", m.steer)
	}
}

// TestSteer_BurnedAckStillIgnored is R5-3: an ack for a drained (burned) id
// is dropped — the burned guard must survive relaxation.
func TestSteer_BurnedAckStillIgnored(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "initial")

	m = enqueueSteer(t, m, "one")
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "one", MessageID: "steer-0001"})
	m = mm.(Model)
	mm2, _ := m.Update(client.SteerEchoMsg{Text: "one", MessageID: "steer-0001"})
	m = mm2.(Model)
	if m.steer != nil {
		t.Fatalf("the echo must clear the live bundle, got %+v", m.steer)
	}

	// A late duplicate ack for the burned id must NOT resurrect a card.
	mm3, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "one", MessageID: "steer-0001"})
	m = mm3.(Model)
	if m.steer != nil {
		t.Fatalf("the duplicate ack must be ignored, got %+v", m.steer)
	}
}
