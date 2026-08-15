package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ---- recording diagnostics test double (mirrors llmresilience's pattern) ----

// diagRecord is one captured diagnostics line.
type diagRecord struct {
	level port.Level
	msg   string
	args  []any
}

// recordingDiag is a slice-recording port.Diagnostics test double, concurrency-
// safe because the reconnect path logs from the call goroutine while the test
// drains on another. Copied from internal/adapter/llmresilience/diagnostics_test.go
// (not cross-imported — that is a different package).
type recordingDiag struct {
	mu      sync.Mutex
	records []diagRecord
}

func (d *recordingDiag) Log(_ context.Context, level port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, diagRecord{level: level, msg: msg, args: args})
}

func (d *recordingDiag) With(args ...any) port.Diagnostics {
	return &boundDiag{parent: d, bound: args}
}

type boundDiag struct {
	parent *recordingDiag
	bound  []any
}

func (b *boundDiag) Log(ctx context.Context, level port.Level, msg string, args ...any) {
	merged := make([]any, 0, len(b.bound)+len(args))
	merged = append(merged, b.bound...)
	merged = append(merged, args...)
	b.parent.Log(ctx, level, msg, merged...)
}

func (b *boundDiag) With(args ...any) port.Diagnostics {
	return &boundDiag{parent: b.parent, bound: append(append([]any{}, b.bound...), args...)}
}

// count returns the number of captured records whose msg contains sub.
func (d *recordingDiag) count(sub string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, r := range d.records {
		if strings.Contains(r.msg, sub) {
			n++
		}
	}
	return n
}

var (
	_ port.Diagnostics = (*recordingDiag)(nil)
	_ port.Diagnostics = (*boundDiag)(nil)
)

// ---- restartable MCP test server ----

// restartableServer is an MCP server whose URL stays stable across "restarts".
// It owns a single net.Listener + http.Server whose Handler is a swappable
// *StreamableHTTPHandler. A "restart" builds a FRESH handler (with a NEW empty
// session map) and atomically swaps it in: the listener/URL are unchanged, but
// the old client's session ID is now unknown to the new handler, so the next
// request returns 404 "session not found" — the SDK's errSessionMissing, i.e.
// the connection-drop class. This mirrors a real server process restart (same
// URL, lost in-memory session state) far more faithfully than tearing the
// listener down (which yields a raw "connection refused" transport error the
// SDK wraps as ErrRejected, NOT a session drop).
type restartableServer struct {
	listener net.Listener
	server   *http.Server
	hmu      sync.Mutex
	handler  http.Handler
}

// newMCPHandler builds a fresh StreamableHTTPHandler over a fresh SDK server
// exposing the echo tool. Each call gets a NEW session map, so a client holding
// a session ID from a prior handler sees 404 "session not found".
func newMCPHandler() http.Handler {
	return newMCPHandlerWithEcho(func(_ context.Context, _ *mcpsdk.CallToolRequest, in echoArgs) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + in.Text}},
		}, nil, nil
	})
}

// newMCPHandlerWithEcho builds a fresh handler whose echo tool runs echoFn. Used
// to swap in an echo that returns IsError (a server-level error, not a drop)
// after a restart. Registers the test resources+prompts too so a restart keeps
// the resource/prompt capability advertised (the resource/prompt reconnect
// tests need them).
func newMCPHandlerWithEcho(echoFn func(context.Context, *mcpsdk.CallToolRequest, echoArgs) (*mcpsdk.CallToolResult, any, error)) http.Handler {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "restartable", Version: "v1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echoes the input text"}, echoFn)
	addTestResourcesAndPrompts(srv)
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
}

func newRestartableServer(t *testing.T) *restartableServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rs := &restartableServer{listener: ln, handler: newMCPHandler()}
	rs.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.hmu.Lock()
		h := rs.handler
		rs.hmu.Unlock()
		h.ServeHTTP(w, r)
	})}
	go rs.server.Serve(ln)
	t.Cleanup(rs.stop)
	return rs
}

func (rs *restartableServer) url() string { return "http://" + rs.listener.Addr().String() + "/mcp" }

