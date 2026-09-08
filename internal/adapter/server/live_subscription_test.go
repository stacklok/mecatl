package server_test

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestFireDelivery_Scenario6_RemoteTUIRendersDeliveryLive verifies AC6.1: a
// REMOTE mecated renders the delivered note live as a delivery card — parity over
// the wire. The gRPC StreamSessionLive handler (the unified transport serving
// embedded AND remote) relays the delivery run's events to a connected client.
// This is the wire-side test: it opens StreamSessionLive over bufconn gRPC (the
// same path a remote mecatui dials), drives a delivery run, and asserts the
// client receives the delivery's EvUserPrompt (the note body) live.
func TestFireDelivery_Scenario6_RemoteTUIRendersDeliveryLive(t *testing.T) {
	svc := newLiveSubscriptionService(t,
		mockllm.TextTurn("ack: delivery received"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin := createLiveSubscriptionOrigin(t, svc)

	cl, cleanup := dialLiveSubscriptionGRPC(t, svc)
	defer cleanup()
	// Warm up the lazy gRPC connection: grpc.NewClient defers the dial until the
	// first RPC. A quick unary GetSession forces the connection + handler goroutine
	// up before we open the server-streaming live subscription (whose handler calls
	// Subscribe asynchronously — without the warmup the first probe events are
	// published before Subscribe runs and are lost).
	warmupLiveConn(t, cl, origin)
	stream, err := cl.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(origin)})
	if err != nil {
		t.Fatalf("StreamSessionLive: %v", err)
	}

	var (
		mu  sync.Mutex
		evs []*mecatlv1.Event
		wg  sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			ev, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		}
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()

	// Probe the subscription is registered BEFORE driving the delivery: the
	// gRPC handler calls Subscribe asynchronously after the client opens the
	// stream, so a delivery published before Subscribe returns is lost. Retry a
	// probe event until it lands — confirming the subscription is live.
	if !probeLiveSubscription(svc, origin, &mu, &evs, 3*time.Second) {
		count, types := liveEventSummary(&mu, &evs)
		t.Fatalf("StreamSessionLive probe did not arrive within 3s — the subscription is not live; got %d events: %v", count, types)
	}

	deliveryNote := "<<<UNTRUSTED\n[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text\n<<<UNTRUSTED\n"
	deliveryDone := driveLiveDeliveryRun(ctx, t, svc, origin, deliveryNote)

	if !waitForLiveEvent(&mu, &evs, "user_prompt", "scheduled task nightly-sync", 3*time.Second) {
		count, types := liveEventSummary(&mu, &evs)
		t.Fatalf("StreamSessionLive did NOT relay the delivery EvUserPrompt within 3s; got %d events: %v", count, types)
	}
	awaitLiveDeliveryPublisher(t, deliveryDone, &mu, &evs, 3*time.Second)

	// AC6.1: the live stream relayed the delivery's EvUserPrompt (the note body)
	// so a connected client renders the delivery card live.
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, ev := range evs {
		if ev.GetType() == "user_prompt" && strings.Contains(ev.GetUserPrompt().GetText(), "scheduled task nightly-sync") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("live stream did NOT relay the delivery EvUserPrompt; got %d events: %v", len(evs), liveEventTypes(evs))
	}
}

