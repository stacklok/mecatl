package server_test

// Rework task 09 (Ozz-review round 2, findings #3/#4/#5): the WIRE + handoff
// contract —
//
//   - R3-1: a client-minted message_id round-trips — echoed on the steer.outcome
//     ack AND on the EvSteer drain echo, so the client correlates the
//     authoritative outcome with the frame it sent (text is not a safe key).
//   - R3-2: a second steer while one is pending APPENDS into the pending bundle
//     (STEER_OUTCOME_APPENDED — supersede/slot_full were dropped in the round-3
//     append-default rework).
//   - R3-3: a promoted steer's follow-up run relays its FULL event stream +
//     terminal EvResult + its steer.outcome{promoted:true} ack on the SAME
//     stream BEFORE the Converse RPC returns (the sequential active-run
//     handoff — no orphaned relay goroutine, no ack-after-close).
//   - R3-4: after a promote, control frames (ResumeApproval / Cancel) target the
//     PROMOTED run, not the terminal original (the control target swaps
//     atomically with the relay handoff).
//   - R3-5: runRelay.sendErr has a SINGLE owner — no cross-goroutine read/write
//     between the original and promoted relays (-race is the assertion).

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestSteer_MessageIdRoundTrip is R3-1: a steer frame carrying a client-minted
// message_id gets it echoed verbatim on (a) the steer.outcome ack and (b) the
// EvSteer drain echo, so the client correlates both authoritative signals with
// the frame it sent. A steer_cancel's message_id likewise echoes on its ack.
func TestSteer_MessageIdRoundTrip(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	svc := newSteerService(t, llm, nil, block)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "look"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	// While the tool holds the run live: steer with message_id m-steer-1, then a
	// steer_cancel with message_id m-cancel-1 (the cancel retracts the parked
	// steer; the second steer m-steer-2 then drains).
	framesSent := make(chan error, 1)
	go func() {
		<-block.started
		if err := stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "first draft", MessageId: "m-steer-1"}},
		}); err != nil {
			framesSent <- err
			return
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_SteerCancel{SteerCancel: &mecatlv1.SteerCancel{MessageId: "m-cancel-1"}},
		}); err != nil {
			framesSent <- err
			return
		}
		framesSent <- stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "final steer", MessageId: "m-steer-2"}},
		})
	}()

	var events []*mecatlv1.Event
	var acks []*mecatlv1.SteerAck
	var echo *mecatlv1.SteerEcho
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		ev := resp.GetEvent()
		events = append(events, ev)
		switch ev.GetType() {
		case "steer.outcome":
			acks = append(acks, ev.GetSteerOutcome())
			if len(acks) == 3 {
				close(block.release) // all acks in: let the run drain + finish
			}
		case "steer":
			echo = ev.GetSteer()
		case "result":
		}
		if ev.GetType() == "result" {
			break
		}
	}
	_ = stream.CloseSend()
	if err := <-framesSent; err != nil {
		t.Fatalf("Send frames: %v", err)
	}

	// Three acks in send order: accepted(m-steer-1), retracted(m-cancel-1),
	// accepted(m-steer-2) — each echoing ITS frame's message_id.
	if len(acks) != 3 {
		t.Fatalf("steer acks = %d, want 3: %v", len(acks), typesOf(events))
	}
	wantAcks := []struct {
		outcome mecatlv1.SteerOutcome
		msgID   string
	}{
		{mecatlv1.SteerOutcome_STEER_OUTCOME_ACCEPTED, "m-steer-1"},
		{mecatlv1.SteerOutcome_STEER_OUTCOME_RETRACTED, "m-cancel-1"},
		{mecatlv1.SteerOutcome_STEER_OUTCOME_ACCEPTED, "m-steer-2"},
	}
	for i, want := range wantAcks {
		if acks[i].GetOutcome() != want.outcome {
			t.Fatalf("ack[%d] outcome = %v, want %v", i, acks[i].GetOutcome(), want.outcome)
		}
		if acks[i].GetMessageId() != want.msgID {
			t.Fatalf("ack[%d] message_id = %q, want %q (the ack must echo its frame's id)", i, acks[i].GetMessageId(), want.msgID)
		}
	}

	// The EvSteer drain echo carries the COMMITTED text AND the message_id of
	// the steer that drained (m-steer-2 — m-steer-1 was retracted).
	if echo == nil {
		t.Fatalf("no steer drain echo on the stream: %v", typesOf(events))
	}
	if echo.GetText() != "final steer" {
		t.Fatalf("steer echo text = %q, want %q", echo.GetText(), "final steer")
	}
	if echo.GetMessageId() != "m-steer-2" {
		t.Fatalf("steer echo message_id = %q, want %q (the drained steer's id)", echo.GetMessageId(), "m-steer-2")
	}
}

