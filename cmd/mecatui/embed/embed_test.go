package embed_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/trace"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/embed"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
	"github.com/stacklok/mecatl/internal/app"
)

// driveTurn dials the embedded server's UNIX socket as a raw gRPC client, creates
// a session, and pushes ONE real prompt through a Converse stream, draining it to
// completion (the terminal "result" event, then EOF). It exists so a perf test
// can run a turn through the embedded engine — the only thing that makes the
// domain-metrics EventSink/Logger injection emit a mecatl_ series — rather than
// relying on the runtime collector alone. It fails the test on any wire error so
// a broken Converse path surfaces here, not as a confusing empty-metrics
// assertion downstream. It uses the generated proto client directly (this package
// is one of the few allowed to import contracts/gen) because the higher-level
// client.Stream exposes no synchronous Recv for a test to drain.
func driveTurn(ctx context.Context, t *testing.T, target, _ string) {
	t.Helper()
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial embedded server for turn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	svc := mecatlv1.NewHarnessServiceClient(conn)

	cs, err := svc.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession for turn: %v", err)
	}
	stream, err := svc.Converse(ctx)
	if err != nil {
		t.Fatalf("open Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{
			Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "hello"},
		},
	}); err != nil {
		t.Fatalf("send prompt: %v", err)
	}
	// Drain to EOF. The terminal "result" event arrives just before the server
	// closes the stream; reaching EOF means the run completed and the engine
	// emitted its run/turn events through the injected Sink.
	for {
		_, rerr := stream.Recv()
		if errors.Is(rerr, io.EOF) {
			return
		}
		if rerr != nil {
			t.Fatalf("Converse Recv: %v", rerr)
		}
	}
}

// TestStartServesOverSocket is the end-to-end proof of the embedded path: Start
// builds the harness from a (mock-provider) app.Config, serves it over a private
// UNIX socket, and the ordinary TUI client drives a real unary RPC
// (CreateSession) across that socket — no TCP port, no daemon.
func TestStartServesOverSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workspace := t.TempDir()
	srv, err := embed.Start(ctx, app.Config{
		Workspace:  workspace,
		Model:      "mock-model",
		UseMock:    true, // offline: no network, no OPENAI_API_KEY needed
		Shell:      "/bin/sh",
		Compaction: "heuristic",
		Tokenizer:  "heuristic",
	}, embed.PerfConfig{})
	if err != nil {
		t.Fatalf("embed.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	target := srv.Target()
	if target == "" {
		t.Fatal("Target() is empty")
	}

	// Readiness IS a real client dial + session create over the socket (the one
	// assertion — no separate health probe): a served socket answers CreateSession.
	cl, err := client.Dial(client.DialConfig{Server: target})
	if err != nil {
		t.Fatalf("dial embedded server: %v", err)
	}
	defer func() { _ = cl.Close() }()

	sessID, _, _, err := cl.CreateSession(ctx, workspace, client.ModeFromString("default"), client.ModelSelection{})
	if err != nil {
		t.Fatalf("CreateSession over embedded socket: %v", err)
	}
	if sessID == "" {
		t.Fatal("CreateSession returned an empty session id")
	}
	if _, err := cl.GetStorageHealth(ctx); err != nil {
		t.Fatalf("local embedded storage operator was denied: %v", err)
	}
}

// TestStartWithMemoryDirServes asserts the embedded server builds and serves when
// a memory directory is configured — exercising app.Build's memory-registration
// gate (build.go: MemoryDir != "" ⇒ memory.New + memory.Register) end to end
// without network. A bad/empty dir would surface as a build error or a CreateSession
// failure; a clean session id proves the gate ran and registered without blowing up.
// It also exercises the slash-command gate (EnableCommands ⇒ a command lister ⇒
// caps.SlashCommands) over the same wire, since the embedded server now enables both
// opt-ins by default.
func TestStartWithMemoryDirServes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workspace := t.TempDir()
	srv, err := embed.Start(ctx, app.Config{
		Workspace:      workspace,
		Model:          "mock-model",
		UseMock:        true, // offline: no network, no OPENAI_API_KEY needed
		Shell:          "/bin/sh",
		Compaction:     "heuristic",
		Tokenizer:      "heuristic",
		MemoryDir:      t.TempDir(), // turns on the Remember/Recall registration gate
		EnableCommands: true,        // turns on the slash-command lister (caps.SlashCommands)
		// Project-tier (workspace-relative) slash-command dirs are repo-injected
		// steering, withheld on an UNTRUSTED workspace (Workspace-Trust Phase 2a). This
		// test asserts EnableCommands ⇒ a wired lister, which now requires a TRUSTED
		// workspace; an untrusted repo correctly degrades to no command lister.
		TrustProject: true,
	}, embed.PerfConfig{})
	if err != nil {
		t.Fatalf("embed.Start with MemoryDir: %v", err)
	}
	defer func() { _ = srv.Close() }()

	cl, err := client.Dial(client.DialConfig{Server: srv.Target()})
	if err != nil {
		t.Fatalf("dial embedded server: %v", err)
	}
	defer func() { _ = cl.Close() }()

	sessID, caps, resolved, err := cl.CreateSession(ctx, workspace, client.ModeFromString("default"), client.ModelSelection{})
	if err != nil {
		t.Fatalf("CreateSession over embedded socket (memory enabled): %v", err)
	}
	if sessID == "" {
		t.Fatal("CreateSession returned an empty session id")
	}
	// The capabilities ride the create response over the embedded socket: with a
	// MemoryDir configured, app.Build registers the Remember tool, so the server
	// must report memory=true. This is the end-to-end proof that caps flow from
	// the BUILT catalog through the wire to the client (not a static guess).
	if !caps.Memory {
		t.Errorf("caps.Memory = false, want true (MemoryDir configured ⇒ Remember registered)")
	}
	if !caps.SlashCommands {
		t.Errorf("caps.SlashCommands = false, want true (EnableCommands ⇒ command lister wired)")
	}
	// The EFFECTIVE model rides the create response too: a zero-selector default
	// session resolves to the configured Model ("mock-model"), echoed verbatim from
	// the composition single source (DefaultResolvedModel) — NOT the empty model_id
	// the request carried. This is the end-to-end proof that resolved_model flows
	// from composition through the wire to the client for the default path.
	if resolved.ModelID != "mock-model" {
		t.Errorf("resolved.ModelID = %q, want %q (zero-selector default echoes the resolved model, not the empty request)", resolved.ModelID, "mock-model")
	}
	if resolved.ProviderID == "" {
		t.Errorf("resolved.ProviderID is empty, want the resolved default provider")
	}
}