// restart swaps in a fresh handler (new session map) on the SAME listener. The
// URL is unchanged; the old client session ID is now unknown → 404 → drop.
func (rs *restartableServer) restart() {
	rs.restartWith(newMCPHandler())
}

// restartWith swaps in the given handler (new session map) on the SAME listener.
// Used by tests that need a different tool behaviour after a restart (e.g. an
// echo that returns IsError). The URL is unchanged; the old client session ID is
// now unknown → 404 → drop.
func (rs *restartableServer) restartWith(h http.Handler) {
	rs.hmu.Lock()
	rs.handler = h
	rs.hmu.Unlock()
}

// stop permanently tears down the listener (no restart possible).
func (rs *restartableServer) stop() {
	_ = rs.server.Close()
	_ = rs.listener.Close()
}

// rejectAll swaps in a handler that 404s every request while keeping the
// listener up. This models a "permanently down" server whose endpoint still
// accepts TCP connections but rejects all MCP traffic: the live client session
// gets 404 "session not found" (a drop), and the reconnect dial also fails
// (initialize 404), so a call surfaces "unavailable after reconnect".
func (rs *restartableServer) rejectAll() {
	rs.hmu.Lock()
	defer rs.hmu.Unlock()
	rs.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "session not found", http.StatusNotFound)
	})
}

// connectRestartable connects a *Server to rs with a recording diag and a short
// connect timeout (so a permanently-down reconnect fails fast in tests).
func connectRestartable(t *testing.T, rs *restartableServer, diag port.Diagnostics) *Server {
	t.Helper()
	if diag == nil {
		diag = &recordingDiag{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{
		Name:    "rs",
		URL:     rs.url(),
		Timeout: time.Second,
	}, diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func echoTool(s *Server) tool.Tool {
	for _, t := range s.Tools() {
		if t.Spec().Name == "mcp__rs__echo" {
			return t
		}
	}
	return nil
}

func callEcho(ctx context.Context, t *testing.T, s *Server, text string) session.ToolResult {
	t.Helper()
	rt := echoTool(s)
	if rt == nil {
		t.Fatalf("echo tool not found")
	}
	call := session.NewToolCall("c", "mcp__rs__echo", jsonRaw(`{"text":"`+text+`"}`))
	res, err := rt.Execute(ctx, call, tool.Environment{})
	if err != nil {
		t.Fatalf("echo Execute returned Go error (expected a ToolResult): %v", err)
	}
	return res
}

// jsonRaw is a tiny helper to avoid importing encoding/json in the test file's
// helper signatures (kept for parity with the rest of the package's tests).
func jsonRaw(s string) []byte { return []byte(s) }

// ---- tests ----

// TestReconnectOnDropSucceeds: after the server restarts (dropping the
// connection), a subsequent echo call transparently reconnects and succeeds,
// with exactly one "reconnecting" + one "reconnected" diagnostic line.
func TestReconnectOnDropSucceeds(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	// Baseline call succeeds before any drop.
	res := callEcho(context.Background(), t, s, "first")
	if res.IsError || res.Content != "echo:first" {
		t.Fatalf("baseline echo = %+v, want echo:first", res)
	}

	// Drop the connection and restart on the same URL.
	rs.restart()
	// Give the old server's connection-close a moment to propagate to the SDK.
	time.Sleep(50 * time.Millisecond)

	res = callEcho(context.Background(), t, s, "second")
	if res.IsError {
		t.Fatalf("post-restart echo was a tool error, expected success: %+v", res)
	}
	if res.Content != "echo:second" {
		t.Errorf("post-restart echo content = %q, want echo:second", res.Content)
	}

	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want 1", got)
	}
	if got := diag.count("mcp server reconnected"); got != 1 {
		t.Errorf("reconnected lines = %d, want 1", got)
	}
	if got := diag.count("mcp server reconnect failed"); got != 0 {
		t.Errorf("reconnect-failed lines = %d, want 0", got)
	}
}

// TestPermanentlyDownServerSurfacesClearError: when the server is permanently
// down (its endpoint rejects all MCP traffic with 404), an echo call performs
// ONE bounded reconnect attempt and surfaces a TOOL ERROR carrying the clear
// "unavailable after reconnect" message — never the raw transport string.
func TestPermanentlyDownServerSurfacesClearError(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	// Baseline sanity.
	_ = callEcho(context.Background(), t, s, "ok")

	// "Permanently down": the endpoint stays up but 404s all MCP traffic, so the
	// live session drops AND the reconnect dial cannot re-establish.
	rs.rejectAll()

	res := callEcho(context.Background(), t, s, "after")
	if !res.IsError {
		t.Fatalf("expected a tool-error result, got %+v", res)
	}
	if !strings.Contains(res.Content, "unavailable after reconnect") {
		t.Errorf("error content = %q, want it to contain \"unavailable after reconnect\"", res.Content)
	}
	// The raw transport strings must NOT leak to the model-facing message.
	for _, leak := range []string{"session not found", "failed to connect", "connection failed after"} {
		if strings.Contains(res.Content, leak) {
			t.Errorf("error content leaked raw transport string %q: %s", leak, res.Content)
		}
	}
	// The reconnect actually happened: one attempt, one failure (not merely
	// inferred from the error string).
	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want 1 (a reconnect attempt must happen)", got)
	}
	if got := diag.count("mcp server reconnect failed"); got != 1 {
		t.Errorf("reconnect-failed lines = %d, want 1", got)
	}
}

// TestConcurrentCallsReconnectOnce: N=8 concurrent calls against a restarted
// server all succeed, and the reconnect is serialized under s.mu so exactly ONE
// "reconnecting" line is logged. This is the concurrency invariant.
func TestConcurrentCallsReconnectOnce(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	_ = callEcho(context.Background(), t, s, "warm")

	rs.restart()
	time.Sleep(50 * time.Millisecond)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]string, n)
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res := callEcho(ctx, t, s, fmt.Sprintf("c%d", i))
			if res.IsError || res.Content != fmt.Sprintf("echo:c%d", i) {
				errs[i] = fmt.Sprintf("content=%q isError=%v", res.Content, res.IsError)
			}
		}()
	}
	close(start)
	wg.Wait()

	for i, e := range errs {
		if e != "" {
			t.Errorf("goroutine %d: %s", i, e)
		}
	}

	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want exactly 1 (reconnect must serialize under s.mu)", got)
	}
	if got := diag.count("mcp server reconnected"); got != 1 {
		t.Errorf("reconnected lines = %d, want exactly 1", got)
	}
}

