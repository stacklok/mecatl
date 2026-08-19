package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestLookupSteerMessageIDExactUnderDuplicateTexts is the ADR-0228 invariant
// pin: two identical-text steers tracked back-to-back produce a watermark
// equal to the SECOND (tail) id, and the FIFO is consumed (a second lookup
// yields ""). A text-match correlation would return the first id and leave a
// residue — this test is the executable statement of "positional, not
// textual" that the ADR names.
func TestLookupSteerMessageIDExactUnderDuplicateTexts(t *testing.T) {
	svc := newSteerService(t, nil, nil)
	ctx := context.Background()
	sess, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	svc.TrackSteerMessageIDForTest(sess.ID, "m-1")
	svc.TrackSteerMessageIDForTest(sess.ID, "m-2")

	if got := svc.LookupSteerMessageID(sess.ID); got != "m-2" {
		t.Fatalf("watermark = %q, want tail id %q (text-match would have returned %q)", got, "m-2", "m-1")
	}
	if got := svc.LookupSteerMessageID(sess.ID); got != "" {
		t.Fatalf("second lookup = %q, want empty (the FIFO must be consumed after one drain)", got)
	}
}

// TestSteer_IngressRepairCannotBreakCorrelation pins the invariant that the
// engine's UTF-8 ingress repair (session.ToValidUTF8 on the steer text) never
// affects the wire-side message_id correlation — the id rides a separate
// channel (frame field → Service FIFO → echo) and the correlation is positional,
// not textual. A repaired-steer drain must still echo the tracked id
// (the genuine regression class the ADR-0222-style text-match design would
// have created): if a future repair widens to the id-mapping, this test fails.
func TestSteer_IngressRepairCannotBreakCorrelation(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	svc := newSteerService(t, mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))),
		mockllm.TextTurn("done"),
	), nil, block)
	ctx := context.Background()
	sess, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(ctx, sess.ID, "look", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	<-block.started

	// The steer text carries invalid UTF-8; its id is tracked via the Service
	// Steer path (text repaired at the engine inbox, id untracked by text).
	invalid := string([]byte{0xe2, 0x28})
	outc, promoted, _, err := svc.Steer(ctx, sess.ID, invalid, "m-repair")
	if err != nil || promoted {
		t.Fatalf("Steer on a live run = %v / promoted=%v, want accepted/appended enqueued", err, promoted)
	}
	if outc != agent.SteerAccepted {
		t.Fatalf("Steer outcome = %q, want accepted", outc)
	}
	// The correlation id rides the FIFO unharmed by the repaired text (the
	// positional correlation never consults text). Assert directly, then let
	// the run drain.
	if got := svc.LookupSteerMessageID(sess.ID); got != "m-repair" {
		t.Fatalf("tracked id in the FIFO = %q, want %q — repair must not strand the id", got, "m-repair")
	}
	close(block.release)
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
}