// TestStartListAgentsOverSocket is the end-to-end proof of the /agents
// definition-inventory wiring (issue #15, Gap A/D): with an --agents-dir
// configured, app.Build resolves the registry, projects it into the create
// response's caps (agents=true) and the ListAgents snapshot, and the ordinary TUI
// client reads both across the embedded socket — with no network. A def written
// to disk must surface name/description/resolved-model/tools through the
// client.Agent mapping. It also asserts agents is INDEPENDENT of teams (no member
// engine wired ⇒ teams=false while agents=true).
func TestStartListAgentsOverSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A trusted agents dir with one resolvable def.
	agentsDir := t.TempDir()
	def := "---\n" +
		"name: scout\n" +
		"description: explore the codebase\n" +
		"tools: [Read, Grep]\n" +
		"---\n" +
		"You are a careful explorer.\n"
	if werr := os.WriteFile(filepath.Join(agentsDir, "scout.md"), []byte(def), 0o600); werr != nil {
		t.Fatalf("write agent def: %v", werr)
	}

	workspace := t.TempDir()
	srv, err := embed.Start(ctx, app.Config{
		Workspace:          workspace,
		Model:              "mock-model",
		UseMock:            true,
		Shell:              "/bin/sh",
		Compaction:         "heuristic",
		Tokenizer:          "heuristic",
		AgentsDirs:         []string{agentsDir},
		AgentsConventional: false, // only the explicit dir, deterministic
	}, embed.PerfConfig{})
	if err != nil {
		t.Fatalf("embed.Start with AgentsDirs: %v", err)
	}
	defer func() { _ = srv.Close() }()

	cl, err := client.Dial(client.DialConfig{Server: srv.Target()})
	if err != nil {
		t.Fatalf("dial embedded server: %v", err)
	}
	defer func() { _ = cl.Close() }()

	_, caps, _, err := cl.CreateSession(ctx, workspace, client.ModeFromString("default"), client.ModelSelection{})
	if err != nil {
		t.Fatalf("CreateSession over embedded socket: %v", err)
	}
	if !caps.Agents {
		t.Errorf("caps.Agents = false, want true (AgentsDirs configured ⇒ registry resolved)")
	}
	if caps.Teams {
		t.Errorf("caps.Teams = true, want false (no member engine) — agents must be independent of teams")
	}

	agents, err := cl.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents over embedded socket: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("ListAgents returned %d defs, want 1: %+v", len(agents), agents)
	}
	a := agents[0]
	if a.Name != "scout" || a.Description != "explore the codebase" {
		t.Errorf("agent name/description mismapped: %+v", a)
	}
	if len(a.Tools) == 0 {
		t.Errorf("agent tools should carry the resolved read-only scope, got empty: %+v", a)
	}
}

// TestStartProviderError asserts Start surfaces app.Build's provider error and
// removes the already-created gRPC/admin runtime directory on unwind.
func TestStartProviderError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	// Multi-provider S1: app.Build now AUTO-DETECTS a provider from the environment
	// (OPENAI_API_KEY / OPENROUTER_API_KEY) via its registry, so the zero-keys case
	// only triggers when those vars are genuinely unset. Clear them here so the test
	// is deterministic regardless of the developer/CI environment.
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")

	_, err := embed.Start(ctx, app.Config{Workspace: t.TempDir(), Model: "x"}, embed.PerfConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected an error when no LLM provider is configured")
	}
	entries, readErr := os.ReadDir(runtimeDir)
	if readErr != nil {
		t.Fatalf("ReadDir(runtime): %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed Start left runtime entries: %v", entries)
	}
}

