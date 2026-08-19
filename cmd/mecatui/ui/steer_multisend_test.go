package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// Three sends during one turn must drain via the ONE merged echo; the queue
// clears and the landed line renders exactly once.
func TestSteer_MergeThreeIntoOneDrain(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "original")

	m = enqueueSteer(t, m, "steer 1")
	m = enqueueSteer(t, m, "steer 2")
	m = enqueueSteer(t, m, "steer 3")
	if m.steer == nil || len(m.steer.Sends) != 3 {
		t.Fatalf("three sends must queue, got %+v", m.steer)
	}

	// Acks advance phase but keep the queue (only the echo splits it).
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "steer 1", MessageID: "steer-0001"})
	m = mm.(Model)
	mm, _ = m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAppended, Text: "steer 2", MessageID: "steer-0002"})
	m = mm.(Model)
	mm, _ = m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAppended, Text: "steer 3", MessageID: "steer-0003"})
	m = mm.(Model)
	if m.steer == nil || len(m.steer.Sends) != 3 || m.steer.Phase != steerSent {
		t.Fatalf("acks must advance phase while queue stays whole, got %+v", m.steer)
	}

	// The ONE merged drain echo (watermark = tail) clears the queue and lands
	// the merged text once.
	mm2, _ := m.Update(client.SteerEchoMsg{Text: "steer 1\n\nsteer 2\n\nsteer 3", MessageID: "steer-0003"})
	m = mm2.(Model)
	if m.steer != nil {
		t.Fatalf("the drain echo must clear the card, got %+v", m.steer)
	}
	m.refreshView()
	rendered := stripANSIstr(m.View().Content)
	if n := strings.Count(rendered, "steer 1"); n != 1 {
		t.Fatalf("merged document must land once in the transcript, found %d:\n%s", n, rendered)
	}
}

// An echo with an EMPTY message_id (an id-less sender / legacy server) must
// also clear the queue and land the text — a non-matched id must not strand
// the queue as it did before.
func TestSteer_IdLessEchoClearsQueue(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "original")

	m = enqueueSteer(t, m, "steer 1")
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "steer 1", MessageID: "steer-0001"})
	m = mm.(Model)

	mm2, _ := m.Update(client.SteerEchoMsg{Text: "steer 1", MessageID: ""})
	m = mm2.(Model)
	if m.steer != nil {
		t.Fatalf("the id-less echo must clear the queue, got %+v", m.steer)
	}
}