// TestIsConnectionDropClassifier: table test for the single error classifier.
func TestIsConnectionDropClassifier(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			"wrapped ErrConnectionClosed",
			fmt.Errorf("%w: calling %q: %v", mcpsdk.ErrConnectionClosed, "tools/call", errors.New("x")),
			true,
		},
		{
			"wrapped ErrSessionMissing",
			fmt.Errorf("%w: stale session", mcpsdk.ErrSessionMissing),
			true,
		},
		{
			"wrapped JSON-RPC error containing legacy session signature",
			fmt.Errorf("transport rejected response: %w", &jsonrpc.Error{Code: -32000, Message: "session not found"}),
			false,
		},
		{
			"wrapped JSON-RPC error containing legacy transport signatures",
			fmt.Errorf("outer connection closed: %w", &jsonrpc.Error{Code: -32000, Message: "client is closing: connection refused: EOF"}),
			false,
		},
		{"typed EOF transport cause", fmt.Errorf("request failed: %w", io.EOF), true},
		{"typed refused transport cause", fmt.Errorf("dial failed: %w", syscall.ECONNREFUSED), true},
		{
			"session not found string",
			errors.New("failed to reconnect (session ID: abc): session not found"),
			true,
		},
		{"client is closing string", errors.New("client is closing"), true},
		{"connection closed string", errors.New("connection closed by remote"), true},
		{"connection refused string", errors.New("dial tcp: connection refused"), true},
		{"closed idle HTTP connection", errors.New("Post http://x/mcp: http: server closed idle connection"), true},
		{
			"wrapped JSON-RPC error containing closed idle HTTP connection",
			fmt.Errorf("transport rejected response: %w", &jsonrpc.Error{Code: -32000, Message: "http: server closed idle connection"}),
			false,
		},
		{"generic rejected by transport", errors.New(`calling "tools/call": rejected by transport`), false},
		{
			"rejected wrapper with EOF",
			errors.New(`calling "tools/call": sending "tools/call": rejected by transport: Post "http://127.0.0.1:34239/mcp": EOF`),
			true,
		},
		{"bare EOF (hard-stopped server TCP reset)", errors.New("Post http://x/mcp: EOF"), true},
		{"unknown tool (not a drop)", errors.New("unknown tool foo"), false},
		{"invalid params (not a drop)", errors.New("invalid params"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConnectionDrop(tc.err); got != tc.want {
				t.Errorf("isConnectionDrop(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ---- Fix 1: Close is terminal ----

// TestCallAfterCloseDoesNotDial: once Server.Close has run, a subsequent tool
// call returns a tool error (IsError, Go err nil) carrying a clear
// "unavailable"/"closed" message AND performs NO dial — Close is terminal, not
// a retryable drop. Pinned by asserting diag.count("mcp server reconnecting")
// == 0.
func TestCallAfterCloseDoesNotDial(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	// Baseline call succeeds.
	res := callEcho(context.Background(), t, s, "first")
	if res.IsError || res.Content != "echo:first" {
		t.Fatalf("baseline echo = %+v, want echo:first", res)
	}

	// Close the server (terminal). connectRestartable registered a t.Cleanup
	// Close, but Close is idempotent enough for this test; call it explicitly.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A post-close call must NOT dial — it returns a tool error.
	rt := echoTool(s)
	if rt == nil {
		t.Fatalf("echo tool not found after close")
	}
	call := session.NewToolCall("c", "mcp__rs__echo", jsonRaw(`{"text":"after"}`))
	res, err := rt.Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatalf("post-close Execute returned Go error %v, want a ToolResult (IsError)", err)
	}
	if !res.IsError {
		t.Fatalf("post-close call = %+v, want IsError", res)
	}
	if !strings.Contains(res.Content, "unavailable") && !strings.Contains(res.Content, "closed") {
		t.Errorf("post-close error = %q, want a clear \"unavailable\"/\"closed\" message", res.Content)
	}

	// The ship-blocker: no dial happened. Close is terminal.
	if got := diag.count("mcp server reconnecting"); got != 0 {
		t.Errorf("post-close reconnecting lines = %d, want 0 (Close is terminal; must not dial)", got)
	}
}

// ---- Fix 2: genuinely-down server surfaces a clear error ----

// TestPermanentlyDownServerDialFailureClearError: when the server endpoint is
// GENUINELY down (listener closed → a concrete local Go transport failure such
// as "connection refused", EOF, or "http: server closed idle connection", NOT
// a 404 "session not found" drop), an echo call still surfaces the clear
// "unavailable after reconnect" message — never the raw transport text. The
// path: connect (live session), restart (drop the live session), then stop (close
// the listener so the reconnect DIAL fails). The first post-restart call
// classifies the local transport failure as a drop → triggers reconnect → the
// reconnect dial fails → errReconnectFailed → tool.go maps to the clear message.
func TestPermanentlyDownServerDialFailureClearError(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	// Baseline sanity.
	_ = callEcho(context.Background(), t, s, "ok")

	// Drop the live session (restart → 404), then kill the listener so the
	// reconnect DIAL fails with "connection refused".
	rs.restart()
	rs.stop()

	res := callEcho(context.Background(), t, s, "after")
	if !res.IsError {
		t.Fatalf("expected a tool-error result, got %+v", res)
	}
	if !strings.Contains(res.Content, "unavailable after reconnect") {
		t.Errorf("error content = %q, want it to contain \"unavailable after reconnect\"", res.Content)
	}
	// The raw transport strings must NOT leak to the model-facing message.
	for _, leak := range []string{"connection refused", "dial tcp", "session not found", "server closed idle connection"} {
		if strings.Contains(res.Content, leak) {
			t.Errorf("error content leaked raw transport string %q: %s", leak, res.Content)
		}
	}
	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want 1 (the local transport failure must trigger reconnect)", got)
	}
	if got := diag.count("mcp server reconnect failed"); got != 1 {
		t.Errorf("reconnect-failed lines = %d, want 1", got)
	}
}

// ---- Fix 4: resource/prompt reconnect on drop ----

// TestReadResourceReconnectOnDrop: after the server restarts (dropping the
// session), a ReadMcpResource call transparently reconnects and succeeds, with
// exactly one "reconnecting" diagnostic line.
func TestReadResourceReconnectOnDrop(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	ctx := context.Background()
	// Baseline read succeeds.
	chunks, err := s.readResource(ctx, "test://text")
	if err != nil {
		t.Fatalf("baseline readResource: %v", err)
	}
	if len(chunks) == 0 || chunks[0].Text != "hello resource" {
		t.Fatalf("baseline read = %+v, want hello resource", chunks)
	}

	// Drop the session and restart on the same URL.
	rs.restart()
	time.Sleep(50 * time.Millisecond)

	// Post-restart read reconnects transparently.
	chunks, err = s.readResource(ctx, "test://text")
	if err != nil {
		t.Fatalf("post-restart readResource: %v", err)
	}
	if len(chunks) == 0 || chunks[0].Text != "hello resource" {
		t.Errorf("post-restart read = %+v, want hello resource", chunks)
	}
	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want 1", got)
	}
}

// TestGetPromptReconnectOnDrop: after the server restarts (dropping the
// session), a getPrompt call transparently reconnects and succeeds, with
// exactly one "reconnecting" diagnostic line.
func TestGetPromptReconnectOnDrop(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	ctx := context.Background()
	// Baseline get-prompt succeeds.
	pr, err := s.getPrompt(ctx, "greet", map[string]string{"who": "Ada"})
	if err != nil {
		t.Fatalf("baseline getPrompt: %v", err)
	}
	if len(pr.Messages) != 2 {
		t.Fatalf("baseline messages = %d, want 2", len(pr.Messages))
	}

	// Drop the session and restart on the same URL.
	rs.restart()
	time.Sleep(50 * time.Millisecond)

	// Post-restart get-prompt reconnects transparently.
	pr, err = s.getPrompt(ctx, "greet", map[string]string{"who": "Ada"})
	if err != nil {
		t.Fatalf("post-restart getPrompt: %v", err)
	}
	if len(pr.Messages) != 2 {
		t.Fatalf("post-restart messages = %d, want 2", len(pr.Messages))
	}
	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want 1", got)
	}
}