// mockAppConfig is the offline (mock-provider) app.Config shared by the perf
// tests — no network, no OPENAI_API_KEY.
func mockAppConfig(workspace string) app.Config {
	return app.Config{
		Workspace:  workspace,
		Model:      "mock-model",
		UseMock:    true,
		Shell:      "/bin/sh",
		Compaction: "heuristic",
		Tokenizer:  "heuristic",
	}
}

// TestStartPerfDisabledStartsNoAdminListener asserts that WITHOUT --perf the
// embedded server hosts only the gRPC socket: AdminAddr() is empty and nothing
// extra is bound. This is the default posture (perf off).
func TestStartPerfDisabledStartsNoAdminListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := embed.Start(ctx, mockAppConfig(t.TempDir()), embed.PerfConfig{})
	if err != nil {
		t.Fatalf("embed.Start (perf off): %v", err)
	}
	defer func() { _ = srv.Close() }()

	if addr := srv.AdminAddr(); addr != "" {
		t.Fatalf("AdminAddr() = %q, want empty when perf is disabled", addr)
	}
}

// TestStartPerfMCPRefusesNonLoopback asserts the fail-closed loopback enforcement
// (decision 6 / CWE-306): with perf.MCP set and a NON-loopback admin Addr, Start
// returns an error and binds nothing. The refusal happens before any telemetry or
// flight-recorder side effects, so this is safe to run alongside the one
// perf-enabled test in this binary (it never arms the process FlightRecorder).
func TestStartPerfMCPRefusesNonLoopback(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := embed.Start(ctx, mockAppConfig(t.TempDir()), embed.PerfConfig{
		Enabled: true,
		MCP:     true,
		Addr:    "0.0.0.0:0", // non-loopback: must be refused
	})
	if err == nil {
		if srv != nil {
			_ = srv.Close()
		}
		t.Fatal("Start with perf.MCP on a non-loopback Addr should fail closed, got nil error")
	}
	if !strings.Contains(err.Error(), "non-loopback") {
		t.Errorf("error = %q, want it to mention the non-loopback refusal", err)
	}
}

// TestStartPerfDefaultUnixIsCollisionFree proves two live instances use distinct
// owner-private sockets, both serve HTTP, and Close removes both runtime dirs.
func TestStartPerfDefaultUnixIsCollisionFree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := embed.Start(ctx, mockAppConfig(t.TempDir()), embed.PerfConfig{Enabled: true})
	if err != nil {
		t.Fatalf("start first: %v", err)
	}
	second, err := embed.Start(ctx, mockAppConfig(t.TempDir()), embed.PerfConfig{Enabled: true})
	if err != nil {
		_ = first.Close()
		t.Fatalf("start second: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = second.Close()
			_ = first.Close()
		}
	}()
	if first.AdminNetwork() != "unix" || second.AdminNetwork() != "unix" || first.AdminAddr() == second.AdminAddr() {
		t.Fatalf("admin endpoints = %s:%s, %s:%s; want distinct unix sockets", first.AdminNetwork(), first.AdminAddr(), second.AdminNetwork(), second.AdminAddr())
	}
	for _, sock := range []string{first.AdminAddr(), second.AdminAddr()} {
		info, statErr := os.Lstat(sock)
		if statErr != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
			t.Fatalf("admin socket %q posture = %v, %v; want socket 0600", sock, info, statErr)
		}
		parent, statErr := os.Stat(filepath.Dir(sock))
		if statErr != nil || parent.Mode().Perm() != 0o700 {
			t.Fatalf("admin parent posture = %v, %v; want 0700", parent, statErr)
		}
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}
		resp, getErr := (&http.Client{Transport: transport}).Get("http://unix/metrics")
		if getErr != nil {
			t.Fatalf("GET %q /metrics: %v", sock, getErr)
		}
		_ = resp.Body.Close()
		transport.CloseIdleConnections()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %q /metrics status = %d", sock, resp.StatusCode)
		}
	}
	firstDir, secondDir := filepath.Dir(first.AdminAddr()), filepath.Dir(second.AdminAddr())
	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	closed = true
	for _, dir := range []string{firstDir, secondDir} {
		if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
			t.Errorf("runtime dir %q remains after Close: %v", dir, statErr)
		}
	}
}