// TestFireDelivery_Scenario6_LiveSubscriptionRelaysDeliveryNote verifies AC6.2:
// the live subscription relays the delivery's EvUserPrompt on the wire (the SAME
// recorded note); the other two log-only kinds (EvApproval, EvCompactionArchive)
// stay SKIPPED. The delivery EvUserPrompt is the ONE narrow exception to the
// live-wire log-only skip; a non-delivery EvUserPrompt stays skipped too.
func TestFireDelivery_Scenario6_LiveSubscriptionRelaysDeliveryNote(t *testing.T) {
	svc := newLiveSubscriptionService(t,
		mockllm.TextTurn("ack: delivery received"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin := createLiveSubscriptionOrigin(t, svc)

	cl, cleanup := dialLiveSubscriptionGRPC(t, svc)
	defer cleanup()
	// Warm up the lazy gRPC connection: grpc.NewClient defers the dial until the
	// first RPC. A quick unary GetSession forces the connection + handler goroutine
	// up before we open the server-streaming live subscription (whose handler calls
	// Subscribe asynchronously — without the warmup the first probe events are
	// published before Subscribe runs and are lost).
	warmupLiveConn(t, cl, origin)
	stream, err := cl.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(origin)})
	if err != nil {
		t.Fatalf("StreamSessionLive: %v", err)
	}

	var (
		mu       sync.Mutex
		evs      []*mecatlv1.Event
		received = make(chan struct{}, 1)
		wg       sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			ev, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
			select {
			case received <- struct{}{}:
			default:
			}
		}
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()

	// Probe the subscription is registered BEFORE driving the delivery: the
	// gRPC handler calls Subscribe asynchronously after the client opens the
	// stream, so a delivery published before Subscribe returns is lost. Retry a
	// probe event until it lands — confirming the subscription is live.
	if !probeLiveSubscription(svc, origin, &mu, &evs, 3*time.Second) {
		count, types := liveEventSummary(&mu, &evs)
		t.Fatalf("StreamSessionLive probe did not arrive within 3s — the subscription is not live; got %d events: %v", count, types)
	}

	deliveryNote := "<<<UNTRUSTED\n[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text\n<<<UNTRUSTED\n"
	deliveryDone := driveLiveDeliveryRun(ctx, t, svc, origin, deliveryNote)

	if !waitForLiveEvent(&mu, &evs, "user_prompt", "scheduled task nightly-sync", 3*time.Second) {
		count, types := liveEventSummary(&mu, &evs)
		t.Fatalf("live stream did NOT relay the delivery EvUserPrompt within 3s; got %d events: %v", count, types)
	}
	awaitLiveDeliveryPublisher(t, deliveryDone, &mu, &evs, 3*time.Second)
	if !waitForLiveEventSignal(&mu, &evs, received, "result", "", 3*time.Second) {
		count, types := liveEventSummary(&mu, &evs)
		t.Fatalf("live stream did NOT relay the delivery terminal result within 3s; got %d events: %v", count, types)
	}

	mu.Lock()
	defer mu.Unlock()

	// AC6.2 NEGATIVE: the two other log-only kinds stay SKIPPED on the live wire.
	for _, ev := range evs {
		if ev.GetType() == "approval" {
			t.Fatalf("live stream relayed an EvApproval — the other log-only kinds must stay SKIPPED on the live wire")
		}
		if ev.GetType() == "compaction.archive" {
			t.Fatalf("live stream relayed an EvCompactionArchive — the other log-only kinds must stay SKIPPED on the live wire")
		}
	}

	// AC6.2 NEGATIVE: a non-delivery EvUserPrompt stays skipped. Only the
	// delivery-patterned note is relayed; a harness continuation prompt (the
	// no-progress nudge) is a non-delivery EvUserPrompt and MUST stay skipped.
	// The delivery note is FENCED (renderFireDelivery wraps it in
	// <<<UNTRUSTED…>>>), so a relayed user_prompt must be a fenced delivery note;
	// a bare user prompt (the no-progress nudge) is never fenced and never relayed.
	for _, ev := range evs {
		if ev.GetType() == "user_prompt" {
			text := ev.GetUserPrompt().GetText()
			if !strings.HasPrefix(text, "<<<UNTRUSTED\n[scheduled task ") {
				t.Fatalf("live stream relayed a NON-delivery EvUserPrompt (must stay skipped): %q", text)
			}
		}
	}
}

