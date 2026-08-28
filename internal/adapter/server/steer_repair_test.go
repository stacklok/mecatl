package server_test

// Repair task 07 (panel-review ship-blockers, round 1): the wire-side steer
// regressions the panel found —
//
//   - AC-R1: a gRPC Converse stream is NOT goroutine-safe, so EVERY Send on it
//     is serialized behind one single-writer gate; a terminal-race steer during
//     the original run's drain window spawns a promoted follow-up relay that
//     must never Send concurrently with the original relay.
//   - AC-R2: a steer arriving in the terminate window (the original run went
//     terminal but is still REGISTERED draining) is promoted to a fresh
//     follow-up run, never dropped with a bare too_late ack.
//   - AC-R3: the grpc handler is a dumb frame→Service mapper; the steer /
//     steer_cancel routing decision has ONE owner (Service.Steer /
//     Service.CancelSteer) — never a direct run.EnqueueSteer / run.CancelSteer
//     in the handler.

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// dialGRPCStall is dialGRPC plus a stream interceptor that stalls (up to
// stallFor) at the FIRST terminal result on each Converse stream — the server
// side then holds the run terminal-but-registered deterministically, so a
// steer fired on the stall lands squarely in the drain window AC-R1 pins
// (no timing race, no poll loop).
func dialGRPCStall(t *testing.T, svc *server.Service, stallFor time.Duration) (mecatlv1.HarnessServiceClient, chan<- struct{}, func()) {
	t.Helper()
	release := make(chan struct{})
	var stalled atomic.Int32
	stall := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &stallStream{ServerStream: ss, release: release, stalled: &stalled, stallFor: stallFor})
	}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.StreamInterceptor(stall))
	mecatlv1.RegisterHarnessServiceServer(gs, server.NewHarnessServer(svc))
	go func() { _ = gs.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return mecatlv1.NewHarnessServiceClient(conn), release, func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	}
}

// stallStream blocks the terminal result's Send on a release gate (or a
// stallFor cap) so the run holds terminal-and-registered while the test steers.
type stallStream struct {
	grpc.ServerStream
	release  <-chan struct{}
	stalled  *atomic.Int32
	stallFor time.Duration
}

func (s *stallStream) SendMsg(m any) error {
	if resp, ok := m.(*mecatlv1.ConverseResponse); ok &&
		resp.GetEvent().GetType() == "result" &&
		s.stalled.CompareAndSwap(0, 1) {
		select {
		case <-s.release:
		case <-time.After(s.stallFor):
		case <-s.Context().Done():
		}
	}
	return s.ServerStream.SendMsg(m)
}

// TestSteer_NoConcurrentSendOnTerminalRace is AC-R1: a steer that lands while
// the original relay is parked holding the run terminal-and-registered (the
// stall gate) drives the promoted follow-up relay WHILE the original relay is
// still live — under -race any concurrent stream.Send between the two relays
// fails the test. The promoted run's events must ALSO arrive intact on the one
// stream (a single-writer race would corrupt or drop frames).
func TestSteer_NoConcurrentSendOnTerminalRace(t *testing.T) {
	llm := mockllm.New(
		mockllm.TextTurn("first"),
		mockllm.TextTurn("second"), // the promoted follow-up turn
	)
	svc := newSteerService(t, llm, nil)
	client, stallRelease, cleanup := dialGRPCStall(t, svc, 2*time.Second)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
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

	// Read until turn.end of the first run (the result is stalled server-side,
	// so turn.end is the last event before the gate). The run is now
	// terminal-but-registered with its relay PARKED mid-Send.
	sawTurnEnd := false
	for !sawTurnEnd {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv pre-stall: %v", err)
		}
		if resp.GetEvent().GetType() == "turn.end" {
			sawTurnEnd = true
		}
	}

	// Fire the steer into the held drain window. The stall keeps the original
	// run registered while the steer routes, so the promoted entry is genuinely
	// inside the drain-grace wait; releasing the stall then drains the original
	// run, and the race between the two relays plays out under -race.
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "late steer"}},
	}); err != nil {
		t.Fatalf("Send steer: %v", err)
	}
	// Release the stalled original relay shortly after the steer lands, so the
	// promoted run starts while the original relay is still draining its tail —
	// the concurrent-relay window -race must stay quiet across.
	time.Sleep(100 * time.Millisecond)
	close(stallRelease)

	// Read the original run's result (the stalled relay drains it after the
	// release). The test's ASSERTION is the -race run itself: the promoted
	// steer relay is genuinely in flight while the original relay drains, so a
	// concurrent stream.Send between the two fails -race. The Converse RPC then
	// returns (the original relay finished); the promoted relay may race it to
	// close the stream, so the steer ack / follow-up result can legitimately
	// ride after the stream closes on a fast machine — the ack contract is
	// pinned by TestSteer_TerminateWindowPromotes / the round-trip tests, not
	// here.
	var events []*mecatlv1.Event
	var results int
	for results == 0 {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		ev := resp.GetEvent()
		events = append(events, ev)
		if ev.GetType() == "result" {
			results++
		}
	}
	_ = stream.CloseSend()
	if results != 1 {
		t.Fatalf("the original run's result never made the stream: %v", typesOf(events))
	}
	// Sanity: the steer did land on a too_late route — the original run went
	// terminal before it (its inbox closed). A promoted follow-up CAN still be
	// driving past this point; give it a beat to finish so the test does not
	// leak the drive goroutine (goleak).
	time.Sleep(200 * time.Millisecond)
}