func TestStartPerfExplicitTCPOverride(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := embed.Start(ctx, mockAppConfig(t.TempDir()), embed.PerfConfig{
		Enabled: true,
		Addr:    "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Close() }()
	if srv.AdminNetwork() != "tcp" {
		t.Fatalf("AdminNetwork() = %q, want tcp", srv.AdminNetwork())
	}
	host, service, splitErr := net.SplitHostPort(srv.AdminAddr())
	if splitErr != nil || host != "127.0.0.1" || service == "0" {
		t.Fatalf("AdminAddr() = %q, want resolved loopback TCP address (err=%v)", srv.AdminAddr(), splitErr)
	}
}

// TestStartPerfServesAdminSurface is the end-to-end proof of decision 7: with
// --perf the embedded server brings up an ephemeral loopback admin listener for
// the runtime-introspection surface — /metrics, /debug/vars, /debug/pprof/ — and
// tears it down without leaking. With --perf-mcp and no explicit address it uses
// ephemeral loopback listener for streaming HTTP and reports the resolved port.
// The process FlightRecorder is a sync.Once singleton, so this test accepts the
// documented coalesced/stopped state when another perf test ran first.
func TestStartPerfServesAdminSurface(t *testing.T) {
	// Per-test goleak: the perf surface arms a watchdog ctx, a flight-recorder
	// runtime-trace subscription, an admin HTTP server, and telemetry providers —
	// Close must tear EVERY one down. We deliberately do NOT ignore
	// runtime/trace.Start.func1 or runtime.ReadTrace: those are the goroutines a
	// live flight recorder runs, so after an owner-Stop they MUST be gone. Leaving
	// them un-ignored is what makes this gate FAIL if Close ever forgets to stop
	// the recorder (which would also mask the ownsRecorder fix). The goleak defer
	// is registered FIRST so it runs LAST; Close is registered via t.Cleanup below
	// so it always runs BEFORE the gate, even on an early t.Fatalf (otherwise an
	// early failure would run goleak without Close and false-fail).
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workspace := t.TempDir()
	srv, err := embed.Start(ctx, mockAppConfig(workspace), embed.PerfConfig{
		Enabled:                true,
		MCP:                    true,    // empty Addr => ephemeral loopback for streaming HTTP
		GoroutineWarnThreshold: 1 << 30, // armed but never fires
		GoroutineWarnInterval:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("embed.Start (perf on): %v", err)
	}
	// Register Close via t.Cleanup so it runs before the goleak gate (LIFO: the
	// defer above was registered first, so it fires last) even if a later
	// assertion calls t.Fatalf before the explicit Close.
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = srv.Close()
		}
	})

	addr := srv.AdminAddr()
	if addr == "" {
		t.Fatal("AdminAddr() is empty with perf enabled")
	}
	if host, _, _ := net.SplitHostPort(addr); host != "127.0.0.1" {
		t.Errorf("perf admin bound to %q, want a loopback (127.0.0.1) address", addr)
	}
	// The gRPC socket must still serve alongside the admin surface: a real client
	// dial + session create proves readiness (no separate health probe).
	cl, err := client.Dial(client.DialConfig{Server: srv.Target()})
	if err != nil {
		t.Fatalf("dial embedded gRPC server with perf enabled: %v", err)
	}
	perfSessID, _, _, err := cl.CreateSession(ctx, workspace, client.ModeFromString("default"), client.ModelSelection{})
	if err != nil {
		t.Fatalf("CreateSession with perf enabled: %v", err)
	}
	if perfSessID == "" {
		t.Fatal("CreateSession with perf enabled returned an empty session id")
	}
	_ = cl.Close() // close now — the turn below dials its own raw connection.

	// Drive one real turn through the embedded engine BEFORE scraping /metrics and
	// /debug/flightrecorder: it (a) makes the domain-metrics Sink emit a mecatl_
	// series (the headline injection — runtime-collector series alone would pass a
	// mere non-empty check), and (b) fills the flight-recorder trace window so the
	// snapshot is non-empty.
	driveTurn(ctx, t, srv.Target(), workspace)

	// End-to-end engine → slow-turn buffer → MCP: the turn above emitted an
	// EvTurnEnd that the slow-turn ring buffer (fanned into the embedded engine's
	// Sink) recorded. Calling list_slow_turns over the mounted /mcp must now report
	// at least one turn — proving the whole wiring: the buffer observed the event,
	// the cmd→telemetry→mcpperf bridge handed it to the tool, and /mcp is mounted.
	base := "http://" + addr
	t.Run("/mcp list_slow_turns end-to-end", func(t *testing.T) {
		mc := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "embed-test", Version: "v1"}, nil)
		dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
		defer dialCancel()
		sess, cerr := mc.Connect(dialCtx, &mcpsdk.StreamableClientTransport{
			Endpoint:             base + "/mcp",
			DisableStandaloneSSE: true,
		}, nil)
		if cerr != nil {
			t.Fatalf("connect to /mcp: %v", cerr)
		}
		defer func() { _ = sess.Close() }()

		callCtx, callCancel := context.WithTimeout(ctx, 10*time.Second)
		defer callCancel()
		res, cerr := sess.CallTool(callCtx, &mcpsdk.CallToolParams{
			Name:      "list_slow_turns",
			Arguments: json.RawMessage(`{}`),
		})
		if cerr != nil {
			t.Fatalf("CallTool(list_slow_turns): %v", cerr)
		}
		if res.IsError {
			t.Fatalf("list_slow_turns returned an error result: %+v", res.Content)
		}
		// The structured output carries totalCount AND the turns themselves. After one
		// driven turn we assert not just a non-empty count but that a real scalar
		// payload flowed engine→buffer→MCP: at least one turn object is present, it
		// carries the real event turn_index, and its buffer-stamped ended_at is a
		// populated (non-zero) wall-clock timestamp. A non-empty count alone could
		// mask an empty/zeroed turns array (e.g. a totalCount computed off a stale or
		// dropped payload).
		//
		// NOTE we deliberately do NOT assert duration_ms/ttft_ms > 0: those are
		// wall-clock deltas the engine measures against the streamed response, and the
		// MOCK provider returns instantly, so they legitimately round to 0ms here.
		// Asserting them positive would be a flake, not a stronger proof. ended_at
		// (always stamped by the buffer's clock) is the deterministic scalar that
		// proves a real turn object — not just a count — reached the MCP tool.
		raw, merr := json.Marshal(res.StructuredContent)
		if merr != nil {
			t.Fatalf("marshal structured content: %v", merr)
		}
		var out struct {
			TotalCount int `json:"totalCount"`
			Turns      []struct {
				TurnIndex  int       `json:"turn_index"`
				DurationMs int64     `json:"duration_ms"`
				EndedAt    time.Time `json:"ended_at"`
			} `json:"turns"`
		}
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			t.Fatalf("unmarshal list_slow_turns output %s: %v", raw, uerr)
		}
		if out.TotalCount < 1 {
			t.Fatalf("list_slow_turns totalCount = %d after a driven turn, want >= 1 (engine→buffer→MCP path broken)", out.TotalCount)
		}
		if len(out.Turns) < 1 {
			t.Fatalf("list_slow_turns returned %d turns after a driven turn, want >= 1 (real scalar payload should flow, not just a count): %s", len(out.Turns), raw)
		}
		if out.Turns[0].EndedAt.IsZero() {
			t.Fatalf("list_slow_turns turn[0].ended_at is zero, want a populated buffer-stamped timestamp (proves a real turn object flowed engine→buffer→MCP, not a bare count): %s", raw)
		}
		if out.Turns[0].TurnIndex < 0 {
			t.Fatalf("list_slow_turns turn[0].turn_index = %d, want the real event turn index (>= 0): %s", out.Turns[0].TurnIndex, raw)
		}
	})

	cases := []struct {
		path       string
		wantSubstr string // marker that must appear in the body (empty = any non-empty body)
	}{
		// /metrics must carry a DOMAIN series, not just runtime-collector output —
		// this is the actual proof the EventSink/Logger were injected into the
		// embedded engine. mecatl_events_total is emitted by every run.
		{"/metrics", "mecatl_"},
		{"/debug/vars", "mecatl_runtime"},      // the curated expvar key
		{"/debug/pprof/", "Types of profiles"}, // the pprof index page
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, gerr := http.Get(base + tc.path)
			if gerr != nil {
				t.Fatalf("GET %s: %v", tc.path, gerr)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", tc.path, resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if len(body) == 0 {
				t.Fatalf("GET %s returned an empty body", tc.path)
			}
			if tc.wantSubstr != "" && !strings.Contains(string(body), tc.wantSubstr) {
				t.Errorf("GET %s body missing %q; got %.160q", tc.path, tc.wantSubstr, body)
			}
		})
	}

	// /debug/flightrecorder is the endpoint UNIQUE to the perf wiring (it is only
	// mounted when a recorder is non-nil). After a turn the trace window is
	// non-empty, so when THIS run armed the recorder the snapshot must be 200 + a
	// non-empty octet-stream beginning with the Go execution-trace magic ("go 1."
	// in the v2 trace header).
	//
	// Under `go test -count=N` this whole test re-runs in the SAME process, but
	// ProcessFlightRecorder is a process-lifetime sync.Once: run 1 arms+stops the
	// singleton, so runs 2..N coalesce onto a now-STOPPED recorder
	// (RecorderArmed()==false) and the endpoint correctly serves 503 (nothing to
	// snapshot). We assert the live-snapshot contract on the arming run and the
	// documented stopped-singleton behaviour on the coalesced runs — so -count=N
	// stays green without weakening the real assertion.
	t.Run("/debug/flightrecorder", func(t *testing.T) {
		resp, gerr := http.Get(base + "/debug/flightrecorder")
		if gerr != nil {
			t.Fatalf("GET /debug/flightrecorder: %v", gerr)
		}
		defer func() { _ = resp.Body.Close() }()
		if !srv.RecorderArmed() {
			// Coalesced onto an already-stopped process singleton (a re-run, or a
			// co-running owner): the documented limitation — no live window to snapshot.
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("GET /debug/flightrecorder status = %d, want 503 when this run did not arm the recorder", resp.StatusCode)
			}
			return
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /debug/flightrecorder status = %d, want 200 (this run armed the recorder)", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("GET /debug/flightrecorder Content-Type = %q, want application/octet-stream", ct)
		}
		body, _ := io.ReadAll(resp.Body)
		if len(body) == 0 {
			t.Fatal("GET /debug/flightrecorder returned an empty trace body")
		}
		if !strings.HasPrefix(string(body), "go 1.") {
			t.Errorf("flight-recorder body missing the trace magic; got %.16q", body)
		}
	})

	// Snapshot whether this run armed the recorder BEFORE Close (Close is the act
	// that stops it); the post-Close assertion below depends on it.
	armed := srv.RecorderArmed()

	// Close must tear down the admin listener: a follow-up GET must fail to connect.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	if resp, gerr := http.Get(base + "/metrics"); gerr == nil {
		_ = resp.Body.Close()
		t.Fatalf("admin listener still serving %s after Close — it leaked", base)
	}

	// DIRECT recorder-stop assertion (the reliable half of QA must-add #5): a
	// forgotten recorder Stop does NOT reliably leave a goroutine goleak can see
	// (runtime.ReadTrace is transient — it only runs during a WriteTo), so the
	// goleak gate ABOVE — now run WITHOUT the trace ignores — is necessary but not
	// sufficient on its own. We additionally assert the process FlightRecorder is
	// DISABLED after Close: when this run armed it, Close's owner-Stop must have
	// disabled it. ProcessFlightRecorder returns the same singleton (coalesced); its
	// Enabled() must report false. If Close ever stops the watchdog but forgets the
	// recorder (the ownsRecorder regression), Enabled() stays true and this FAILS —
	// which is the guarantee the goleak gate alone could not give.
	if armed {
		rec, _ := telemetry.ProcessFlightRecorder(trace.FlightRecorderConfig{})
		if rec == nil {
			t.Fatal("ProcessFlightRecorder returned nil after Close; expected the stopped singleton")
		}
		if rec.Enabled() {
			t.Fatal("flight recorder still Enabled() after Close — owner-Stop was skipped (ownsRecorder regression)")
		}
	}
	// The goleak gate (deferred above, runs last) is now run WITHOUT the
	// runtime/trace.Start.func1 + runtime.ReadTrace ignores, so any persistent
	// trace goroutine left by a forgotten Stop also fails it; the direct Enabled()
	// assertion above closes the gap for the transient-goroutine case.
}

