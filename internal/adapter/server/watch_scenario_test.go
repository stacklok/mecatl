package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// --- fixture ----------------------------------------------------------------

// watchService builds a Service whose durable event log is log, over a mockllm
// that answers every turn with one short text reply.
func watchService(t *testing.T, log port.EventLog, ownership bool) *server.Service {
	t.Helper()
	llm := mockllm.New(
		mockllm.ChunksTurn(mockllm.TextChunk("all "), mockllm.TextChunk("done"), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ChunksTurn(mockllm.TextChunk("again"), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ChunksTurn(mockllm.TextChunk("third"), mockllm.DoneChunk(session.StopEndTurn)),
	)
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
		}),
		Store:               memstore.New(),
		EventLog:            log,
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		OwnershipEnforced:   ownership,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// driveRun sends one prompt through the gRPC Converse relay and drains it to
// completion. The relay is what persists the durable log (the loop is
// storage-agnostic), so a run has to go through it for a watch to have anything
// to replay.
//
// It returns its error rather than calling t.Fatalf because several tests drive a
// run from a goroutine (a live follow needs the run to happen while the watch is
// attached), and FailNow from a non-test goroutine does not fail the test.
func driveRun(ctx context.Context, client mecatlv1.HarnessServiceClient, id session.SessionID, text string) error {
	stream, err := client.Converse(ctx)
	if err != nil {
		return fmt.Errorf("converse: %w", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{
		Prompt: &mecatlv1.Prompt{SessionId: string(id), Text: text},
	}}); err != nil {
		return fmt.Errorf("send prompt: %w", err)
	}
	_ = stream.CloseSend()
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("converse recv: %w", err)
		}
	}
}

// driveRunSync drives a run and fails the test on the calling goroutine.
func driveRunSync(t *testing.T, client mecatlv1.HarnessServiceClient, id session.SessionID, text string) {
	t.Helper()
	if err := driveRun(context.Background(), client, id, text); err != nil {
		t.Fatalf("drive run %q: %v", text, err)
	}
}

// driveRunBackground drives a run from a goroutine, reporting a failure with
// Errorf (never Fatalf — see driveRun) and closing the returned channel when the
// run has finished.
func driveRunBackground(t *testing.T, client mecatlv1.HarnessServiceClient, id session.SessionID, text string) (done chan struct{}) {
	done = make(chan struct{})
	go func() {
		defer close(done)
		if err := driveRun(context.Background(), client, id, text); err != nil {
			t.Errorf("background run %q: %v", text, err)
		}
	}()
	return done
}

// waitRun blocks until a background run has finished.
//
// Every test that starts one MUST wait: t.Cleanup closes the gRPC connection, and
// a run still draining its Converse stream at that moment fails with a spurious
// "client connection is closing" rather than the assertion under test.
func waitRun(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("background run did not finish")
	}
}

// watchedRun is a session that has already had one completed run persisted.
func watchedRun(t *testing.T, log port.EventLog) (*server.Service, mecatlv1.HarnessServiceClient, session.SessionID) {
	t.Helper()
	svc := watchService(t, log, false)
	client, cleanup := dialGRPC(t, svc)
	t.Cleanup(cleanup)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	driveRunSync(t, client, sess.ID, "first")
	return svc, client, sess.ID
}

// envelopeLabel renders one envelope as a comparable string: the phase plus
// either the event kind (and text, for a delta) or the phase-only marker.
func envelopeLabel(phase string, ev *mecatlv1.Event) string {
	if ev == nil {
		return phase + "/-"
	}
	if ev.GetType() == string(session.EvMessageDelta) {
		return phase + "/" + ev.GetType() + ":" + ev.GetText()
	}
	return phase + "/" + ev.GetType()
}

// --- AC7.1 ------------------------------------------------------------------