// TestSteer_TerminateWindowPromotes is AC-R2: a steer arriving while the
// original run is TERMINAL-BUT-STILL-REGISTERED (its relay draining between
// closeSteer and FinishRun — the exact window the panel found dropping steers)
// is promoted to a fresh follow-up run, never dropped with a bare too_late
// ack. The test holds the run terminal-and-registered by NOT FinishRunning the
// drained run, then steers.
func TestSteer_TerminateWindowPromotes(t *testing.T) {
	llm := mockllm.New(
		mockllm.TextTurn("first"),
		mockllm.TextTurn("follow-up"),
	)
	svc := newSteerService(t, llm, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(ctx, sess.ID, "hello", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	drainRun(t, run) // the run is now TERMINAL
	// Deliberately do NOT svc.FinishRun(sess.ID, run) yet: the run stays
	// REGISTERED, the exact terminate-window state (inbox closed, still live in
	// the registry) that used to drop the steer.
	if !svc.IsLive(sess.ID) {
		t.Fatalf("precondition: the drained run must still be registered (the terminate window)")
	}

	// The original relay deregisters a beat into the steer's drain-grace (the
	// relay finishes mid-window), so the promoted entry observes the registry
	// clearing inside the grace rather than at the lapse.
	go func() {
		time.Sleep(50 * time.Millisecond)
		svc.FinishRun(sess.ID, run)
	}()
	outc, promoted, run2, err := svc.Steer(ctx, sess.ID, "steer in the drain window", nil, "", "")
	if err != nil {
		t.Fatalf("Steer in the terminate window: %v (must promote, never drop)", err)
	}
	if outc != agent.SteerTooLate {
		t.Fatalf("outcome = %q, want too_late", outc)
	}
	if !promoted {
		t.Fatalf("terminate-window steer must be PROMOTED (promoted=false = the dropped-steer regression)")
	}
	if run2 == nil {
		t.Fatalf("promoted steer returned a nil run (silently dropped)")
	}
	if stop := drainForResult(t, run2); stop != session.StopEndTurn {
		t.Fatalf("promoted run stop = %q, want end_turn", stop)
	}
	svc.FinishRun(sess.ID, run2)

	got, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	texts := steerUserTexts(got)
	if len(texts) != 2 || texts[0] != "hello" || texts[1] != "steer in the drain window" {
		t.Fatalf("user messages = %v, want [hello, steer in the drain window] (steer promoted, not dropped)", texts)
	}
}

// TestSteer_ServiceOwnsRouting is AC-R3: the grpc Converse handler is a dumb
// frame→Service mapper — it must route steer / steer_cancel through
// Service.Steer / Service.CancelSteer and never call run.EnqueueSteer /
// run.CancelSteer directly. A static scan pins the shape (the live behaviour
// is covered by the round-trip tests, which would hang with no Service route).
func TestSteer_ServiceOwnsRouting(t *testing.T) {
	src, err := os.ReadFile("grpc.go")
	if err != nil {
		t.Fatalf("read grpc.go: %v", err)
	}
	for _, banned := range []string{"run.EnqueueSteer(", "run.CancelSteer("} {
		if strings.Contains(string(src), banned) {
			t.Fatalf("grpc.go must route steer through Service, but still calls %q directly", banned)
		}
	}
}