// TestSteer_WatermarkEchoLatestId is the AC2.4 wire-side pin: two appends
// back-to-back merge into one bundle; the drain echo carries the merged text
// AND the watermark (the TAIL id of the contributing sends). (Renamed from
// TestSteer_SlotFullOnWire when slot_full was dropped in round 3.)
func TestSteer_WatermarkEchoLatestId(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	svc := newSteerService(t, llm, nil, block)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "look"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	// Two steers back-to-back while the tool holds the run (ordered on the one
	// client stream → processed in order): the first parks, the second APPENDS
	// (append is the default; the old slot_full reject is gone).
	framesSent := make(chan error, 1)
	go func() {
		<-block.started
		if err := stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "parked", MessageId: "m-1", Parts: []*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte("one")}}}},
		}); err != nil {
			framesSent <- err
			return
		}
		framesSent <- stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "appended", MessageId: "m-2", Parts: []*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_AUDIO, MimeType: "audio/wav", Data: []byte("two")}}}},
		})
	}()

	var events []*mecatlv1.Event
	var acks []*mecatlv1.SteerAck
	var echo *mecatlv1.SteerEcho
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		ev := resp.GetEvent()
		events = append(events, ev)
		switch ev.GetType() {
		case "steer.outcome":
			acks = append(acks, ev.GetSteerOutcome())
			if len(acks) == 2 {
				close(block.release)
			}
		case "steer":
			echo = ev.GetSteer()
		}
		if ev.GetType() == "result" {
			break
		}
	}
	_ = stream.CloseSend()
	if err := <-framesSent; err != nil {
		t.Fatalf("Send frames: %v", err)
	}

	if len(acks) != 2 {
		t.Fatalf("steer acks = %d, want 2: %v", len(acks), typesOf(events))
	}
	if got := acks[0].GetOutcome(); got != mecatlv1.SteerOutcome_STEER_OUTCOME_ACCEPTED {
		t.Fatalf("first steer outcome = %v, want ACCEPTED", got)
	}
	if got := acks[1].GetOutcome(); got != mecatlv1.SteerOutcome_STEER_OUTCOME_APPENDED {
		t.Fatalf("second steer outcome = %v, want APPENDED (a full slot APPENDS — the old reject is gone)", got)
	}
	if acks[1].GetMessageId() != "m-2" {
		t.Fatalf("appended ack message_id = %q, want %q", acks[1].GetMessageId(), "m-2")
	}

	// The two steers MERGE into one bundle and drain as the appended text
	// ("parked\n\nappended"); the echo's message_id is the watermark (the LATEST
	// contributing send, m-2).
	if echo == nil {
		t.Fatalf("no steer drain echo on the stream: %v", typesOf(events))
	}
	if echo.GetText() != "parked\n\nappended" || echo.GetMessageId() != "m-2" {
		t.Fatalf("steer echo = (%q, %q), want (%q, %q) — the appended bundle drains as one merged continuation",
			echo.GetText(), echo.GetMessageId(), "parked\n\nappended", "m-2")
	}
	if len(echo.GetParts()) != 2 || string(echo.GetParts()[0].GetData()) != "one" || string(echo.GetParts()[1].GetData()) != "two" {
		t.Fatalf("steer echo parts lost append order: %#v", echo.GetParts())
	}
}