// startEmbeddedBuiltServer is the test-only embedded-server harness for the
// fire-result-delivery e2e: it builds the FULL composition (internal/app.Build,
// same as cmd/mecatui/embed.Start) and serves it over a private UNIX socket with
// the HarnessService + ScheduleService + gRPC health service registered — the
// same surface embed.Start serves. It returns the gRPC dial target, the built
// *app.Built (whose Service the test reaches to create a schedule with an
// OriginSessionID — see the note in TestFireDelivery_EmbeddedEndToEnd on why
// the wire cannot set that field), and a teardown that GracefulStops + closes.
//
// It differs from embed.Start ONLY in that it hands the caller the *app.Built
// (embed.Start hides it behind *embed.Server). That reach is the ONE seam this
// test needs that embed.Start does not expose, because the v1 wire
// (CreateScheduleRequest.ScheduleSpec) carries NO origin_session_id field —
// OriginSessionID is stamped ONLY by the in-loop Schedule tool
// (engine/agent/sessionorigin.go). The fire, the delivery, and the live
// subscription all run through the REAL composition (app.Build's
// startScheduler wires SetDeliverFireResult; deliverFireResult drives
// StartRunContent + PublishSessionEvent; StreamSessionLive relays it live).
func startEmbeddedBuiltServer(t *testing.T, cfg app.Config) (target string, built *app.Built, teardown func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b, err := app.Build(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("app.Build: %v", err)
	}
	lis, target, dir, err := embed.NewUnixSocketListenerForTest()
	if err != nil {
		b.Close()
		cancel()
		t.Fatalf("listen unix: %v", err)
	}
	grpcSrv := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcSrv, server.NewHarnessServer(b.Service))
	mecatlv1.RegisterScheduleServiceServer(grpcSrv, server.NewScheduleServer(b.Service))
	go func() { _ = grpcSrv.Serve(lis) }()
	return target, b, func() {
		grpcSrv.GracefulStop()
		b.Close()
		cancel()
		_ = os.RemoveAll(dir)
	}
}