// ---- Fix 5: strengthen TestConcurrentCallsReconnectOnce ----

// TestConcurrentReconnectCASOnceAllStale: all N goroutines observe the SAME
// stale session before any reconnect completes, then all enter reconnect. The
// stale-session CAS-skip (s.session != stale) must hand the fresh session to
// the N-1 waiters without a second dial, so exactly ONE "reconnecting" line is
// logged. This makes the CAS load-bearing, unlike the original
// TestConcurrentCallsReconnectOnce where late goroutines fetch the fresh
// session directly from liveSession.
//
// Determinism: a barrier holds all N goroutines at liveSession+CallTool (they
// fetch the stale session, fail the call with a drop, then block) before
// releasing them into reconnect. The first to take s.mu dials; the rest, by the
// time they take s.mu, see s.session != stale and CAS-skip.
func TestConcurrentReconnectCASOnceAllStale(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	_ = callEcho(context.Background(), t, s, "warm")

	rs.restart()
	time.Sleep(50 * time.Millisecond)

	// Capture the stale session pointer the goroutines will fail against, so we
	// can directly exercise reconnect's stale-CAS with a known-dead session.
	s.mu.Lock()
	stale := s.session
	s.mu.Unlock()
	if stale == nil {
		t.Fatalf("no live session to drop")
	}

	const n = 8
	var wg sync.WaitGroup
	// barrier waits until all N goroutines have entered reconnect, forcing them
	// to contend on s.mu with the SAME stale session.
	entered := make(chan struct{}, n)
	startReconnect := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// All N call reconnect with the SAME stale session. Signal we're
			// about to contend, then wait for the barrier release.
			entered <- struct{}{}
			<-startReconnect
			sess, err := s.reconnect(ctx, stale)
			if err != nil {
				t.Errorf("goroutine %d reconnect: %v", i, err)
				return
			}
			_ = sess
		}()
	}
	// Wait for all N to be ready.
	for i := 0; i < n; i++ {
		<-entered
	}
	// Release them all at once into reconnect contention.
	close(startReconnect)
	wg.Wait()

	// Exactly one dial: the first reconnecter dials, the rest CAS-skip via
	// s.session != stale. We assert via the diagnostic line (the dial site logs
	// "mcp server reconnecting" under s.mu before dialing).
	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want exactly 1 (CAS-skip must coalesce N stale callers into one dial)", got)
	}
}