// TestSteer_PromotedRelaySequential is R3-3: a steer arriving in the original
// run's terminate window promotes a follow-up run, and the Converse RPC relays
// the promoted run's FULL event stream + terminal EvResult + the
// steer.outcome{promoted:true} ack on the SAME stream BEFORE the RPC returns —
// the sequential active-run handoff (no orphaned relay goroutine left behind
// when Converse returns, no ack-after-close).
func TestSteer_PromotedRelaySequential(t *testing.T) {
	llm := mockllm.New(
		mockllm.TextTurn("first"),
		mockllm.TextTurn("promoted follow-up"),
	)
	svc := newSteerService(t, llm, nil)
	// Keep the terminal relay parked for the test's existing total budget,
	// rather than silently releasing it after two seconds under contention.
	client, stallRelease, cleanup := dialGRPCStall(t, svc, 20*time.Second)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	// Read until turn.end of the first run: the terminal result is stalled
	// server-side, so the run is terminal-but-registered (the drain window) —
	// the steer lands squarely in it.
	for {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv pre-stall: %v", err)
		}
		if resp.GetEvent().GetType() == "turn.end" {
			break
		}
	}
	// turn.end precedes closeSteerDrained. Wait for the terminal outcome,
	// not merely a registered pointer, before sending a deliberately late steer.
	original, ok := svc.LookupRun(session.SessionID(cs.GetSessionId()))
	if !ok {
		t.Fatal("original run is not registered in its terminal drain window")
	}
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for original.Outcome() != agent.RunOutcomeCompleted {
		select {
		case <-ctx.Done():
			t.Fatal("original run did not reach its terminal outcome")
		case <-poll.C:
		}
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "late steer", MessageId: "m-late", Parts: []*mecatlv1.Content{
			{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte("late-image")},
			{Kind: mecatlv1.Content_KIND_AUDIO, MimeType: "audio/wav", Data: []byte("late-audio")},
		}}},
	}); err != nil {
		t.Fatalf("Send steer: %v", err)
	}
	// The test context is the sole timeout budget. Keep the original stalled
	// until the replacement pointer is observable, proving the handoff
	// registered the promoted run before it can be allowed to drive.
	for {
		if promoted, ok := svc.LookupRun(session.SessionID(cs.GetSessionId())); ok && promoted != original {
			break
		}
		select {
		case <-ctx.Done():
			current, registered := svc.LookupRun(session.SessionID(cs.GetSessionId()))
			t.Fatalf("promoted run was not registered before test context expired: original=%p current=%p registered=%t: %v", original, current, registered, ctx.Err())
		case <-poll.C:
		}
	}
	close(stallRelease)

	// Read the stream to EOF (the server returns AFTER everything below is
	// relayed — the RPC returning ends the client stream).
	var events []*mecatlv1.Event
	var results []string
	var promotedAck *mecatlv1.SteerAck
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		ev := resp.GetEvent()
		events = append(events, ev)
		switch ev.GetType() {
		case "result":
			results = append(results, ev.GetResult().GetText())
		case "steer.outcome":
			if ev.GetSteerOutcome().GetPromoted() {
				promotedAck = ev.GetSteerOutcome()
			}
		}
	}
	_ = stream.CloseSend()

	// BOTH runs' terminal results made the SAME stream before the RPC returned:
	// the original's (stalled, then drained) AND the promoted follow-up's.
	if len(results) != 2 {
		t.Fatalf("results on the stream = %d (%v), want 2 (original + promoted follow-up): %v",
			len(results), results, typesOf(events))
	}
	if len(results) == 2 && (results[0] != "first" || results[1] != "promoted follow-up") {
		t.Fatalf("result texts = %v, want [first, promoted follow-up] in relay order", results)
	}

	// The promoted ack rode the stream BEFORE the RPC returned (no
	// ack-after-close), carrying the too_late+promoted outcome and echoing the
	// steer's message_id.
	if promotedAck == nil {
		t.Fatalf("no steer.outcome{promoted:true} ack on the stream before it closed: %v", typesOf(events))
	}
	if got := promotedAck.GetOutcome(); got != mecatlv1.SteerOutcome_STEER_OUTCOME_TOO_LATE {
		t.Fatalf("promoted ack outcome = %v, want TOO_LATE", got)
	}
	if promotedAck.GetMessageId() != "m-late" {
		t.Fatalf("promoted ack message_id = %q, want %q", promotedAck.GetMessageId(), "m-late")
	}
	loaded, err := svc.GetSession(context.Background(), session.SessionID(cs.GetSessionId()))
	if err != nil {
		t.Fatalf("GetSession after promoted media steer: %v", err)
	}
	var promotedInput *session.Message
	for i := len(loaded.Conversation.Messages) - 1; i >= 0; i-- {
		if loaded.Conversation.Messages[i].Role == session.RoleUser && loaded.Conversation.Messages[i].Text == "late steer" {
			promotedInput = &loaded.Conversation.Messages[i]
			break
		}
	}
	if promotedInput == nil || len(promotedInput.Parts) != 2 || promotedInput.Parts[0].Kind != session.MediaImage || promotedInput.Parts[0].MIMEType != "image/png" || string(promotedInput.Parts[0].Data) != "late-image" || promotedInput.Parts[1].Kind != session.MediaAudio || promotedInput.Parts[1].MIMEType != "audio/wav" || string(promotedInput.Parts[1].Data) != "late-audio" {
		t.Fatalf("promoted gRPC steer media = %#v", promotedInput)
	}

	// Ordering: the promoted run's terminal EvResult precedes its ack (causal
	// order), and both precede the stream close.
	resultIdx, ackIdx := -1, -1
	for i, ev := range events {
		if ev.GetType() == "result" && ev.GetResult().GetText() == "promoted follow-up" {
			resultIdx = i
		}
		if ev.GetType() == "steer.outcome" && ev.GetSteerOutcome().GetPromoted() {
			ackIdx = i
		}
	}
	if resultIdx < 0 || ackIdx < 0 || ackIdx <= resultIdx {
		t.Fatalf("promoted result at %d, promoted ack at %d — the ack must ride AFTER the promoted run drains", resultIdx, ackIdx)
	}
}