// TestSDKServerEnablers_Scenario7_ReplayThenFollow is AC7.1: a watch from the
// beginning replays the durable log in append order, transitions to live, and
// follows through the run's terminal result.
//
// The whole point of the feature is that this is ONE operation. The pre-cursor
// composition — read the log, then subscribe — has a window between the two calls
// in which an append is silently lost, and no test of either half alone can see
// it. So the assertion is the joint one: the replay matches the durable log
// exactly, a boundary is announced, and events appended AFTER the watch attached
// arrive on the same stream.
func TestSDKServerEnablers_Scenario7_ReplayThenFollow(t *testing.T) {
	log := memstore.NewEventLog()
	svc, client, id := watchedRun(t, log)

	// The durable log's own order is the oracle for the replay half.
	wantReplay := readEventLog(t, log, id)
	if len(wantReplay) == 0 {
		t.Fatal("the completed run persisted no events; the fixture proves nothing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}

	var replayed []session.EventType
	var boundaries int
	var live []session.EventType
	// Cursors are positions, so a position delivered in the replay must never
	// reappear in the follow. This is the seam the two-phase read creates, and the
	// failure it can produce is a DUPLICATE rather than a loss: a follow that
	// resumed from the watch's original cursor instead of where the replay stopped
	// would re-deliver the whole transcript, in order, looking healthy to any
	// assertion that only counts or only checks ordering.
	replayCursors := map[port.Cursor]bool{}
	secondRunStarted := false
	sawLiveResult := false
	var secondRun chan struct{}

	for env, iterErr := range envelopes {
		if iterErr != nil {
			t.Fatalf("watch: %v", iterErr)
		}
		switch {
		case env.Event == nil && env.Phase == server.WatchPhaseLive:
			boundaries++
			// Only now is the watch provably caught up, so a run started here can
			// only land in the live phase. Doing it before the boundary would make
			// the replay/live split a race rather than a contract.
			if !secondRunStarted {
				secondRunStarted = true
				secondRun = driveRunBackground(t, client, id, "second")
			}
		case env.Phase == server.WatchPhaseReplay:
			replayed = append(replayed, env.Event.Type)
			replayCursors[env.Cursor] = true
		case env.Phase == server.WatchPhaseLive:
			if replayCursors[env.Cursor] {
				t.Fatalf("live envelope %d re-delivered replay cursor %q; the follow must resume where the replay stopped, not re-read from the start",
					len(live), env.Cursor)
			}
			live = append(live, env.Event.Type)
			if env.Event.Type == session.EvResult {
				sawLiveResult = true
			}
		default:
			t.Fatalf("unexpected phase %q", env.Phase)
		}
		if sawLiveResult {
			break
		}
	}

	if secondRun != nil {
		waitRun(t, secondRun)
	}
	if len(replayed) != len(wantReplay) {
		t.Fatalf("replayed %d events, want the durable log's %d", len(replayed), len(wantReplay))
	}
	for i, ev := range wantReplay {
		if replayed[i] != ev.Type {
			t.Fatalf("replay[%d] = %s, want %s — the replay must be the durable log in APPEND order", i, replayed[i], ev.Type)
		}
	}
	if boundaries != 1 {
		t.Fatalf("saw %d replay→live boundary frames, want exactly 1", boundaries)
	}
	if !sawLiveResult {
		t.Fatal("the follow never reached the second run's terminal EvResult")
	}
	if len(live) == 0 {
		t.Fatal("no live events followed the boundary")
	}
	// Exactly the second run's records, no more: the durable log's total minus the
	// prefix the replay already delivered.
	if all := readEventLog(t, log, id); len(replayed)+len(live) != len(all) {
		t.Fatalf("replay(%d)+live(%d) = %d envelopes, want the durable log's %d — the follow must deliver only what the replay did not",
			len(replayed), len(live), len(replayed)+len(live), len(all))
	}
}

// TestSDKServerEnablers_Scenario7_BoundaryFrameDoesNotWaitForAnEvent pins the
// reason the boundary is a frame of its own rather than a flag on the next event.
//
// port.LogRecord.Live marks records that arrived after a read caught up — but
// only once such a record ARRIVES. On an idle or finished session none ever does,
// so a client keyed on that flag would wait forever to learn it was caught up.
// This is the case that would regress silently if the two-phase read were
// "simplified" into a single Follow read.
func TestSDKServerEnablers_Scenario7_BoundaryFrameDoesNotWaitForAnEvent(t *testing.T) {
	log := memstore.NewEventLog()
	svc, _, id := watchedRun(t, log)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	// Nothing more will ever be appended to this session, so reaching the boundary
	// at all is the assertion.
	for env, iterErr := range envelopes {
		if iterErr != nil {
			t.Fatalf("watch: %v", iterErr)
		}
		if env.Event == nil && env.Phase == server.WatchPhaseLive {
			return
		}
	}
	t.Fatal("the watch drained an idle session's log without announcing the replay→live boundary")
}

// --- AC7.2 ------------------------------------------------------------------

// TestSDKServerEnablers_Scenario7_ResumeFromCursorHasNoGap is AC7.2: a watch
// resumed from a cursor delivers exactly the events after it, with no gap and no
// reordering.
//
// "No gap and no reordering" is asserted by RECOMPOSITION rather than by
// inspecting cursors: the prefix one watch delivered plus the suffix a resumed
// watch delivered must equal the durable log exactly, once, in order. A cursor
// that were off by one in either direction would either duplicate or drop the
// record at the seam, and both show up as a mismatch here — whereas a test that
// only counted events would pass while silently re-delivering one.
func TestSDKServerEnablers_Scenario7_ResumeFromCursorHasNoGap(t *testing.T) {
	log := memstore.NewEventLog()
	svc, _, id := watchedRun(t, log)
	want := readEventLog(t, log, id)
	if len(want) < 4 {
		t.Fatalf("fixture persisted only %d events; too few to split", len(want))
	}
	cut := len(want) / 2

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	collect := func(after port.Cursor, stopAfter int) (kinds []session.EventType, last port.Cursor) {
		envelopes, err := svc.WatchSessionEvents(ctx, id, after, "")
		if err != nil {
			t.Fatalf("WatchSessionEvents(after=%q): %v", after, err)
		}
		last = after
		for env, iterErr := range envelopes {
			if iterErr != nil {
				t.Fatalf("watch: %v", iterErr)
			}
			if env.Event == nil {
				// The boundary frame: everything durable has been delivered.
				if stopAfter == 0 {
					return kinds, last
				}
				continue
			}
			kinds = append(kinds, env.Event.Type)
			// A client persists the cursor of the envelope it PROCESSED, which is
			// exactly what it hands back on resume.
			last = env.Cursor
			if stopAfter > 0 && len(kinds) == stopAfter {
				return kinds, last
			}
		}
		return kinds, last
	}

	prefix, resume := collect("", cut)
	if len(prefix) != cut {
		t.Fatalf("prefix has %d events, want %d", len(prefix), cut)
	}
	if resume == "" {
		t.Fatal("the prefix watch yielded no cursor to resume from")
	}
	suffix, _ := collect(resume, 0)

	got := append(append([]session.EventType{}, prefix...), suffix...)
	if len(got) != len(want) {
		t.Fatalf("prefix(%d)+suffix(%d) = %d events, want the durable log's %d — a resume must neither drop nor duplicate at the seam",
			len(prefix), len(suffix), len(got), len(want))
	}
	for i, ev := range want {
		if got[i] != ev.Type {
			t.Fatalf("recomposed[%d] = %s, want %s", i, got[i], ev.Type)
		}
	}

	t.Run("a tampered cursor is rejected, never coerced", func(t *testing.T) {
		// Cursor opacity is a promise, and the generation inside a cursor is what
		// keeps it: a decoded, hand-edited token must fail loudly rather than
		// resolve to "approximately the right place", which is indistinguishable
		// from the right place until data is already lost.
		envelopes, err := svc.WatchSessionEvents(ctx, id, port.Cursor("not-a-cursor!"), "")
		if err != nil {
			// An eager rejection is equally acceptable.
			if !errors.Is(err, port.ErrCursorMalformed) {
				t.Fatalf("tampered cursor error = %v, want ErrCursorMalformed", err)
			}
			return
		}
		var seen error
		for _, iterErr := range envelopes {
			if iterErr != nil {
				seen = iterErr
				break
			}
			t.Fatal("a tampered cursor delivered an envelope; it must be rejected")
		}
		if !errors.Is(seen, port.ErrCursorMalformed) {
			t.Fatalf("tampered cursor error = %v, want ErrCursorMalformed", seen)
		}
	})
}

// --- AC7.3 ------------------------------------------------------------------

// TestSDKServerEnablers_Scenario7_WatchTransportParity is AC7.3: gRPC and SSE
// deliver the same envelopes in the same order for the same session.
//
// Parity is structural — both handlers consume Service.WatchSessionEvents — so
// this test's job is to keep it that way. The failure it exists to catch is a
// future "small" divergence in one handler: a filter applied on one wire only, a
// phase renamed, a cursor omitted, the boundary frame dropped because it "has no
// event". Each transport is read to the boundary, which makes the comparison
// deterministic without racing a live run.
func TestSDKServerEnablers_Scenario7_WatchTransportParity(t *testing.T) {
	log := memstore.NewEventLog()
	svc, client, id := watchedRun(t, log)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var viaGRPC []string
	stream, err := client.WatchSessionEvents(ctx, &mecatlv1.WatchSessionEventsRequest{SessionId: string(id)})
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	for {
		resp, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("grpc watch recv: %v", recvErr)
		}
		viaGRPC = append(viaGRPC, resp.GetCursor()+" "+envelopeLabel(resp.GetPhase(), resp.GetEvent()))
		if resp.GetEvent() == nil && resp.GetPhase() == server.WatchPhaseLive {
			break
		}
	}

	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/sessions/"+string(id)+"/watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse watch: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse watch status = %d, want 200", resp.StatusCode)
	}

	var viaSSE []string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame mecatlv1.WatchSessionEventsResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatalf("decode sse frame %q: %v", line, err)
		}
		viaSSE = append(viaSSE, frame.GetCursor()+" "+envelopeLabel(frame.GetPhase(), frame.GetEvent()))
		if frame.GetEvent() == nil && frame.GetPhase() == server.WatchPhaseLive {
			break
		}
	}

	if len(viaGRPC) != len(viaSSE) {
		t.Fatalf("gRPC delivered %d envelopes, SSE delivered %d", len(viaGRPC), len(viaSSE))
	}
	for i := range viaGRPC {
		if viaGRPC[i] != viaSSE[i] {
			t.Fatalf("envelope %d: gRPC %q != SSE %q — both transports consume one Service method and must not diverge", i, viaGRPC[i], viaSSE[i])
		}
	}
	if len(viaGRPC) < 2 {
		t.Fatalf("only %d envelopes compared; the fixture proves nothing", len(viaGRPC))
	}
}