// ---- Fix 6: three smaller test additions ----

// TestReconnectThenRetryFailsWithNonDropError: connect; restart; configure the
// NEW server's echo tool to return IsError (a server-level error, NOT a drop).
// Call echo → first call drops → reconnect succeeds → retry call returns the
// IsError content (NOT a reconnect error, NOT a double-reconnect). Asserts the
// retry after a successful reconnect surfaces a genuine call-level fault
// unchanged, and exactly one reconnect happened.
func TestReconnectThenRetryFailsWithNonDropError(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	s := connectRestartable(t, rs, diag)

	// Baseline call succeeds.
	_ = callEcho(context.Background(), t, s, "first")

	// Restart to a handler whose echo returns IsError (a server-level error).
	rs.restartWith(newMCPHandlerWithEcho(func(_ context.Context, _ *mcpsdk.CallToolRequest, _ echoArgs) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			IsError: true,
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "boom-after-restart"}},
		}, nil, nil
	}))
	time.Sleep(50 * time.Millisecond)

	// The first call drops (stale session) → reconnect → retry returns IsError.
	res := callEcho(context.Background(), t, s, "x")
	if !res.IsError {
		t.Fatalf("expected IsError result from the retried call, got %+v", res)
	}
	if res.Content != "boom-after-restart" {
		t.Errorf("retry content = %q, want boom-after-restart (the server-level error, not a reconnect error)", res.Content)
	}
	if got := diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnecting lines = %d, want 1 (no double-reconnect on a non-drop retry fault)", got)
	}
}