// TestFireDelivery_Scenario6_TransportProjectionParity verifies AC6.4: remote
// and embedded render the SAME delivery card for the SAME recorded note (one
// projection, two transports). The embedded path and the remote path both dial
// the SAME gRPC StreamSessionLive RPC (the unified bridge), so the proto bytes
// the client receives are identical by construction — the TUI client's
// EventToMsg projects them to the SAME DeliveryNoteMsg. This wire-side test pins
// the contract the projection depends on: the live wire's delivery
// user_prompt.text equals the note the engine RECORDED in the session
// (byte-identical), so the two transports carry the SAME bytes.
func TestFireDelivery_Scenario6_TransportProjectionParity(t *testing.T) {
	svc := newLiveSubscriptionService(t,
		mockllm.TextTurn("ack: delivery received"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin := createLiveSubscriptionOrigin(t, svc)

	cl, cleanup := dialLiveSubscriptionGRPC(t, svc)
	defer cleanup()
	// Warm up the lazy gRPC connection: grpc.NewClient defers the dial until the
	// first RPC. A quick unary GetSession forces the connection + handler goroutine
	// up before we open the server-streaming live subscription (whose handler calls
	// Subscribe asynchronously — without the warmup the first probe events are
	// published before Subscribe runs and are lost).
	warmupLiveConn(t, cl, origin)
	stream, err := cl.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(origin)})
	if err != nil {
		t.Fatalf("StreamSessionLive: %v", err)
	}

	var (
		mu  sync.Mutex
		evs []*mecatlv1.Event
		wg  sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			ev, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		}
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()

	// Probe the subscription is registered BEFORE driving the delivery: the
	// gRPC handler calls Subscribe asynchronously after the client opens the
	// stream, so a delivery published before Subscribe returns is lost. Retry a
	// probe event until it lands — confirming the subscription is live.
	if !probeLiveSubscription(svc, origin, &mu, &evs, 3*time.Second) {
		count, types := liveEventSummary(&mu, &evs)
		t.Fatalf("StreamSessionLive probe did not arrive within 3s — the subscription is not live; got %d events: %v", count, types)
	}

	deliveryNote := "<<<UNTRUSTED\n[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text\n<<<UNTRUSTED\n"
	deliveryDone := driveLiveDeliveryRun(ctx, t, svc, origin, deliveryNote)

	if !waitForLiveEvent(&mu, &evs, "user_prompt", "scheduled task nightly-sync", 3*time.Second) {
		count, types := liveEventSummary(&mu, &evs)
		t.Fatalf("live stream did NOT relay the delivery note within 3s; got %d events: %v", count, types)
	}
	awaitLiveDeliveryPublisher(t, deliveryDone, &mu, &evs, 3*time.Second)

	mu.Lock()
	var liveText string
	for _, ev := range evs {
		if ev.GetType() == "user_prompt" && strings.Contains(ev.GetUserPrompt().GetText(), "scheduled task nightly-sync") {
			liveText = ev.GetUserPrompt().GetText()
			break
		}
	}
	mu.Unlock()
	if liveText == "" {
		t.Fatal("live stream did not relay the delivery user_prompt")
	}

	// Parity: the note the engine RECORDED in the session (the persisted
	// conversation) must byte-match the live wire's user_prompt.text — the two
	// transports (live wire + the session's own recorded history, which is what
	// the durable-log replay and the embedded in-process path both surface)
	// carry the SAME bytes. The TUI client's EventToMsg is a pure function over
	// these bytes, so byte-equivalence ⇒ projection equivalence.
	sess, err := svc.GetSession(ctx, origin)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("delivery session state = %s, want %s", sess.State, session.StateCompleted)
	}
	var recordedText string
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "scheduled task nightly-sync") {
			recordedText = m.Text
			break
		}
	}
	if recordedText == "" {
		t.Fatalf("recorded session conversation does not contain the delivery note")
	}
	if liveText != recordedText {
		// The live wire carries the EvUserPrompt.text (the recorded prompt body),
		// which is the SAME bytes the engine recorded. The recorded conversation
		// message text is the fenced-untrusted note; the live wire carries the
		// SAME text. Assert byte-equivalence.
		t.Fatalf("transport projection drift: live=%q recorded=%q (must be byte-identical)", liveText, recordedText)
	}
}

// TestCallerSeparation_Scenario3_LiveSubscriptionIsOwnerCheckedOverGRPC pins
// AC3.6 at the wire boundary: the gRPC StreamSessionLive handler now threads
// stream.Context() into Service.Subscribe, so a foreign caller's stream is
// refused NotFound and never receives the session owner's live events, while
// the owner's own stream receives them normally.
func TestCallerSeparation_Scenario3_LiveSubscriptionIsOwnerCheckedOverGRPC(t *testing.T) {
	svc := newLiveSubscriptionServiceOwnershipEnforced(t)
	aliceCtx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser})
	sess, err := svc.CreateSessionWithProfile(aliceCtx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cl, cleanup := dialLiveSubscriptionGRPCWithTestAuth(t, svc)
	defer cleanup()

	// Bob's stream must be refused NotFound and receive nothing.
	bobCtx := metadata.AppendToOutgoingContext(context.Background(), testSubjectMetadataKey, "bob")
	bobStream, err := cl.StreamSessionLive(bobCtx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("StreamSessionLive (bob): %v", err)
	}
	if _, rerr := bobStream.Recv(); status.Code(rerr) != codes.NotFound {
		t.Fatalf("bob's StreamSessionLive.Recv = %v, want codes.NotFound", rerr)
	}

	// Alice's own stream succeeds and receives the probe event.
	aliceStreamBase := metadata.AppendToOutgoingContext(context.Background(), testSubjectMetadataKey, "alice")
	aliceStreamCtx, cancelAliceStream := context.WithCancel(aliceStreamBase)
	aliceStream, err := cl.StreamSessionLive(aliceStreamCtx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("StreamSessionLive (alice): %v", err)
	}
	var (
		mu  sync.Mutex
		evs []*mecatlv1.Event
		wg  sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			ev, rerr := aliceStream.Recv()
			if rerr != nil {
				return
			}
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		}
	}()
	defer func() {
		cancelAliceStream()
		wg.Wait()
	}()
	if !probeLiveSubscription(svc, sess.ID, &mu, &evs, 3*time.Second) {
		count, types := liveEventSummary(&mu, &evs)
		t.Fatalf("alice's StreamSessionLive probe did not arrive within 3s; got %d events: %v", count, types)
	}
}

