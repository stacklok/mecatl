package server_test

// Scenario 5 of docs/acceptance/steer-while-running.md: the WIRE surface — the
// steer rides the existing bidi Converse stream as new ConverseRequest oneof
// arms (steer / steer_cancel) alongside prompt / resume_approval / cancel /
// cancel_child; the server advertises the feature via ServerCapabilities.steer
// (the additive-grow discipline), computed ONCE in composition
// (Service.capabilities()); and the engine's EvSteer drain echo projects onto
// the same stream as the client-visible steer event.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func assertWireSteerParts(t *testing.T, parts []*mecatlv1.Content) {
	t.Helper()
	if len(parts) != 2 || parts[0].GetKind() != mecatlv1.Content_KIND_IMAGE || parts[0].GetMimeType() != "image/png" || string(parts[0].GetData()) != string([]byte{0x01, 0x02}) || parts[1].GetKind() != mecatlv1.Content_KIND_AUDIO || parts[1].GetMimeType() != "audio/wav" || string(parts[1].GetData()) != string([]byte{0x03, 0x04, 0x05}) {
		t.Fatalf("wire steer parts = %#v", parts)
	}
}

// TestSteer_ConverseFrameRoundTrip is AC5.1: a client sends a `steer` frame
// mid-run on the Converse stream and observes (a) the authoritative
// accepted-outcome ack, (b) the EvSteer drain echo carrying the committed text,
// and (c) the injected message reaching the model as an ordinary user turn on
// the following turn — all on the SAME stream.
//
// The blockingTextTool parks the run mid-dispatch (genuinely LIVE); the client
// sends the steer frame while the tool holds the run, then releases it — the
// steer drains at the upcoming turn boundary on the still-live run.
func TestSteer_ConverseFrameRoundTrip(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	var reqsMu sync.Mutex
	var reqs []port.LLMRequest
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
			reqsMu.Lock()
			reqs = append(reqs, r)
			reqsMu.Unlock()
		})},
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("turn two done"),
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

	// The tool-call event means the run reached dispatch; wait until the tool
	// is genuinely holding the run (the enqueue hits a live inbox), then steer.
	steerSent := make(chan error, 1)
	go func() {
		<-block.started
		steerSent <- stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{
				Text: "also check b.go",
				Parts: []*mecatlv1.Content{
					{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{0x01, 0x02}},
					{Kind: mecatlv1.Content_KIND_AUDIO, MimeType: "audio/wav", Data: []byte{0x03, 0x04, 0x05}},
				},
			}},
		})
	}()

	var events []*mecatlv1.Event
	var steerAck *mecatlv1.Event
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
		if ev.GetType() == "steer.outcome" {
			steerAck = ev
			// The steer is parked; release the tool so the run drains it at the
			// upcoming boundary and drives the second turn.
			close(block.release)
		}
		if ev.GetType() == "result" {
			break
		}
	}
	_ = stream.CloseSend()
	if err := <-steerSent; err != nil {
		t.Fatalf("Send steer: %v", err)
	}

	// (a) The authoritative ack: the steer was ACCEPTED into the live inbox.
	if steerAck == nil {
		t.Fatalf("no steer.outcome ack on the stream: %v", typesOf(events))
	}
	if got := steerAck.GetSteerOutcome().GetOutcome(); got != mecatlv1.SteerOutcome_STEER_OUTCOME_ACCEPTED {
		t.Fatalf("steer outcome = %v, want ACCEPTED", got)
	}

	// (b) The EvSteer echo carries the COMMITTED text (client-visible).
	var steerEv *mecatlv1.Event
	for _, e := range events {
		if e.GetType() == "steer" {
			steerEv = e
			break
		}
	}
	if steerEv == nil {
		t.Fatalf("no steer echo event on the stream: %v", typesOf(events))
	}
	if got := steerEv.GetSteer().GetText(); got != "also check b.go" {
		t.Fatalf("steer echo text = %q, want %q", got, "also check b.go")
	}
	assertWireSteerParts(t, steerEv.GetSteer().GetParts())

	// (c) The injected message reached the model as an ordinary user turn on
	// the following turn (recorded == streamed == model-view).
	reqsMu.Lock()
	defer reqsMu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2 (the steer feeds the following turn)", len(reqs))
	}
	msgs := reqs[1].Messages
	final := msgs[len(msgs)-1]
	if final.Role != session.RoleUser || final.Text != "also check b.go" {
		t.Fatalf("final replayed message = (%q, %q), want (user, %q)", final.Role, final.Text, "also check b.go")
	}
	if len(final.Parts) != 2 || final.Parts[0].Kind != session.MediaImage || final.Parts[0].MIMEType != "image/png" || string(final.Parts[0].Data) != string([]byte{0x01, 0x02}) || final.Parts[1].Kind != session.MediaAudio || final.Parts[1].MIMEType != "audio/wav" || string(final.Parts[1].Data) != string([]byte{0x03, 0x04, 0x05}) {
		t.Fatalf("provider-facing steer parts = %#v", final.Parts)
	}

	res := lastResult(t, events)
	if res.GetStop() != "end_turn" || res.GetText() != "turn two done" {
		t.Fatalf("result = %+v", res)
	}
}

