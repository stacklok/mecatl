package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// getHostileServer wraps a healthy MCP POST endpoint with a GET-hostile edge:
// every standalone GET gets a 200 text/event-stream response that is closed
// immediately (no events, no retry hints) — the gateway shape that kills the
// SDK's standalone SSE stream while POST request/response traffic stays
// healthy. The counter records how many GETs arrived.
type getHostileServer struct {
	url  string
	gets *atomic.Int32
}

func newGetHostileServer(t *testing.T) *getHostileServer {
	t.Helper()
	// The healthy POST half reuses the package's established echo-server
	// handler (it also registers the test resources/prompts, inert here —
	// these tests only call echo + count GETs + assert reconnect diag).
	mcpHandler := newMCPHandler()
	gets := new(atomic.Int32)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			// 200 + the SSE content type, then close: the friendliest possible
			// "accepted then dropped" — a hostile gateway shape the SDK cannot
			// distinguish from a flaky stream, so its reconnect loop retries.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpSrv := httptest.NewServer(wrapped)
	t.Cleanup(httpSrv.Close)
	return &getHostileServer{url: httpSrv.URL, gets: gets}
}

// newPreHeaderCloseServer is a DIFFERENT GET-hostile shape than
// newGetHostileServer: rather than every GET completing a 200 response with
// an empty body, only the FIRST GET does (so the initial synchronous
// standalone-SSE handshake inside Connect succeeds and a live Server comes
// back, exactly like an ordinary GET-hostile gateway's opening move) — every
// GET AFTER that hijacks the raw connection and closes it before writing any
// HTTP response at all, so the client sees a transport-level failure
// (EOF/connection reset), not a response. This is the shape a gateway that
// starts hard-resetting mid-session takes, and is what
// sseMonitorRoundTripper.RoundTrip's error branch (not its response-body
// branch) must observe — the bug being characterized is that, before that
// branch existed, none of these later failures ever counted toward
// hostility, so Hostile() never tripped and every reconnect reopened the
// doomed GET indefinitely.
func newPreHeaderCloseServer(t *testing.T) *getHostileServer {
	t.Helper()
	mcpHandler := newMCPHandler()
	gets := new(atomic.Int32)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			n := gets.Add(1)
			if n == 1 {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				return
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support Hijack")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("Hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpSrv := httptest.NewServer(wrapped)
	t.Cleanup(httpSrv.Close)
	return &getHostileServer{url: httpSrv.URL, gets: gets}
}

// TestGetHostileGatewayReconnectsTransparently is the CHARACTERIZATION test
// for the issue's claim (ADR 0326): against a GET-hostile gateway with the
// standalone SSE stream ENABLED (the ADR 0057 default), the SDK's SSE
// reconnect loop exhausts its retry budget and fails the WHOLE connection —
// POST included — so the next tool call rides the ADR 0056 withSession →
// reconnect path and succeeds transparently, with exactly one reconnecting/
// reconnected diagnostic pair. The session is recovered, not permanently
// lost, but the reconnect pays a fresh initialize (a new Mcp-Session-Id:
// server-side session state is lost).
//
// As of ADR 0327 the churn does NOT repeat forever: sseHealthTracker observes
// the SAME consecutive zero-byte GET closes this test drives (it trips at
// earlyCloseThreshold, well before the SDK's own 6-GET retry budget
// exhausts), so by the time this reconnect's dial runs, the verdict is
// already hostile and the reconnected session never reopens the standalone
// GET. That is the intended behavior change ADR 0327 exists to produce — the
// "churn repeats" residual cost this test used to characterize is exactly
// what auto-detection removes. The GET count therefore PLATEAUS instead of
// growing past the initial round.
func TestGetHostileGatewayReconnectsTransparently(t *testing.T) {
	diag := &recordingDiag{}
	h := newGetHostileServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{Name: "rs", URL: h.url, Timeout: 2 * time.Second}, diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Baseline: POST works even though the initial GET was closed.
	res := callEcho(context.Background(), t, s, "first")
	if res.IsError || res.Content != "echo:first" {
		t.Fatalf("baseline echo = %+v, want echo:first", res)
	}

	// The SDK's handleSSE retries the dropped GET (default maxRetries=5), then
	// fails the whole connection. Wait for the budget to exhaust: 6 GETs total
	// (1 initial + 5 retries).
	eventually(t, 30*time.Second, func() bool { return h.gets.Load() >= 6 },
		"the SSE reconnect budget never exhausted (want >= 6 GETs)")

	// The next call must transparently reconnect and succeed — the ADR 0056
	// mitigation the issue's "loses the MCP session" overstates.
	res = callEcho(context.Background(), t, s, "second")
	if res.IsError || res.Content != "echo:second" {
		t.Fatalf("post-kill echo = %+v, want echo:second via transparent reconnect", res)
	}
	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want exactly 1", got)
	}
	if got := diag.count("mcp server reconnected"); got != 1 {
		t.Errorf("reconnected lines = %d, want exactly 1", got)
	}
	if got := diag.count("mcp server reconnect failed"); got != 0 {
		t.Errorf("reconnect-failed lines = %d, want 0", got)
	}

	// ADR 0327: auto-detection has already tripped by this point (it needs
	// only earlyCloseThreshold consecutive hostile GETs, far fewer than the
	// 6 the SDK's own budget required), so the reconnected session's dial
	// suppresses the standalone GET. Give any would-be GET a generous window,
	// then assert the count never grew past what round 1 produced.
	afterReconnect := h.gets.Load()
	time.Sleep(3 * time.Second)
	if got := h.gets.Load(); got != afterReconnect {
		t.Errorf("GET count grew after reconnect (%d -> %d); auto-detect (ADR 0327) should have suppressed the reopened stream", afterReconnect, got)
	}
	if !s.sseHealth.Hostile() {
		t.Error("sseHealth.Hostile() = false, want true after the observed churn")
	}
	if got := diag.count("mcp: standalone SSE stream auto-disabled"); got != 1 {
		t.Errorf("auto-disabled WARN lines = %d, want exactly 1", got)
	}

	// The suppressed stream must not affect ordinary POST traffic going
	// forward — a further call still succeeds, with no more reconnect churn.
	res = callEcho(context.Background(), t, s, "third")
	if res.IsError || res.Content != "echo:third" {
		t.Fatalf("post-trip echo = %+v, want echo:third", res)
	}
	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines after the post-trip call = %d, want still exactly 1", got)
	}
}