// TestSDKServerEnablers_Scenario7_WatchUnsupportedIsHonest pins the refusal a
// non-cursor backend earns. Degrading to "replay everything from the beginning"
// would be a correctness problem dressed as a performance one: the client would
// re-process events it had already acted on and never learn its cursor meant
// nothing.
func TestSDKServerEnablers_Scenario7_WatchUnsupportedIsHonest(t *testing.T) {
	svc := watchService(t, plainEventLog{inner: memstore.NewEventLog()}, false)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WatchSessionEvents(context.Background(), sess.ID, "", ""); !errors.Is(err, server.ErrWatchUnsupported) {
		t.Fatalf("watch over a non-cursor log = %v, want ErrWatchUnsupported", err)
	}

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	stream, err := client.WatchSessionEvents(context.Background(), &mecatlv1.WatchSessionEventsRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("open watch: %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Unimplemented {
		t.Fatalf("grpc watch code = %v, want Unimplemented", status.Code(err))
	}

	noLog := watchService(t, nil, false)
	sess2, err := noLog.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noLog.WatchSessionEvents(context.Background(), sess2.ID, "", ""); !errors.Is(err, server.ErrNoEventLog) {
		t.Fatalf("watch with no log = %v, want ErrNoEventLog", err)
	}
}

// --- AC7.4 ------------------------------------------------------------------

// TestSDKServerEnablers_Scenario7_WatchOwnershipEnforced is AC7.4: a watch is
// ownership-checked, and a caller who may not read the session is refused.
//
// The check is EAGER — before any envelope — and it asks the same question
// GetSession asks. A watch is at least as revealing as a read: the durable log
// holds the whole transcript, verdicts and user prompts included.
func TestSDKServerEnablers_Scenario7_WatchOwnershipEnforced(t *testing.T) {
	svc := watchService(t, memstore.NewEventLog(), true)
	alice := session.WithPrincipal(context.Background(), &session.Principal{
		Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser,
	})
	bob := session.WithPrincipal(context.Background(), &session.Principal{
		Issuer: "https://issuer.example", Subject: "bob", GrantType: session.GrantTypeUser,
	})
	sess, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := svc.WatchSessionEvents(alice, sess.ID, "", ""); err != nil {
		t.Fatalf("owner watch: %v", err)
	}
	if _, err := svc.WatchSessionEvents(bob, sess.ID, "", ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("non-owner watch = %v, want ErrNotFound (existence is concealed, not merely refused)", err)
	}
	if _, err := svc.WatchSessionEvents(context.Background(), sess.ID, "", ""); err == nil {
		t.Fatal("unauthenticated watch succeeded; ownership must be enforced")
	}

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	stream, err := client.WatchSessionEvents(bob, &mecatlv1.WatchSessionEventsRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("open watch: %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.NotFound {
		t.Fatalf("grpc non-owner watch code = %v, want NotFound", status.Code(err))
	}

	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	req, err := http.NewRequestWithContext(bob, http.MethodGet, srv.URL+"/v1/sessions/"+string(sess.ID)+"/watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("sse non-owner watch status = %d, want 404 (refused BEFORE the 200 that would commit a stream)", resp.StatusCode)
	}
}

// --- AC7.5 ------------------------------------------------------------------

// TestADR_0250_SlowWatcherTerminatesWithoutBackpressure is AC7.5: a slow watcher
// is terminated with a resumable error and never backpressures the run; the run
// completes normally.
//
// This is the behaviour a durable cursor exists to make possible. Service.
// Subscribe — the pre-cursor live registry — DROPS events for a slow subscriber:
// the stream stays open and the client never learns it is missing data. Both
// halves are asserted, because either alone is satisfiable by the wrong
// implementation: dropping would pass "the run completes", and a blocking write
// would pass "nothing was dropped".
func TestADR_0250_SlowWatcherTerminatesWithoutBackpressure(t *testing.T) {
	defer server.ShrinkWatchDeliveryForTest(2, 150*time.Millisecond)()

	log := memstore.NewEventLog()
	svc, client, id := watchedRun(t, log)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}

	var termErr error
	delivered := 0
	for env, iterErr := range envelopes {
		if iterErr != nil {
			termErr = iterErr
			break
		}
		delivered++
		if delivered == 1 {
			// Stall. A second run appends into the log while this watcher refuses to
			// drain, which is the only way to make "too slow" happen at all.
			<-driveRunBackground(t, client, id, "second")
			time.Sleep(4 * 150 * time.Millisecond)
		}
		_ = env
	}

	if !errors.Is(termErr, server.ErrWatchLagging) {
		t.Fatalf("slow watcher ended with %v, want ErrWatchLagging — it must be TERMINATED, never silently dropped", termErr)
	}

	// The run is unaffected: it ran to a clean end, and BOTH runs' events are all
	// durable. A watcher reads storage, so it structurally cannot backpressure the
	// append path — this asserts that structure was not traded away for a blocking
	// write, which would have stalled the second run behind the stalled watcher
	// and left its terminal result unrecorded.
	sess, err := svc.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.State == session.StateFailed || sess.State == session.StateCancelled {
		t.Fatalf("session state = %s; a slow watcher must not affect the run", sess.State)
	}
	logged := readEventLog(t, log, id)
	results := 0
	for _, ev := range logged {
		if ev.Type == session.EvResult {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("durable log holds %d terminal results, want 2 (both runs recorded despite the stalled watcher)", results)
	}

	// The termination is RESUMABLE, and the resume point is the client's own last
	// cursor — never a server-side one, which would skip the buffered tail the
	// client never received.
	if code := server.ClassifyErrorCodeForTest(termErr); code != "watch_lagging" {
		t.Fatalf("error code = %q, want watch_lagging", code)
	}
}

// --- AC7.6 ------------------------------------------------------------------

// TestADR_0250_AppendFailureTerminatesLocalWatchers is AC7.6: a durable append
// failure terminates watchers in that process with ActivityGapError without
// advancing their cursor, and the owned run continues.
//
// "Without advancing their cursor" is structural rather than a value to check:
// the error carries NO cursor, so there is nothing for a client to resume from
// except the last envelope it actually received. That is asserted directly — a
// server-supplied cursor here would be the bug, because it would point past
// envelopes still sitting in the delivery buffer.
func TestADR_0250_AppendFailureTerminatesLocalWatchers(t *testing.T) {
	inner := memstore.NewEventLog()
	log := newCountingCursorLog(inner)
	svc, client, id := watchedRun(t, log)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}

	var termErr error
	var secondRun chan struct{}
	sawBoundary := false
	for env, iterErr := range envelopes {
		if iterErr != nil {
			termErr = iterErr
			break
		}
		if env.Event == nil && env.Phase == server.WatchPhaseLive && !sawBoundary {
			sawBoundary = true
			// Break the log, then drive a run: the first relayed append fails and
			// must fault this attached watcher.
			log.failAppends(true)
			secondRun = driveRunBackground(t, client, id, "second")
		}
	}

	var gap *server.ActivityGapError
	if !errors.As(termErr, &gap) {
		t.Fatalf("watch ended with %v, want *ActivityGapError", termErr)
	}
	if !errors.Is(termErr, server.ErrActivityGap) {
		t.Fatalf("ActivityGapError does not classify as ErrActivityGap: %v", termErr)
	}
	if code := server.ClassifyErrorCodeForTest(termErr); code != "activity_gap" {
		t.Fatalf("error code = %q, want activity_gap", code)
	}
	// The terminal reaches the CLIENT — as a gRPC status message and an SSE error
	// field — so it must not carry the backend's prose. A raw store error embeds
	// infrastructure detail (a Redis dial address, a jsonlstore path); GetSession
	// already withholds it for that reason, and the same applies at least as
	// strongly to a field the client reads. The operator keeps every byte: the
	// cause goes to the durable gap marker and to the recorder's append-failure
	// WARN.
	if strings.Contains(termErr.Error(), errAppendRejected.Error()) {
		t.Fatalf("client-facing terminal %q leaks the raw backend cause", termErr)
	}

	// TIER 1 was attempted: one best-effort durable gap marker. It is also where
	// the cause SURVIVES — the client-facing terminal above is bare, so if the
	// marker did not carry the backend error the operator would have lost it.
	_, gaps, _ := log.counts()
	if gaps == 0 {
		t.Fatal("no gap marker was attempted; the best-effort cross-process tier never ran")
	}
	markerReason := ""
	for rec, readErr := range inner.ReadAfter(context.Background(), id, "", port.ReadOptions{}) {
		if readErr != nil {
			t.Fatalf("read back the log: %v", readErr)
		}
		if rec.Kind == port.LogRecordGap {
			markerReason = rec.GapReason
		}
	}
	if !strings.Contains(markerReason, errAppendRejected.Error()) {
		t.Fatalf("durable gap marker reason = %q, want the backend cause — redacting the client-facing error must not lose it for the operator", markerReason)
	}

	// The owned run continued. A broken log must not break a live run — that is
	// existing relay discipline, and it is the half of AC7.6 that a "fail the run
	// on append error" implementation would violate while still passing the
	// termination assertion above.
	if secondRun != nil {
		waitRun(t, secondRun)
	}
	log.failAppends(false)
	sess, err := svc.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.State == session.StateFailed {
		t.Fatalf("session state = %s; an append failure must not fail the run", sess.State)
	}
}

// --- AC7.7 ------------------------------------------------------------------

// TestADR_0250_GapMarkerObservedCrossProcess is AC7.7: the best-effort durable
// gap marker, when it lands, is observed by watchers in a SECOND process.
//
// Two independent Services over two independent jsonlstore handles on ONE
// directory model two replicas — the same shape Scenario 6 uses, and the only
// honest one available offline: what makes cross-process observation work is that
// the marker is a durable record, not an in-memory notification, so a second
// adapter instance reading the same bytes is exactly the property under test.
//
// Note what this test does NOT claim. It proves the marker is observable WHEN IT
// LANDS. ADR 0250 decision 6 is deliberately weaker than an absolute: a failed
// append consumes no position, so it leaves nothing for another process to see,
// and a total backend outage cannot record its own failure. Writer A's
// AppendEvent fails while its AppendGap succeeds precisely because that is the
// LIKELY failure the tier covers — one rejected record — and not the outage it
// does not.
func TestADR_0250_GapMarkerObservedCrossProcess(t *testing.T) {
	dir := t.TempDir()

	writerStore, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("writer store: %v", err)
	}
	readerStore, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("reader store: %v", err)
	}

	// Process A: its event appends fail, its gap marker lands.
	writerLog := newCountingCursorLog(writerStore)
	writerSvc, writerClient, id := watchedRun(t, writerLog)

	// Process B: a separate Service over a separate handle on the same bytes.
	readerSvc := watchService(t, readerStore, false)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	envelopes, err := readerSvc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("cross-process watch: %v", err)
	}

	sawGap := false
	sawBoundary := false
	var secondRun chan struct{}
	for env, iterErr := range envelopes {
		if iterErr != nil {
			t.Fatalf("cross-process watch: %v", iterErr)
		}
		if env.Phase == server.WatchPhaseGap {
			sawGap = true
			if env.Event != nil {
				t.Fatal("a gap frame carries an event; a gap is a delivery-envelope phase, never a session.Event (ADR 0250 decision 5)")
			}
			if env.Cursor == "" {
				t.Fatal("a gap frame carries no cursor; it occupies a real append position and cursors must advance past it")
			}
			break
		}
		if env.Event == nil && env.Phase == server.WatchPhaseLive && !sawBoundary {
			sawBoundary = true
			writerLog.failAppends(true)
			secondRun = driveRunBackground(t, writerClient, id, "second")
		}
	}
	if secondRun != nil {
		waitRun(t, secondRun)
	}
	if !sawGap {
		t.Fatal("the second process never observed the durable gap marker writer A recorded")
	}
	_ = writerSvc
}