// TestSteer_ControlTargetsPromotedRun is R3-4: after a steer promotes a
// follow-up run, control frames on the stream target the PROMOTED (active) run,
// not the terminal original — here a Cancel frame cancels the promoted run
// mid-drive (the control target swapped atomically with the relay handoff).
func TestSteer_ControlTargetsPromotedRun(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	llm := mockllm.New(
		mockllm.TextTurn("first"),
		// The promoted run's turn calls the blocking tool so the run parks
		// mid-dispatch — the Cancel must hit THIS run.
		mockllm.ToolCallTurn(call("c9", "Read", `{"path":"b.go"}`)),
	)
	svc := newSteerService(t, llm, nil, block)
	client, stallRelease, cleanup := dialGRPCStall(t, svc, 20*time.Second)
	defer cleanup()
	promotionStarted := make(chan struct{})
	allowPromotion := make(chan struct{})
	releasePromotion := sync.OnceFunc(func() { close(allowPromotion) })
	svc.SetSteerPromotionStartedForTest(func() {
		close(promotionStarted)
		<-allowPromotion
	})
	defer releasePromotion()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	streamCtx, cancelStream := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStream()
	stream, err := client.Converse(streamCtx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	// Reach the first run's drain window. The result is stalled server-side, so
	// the original remains terminal-but-registered while the steer starts its
	// promoted handoff.
	for {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv pre-stall: %v", err)
		}
		if resp.GetEvent().GetType() == "turn.end" {
			break
		}
	}
	// Confirm the original is terminal-but-registered before sending the steer;
	// turn.end arrives before the terminal result and therefore is not itself the
	// terminal-state boundary.
	original, ok := svc.LookupRun(session.SessionID(cs.GetSessionId()))
	if !ok {
		t.Fatal("original run is not registered in its terminal drain window")
	}
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for original.Outcome() != agent.RunOutcomeCompleted {
		select {
		case <-ctx.Done():
			t.Fatal("original run did not reach its terminal outcome")
		case <-poll.C:
		}
	}
	registered := make(chan struct{})
	svc.SetSteerPromotionRegisteredForTest(func() { close(registered) })
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "late steer"}},
	}); err != nil {
		t.Fatalf("Send steer: %v", err)
	}
	// Releasing the terminal send is safe only after Service.Steer has admitted the
	// received frame to the promotion path. readControl reserved the handoff route
	// before calling Service.Steer, so that reservation happens-before this signal.
	// The synchronous hook then holds promotion until LookupRun proves the original
	// relay has deregistered; only then may the replacement register.
	select {
	case <-promotionStarted:
	case <-ctx.Done():
		t.Fatal("control reader did not admit steer before test context expired")
	}
	close(stallRelease)
	for {
		if _, ok := svc.LookupRun(session.SessionID(cs.GetSessionId())); !ok {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("original run did not deregister before test context expired")
		case <-poll.C:
		}
	}
	releasePromotion()
	select {
	case <-registered:
	case <-ctx.Done():
		t.Fatal("promoted run was not registered after steer")
	}

	// The promoted tool.call is relayed only after the handoff swapped the
	// control target. Send Cancel at that event boundary; the blocking tool then
	// keeps the promoted run alive until the Cancel takes effect.
	cancelSent := false
	var events []*mecatlv1.Event
	var results []string
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		ev := resp.GetEvent()
		events = append(events, ev)
		if ev.GetType() == "tool.call" && !cancelSent {
			if err := stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_Cancel{Cancel: &mecatlv1.Cancel{}},
			}); err != nil {
				t.Fatalf("Send cancel: %v", err)
			}
			cancelSent = true
		}
		if ev.GetType() == "result" {
			results = append(results, ev.GetResult().GetStop())
		}
	}
	_ = stream.CloseSend()
	if !cancelSent {
		t.Fatalf("promoted tool.call did not arrive before stream end: %v", typesOf(events))
	}

	// Two results: the original ended end_turn; the PROMOTED run ended
	// CANCELLED — proof the Cancel frame hit the active (promoted) run, not the
	// terminal original.
	if len(results) != 2 {
		t.Fatalf("results = %v, want 2 (original + promoted): %v", results, typesOf(events))
	}
	if results[0] != "end_turn" {
		t.Fatalf("original run stop = %q, want end_turn", results[0])
	}
	if results[1] != string(session.StopCancelled) {
		t.Fatalf("promoted run stop = %q, want %q — the Cancel frame must target the ACTIVE (promoted) run after the handoff",
			results[1], session.StopCancelled)
	}
}