// TestDisableNotificationsOptsOutOfStandaloneGET is the ADR 0326 fix test:
// connected with ServerConfig.DisableNotifications against the SAME
// GET-hostile gateway, the client NEVER opens the standalone GET (zero GETs
// observed across calls), the tool call succeeds on the first try, and no
// reconnect diagnostic fires — the kill→reconnect→kill churn is gone. The
// trade-off (honest): list-changed notifications for this server no longer
// arrive; the cached tool/resource/prompt snapshot is static until a
// reconnect (the pre-ADR-0057 contract).
func TestDisableNotificationsOptsOutOfStandaloneGET(t *testing.T) {
	diag := &recordingDiag{}
	h := newGetHostileServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{
		Name:                 "rs",
		URL:                  h.url,
		Timeout:              2 * time.Second,
		DisableNotifications: true,
	}, diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Several calls, all first-try successes; give any would-be GET a window.
	for _, text := range []string{"a", "b", "c"} {
		res := callEcho(context.Background(), t, s, text)
		if res.IsError || res.Content != "echo:"+text {
			t.Fatalf("echo(%q) = %+v, want echo:%s", text, res, text)
		}
	}
	// A GET, if one were opened, would be retried by the SDK within this
	// window (its initial retry backoff is sub-second); 3s is generous.
	time.Sleep(3 * time.Second)

	if got := h.gets.Load(); got != 0 {
		t.Errorf("standalone GETs observed = %d, want 0 (DisableNotifications must suppress the stream)", got)
	}
	if got := diag.count("mcp server reconnecting"); got != 0 {
		t.Errorf("reconnecting lines = %d, want 0 (no kill → no churn)", got)
	}
}

// TestAutoDisablesOnPreHeaderConnectionClose is the characterization test for
// the OTHER GET-hostile shape (a transport-level failure — EOF/connection
// reset before any response, per newPreHeaderCloseServer): auto-detection
// (ADR 0327) must observe THIS shape too, not only a completed 200-then-
// empty-body response. Without sseMonitorRoundTripper's error branch
// recording it, Hostile() would never trip and every reconnect would reopen
// the doomed GET indefinitely.
func TestAutoDisablesOnPreHeaderConnectionClose(t *testing.T) {
	diag := &recordingDiag{}
	h := newPreHeaderCloseServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{Name: "rs", URL: h.url, Timeout: 2 * time.Second}, diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	res := callEcho(context.Background(), t, s, "first")
	if res.IsError || res.Content != "echo:first" {
		t.Fatalf("baseline echo = %+v, want echo:first", res)
	}

	// The tracker trips within a few consecutive attempts (earlyCloseThreshold),
	// well before the SDK's own retry budget exhausts — same timing shape as
	// the 200-then-close case. Tripping does not, by itself, cut the SDK's own
	// in-flight retry loop short (ADR 0327's rejected alternative); it only
	// changes what the NEXT dial does.
	eventually(t, 10*time.Second, func() bool { return s.sseHealth.Hostile() },
		"sseHealth never tripped against a pre-header-closing gateway")

	// The SDK's own connectSSE retry loop absorbs all 5 retries for this ONE
	// failing reconnect attempt internally (1 initial success + 5 failing
	// retries = 6 GETs total) before reporting failure and poisoning the
	// connection — mirroring TestGetHostileGatewayReconnectsTransparently's
	// budget-exhaustion wait.
	eventually(t, 30*time.Second, func() bool { return h.gets.Load() >= 6 },
		"the SSE reconnect budget never exhausted (want >= 6 GETs)")

	// A subsequent call transparently reconnects once it observes the drop —
	// c.fail() runs asynchronously in the SDK's own goroutine, so give a short
	// window rather than assuming the very first post-exhaustion call races
	// ahead of it (an earlier call may still complete on the not-yet-failed
	// session; that's fine, it just means the drop surfaces on the next one).
	eventually(t, 10*time.Second, func() bool {
		res := callEcho(context.Background(), t, s, "second")
		return !res.IsError && res.Content == "echo:second" && diag.count("mcp server reconnecting") == 1
	}, "post-kill echo never triggered exactly one transparent reconnect")

	afterReconnect := h.gets.Load()
	time.Sleep(3 * time.Second)
	if got := h.gets.Load(); got != afterReconnect {
		t.Errorf("GET attempt count grew after reconnect (%d -> %d); auto-detect should have suppressed further attempts", afterReconnect, got)
	}
}