// --- AC7.8 ------------------------------------------------------------------

// TestADR_0250_OneAppendPerEvent is AC7.8: exactly one append occurs per event,
// and cursor assignment happens at the persistence chokepoint rather than at the
// emit site.
//
// The invariant is one append per appended RECORD, which is NOT one append per
// emitted event: RunEventRecorder deliberately COALESCES streaming text deltas
// into bounded chunks, so a turn of many delta events legitimately becomes one
// record. This test pins both halves at once, because "one append per event"
// read literally would forbid the coalescing the recorder exists to do.
func TestADR_0250_OneAppendPerEvent(t *testing.T) {
	inner := memstore.NewEventLog()
	log := newCountingCursorLog(inner)
	_, _, id := watchedRun(t, log)

	appended, gaps, legacy := log.counts()
	records := readEventLog(t, log, id)

	if appended != len(records) {
		t.Fatalf("%d AppendEvent calls produced %d records; exactly one append per appended record", appended, len(records))
	}
	if gaps != 0 {
		t.Fatalf("%d gap markers on a healthy run, want 0", gaps)
	}
	// ONE write path. With a cursor backend wired, the chokepoint must mint the
	// position through the cursor seam; a surviving legacy Append would mean two
	// write paths that agree only by discipline.
	if legacy != 0 {
		t.Fatalf("%d legacy Append calls; the persistence chokepoint must use the cursor seam when the backend offers it", legacy)
	}

	// The coalescing survived: the relay streamed the original chunk boundaries
	// while the durable log holds one merged record.
	deltas := 0
	for _, ev := range records {
		if ev.Type == session.EvMessageDelta {
			deltas++
		}
	}
	if deltas != 1 {
		t.Fatalf("durable message-delta records = %d, want 1 coalesced record (the mockllm turn streamed two chunks)", deltas)
	}

	// Positions are minted by the LOG, one per record, and reach the client on the
	// envelope. Distinct, non-empty cursors are what a client's resume depends on;
	// a locally-invented or reused position would show up here.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svc := watchService(t, log, false)
	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	seen := map[port.Cursor]bool{}
	count := 0
	for env, iterErr := range envelopes {
		if iterErr != nil {
			t.Fatalf("watch: %v", iterErr)
		}
		if env.Event == nil {
			break
		}
		if env.Cursor == "" {
			t.Fatalf("envelope %d carries an empty cursor", count)
		}
		if seen[env.Cursor] {
			t.Fatalf("cursor %q was delivered twice; positions must be one per record", env.Cursor)
		}
		seen[env.Cursor] = true
		count++
	}
	if count != len(records) {
		t.Fatalf("watch delivered %d events, want the log's %d", count, len(records))
	}
}