// TestSteer_ConverseCancelRetracts drives the steer_cancel arm over the wire:
// a stale qualified cancel is refused without touching the pending steer, then
// a cancel naming the live run retracts it. The stream stays alive throughout.
func TestSteer_ConverseCancelRetracts(t *testing.T) {
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

	// Steer, then immediately steer_cancel, both while the tool holds the run
	// (ordered on the same client stream → processed in order).
	framesSent := make(chan error, 1)
	go func() {
		<-block.started
		live, ok := svc.LookupRun(session.SessionID(cs.GetSessionId()))
		if !ok {
			framesSent <- errors.New("live run not registered")
			return
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "scratch that"}},
		}); err != nil {
			framesSent <- err
			return
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_SteerCancel{SteerCancel: &mecatlv1.SteerCancel{
				ExpectedRunId: live.RunID(),
				MessageId:     strings.Repeat("é", 65),
			}},
		}); err != nil {
			framesSent <- err
			return
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_SteerCancel{SteerCancel: &mecatlv1.SteerCancel{ExpectedRunId: "stale-run"}},
		}); err != nil {
			framesSent <- err
			return
		}
		framesSent <- stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_SteerCancel{SteerCancel: &mecatlv1.SteerCancel{ExpectedRunId: live.RunID()}},
		})
	}()

	var events []*mecatlv1.Event
	var outcomes []mecatlv1.SteerOutcome
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
		if ev.GetType() == "steer.outcome" {
			outcomes = append(outcomes, ev.GetSteerOutcome().GetOutcome())
			if len(outcomes) == 4 {
				close(block.release) // every control ack is in: let the run finish
			}
		}
		if ev.GetType() == "result" {
			break
		}
	}
	_ = stream.CloseSend()
	if err := <-framesSent; err != nil {
		t.Fatalf("Send frames: %v", err)
	}

	// The overlong-id and stale cancels report NONE_PENDING but leave the pending
	// steer intact, proven by the following matching cancel returning RETRACTED.
	if len(outcomes) != 4 {
		t.Fatalf("steer outcomes = %v, want [ACCEPTED NONE_PENDING NONE_PENDING RETRACTED]", outcomes)
	}
	if outcomes[0] != mecatlv1.SteerOutcome_STEER_OUTCOME_ACCEPTED ||
		outcomes[1] != mecatlv1.SteerOutcome_STEER_OUTCOME_NONE_PENDING ||
		outcomes[2] != mecatlv1.SteerOutcome_STEER_OUTCOME_NONE_PENDING ||
		outcomes[3] != mecatlv1.SteerOutcome_STEER_OUTCOME_RETRACTED {
		t.Fatalf("steer outcomes = %v, want [ACCEPTED NONE_PENDING NONE_PENDING RETRACTED]", outcomes)
	}
	if hasType(events, "steer") {
		t.Fatalf("a retracted steer must never drain/echo: %v", typesOf(events))
	}
}

// TestSteer_CapabilityAdvertised verifies ServerCapabilities.steer is true
// when the engine's steer knob is armed and false when runtime-disabled, read
// over the gRPC CreateSession echo used by clients.
func TestSteer_CapabilityAdvertised(t *testing.T) {
	on := capsFromCreate(t, newSteerService(t, mockllm.New(mockllm.TextTurn("x")), nil))
	if !on.GetSteer() {
		t.Fatal("steer capability = false, want true (EnableSteer armed)")
	}

	// Steer knob OFF: newSteerService always arms it, so build a plain service.
	off := capsFromCreate(t, newService(t, mockllm.New(mockllm.TextTurn("x")), allowRules()))
	if off.GetSteer() {
		t.Fatal("steer capability = true, want false (EnableSteer off)")
	}
}

// TestSteer_CapabilitySingleSource is AC5.2 half two: the SAME computed bit
// reaches every sink that surfaces it — the CreateSession echo AND the Session
// snapshot re-hydration path (GetSession) — because both read the ONE
// Service.capabilities() value; nothing recomputes per sink.
func TestSteer_CapabilitySingleSource(t *testing.T) {
	svcs := map[string]*server.Service{
		"enabled":  newSteerService(t, mockllm.New(mockllm.TextTurn("x")), nil),
		"disabled": newService(t, mockllm.New(mockllm.TextTurn("x")), allowRules()),
	}
	for name, svc := range svcs {
		t.Run(name, func(t *testing.T) {
			client, cleanup := dialGRPC(t, svc)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			createCaps := cs.GetCapabilities().GetSteer()

			// The re-hydration sink: GetSession's Session snapshot carries the
			// SAME capabilities value CreateSession echoed.
			gs, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: cs.GetSessionId()})
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			snapCaps := gs.GetSession().GetCapabilities().GetSteer()

			if snapCaps != createCaps {
				t.Fatalf("GetSession steer cap = %v, CreateSession said %v — the bit must be ONE composition-computed value, never recomputed per sink", snapCaps, createCaps)
			}
		})
	}
}