// --- helpers ---

// testSubjectMetadataKey is the outgoing gRPC metadata key the test-local
// auth interceptor (testPrincipalStreamInterceptor) reads to mint a
// session.Principal for the stream context — a stand-in for real OIDC
// verification, since the production principalStream in authn.go is
// unexported.
const testSubjectMetadataKey = "x-test-subject"

// testPrincipalStreamInterceptor mints a session.Principal from the
// x-test-subject incoming metadata key and installs it on the stream's
// context, mirroring authn.go's principalStream (unexported, so this is a
// small test-local equivalent) closely enough to exercise
// Service.Subscribe's ctx-gated ownership check end-to-end over gRPC.
func testPrincipalStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if subs := md.Get(testSubjectMetadataKey); len(subs) > 0 && subs[0] != "" {
				p := &session.Principal{Issuer: "https://idp.example", Subject: subs[0], GrantType: session.GrantTypeUser}
				ctx = session.WithPrincipal(ctx, p)
			}
		}
		return handler(srv, testPrincipalStream{ServerStream: ss, ctx: ctx})
	}
}

// testPrincipalStream overrides a ServerStream's Context, mirroring authn.go's
// unexported principalStream.
type testPrincipalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s testPrincipalStream) Context() context.Context { return s.ctx }

// newLiveSubscriptionServiceOwnershipEnforced mirrors newLiveSubscriptionService
// but sets OwnershipEnforced so Service.Subscribe's ctx-gated GetSession check
// is live.
func newLiveSubscriptionServiceOwnershipEnforced(t *testing.T, turns ...mockllm.Turn) *server.Service {
	t.Helper()
	storeDir := t.TempDir()
	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	llm := mockllm.New(turns...)
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  store,

		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
		OwnershipEnforced:   true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// dialLiveSubscriptionGRPCWithTestAuth mirrors dialLiveSubscriptionGRPC but
// installs testPrincipalStreamInterceptor so a caller can carry a principal on
// stream.Context() via the x-test-subject outgoing metadata key.
func dialLiveSubscriptionGRPCWithTestAuth(t *testing.T, svc *server.Service) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.StreamInterceptor(testPrincipalStreamInterceptor()))
	mecatlv1.RegisterHarnessServiceServer(gs, server.NewHarnessServer(svc))
	go func() { _ = gs.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return mecatlv1.NewHarnessServiceClient(conn), func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	}
}

// warmupLiveConn forces the lazy gRPC connection to establish by issuing a quick
// unary GetSession before the server-streaming live subscription is opened. The
// dial-on-first-RPC behaviour of grpc.NewClient means the StreamSessionLive
// handler's Subscribe runs asynchronously after the client opens the stream;
// warming up the connection first makes that window short and predictable.
func warmupLiveConn(t *testing.T, cl mecatlv1.HarnessServiceClient, id session.SessionID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = cl.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: string(id)})
}

// createLiveSubscriptionOrigin creates an origin session and drives it to
// completed so a delivery run can deliver into it.
func createLiveSubscriptionOrigin(t *testing.T, svc *server.Service) session.SessionID {
	t.Helper()
	sess, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(context.Background(), sess.ID, "initial prompt", nil)
	if err != nil {
		t.Fatalf("StartRunContent (origin): %v", err)
	}
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
	return sess.ID
}

// driveLiveDeliveryRun drives a delivery run with the note and publishes its
// events to the origin's live subscription (mirroring deliverFireResult). It
// returns a channel that closes after the publisher has drained the run and
// FinishRun has persisted its terminal state.
func driveLiveDeliveryRun(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, note string) <-chan struct{} {
	t.Helper()
	run, err := svc.StartRunContent(ctx, id, note, nil)
	if err != nil {
		t.Fatalf("StartRunContent (delivery): %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range run.Events() {
			svc.PublishSessionEvent(id, ev)
		}
		svc.FinishRun(id, run)
	}()
	t.Cleanup(func() { <-done })
	return done
}