// --- AC7.9 ------------------------------------------------------------------

// TestSDKServerEnablers_Scenario7_LegacyStreamEndpointsUnchanged is AC7.9: the
// existing StreamSessionEvents and StreamSessionLive endpoints behave identically
// to today.
//
// Adding a third read path is the moment the other two are most likely to be
// "unified" into it. The two properties that would break first are asserted: the
// replay endpoint still ENDS (it is bounded — a client waits on its EOF), and it
// still relays ALL events including the three log-only kinds, which the live wire
// filters and the watch, being a read-back, must not.
func TestSDKServerEnablers_Scenario7_LegacyStreamEndpointsUnchanged(t *testing.T) {
	log := memstore.NewEventLog()
	svc, client, id := watchedRun(t, log)
	want := readEventLog(t, log, id)

	logOnly := 0
	for _, ev := range want {
		switch ev.Type {
		case session.EvApproval, session.EvCompactionArchive, session.EvUserPrompt:
			logOnly++
		}
	}
	if logOnly == 0 {
		t.Fatal("fixture recorded no log-only events; the relay-discipline half proves nothing")
	}

	t.Run("gRPC StreamSessionEvents replays every kind and ends", func(t *testing.T) {
		stream, err := client.StreamSessionEvents(context.Background(), &mecatlv1.StreamSessionEventsRequest{SessionId: string(id)})
		if err != nil {
			t.Fatalf("StreamSessionEvents: %v", err)
		}
		var got []session.EventType
		for {
			resp, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				break // the stream ENDS — the property a follow would have destroyed
			}
			if recvErr != nil {
				t.Fatalf("recv: %v", recvErr)
			}
			got = append(got, session.EventType(resp.GetType()))
		}
		if len(got) != len(want) {
			t.Fatalf("replayed %d events, want %d", len(got), len(want))
		}
		for i, ev := range want {
			if got[i] != ev.Type {
				t.Fatalf("event[%d] = %s, want %s", i, got[i], ev.Type)
			}
		}
	})

	t.Run("SSE /events replays every kind and closes", func(t *testing.T) {
		srv := httptest.NewServer(server.NewHTTPHandler(svc))
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/v1/sessions/" + string(id) + "/events")
		if err != nil {
			t.Fatalf("sse: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body) // returns because the handler CLOSES
		if err != nil {
			t.Fatalf("read sse body: %v", err)
		}
		frames := 0
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "data: ") {
				frames++
			}
		}
		if frames != len(want) {
			t.Fatalf("sse replayed %d frames, want %d", frames, len(want))
		}
		// The legacy frame shape is a bare Event, NOT a watch envelope: a client
		// parsing it must not suddenly need to unwrap {event,cursor,phase}.
		first := ""
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "data: ") {
				first = strings.TrimPrefix(line, "data: ")
				break
			}
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(first), &probe); err != nil {
			t.Fatalf("decode first frame: %v", err)
		}
		if _, wrapped := probe["phase"]; wrapped {
			t.Fatal("the legacy /events frame grew a watch envelope; it must stay a bare Event")
		}
		if _, wrapped := probe["cursor"]; wrapped {
			t.Fatal("the legacy /events frame grew a cursor; it must stay a bare Event")
		}
	})

	t.Run("StreamSessionLive still filters the log-only kinds", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, err := client.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(id)})
		if err != nil {
			t.Fatalf("StreamSessionLive: %v", err)
		}
		received := make(chan session.EventType, 64)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				resp, recvErr := stream.Recv()
				if recvErr != nil {
					return
				}
				select {
				case received <- session.EventType(resp.GetType()):
				default:
				}
			}
		}()

		// The server-side Subscribe registers when the handler runs, which is
		// asynchronous to the client's stream open — so publish on a tick until
		// something is delivered rather than racing a single publish. Every tick
		// publishes the log-only kind FIRST, so a relayed EvApproval would arrive
		// ahead of the EvTurnEnd that ends the loop.
		deadline := time.After(10 * time.Second)
		var first session.EventType
	wait:
		for {
			svc.PublishSessionEvent(id, session.Event{Type: session.EvApproval})
			svc.PublishSessionEvent(id, session.Event{Type: session.EvTurnEnd})
			select {
			case first = <-received:
				break wait
			case <-deadline:
				t.Fatal("no live event arrived")
			case <-time.After(20 * time.Millisecond):
			}
		}
		cancel()
		<-done

		if first != session.EvTurnEnd {
			t.Fatalf("first live event = %s, want %s — EvApproval is published first each tick, so anything else means the log-only skip regressed", first, session.EvTurnEnd)
		}
		for {
			select {
			case ev := <-received:
				if ev == session.EvApproval {
					t.Fatal("StreamSessionLive relayed EvApproval; the live wire's log-only skip is unchanged behaviour")
				}
			default:
				return
			}
		}
	})
}