// TestFireDelivery_EmbeddedEndToEnd is the DoD #8 ship-gate for the
// fire-result-delivery plan: a schedule whose fire reports back into its
// ORIGINATING session's LIVE StreamSessionLive subscription, end-to-end through
// the REAL embedded composition (app.Build + the in-process scheduler +
// deliverFireResult + PublishSessionEvent), over a REAL gRPC UNIX socket with a
// REAL client. Fully offline (UseMock).
//
// The chain exercised: app.Build(startScheduler wires SetFire +
// SetDeliverFireResult over the durable DeliveryQueue) → the scheduler's tick
// loop fires a near-future one-shot schedule → fireClaimed runs the FireFunc
// (the fire's run on the mock provider) → RecordFire → deliverFireResult
// renders the fenced note, Enqueue, and drives a delivery run into the origin
// via StartRunContent (loadAndReopen reopens the completed origin) → the run's
// events are fanned to the origin's live subscribers via
// PublishSessionEvent → the gRPC StreamSessionLive handler relays the delivery
// EvUserPrompt (the ONE log-only-kind exception) to the connected client → the
// test asserts the note arrived LIVE, names the schedule + fire id, and is the
// fenced delivery note.
//
// OriginSessionID reach: the v1 CreateScheduleRequest.ScheduleSpec proto has
// NO origin_session_id field (the wire carries no such field by design — see
// engine/CHANGELOG.md + internal/adapter/server/grpc_schedule.go's
// protoToScheduleSpec, which never maps it). OriginSessionID is stamped ONLY by
// the in-loop Schedule tool, from the run context (engine/agent/scheduletool.go
// + engine/agent/sessionorigin.go). The canned UseMock provider cannot drive a
// Schedule tool call, so the test reaches the just-built *app.Built.Service
// directly to create the schedule with OriginSessionID set — the ONE in-process
// step in an otherwise real-wire e2e. The fire, delivery, and live
// subscription all run through the REAL composition path.
func TestFireDelivery_EmbeddedEndToEnd(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workspace := t.TempDir()
	cfg := mockAppConfig(workspace)
	// The durable jsonlstore (StoreDir) exposes a ScheduleStore, so the scheduler
	// + the durable DeliveryQueue wire up (buildScheduler + buildDeliveryQueue).
	cfg.StoreDir = t.TempDir()
	// The scheduler is ON by default, but be explicit: this test exercises the
	// tick loop's fire path, so the tick must actually run.
	cfg.SchedulerEnabled = true
	// A short tick so the near-future one-shot is picked up promptly.
	cfg.SchedulerTickInterval = 50 * time.Millisecond
	// The workspace is a throwaway temp dir; trust it so the fire's session
	// (rooted at the schedule's Workspace) mints a real workspace session.
	cfg.TrustProject = true

	target, built, teardown := startEmbeddedBuiltServer(t, cfg)
	defer teardown()

	// Dial a REAL gRPC client over the UNIX socket (the embedded product's
	// transport). Use the generated client directly so the test drains the
	// server-streaming StreamSessionLive Recv (the client.Client wrapper
	// projects to tea.Msg; the raw stream carries the proto Event the
	// assertion reads).
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial embedded server: %v", err)
	}
	defer func() { _ = conn.Close() }()
	hc := mecatlv1.NewHarnessServiceClient(conn)

	// 1. Create the ORIGIN session S over the real socket.
	cs, err := hc.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession origin: %v", err)
	}
	originID := session.SessionID(cs.GetSessionId())
	if originID == "" {
		t.Fatal("CreateSession returned an empty session id")
	}

	// 2. Open the LIVE StreamSessionLive subscription for S; collect events on a
	// goroutine. The gRPC handler calls Subscribe asynchronously after the
	// client opens the stream, so warm up the lazy connection first with a
	// quick unary GetSession (the same discipline the wire-side live-subscription
	// tests use).
	if _, werr := hc.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: string(originID)}); werr != nil {
		t.Fatalf("warmup GetSession: %v", werr)
	}
	stream, err := hc.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(originID)})
	if err != nil {
		t.Fatalf("StreamSessionLive: %v", err)
	}
	var (
		evMu sync.Mutex
		evs  []*mecatlv1.Event
	)
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for {
			ev, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			evMu.Lock()
			evs = append(evs, ev)
			evMu.Unlock()
		}
	}()

	// Wait for the subscription to actually register before firing: the gRPC
	// handler's Subscribe runs asynchronously, so an event published before it
	// returns is lost. Probe with a no-op event until it lands (the same robust
	// synchronization the wire-side live-subscription tests use).
	probeDeadline := time.Now().Add(5 * time.Second)
	probe := session.Event{Type: session.EvNoProgress, Text: "delivery-e2e-probe"}
	for !liveHasEvent(&evMu, &evs, "no_progress", "delivery-e2e-probe") {
		if time.Now().After(probeDeadline) {
			evMu.Lock()
			types := liveEventTypes(evs)
			evMu.Unlock()
			t.Fatalf("StreamSessionLive probe did not arrive within 5s — the subscription is not live; got %d events: %v", len(evs), types)
		}
		built.Service.PublishSessionEvent(originID, probe)
		time.Sleep(10 * time.Millisecond)
	}

	// 3. Create a schedule whose fire reports back into S. The wire cannot set
	// OriginSessionID (see the doc comment), so reach the just-built Service
	// directly — the ONE in-process step. A near-future one-shot the short tick
	// picks up exercises the REAL tick→fire→deliver chain (stronger than FireNow:
	// it proves the tick loop, the Claim-before-fire, and the deliverFireResult
	// callback all wire together). The schedule is read-leaning (mutating:false)
	// in plan mode, rooted at the workspace.
	schedName := "embedded-delivery-e2e"
	spec := port.ScheduleSpec{
		Name:            schedName,
		Prompt:          "monitor the build",
		Workspace:       workspace,
		Mode:            session.ModePlan,
		Mutating:        false,
		OriginSessionID: originID, // <- the metadata-only routing key the wire cannot carry
		Trigger: port.TriggerSpec{
			OneShot: time.Now().Add(200 * time.Millisecond), // the short tick picks it up
		},
	}
	if _, cerr := built.Service.CreateSchedule(ctx, spec); cerr != nil {
		t.Fatalf("CreateSchedule: %v", cerr)
	}

	// 4. Bounded-poll (require.Eventually-style, ~15s deadline — NOT a fixed
	// sleep) for the subscription to yield an EvUserPrompt whose text contains
	// the delivery provenance header "[scheduled task <name> (fire <id>)".
	// The short tick fires the one-shot; the scheduler drives deliverFireResult;
	// the delivery run's EvUserPrompt is fanned to this subscription and relayed
	// live (the ONE log-only-kind exception).
	// The started notice ("... started (fire ...)") and the terminal note
	// ("... completed with stop reason: ...") both carry the delivery header
	// prefix "[scheduled task <name> (fire <id>)"; this test asserts the TERMINAL
	// delivery, so match the stop-reason marker, not just the shared prefix (a
	// started note arriving first would otherwise satisfy the looser match and
	// the stop-reason assertion would fail).
	const terminalMarker = "completed with stop reason:"
	deadline := time.Now().Add(15 * time.Second)
	var deliveredNote string
	for {
		if txt, ok := liveFirstUserPromptContaining(&evMu, &evs, terminalMarker); ok {
			deliveredNote = txt
			break
		}
		if time.Now().After(deadline) {
			evMu.Lock()
			types := liveEventTypes(evs)
			evMu.Unlock()
			// Diagnose: did the fire run at all? List the schedule's fires.
			fires, _ := built.Service.ListFires(ctx, schedName)
			fireIDs := make([]string, 0, len(fires))
			for _, f := range fires {
				fireIDs = append(fireIDs, f.ID)
			}
			t.Fatalf("StreamSessionLive did NOT relay the delivery EvUserPrompt within 15s; got %d events: %v (fires: %v)", len(evs), types, fireIDs)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 5. Assert: the note arrived LIVE on the subscription, names the schedule
	// and the fire id, and is the fenced delivery note (renderFireDelivery wraps
	// the header + body in governance.FenceUntrusted).
	if !strings.Contains(deliveredNote, "[scheduled task "+schedName+" ") {
		t.Errorf("delivered note does not name the schedule: %q (want prefix %q)", deliveredNote, "[scheduled task "+schedName+" ")
	}
	if !strings.Contains(deliveredNote, "(fire ") {
		t.Errorf("delivered note does not name the fire id: %q (want an '(fire <id>)' segment)", deliveredNote)
	}
	if !strings.Contains(deliveredNote, "completed with stop reason:") {
		t.Errorf("delivered note does not state the stop reason: %q", deliveredNote)
	}
	// The fenced-untrusted wrapper: the note body is inside an
	// governance.FenceUntrusted block (<<<UNTRUSTED…<<<UNTRUSTED — the SAME marker
	// opens and closes the block). Assert the fence markers are present so the
	// note is the genuine rendered delivery note, not a stray user_prompt.
	if !strings.Contains(deliveredNote, "<<<UNTRUSTED") {
		t.Errorf("delivered note is not fenced-untrusted (missing <<<UNTRUSTED markers): %q", deliveredNote)
	}
}

// liveHasEvent reports whether the collected live-stream events include one of
// the given type whose text (top-level Event.text or UserPrompt.text for a
// user_prompt) contains substr. Caller holds no lock.
func liveHasEvent(mu *sync.Mutex, evs *[]*mecatlv1.Event, typ, substr string) bool {
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *evs {
		if ev.GetType() != typ {
			continue
		}
		if up := ev.GetUserPrompt(); up != nil {
			if strings.Contains(up.GetText(), substr) {
				return true
			}
			continue
		}
		if strings.Contains(ev.GetText(), substr) {
			return true
		}
	}
	return false
}

// liveFirstUserPromptContaining returns the text of the first collected
// user_prompt event whose text contains substr, and ok=true; ("", false) if
// none yet. Caller holds no lock.
func liveFirstUserPromptContaining(mu *sync.Mutex, evs *[]*mecatlv1.Event, substr string) (string, bool) {
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *evs {
		if ev.GetType() != "user_prompt" {
			continue
		}
		if up := ev.GetUserPrompt(); up != nil && strings.Contains(up.GetText(), substr) {
			return up.GetText(), true
		}
	}
	return "", false
}

// liveEventTypes returns the type strings of the collected events for
// diagnostics. Caller holds no lock.
func liveEventTypes(evs []*mecatlv1.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.GetType())
	}
	return out
}