// probeLiveSubscription publishes a probe event repeatedly until it arrives on
// the stream (confirming the gRPC handler's Subscribe has run). The handler calls
// Subscribe asynchronously after the client opens the stream, so a probe published
// before Subscribe returns is lost; retrying until the probe lands is the robust
// synchronization. Returns true on confirmation, false on timeout.
func probeLiveSubscription(svc *server.Service, id session.SessionID, mu *sync.Mutex, evs *[]*mecatlv1.Event, timeout time.Duration) bool {
	probe := session.Event{Type: session.EvNoProgress, Text: "live-subscription-probe"}
	deadline := time.Now().Add(timeout)
	for {
		if waitForLiveEvent(mu, evs, "no_progress", "live-subscription-probe", 100*time.Millisecond) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		svc.PublishSessionEvent(id, probe)
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForLiveEvent polls the event slice under mu for an event of the given type
// whose text (the top-level Event.text for no_progress/etc., or the user_prompt
// text for a user_prompt) contains substr, with a deadline. Returns true on found.
func waitForLiveEvent(mu *sync.Mutex, evs *[]*mecatlv1.Event, typ, substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		mu.Lock()
		for _, ev := range *evs {
			if ev.GetType() != typ {
				continue
			}
			// A user_prompt carries its text in the UserPrompt submessage; other
			// kinds (no_progress, etc.) carry it in the top-level Event.text.
			if up := ev.GetUserPrompt(); up != nil {
				if strings.Contains(up.GetText(), substr) {
					mu.Unlock()
					return true
				}
				continue
			}
			if strings.Contains(ev.GetText(), substr) {
				mu.Unlock()
				return true
			}
		}
		mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitLiveDeliveryPublisher bounds the asynchronous delivery publisher while
// preserving driveLiveDeliveryRun's cleanup fallback for assertion failures.
func awaitLiveDeliveryPublisher(t *testing.T, done <-chan struct{}, mu *sync.Mutex, evs *[]*mecatlv1.Event, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		count, types := liveEventSummary(mu, evs)
		t.Fatalf("delivery publisher did not drain and finish within %s; got %d live events: %v", timeout, count, types)
	}
}

// waitForLiveEventSignal waits for an event receiver notification rather than
// polling, so a terminal event establishes that all preceding wire events were
// received in order.
func waitForLiveEventSignal(mu *sync.Mutex, evs *[]*mecatlv1.Event, received <-chan struct{}, typ, substr string, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		mu.Lock()
		for _, ev := range *evs {
			if ev.GetType() != typ {
				continue
			}
			if up := ev.GetUserPrompt(); up != nil {
				if strings.Contains(up.GetText(), substr) {
					mu.Unlock()
					return true
				}
				continue
			}
			if strings.Contains(ev.GetText(), substr) {
				mu.Unlock()
				return true
			}
		}
		mu.Unlock()

		select {
		case <-received:
		case <-timer.C:
			return false
		}
	}
}

// liveEventSummary snapshots the event diagnostics without leaving mu locked if
// the caller reports a fatal assertion and receiver cleanup must join.
func liveEventSummary(mu *sync.Mutex, evs *[]*mecatlv1.Event) (int, []string) {
	mu.Lock()
	defer mu.Unlock()
	return len(*evs), liveEventTypes(*evs)
}

// liveEventTypes returns the type strings of the events for diagnostics.
func liveEventTypes(evs []*mecatlv1.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.GetType())
	}
	return out
}

// newLiveSubscriptionService builds a Service with jsonlstore + mockllm for the
// live-subscription wire tests (mirrors newSubscriptionService).
func newLiveSubscriptionService(t *testing.T, turns ...mockllm.Turn) *server.Service {
	t.Helper()
	storeDir := t.TempDir()
	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	llm := mockllm.New(turns...)
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  store,

		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// dialLiveSubscriptionGRPC stands up an in-memory gRPC server (bufconn) backed
// by svc — the SAME wire path a remote mecatui dials. Returns the client + cleanup.
func dialLiveSubscriptionGRPC(t *testing.T, svc *server.Service) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(gs, server.NewHarnessServer(svc))
	go func() { _ = gs.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return mecatlv1.NewHarnessServiceClient(conn), func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	}
}