// TestADR_0250_WatchEnvelopeSurvivesAnInvalidUTF8Cursor pins the mapper's
// mechanical UTF-8 backstop on the one watch field that is not harness-authored.
//
// A cursor is BACKEND-owned. The four in-tree backends mint ASCII, so only a
// third-party port.CursorEventLog can produce an invalid one — and a protobuf
// string field REJECTS invalid UTF-8 at marshal time, which is exactly how issue
// #402 killed a live Converse stream with codes.Internal. Repairing the token
// does corrupt it; that is the better failure, because a corrupt cursor is
// rejected loudly at the next resume while a dead stream takes the whole live
// view with it.
func TestADR_0250_WatchEnvelopeSurvivesAnInvalidUTF8Cursor(t *testing.T) {
	inner := memstore.NewEventLog()
	_, _, id := watchedRun(t, inner)
	svc := watchService(t, badCursorLog{CursorEventLog: inner}, false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	stream, err := client.WatchSessionEvents(ctx, &mecatlv1.WatchSessionEventsRequest{SessionId: string(id)})
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	frames := 0
	for {
		resp, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("recv after %d frames: %v — an invalid cursor must not fail marshaling", frames, recvErr)
		}
		frames++
		if !utf8.ValidString(resp.GetCursor()) {
			t.Fatalf("frame %d delivered an invalid-UTF-8 cursor %q", frames, resp.GetCursor())
		}
		if resp.GetEvent() == nil && resp.GetPhase() == server.WatchPhaseLive {
			break
		}
	}
	if frames < 2 {
		t.Fatalf("only %d frames; the fixture proves nothing", frames)
	}
}