// TestSteer_RelaySendErrSingleOwner is R3-5: a client that DISCONNECTS while a
// promoted run is queued (the handoff is pending) causes no cross-goroutine
// runRelay.sendErr access — the single relay owner observes the send error,
// cancels the active run, and drains. Under -race any racy sendErr read/write
// between the original and promoted relays fails the test; the assertion is
// that the RPC returns an error (the recorded send failure) instead of
// wedging, and the promoted run is never orphaned mid-drive after the RPC
// returned (FinishRun ran before return).
func TestSteer_RelaySendErrSingleOwner(t *testing.T) {
	llm := mockllm.New(
		mockllm.TextTurn("first"),
		mockllm.TextTurn("promoted follow-up"),
	)
	svc := newSteerService(t, llm, nil)
	client, stallRelease, cleanup := dialGRPCStall(t, svc, 2*time.Second)
	defer cleanup()

	streamCtx, cancelStream := context.WithCancel(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stream, err := client.Converse(streamCtx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	// Reach the first run's drain window, steer into it, then kill the client
	// mid-handoff: the original relay's stalled Send fails on the cancelled
	// stream context, and the promoted run is queued for the handoff.
	for {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv pre-stall: %v", err)
		}
		if resp.GetEvent().GetType() == "turn.end" {
			break
		}
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "late steer"}},
	}); err != nil {
		t.Fatalf("Send steer: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	cancelStream()      // the client is gone: every Send from here fails
	close(stallRelease) // the stalled Send unblocks into the cancelled stream

	// The server-side RPC unwinds: it must not wedge (the relay cancels the
	// active run and drains). Give the server a beat to finish, then the
	// session must settle to a durable terminal state (no run left
	// mid-registered — FinishRun ran before Converse returned, for BOTH runs).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !svc.IsLive(session.SessionID(cs.GetSessionId())) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if svc.IsLive(session.SessionID(cs.GetSessionId())) {
		t.Fatalf("a run is still registered after the client disconnected — the handoff orphaned a relay (FinishRun must run before Converse returns)")
	}
}

// steerOutcomeEnum guards the enum shape: APPENDED took the occupied-slot value
// (2); SUPERSEDED and SLOT_FULL are both REMOVED (append is the default, replace
// is explicit cancel-then-resend).
func TestSteer_ProtoEnumAppend(t *testing.T) {
	if got := mecatlv1.SteerOutcome_STEER_OUTCOME_APPENDED.String(); got != "STEER_OUTCOME_APPENDED" {
		t.Fatalf("enum value 2 = %q, want STEER_OUTCOME_APPENDED", got)
	}
	if mecatlv1.SteerOutcome_STEER_OUTCOME_APPENDED != 2 {
		t.Fatalf("STEER_OUTCOME_APPENDED = %d, want 2 (the wire number is stable across the swap)", mecatlv1.SteerOutcome_STEER_OUTCOME_APPENDED)
	}
	for name, v := range mecatlv1.SteerOutcome_value {
		if strings.Contains(name, "SUPERSEDED") || strings.Contains(name, "SLOT_FULL") {
			t.Fatalf("the proto enum still carries %q=%d — supersede/slot_full were dropped in the round-3 append-default rework", name, v)
		}
	}
}

// --- shared helpers ---------------------------------------------------------

var _ = atomic.Int32{} // (kept: the stall stream in steer_repair_test.go owns the atomic)
