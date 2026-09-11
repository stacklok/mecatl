package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestSteer_IngressRepairCannotBreakCorrelation pins the invariant that the
// engine's UTF-8 ingress repair (session.ToValidUTF8 on the steer text) never
// affects message_id correlation. The id rides in the same engine bundle as
// the repaired content, and the committed EvSteer must echo it unchanged.
func TestSteer_IngressRepairCannotBreakCorrelation(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	svc := newSteerService(t, mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))),
		mockllm.TextTurn("done"),
	), nil, block)
	ctx := context.Background()
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(ctx, sess.ID, "look", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	<-block.started

	// The steer text carries invalid UTF-8; its id is bundled by the engine and
	// never derived from the repaired text.
	invalid := string([]byte{0xe2, 0x28})
	outc, promoted, _, err := svc.Steer(ctx, sess.ID, invalid, nil, "m-repair", "")
	if err != nil || promoted {
		t.Fatalf("Steer on a live run = %v / promoted=%v, want accepted/appended enqueued", err, promoted)
	}
	if outc != agent.SteerAccepted {
		t.Fatalf("Steer outcome = %q, want accepted", outc)
	}
	close(block.release)
	var committed *session.SteerPayload
	for ev := range run.Events() {
		if ev.Type == session.EvSteer {
			committed = ev.Steer
		}
	}
	svc.FinishRun(sess.ID, run)
	if committed == nil || committed.MessageID != "m-repair" || committed.Text != session.ToValidUTF8(invalid) {
		t.Fatalf("committed steer = %#v, want repaired text and message id %q", committed, "m-repair")
	}
}