// --- AC7.10 -----------------------------------------------------------------

// TestSDKServerEnablers_Scenario7_RunFilterDeliversOneRunAndEveryGap is AC7.10:
// a watch narrowed by run_id delivers exactly that run's events, delivers every
// gap regardless of the filter, and advances its internal position over the
// records it dropped.
//
// The third clause is the one with no other witness. resumeFrom is set BEFORE the
// filter runs, so the follow resumes past records the filter excluded rather than
// re-reading them; moving that assignment inside the delivery branch is the
// natural-looking tidy-up, and nothing else in the suite would notice. The
// fixture therefore ends the replay on a FILTERED-OUT record, so the boundary
// cursor can only be right if the position advanced over it.
func TestSDKServerEnablers_Scenario7_RunFilterDeliversOneRunAndEveryGap(t *testing.T) {
	log := memstore.NewEventLog()
	svc, client, id := watchedRun(t, log)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Run A is already durable. Its id has to be discovered from the log: the
	// server mints run ids, so a test cannot choose one in advance.
	runA := ""
	for _, ev := range readEventLog(t, log, id) {
		if ev.RunID != "" {
			runA = ev.RunID
			break
		}
	}
	if runA == "" {
		t.Fatal("no run id on any recorded event; the filter has nothing to select")
	}

	// A durable gap inside the replay window. Appended directly rather than by
	// faulting an append, because faulting would also TERMINATE this watcher
	// (tier 2) and the frame is what is under test.
	if _, err := log.AppendGap(ctx, id, "injected replay gap"); err != nil {
		t.Fatalf("AppendGap: %v", err)
	}
	// Run B lands AFTER the gap, so the last record in the log is one the filter
	// excludes.
	driveRunSync(t, client, id, "second")

	lastCursor := port.Cursor("")
	for rec, err := range log.ReadAfter(ctx, id, "", port.ReadOptions{}) {
		if err != nil {
			t.Fatalf("read back the log: %v", err)
		}
		lastCursor = rec.Cursor
	}

	envelopes, err := svc.WatchSessionEvents(ctx, id, "", runA)
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	next, stop := iter.Pull2(envelopes)
	defer stop()

	gaps, events, boundary := 0, 0, port.Cursor("")
	for {
		env, iterErr, ok := next()
		if !ok {
			t.Fatal("watch ended before the boundary frame")
		}
		if iterErr != nil {
			t.Fatalf("watch failed during replay: %v", iterErr)
		}
		if env.Event == nil && env.Phase == server.WatchPhaseLive {
			boundary = env.Cursor
			break
		}
		if env.Phase == server.WatchPhaseGap {
			gaps++
			continue
		}
		events++
		if env.Event.RunID != runA {
			t.Fatalf("filtered watch delivered an event from run %q, want only %q", env.Event.RunID, runA)
		}
	}
	if events == 0 {
		t.Fatal("the filter delivered no events at all; it selects nothing rather than one run")
	}
	if gaps != 1 {
		t.Fatalf("delivered %d gap frames, want 1 — a gap is delivered WHATEVER the filter says, because a failed append left no record to attribute to a run", gaps)
	}
	if boundary != lastCursor {
		t.Fatalf("boundary cursor = %q, want the last record in the log %q — the watch position must advance over filtered-out records, or the follow re-reads them", boundary, lastCursor)
	}

	// The live half. Run C is excluded too, so the only thing that may arrive is
	// the gap appended after it — which simultaneously proves the follow is awake,
	// that it read and dropped run C, and that gaps bypass the filter live as well
	// as in replay.
	driveRunSync(t, client, id, "third")
	if _, err := log.AppendGap(ctx, id, "injected live gap"); err != nil {
		t.Fatalf("AppendGap: %v", err)
	}
	env, iterErr, ok := next()
	if !ok || iterErr != nil {
		t.Fatalf("live follow ended (ok=%v) with %v, want the gap frame", ok, iterErr)
	}
	if env.Phase != server.WatchPhaseGap || env.Event != nil {
		t.Fatalf("live frame = %s/%v, want an event-less gap — run C's events must be filtered out and the gap must not be", env.Phase, env.Event)
	}
}