// TestIsErrorDoesNotTriggerReconnect: on a HEALTHY server, a tool returning
// IsError is a successful RPC (not a connection drop) and must NOT trigger a
// reconnect. The boom tool returns IsError; asserting zero reconnects pins that
// a server-level error is never misclassified as a drop.
func TestIsErrorDoesNotTriggerReconnect(t *testing.T) {
	diag := &recordingDiag{}
	rs := newRestartableServer(t)
	// Swap to a handler that also exposes a "boom" tool returning IsError.
	rs.restartWith(newMCPHandlerWithEcho(func(_ context.Context, _ *mcpsdk.CallToolRequest, _ echoArgs) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			IsError: true,
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "kaboom"}},
		}, nil, nil
	}))
	s := connectRestartable(t, rs, diag)

	// Find the echo tool (the only tool the restartable handler exposes) and
	// call it — it returns IsError.
	rt := echoTool(s)
	if rt == nil {
		t.Fatalf("echo tool not found")
	}
	call := session.NewToolCall("c", "mcp__rs__echo", jsonRaw(`{"text":"x"}`))
	res, err := rt.Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute Go error: %v", err)
	}
	if !res.IsError || res.Content != "kaboom" {
		t.Fatalf("expected IsError kaboom, got %+v", res)
	}
	// A server-level error is a successful RPC — no reconnect.
	if got := diag.count("mcp server reconnecting"); got != 0 {
		t.Errorf("reconnecting lines = %d, want 0 (IsError is a successful RPC, not a drop)", got)
	}
}