// --- AC7.11 -----------------------------------------------------------------

// TestSDKServerEnablers_Scenario7_TerminalErrorIsValidSSE is AC7.11: a
// stream-terminal error is DELIVERABLE to a conforming SSE client, on both the
// watch route and the older replay route.
//
// A terminal frame written without a `data: ` prefix is not SSE at all: the
// EventSource grammar splits a line into `field: value` at the first colon, so a
// bare `{"code":...}` parses as the unrecognised field `{"code"` and is
// DISCARDED. The client sees the stream fall silent and cannot tell a delivery
// gap from a clean end — which is precisely the failure ADR 0250 exists to
// abolish, reintroduced one layer down at the transport. Producing the right
// error and framing it unreadably is the same bug as not producing it.
//
// The assertion parses like a CONFORMING CLIENT — it reads the `event:` tag and
// the `data:` payload — rather than scanning the raw body for a substring, which
// would pass on the malformed framing too.
//
// The terminal chosen is activity_gap rather than watch_lagging because it is the
// one reachable over SSE deterministically: lagging needs the delivery buffer to
// fill, and an SSE handler drains into a socket buffer that absorbs a test-sized
// run, so a stalled reader never applies backpressure. Both terminals go through
// the same writeSSEError helper and take their code from the same classifyError,
// so the framing is covered; watch_lagging's own production is pinned at the
// service level by AC7.5.
func TestSDKServerEnablers_Scenario7_TerminalErrorIsValidSSE(t *testing.T) {
	t.Run("watch route surfaces activity_gap", func(t *testing.T) {
		log := newCountingCursorLog(memstore.NewEventLog())
		svc, client, id := watchedRun(t, log)
		srv := httptest.NewServer(server.NewHTTPHandler(svc))
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		resp := openSSE(ctx, t, srv.URL+"/v1/sessions/"+string(id)+"/watch")
		defer func() { _ = resp.Body.Close() }()

		frames := make(chan sseFrame, 64)
		go scanSSE(resp.Body, frames)

		// Drain to the boundary, then break the log and drive a run: the first
		// relayed append fails and faults this attached watcher.
		for f := range frames {
			if f.tag == "error" {
				t.Fatalf("unexpected early error frame %q", f.data)
			}
			var env mecatlv1.WatchSessionEventsResponse
			if err := json.Unmarshal([]byte(f.data), &env); err != nil {
				t.Fatalf("decode %q: %v", f.data, err)
			}
			if env.GetEvent() == nil && env.GetPhase() == server.WatchPhaseLive {
				break
			}
		}
		log.failAppends(true)
		done := driveRunBackground(t, client, id, "second")

		frame := awaitErrorFrame(t, frames)
		if frame.Code != "activity_gap" {
			t.Fatalf("sse terminal code = %q, want activity_gap", frame.Code)
		}
		if frame.Error == "" {
			t.Fatal("sse terminal frame carries no error text")
		}
		if strings.Contains(frame.Error, errAppendRejected.Error()) {
			t.Fatalf("sse terminal frame %q leaks the raw backend cause to the client", frame.Error)
		}
		waitRun(t, done)
		log.failAppends(false)
	})

	// The same defect was INHERITED from the older replay route, which had the
	// identical unprefixed write and the identical absence of coverage. One shared
	// helper fixes both, so both are pinned; a client of /events could not see a
	// mid-replay storage fault either.
	t.Run("replay route surfaces a mid-stream fault", func(t *testing.T) {
		svc := watchService(t, failingReadLog{inner: memstore.NewEventLog()}, false)
		srv := httptest.NewServer(server.NewHTTPHandler(svc))
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		sess, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		resp := openSSE(ctx, t, srv.URL+"/v1/sessions/"+string(sess.ID)+"/events")
		defer func() { _ = resp.Body.Close() }()

		frames := make(chan sseFrame, 64)
		go scanSSE(resp.Body, frames)
		frame := awaitErrorFrame(t, frames)
		if frame.Error == "" {
			t.Fatalf("replay-route error frame carries no error text: %+v", frame)
		}
	})
}

// --- SSE reading helpers ------------------------------------------------------

// sseFrame is one parsed Server-Sent Event: its optional `event:` tag and its
// `data:` payload. Parsing the tag is the point — a client routes on it, and a
// test that only greps for `data:` cannot tell a well-formed error frame from a
// success frame that happens to contain the same bytes.
type sseFrame struct {
	tag  string
	data string
}

// scanSSE parses an SSE body into frames and closes out at end of stream.
func scanSSE(body io.Reader, out chan<- sseFrame) {
	defer close(out)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	tag := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			tag = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			out <- sseFrame{tag: tag, data: strings.TrimPrefix(line, "data: ")}
			tag = ""
		}
	}
}

// awaitErrorFrame consumes frames until the `event: error` terminal arrives,
// failing if the stream ends without one.
func awaitErrorFrame(t *testing.T, frames <-chan sseFrame) (frame struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}) {
	t.Helper()
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("SSE stream ended with no `event: error` frame carrying a `data:` payload — a terminal the client cannot parse is a terminal the client never receives")
			}
			if f.tag != "error" {
				continue
			}
			if err := json.Unmarshal([]byte(f.data), &frame); err != nil {
				t.Fatalf("decode sse error payload %q: %v", f.data, err)
			}
			return frame
		case <-time.After(20 * time.Second):
			t.Fatal("timed out waiting for the SSE terminal frame")
		}
	}
}

// openSSE issues the GET and asserts the stream opened.
func openSSE(ctx context.Context, t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse GET %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse GET %s status = %d, want 200", url, resp.StatusCode)
	}
	return resp
}
