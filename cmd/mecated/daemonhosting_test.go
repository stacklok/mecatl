package main

// Scenario 8 of docs/acceptance/sdk-server-enablers.md (issue #821): daemon
// hosting — UDS, HTTP-disable, ready file, lifetime pipe.
//
// The serve-driving tests run the REAL serve() against the real listener,
// ready-file, and lifetime-pipe code paths over an offline service (mockllm +
// memstore + memfs), so what is asserted is the binary's behaviour rather than a
// helper's. They are fully offline and consume zero provider turns: no test here
// starts a run.

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// captureLogs redirects the slog default for the duration of the test and returns
// a reader for everything written to it.
//
// serve() logs from goroutines, hence the mutex: the handler and the reader race
// otherwise, and a -race failure in a security assertion is worse than useless.
// The default is restored on cleanup so a later test is not left writing into a
// dead buffer.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// socketPathForTest returns a short, absolute, clean socket path whose parent
// directory does NOT exist yet, so ensureSocketDir's create-owner-only branch is
// the one under test.
//
// It falls back to a private directory under /tmp when the test temp root is too
// long for sockaddr_un.sun_path — on macOS the default TMPDIR
// (/var/folders/xy/…/T/) is already about half the 103-byte budget, so a nested
// t.TempDir() plus a filename genuinely does not fit. That is the same fallback
// cmd/mecatui/embed makes for the same reason.
func socketPathForTest(t *testing.T) string {
	t.Helper()
	candidate := filepath.Join(t.TempDir(), "s", "g.sock")
	if len(candidate) < sunPathMax {
		return candidate
	}
	base, err := os.MkdirTemp("/tmp", "mecated-uds-")
	if err != nil {
		t.Fatalf("create short socket base: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	candidate = filepath.Join(base, "s", "g.sock")
	if len(candidate) >= sunPathMax {
		t.Skipf("no socket path under %d bytes is available on this host (got %q)", sunPathMax, candidate)
	}
	return candidate
}

// freeTCPAddr binds and immediately releases a loopback port, returning the
// address. A test uses it as a --grpc-addr / --metrics-addr value it then
// asserts was NEVER bound: re-binding the same address after serve() is up is a
// direct, non-flaky proof that the listener does not exist. Asserting against a
// fixed default port instead would fail whenever something unrelated held it.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe a free port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release probe port: %v", err)
	}
	return addr
}

// assertPortFree fails when addr can be bound — i.e. when serve() left a
// listener on a port the test expected it never to open.
func assertPortFree(t *testing.T, addr, why string) {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("%s: %s is still bound (%v)", why, addr, err)
	}
	_ = l.Close()
}

// servedDaemon is a serve() running in the background, plus the handles a test
// needs to inspect and stop it.
type servedDaemon struct {
	cfg  config
	done <-chan error
	stop context.CancelFunc
}

// startServe runs the real serve() in a goroutine and blocks until its ready
// file exists, which is exactly the barrier a spawning parent would use. It
// registers cleanup that cancels serve and asserts a clean return, so a test
// body can concentrate on its own assertion.
//
// cfg.readyFile is set here when the caller left it empty: readiness has to be
// observable for the harness to be usable, and a test that wants to assert
// something ABOUT the ready file simply sets its own path first.
func startServe(t *testing.T, cfg config) *servedDaemon {
	t.Helper()
	return startServeWith(t, cfg, newOfflineService(t))
}

// startServeWith is startServe over a caller-supplied service, for a test that
// needs the service's OWN configuration (a deployment label, say) to be what the
// assertion is about.
func startServeWith(t *testing.T, cfg config, svc *server.Service) *servedDaemon {
	t.Helper()
	if cfg.readyFile == "" {
		cfg.readyFile = filepath.Join(t.TempDir(), "ready.json")
	}
	if err := validateEffectiveConfig(cfg); err != nil {
		t.Fatalf("validateEffectiveConfig rejected the harness config: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, svc, prometheus.NewRegistry(), nil, nil) }()

	d := &servedDaemon{cfg: cfg, done: done, stop: cancel}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve returned %v, want a clean shutdown", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("serve did not return within 20s of cancellation")
		}
	})
	waitForReadyFile(t, cfg.readyFile, done)
	return d
}

// waitForReadyFile polls path until it exists, failing fast if serve() exits
// first (so a startup error surfaces as itself rather than as a timeout).
func waitForReadyFile(t *testing.T, path string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("serve exited before publishing %q: %v", path, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("ready file %q did not appear within 20s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// readReadyDoc decodes the published ready file.
func readReadyDoc(t *testing.T, path string) (readyDoc, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ready file: %v", err)
	}
	var doc readyDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("ready file %q is not valid JSON (%v):\n%s", path, err, raw)
	}
	return doc, raw
}

// dialCompatibilityOverSocket proves the gRPC server is genuinely serving on the
// socket by making a real RPC. GetCompatibilityInfo is the right probe: it is
// authenticated like every other RPC (auth is off in these tests), creates no
// session, and consumes no provider turn.
func dialCompatibilityOverSocket(t *testing.T, socketPath string) *mecatlv1.GetCompatibilityInfoResponse {
	t.Helper()
	conn, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial %q: %v", socketPath, err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := mecatlv1.NewHarnessServiceClient(conn).GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo over the socket: %v", err)
	}
	return resp
}

// udsConfig is the shape a spawned local daemon runs with: gRPC on a socket, no
// HTTP surface, no admin listener.
func udsConfig(socketPath string) config {
	return config{
		grpcAddr:       "127.0.0.1:0", // the suppressed default; never bound
		grpcUnixSocket: socketPath,
		httpAddr:       "",
		metricsAddr:    "",
	}
}

// ---------------------------------------------------------------------------
// AC8.1
// ---------------------------------------------------------------------------

// TestSDKServerEnablers_Scenario8_UDSOnlyOpensNoPort is AC8.1.
//
// Two halves, because the criterion has two: the socket must actually SERVE
// gRPC while opening no TCP port, and combining it with configured TCP gRPC must
// be refused at STARTUP rather than resolved by a silent precedence rule.
//
// The no-port half is asserted by re-binding the address that --grpc-addr named:
// if the default had not been suppressed, that bind would fail.
func TestSDKServerEnablers_Scenario8_UDSOnlyOpensNoPort(t *testing.T) {
	sock := socketPathForTest(t)
	suppressed := freeTCPAddr(t)

	cfg := udsConfig(sock)
	cfg.grpcAddr = suppressed
	d := startServe(t, cfg)

	// The socket serves the real gRPC surface.
	if got := dialCompatibilityOverSocket(t, sock).GetApiMajor(); got != server.APIMajor {
		t.Errorf("api_major over the socket = %d, want %d", got, server.APIMajor)
	}

	// ... and no TCP port was opened for it.
	assertPortFree(t, suppressed, "--grpc-unix-socket must suppress the --grpc-addr default")

	doc, _ := readReadyDoc(t, d.cfg.readyFile)
	if doc.Transport != transportUnix {
		t.Errorf("ready file transport = %q, want %q", doc.Transport, transportUnix)
	}
	if doc.SocketPath != sock {
		t.Errorf("ready file socket_path = %q, want %q", doc.SocketPath, sock)
	}
	if doc.HTTPAddress != "" {
		t.Errorf("ready file http_address = %q, want it absent when the HTTP listener is disabled", doc.HTTPAddress)
	}
}

// TestSDKServerEnablers_Scenario8_UDSRejectsConfiguredTCPGRPC is the startup-
// rejection half of AC8.1.
//
// Both configuration sources are covered, because the exclusion is about what
// the operator ASKED for, not about which value would have won: a daemon config
// file naming grpc_addr is the same contradiction as an explicit flag. It also
// pins the negative — a socket with grpc_addr merely left at its DEFAULT is the
// normal case and must be accepted, or the flag would be unusable.
func TestSDKServerEnablers_Scenario8_UDSRejectsConfiguredTCPGRPC(t *testing.T) {
	sock := socketPathForTest(t)

	cases := []struct {
		name    string
		cfg     config
		wantErr bool
		reason  string
	}{
		{
			name:   "socket with grpc-addr at its default is the normal case",
			cfg:    udsConfig(sock),
			reason: "--grpc-addr carries a non-empty default; treating that as a request would make --grpc-unix-socket unusable",
		},
		{
			name:    "socket plus an explicit --grpc-addr",
			cfg:     withExplicit(udsConfig(sock), "grpc-addr"),
			wantErr: true,
			reason:  "silently ignoring it would leave the operator believing a TCP port was open",
		},
		{
			name:    "socket plus a config-file grpc_addr",
			cfg:     withFileGRPCAddr(udsConfig(sock)),
			wantErr: true,
			reason:  "a file-supplied address must not bypass the exclusion the flag enforces",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEffectiveConfig(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateEffectiveConfig = nil, want a rejection: %s", tc.reason)
				}
				if !strings.Contains(err.Error(), "mutually exclusive") {
					t.Errorf("rejection %q does not name the exclusion", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateEffectiveConfig = %v, want nil: %s", err, tc.reason)
			}
		})
	}
}

// TestSDKServerEnablers_Scenario8_SocketPathIsValidatedAtStartup pins the Darwin
// sun_path bound and the absolute/clean requirements (AC8.1's startup-rejection
// discipline applied to the path itself).
//
// The length case is the one that earns its keep: without it an over-long path
// fails inside bind(2) as a bare EINVAL, which tells an operator nothing about
// the actual constraint.
func TestSDKServerEnablers_Scenario8_SocketPathIsValidatedAtStartup(t *testing.T) {
	longPath := "/tmp/" + strings.Repeat("d/", sunPathMax) + "g.sock"
	cases := []struct {
		name string
		path string
		want string // substring the error must name
	}{
		{name: "relative", path: "run/g.sock", want: "absolute"},
		{name: "unclean", path: "/tmp/./run/g.sock", want: "clean"},
		{name: "trailing separator", path: "/tmp/run/", want: "clean"},
		{name: "over the sun_path bound", path: longPath, want: "sun_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUnixSocketPath(tc.path)
			if err == nil {
				t.Fatalf("validateUnixSocketPath(%q) = nil, want a startup error", tc.path)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q, so it does not tell the operator the constraint", err, tc.want)
			}
		})
	}

	ok := "/tmp/mecated/g.sock"
	if err := validateUnixSocketPath(ok); err != nil {
		t.Fatalf("validateUnixSocketPath(%q) = %v, want nil", ok, err)
	}
}

// ---------------------------------------------------------------------------
// AC8.2
// ---------------------------------------------------------------------------

// TestSDKServerEnablers_Scenario8_HTTPDisabled is AC8.2.
//
// An empty --http-addr disables the HTTP/SSE listener AND the admin/metrics
// listener. The metrics half is the part worth a test: --metrics-addr keeps its
// loopback default, so a daemon that disabled HTTP but kept a listener on 9090
// would still be holding a TCP port — and "the HTTP listeners are disabled"
// would be false.
func TestSDKServerEnablers_Scenario8_HTTPDisabled(t *testing.T) {
	sock := socketPathForTest(t)
	adminAddr := freeTCPAddr(t)

	cfg := udsConfig(sock)
	cfg.httpAddr = ""
	cfg.metricsAddr = adminAddr // explicitly set, and still disabled with HTTP

	// Structural: no admin server is even constructed.
	if srv, _ := buildAdminServer(cfg, prometheus.NewRegistry(), nil, nil); srv != nil {
		t.Errorf("buildAdminServer returned a server for an empty --http-addr; the admin listener must be disabled with the HTTP surface")
	}
	// The negative control, so the assertion above is about the COUPLING and not
	// about buildAdminServer returning nil for everything: with an HTTP surface,
	// the same --metrics-addr does produce an admin server.
	control := cfg
	control.httpAddr = "127.0.0.1:0"
	if srv, _ := buildAdminServer(control, prometheus.NewRegistry(), nil, nil); srv == nil {
		t.Fatal("test is vacuous: buildAdminServer returned nil even WITH an HTTP surface")
	}

	d := startServe(t, cfg)

	// Behavioural: the admin port was never bound.
	assertPortFree(t, adminAddr, "an empty --http-addr must disable the admin/metrics listener")

	// gRPC still works — disabling HTTP disables a transport, not the harness.
	dialCompatibilityOverSocket(t, sock)

	if doc, _ := readReadyDoc(t, d.cfg.readyFile); doc.HTTPAddress != "" {
		t.Errorf("ready file http_address = %q, want it omitted when HTTP is disabled", doc.HTTPAddress)
	}
}

// TestSDKServerEnablers_Scenario8_HTTPDisabledRefusesPerfMCP pins the one
// combination an empty --http-addr makes incoherent: --perf-mcp mounts /mcp on
// the admin listener that no longer exists.
//
// Refusing at startup rather than mounting an unreachable endpoint matters
// because the failure would otherwise be invisible — the operator would see a
// running daemon and an MCP endpoint that never answers.
func TestSDKServerEnablers_Scenario8_HTTPDisabledRefusesPerfMCP(t *testing.T) {
	cfg := udsConfig("/tmp/mecated/g.sock")
	cfg.httpAddr = ""
	cfg.metricsAddr = "127.0.0.1:9090"
	cfg.perfMCP = true

	err := validateEffectiveConfig(cfg)
	if err == nil {
		t.Fatal("validateEffectiveConfig = nil, want --perf-mcp refused when an empty --http-addr disabled the admin listener")
	}
	if !strings.Contains(err.Error(), "--perf-mcp") || !strings.Contains(err.Error(), "--http-addr") {
		t.Errorf("rejection %q must name both flags so the operator knows which to drop", err)
	}
}

// TestSDKServerEnablers_Scenario8_DisabledAndSocketListenersAreNotNetworkBoundaries
// pins the workspace-authority consequence of the two new listener shapes
// (ADR 0237).
//
// A UNIX socket is reachable only through filesystem permission on one path, and
// a disabled listener is reachable not at all — both strictly narrower than the
// loopback TCP bind that already grants client-selected authority. Reading an
// empty --http-addr as "not loopback" would have demanded --workspace from
// exactly the local spawned daemon that has no network surface at all.
//
// The EMPTY-ADDRESS row is the one that has to be read carefully, because the
// same empty string means opposite things to the two callers. For gRPC it is a
// WILDCARD bind — net.Listen("tcp", "") binds [::] on a kernel-chosen port — so
// it must classify as a boundary; the helper answers for gRPC's meaning. HTTP's
// disabled case is asserted separately below, at the composed decision, because
// that is where serve()'s skip actually lives.
func TestSDKServerEnablers_Scenario8_DisabledAndSocketListenersAreNotNetworkBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		addr       string
		unixSocket bool
		want       bool
	}{
		{name: "empty tcp addr is a wildcard bind", addr: "", want: true},
		{name: "unix socket", addr: "127.0.0.1:8080", unixSocket: true, want: false},
		{name: "loopback tcp", addr: "127.0.0.1:8080", want: false},
		{name: "wildcard tcp", addr: "0.0.0.0:8080", want: true},
		{name: "public tcp", addr: "192.0.2.10:8080", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := listenerIsNetworkBoundary(tc.addr, tc.unixSocket); got != tc.want {
				t.Fatalf("listenerIsNetworkBoundary(%q, unix=%v) = %v, want %v", tc.addr, tc.unixSocket, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC8.3
// ---------------------------------------------------------------------------

// TestSDKServerEnablers_Scenario8_ReadyFileAtomicAndLate is AC8.3.
//
// "Late" is asserted the way a parent would exploit it: the instant the file
// exists, dial the socket it names. If the file were published before the
// listeners bound, that dial would fail — which is precisely the race the file
// exists to remove.
func TestSDKServerEnablers_Scenario8_ReadyFileAtomicAndLate(t *testing.T) {
	sock := socketPathForTest(t)
	readyPath := filepath.Join(t.TempDir(), "ready.json")

	cfg := udsConfig(sock)
	cfg.readyFile = readyPath
	// The label rides the SERVICE, not this config: publishReadyFile reads the
	// CompatibilityInfo projection precisely so it never touches raw config
	// (AC8.4). Setting it on the service is therefore the test that the wiring
	// goes through the projection at all.
	startServeWith(t, cfg, offlineServiceWithDeployment(t, "eu-west-1 staging"))

	doc, raw := readReadyDoc(t, readyPath)

	// The file is dialable the moment it exists: startServe returned as soon as
	// it appeared, and this RPC is the first thing to touch the socket.
	info := dialCompatibilityOverSocket(t, sock)

	if doc.Schema != readyDocSchema {
		t.Errorf("schema = %q, want %q", doc.Schema, readyDocSchema)
	}
	if doc.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", doc.PID, os.Getpid())
	}
	if doc.Transport != transportUnix {
		t.Errorf("transport = %q, want %q", doc.Transport, transportUnix)
	}
	if doc.SocketPath != sock || doc.GRPCAddress != sock {
		t.Errorf("socket_path/grpc_address = %q/%q, want both %q", doc.SocketPath, doc.GRPCAddress, sock)
	}
	// The descriptive half must agree with the projection the server itself
	// serves — the whole point of sourcing it from CompatibilityInfo.
	if doc.APIMajor != info.GetApiMajor() {
		t.Errorf("api_major = %d, want the served %d", doc.APIMajor, info.GetApiMajor())
	}
	if !reflect.DeepEqual(doc.Features, info.GetFeatures()) {
		t.Errorf("features = %v, want the served %v", doc.Features, info.GetFeatures())
	}
	if doc.Deployment != "eu-west-1 staging" {
		t.Errorf("deployment = %q, want the operator-set label", doc.Deployment)
	}

	// Owner-only: the file names a socket a local peer could otherwise discover.
	fi, err := os.Stat(readyPath)
	if err != nil {
		t.Fatalf("stat ready file: %v", err)
	}
	if fi.Mode().Perm() != readyFileMode {
		t.Errorf("ready file mode = %s, want %s", fi.Mode().Perm(), readyFileMode)
	}

	// No temp litter left in the directory beside it.
	assertNoReadyTemps(t, filepath.Dir(readyPath))

	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("ready file should end with a newline so a shell `cat` reads cleanly")
	}
}

// TestSDKServerEnablers_Scenario8_ReadyFileWriteIsAtomic pins writeReadyFile's
// atomicity properties directly: a same-directory temp plus rename, no leftover
// temp on success or failure, and a complete document when overwriting an
// existing file.
//
// The overwrite case is the one a truncating writer gets wrong: a parent polling
// the path during a restart would observe a prefix of the new document and have
// no way to distinguish it from a corrupt one.
func TestSDKServerEnablers_Scenario8_ReadyFileWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ready.json")

	// A filler byte that cannot occur in the new document. "x" would be a false
	// failure: the transport value is "unix".
	const filler = "Z"
	if err := os.WriteFile(path, []byte(strings.Repeat(filler, 4096)), 0o600); err != nil {
		t.Fatalf("seed a longer stale file: %v", err)
	}

	doc := readyDoc{Schema: readyDocSchema, PID: 4242, Transport: transportUnix, GRPCAddress: "/tmp/g.sock", SocketPath: "/tmp/g.sock", APIMajor: 1}
	if err := writeReadyFile(path, doc); err != nil {
		t.Fatalf("writeReadyFile: %v", err)
	}

	got, raw := readReadyDoc(t, path)
	if !reflect.DeepEqual(got, doc) {
		t.Errorf("round trip = %+v, want %+v", got, doc)
	}
	if strings.Contains(string(raw), filler) {
		t.Error("the published file retains bytes of the longer stale document: the write truncated in place instead of renaming over it")
	}
	assertNoReadyTemps(t, dir)

	// A failure must also leave nothing behind.
	if err := writeReadyFile(filepath.Join(dir, "no-such-dir", "ready.json"), doc); err == nil {
		t.Error("writeReadyFile into a missing directory = nil, want an error")
	}
	assertNoReadyTemps(t, dir)
}

// assertNoReadyTemps fails when a ready-file temp survived in dir.
func assertNoReadyTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %q: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mecated-ready-") {
			t.Errorf("leftover ready-file temp %q in %q", e.Name(), dir)
		}
	}
}

// TestSDKServerEnablers_Scenario8_ReadyFilePathIsValidated pins the absolute-path
// requirement: the file is written after the listeners bind, by which point the
// daemon's working directory is not something the spawning parent controls or
// should have to reason about.
func TestSDKServerEnablers_Scenario8_ReadyFilePathIsValidated(t *testing.T) {
	cfg := config{grpcAddr: "127.0.0.1:8080", httpAddr: "127.0.0.1:8081", readyFile: "ready.json"}
	err := validateEffectiveConfig(cfg)
	if err == nil {
		t.Fatal("validateEffectiveConfig = nil, want a relative --ready-file rejected")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("rejection %q does not name the constraint", err)
	}
}

// ---------------------------------------------------------------------------
// AC8.4
// ---------------------------------------------------------------------------

// TestSDKServerEnablers_Scenario8_ReadinessCarriesNoSecrets is AC8.4.
//
// Two layers, deliberately:
//
//   - STRUCTURAL: readyDoc's field set is an allowlist, asserted by reflection.
//     A future field lands here as a test failure, which is the point — the file
//     is the one startup artefact a parent reads mechanically, so whatever ends
//     up in it ends up in whatever that parent logs or attaches to a bug report.
//     The list also records the deliberate omissions (capabilities,
//     authentication, TLS) that keep it inside ADR 0245's privacy boundary.
//   - BEHAVIOURAL: a daemon configured with a bearer token publishes a file
//     containing no trace of it, logs nothing carrying it while starting, and
//     does not leak it through any of the startup REJECTIONS either. AC8.4 names
//     all three surfaces — "the ready file, startup logs, and startup errors" —
//     and the file alone was the narrower claim.
func TestSDKServerEnablers_Scenario8_ReadinessCarriesNoSecrets(t *testing.T) {
	wantJSONKeys := map[string]bool{
		"schema": true, "pid": true, "transport": true, "grpc_address": true,
		"socket_path": true, "http_address": true, "api_major": true,
		"features": true, "deployment": true,
	}
	typ := reflect.TypeOf(readyDoc{})
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		key, _, _ := strings.Cut(tag, ",")
		if !wantJSONKeys[key] {
			t.Errorf("readyDoc gained field %q (json %q) — the ready file is an unauthenticated local artefact and its field set is an allowlist; add it to wantJSONKeys only after confirming it can never carry a credential, and keep capabilities/auth/TLS out per ADR 0245", typ.Field(i).Name, key)
		}
		delete(wantJSONKeys, key)
	}
	for key := range wantJSONKeys {
		t.Errorf("readyDoc lost the %q field the ready-file contract promises", key)
	}

	const token = "s3cret-bearer-token-do-not-leak" //nolint:gosec // a test fixture, deliberately secret-SHAPED
	sock := socketPathForTest(t)
	readyPath := filepath.Join(t.TempDir(), "ready.json")

	logs := captureLogs(t)

	cfg := udsConfig(sock)
	cfg.readyFile = readyPath
	cfg.authToken = token
	cfg.rateLimit = 10
	cfg.deploymentID = "prod"
	startServe(t, cfg)

	_, raw := readReadyDoc(t, readyPath)
	if strings.Contains(string(raw), token) {
		t.Fatalf("the ready file carries the bearer token:\n%s", raw)
	}
	for _, forbidden := range []string{"auth", "token", "tls", "credential", "capabilit"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Errorf("the ready file mentions %q; it must carry no authentication, TLS, or capability detail (ADR 0245):\n%s", forbidden, raw)
		}
	}

	// Surface 2: the startup logs. logListenerPosture and the socket/pipe lines
	// all run before the ready file appears, so the capture above covers them.
	if got := logs(); strings.Contains(got, token) {
		t.Errorf("the startup logs carry the bearer token:\n%s", got)
	}

	// Surface 3: the startup ERRORS. Every rejection path added by daemon hosting
	// composes its message from a path, a mode, an fd number, or a syscall error
	// — never from config. Driving them with a credential in the config is what
	// keeps that true as the messages grow.
	t.Run("rejections", func(t *testing.T) {
		rejectLogs := captureLogs(t)
		for _, tc := range []struct {
			name string
			cfg  func(t *testing.T) config
		}{
			{
				name: "socket path is a regular file",
				cfg: func(t *testing.T) config {
					p := filepath.Join(t.TempDir(), "not-a-socket")
					if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
						t.Fatalf("seed: %v", err)
					}
					return udsConfig(p)
				},
			},
			{
				name: "socket directory is world-writable",
				cfg: func(t *testing.T) config {
					dir := t.TempDir()
					if err := os.Chmod(dir, 0o777); err != nil {
						t.Fatalf("chmod: %v", err)
					}
					return udsConfig(filepath.Join(dir, "g.sock"))
				},
			},
			{
				name: "socket path is too long for sun_path",
				cfg: func(_ *testing.T) config {
					return udsConfig("/" + strings.Repeat("d", sunPathMax) + "/g.sock")
				},
			},
			{
				name: "lifetime pipe fd is not open",
				cfg: func(t *testing.T) config {
					c := udsConfig(socketPathForTest(t))
					c.lifetimePipeFD = closedDescriptor(t)
					return c
				},
			},
			{
				name: "ready file directory does not exist",
				cfg: func(t *testing.T) config {
					c := udsConfig(socketPathForTest(t))
					c.readyFile = filepath.Join(t.TempDir(), "absent", "ready.json")
					return c
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c := tc.cfg(t)
				c.authToken = token
				c.rateLimit = 10
				if c.readyFile == "" {
					c.readyFile = filepath.Join(t.TempDir(), "ready.json")
				}
				err := startupRejection(t, c)
				if strings.Contains(err.Error(), token) {
					t.Errorf("the startup error carries the bearer token: %v", err)
				}
			})
		}
		if got := rejectLogs(); strings.Contains(got, token) {
			t.Errorf("a rejection path logged the bearer token:\n%s", got)
		}
	})
}

// startupRejection runs the real startup path for a configuration that must not
// come up, and returns the error it produced.
//
// It accepts a rejection from EITHER gate — validateEffectiveConfig or serve()
// itself — because which one catches a given misconfiguration is an
// implementation detail, while "it is refused, and the refusal says nothing
// secret" is the contract under test.
func startupRejection(t *testing.T, cfg config) error {
	t.Helper()
	if err := validateEffectiveConfig(cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, newOfflineService(t), prometheus.NewRegistry(), nil, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve returned nil, want the configuration refused at startup")
		}
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("serve neither started nor failed within 20s")
		return nil
	}
}

// ---------------------------------------------------------------------------
// AC8.5
// ---------------------------------------------------------------------------

// TestSDKServerEnablers_Scenario8_LifetimePipeEOFStops is AC8.5 — the
// parent-crash path.
//
// The contract is that the parent never has to signal anything: it holds the
// write end and does nothing. When it dies for any reason the kernel closes its
// descriptors, the read end sees EOF, and the daemon stops through the SAME
// graceful path a SIGTERM takes — the in-flight session still persists, which is
// exactly when a parent crash would otherwise be felt.
func TestSDKServerEnablers_Scenario8_LifetimePipeEOFStops(t *testing.T) {
	// The read end is a RAW descriptor with no Go owner, because serve adopts it:
	// openLifetimePipe wraps it and Closes it on return. An os.Pipe read end would
	// leave two owners of one fd, and the loser closes that number again later,
	// after something unrelated may hold it.
	fd, w := rawPipe(t)

	sock := socketPathForTest(t)
	cfg := udsConfig(sock)
	cfg.readyFile = filepath.Join(t.TempDir(), "ready.json")
	cfg.lifetimePipeFD = fd

	if err := validateEffectiveConfig(cfg); err != nil {
		t.Fatalf("validateEffectiveConfig: %v", err)
	}
	svc := newOfflineService(t)
	// A context that is NEVER cancelled: the pipe must be what stops serve, so
	// a passing test cannot be explained by the signal path.
	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), cfg, svc, prometheus.NewRegistry(), nil, nil) }()
	waitForReadyFile(t, cfg.readyFile, done)

	dialCompatibilityOverSocket(t, sock)

	if err := w.Close(); err != nil {
		t.Fatalf("close the parent's write end: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v after the lifetime pipe closed, want a clean graceful shutdown", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop within 20s of the lifetime pipe reaching EOF")
	}
}

// TestSDKServerEnablers_Scenario8_LifetimePipeIsOptionalAndValidated pins the
// inert default and the descriptor rejection.
//
// Rejecting 0/1/2 is not pedantry: fd 0 is stdin, and treating stdin's EOF as
// "the parent died" would stop the daemon the moment it was started from any
// non-interactive shell.
func TestSDKServerEnablers_Scenario8_LifetimePipeIsOptionalAndValidated(t *testing.T) {
	inert, err := openLifetimePipe(0)
	if err != nil {
		t.Fatalf("openLifetimePipe(0) = %v, want nil: 0 means no pipe was configured", err)
	}
	if inert.Enabled() {
		t.Error("openLifetimePipe(0) must be inert: 0 means no pipe was configured")
	}
	if inert.Closed() != nil {
		t.Error("an inert lifetime pipe must expose a nil channel, which blocks forever in a select")
	}
	inert.Close() // must not panic

	for _, fd := range []int{1, 2} {
		cfg := config{grpcAddr: "127.0.0.1:8080", httpAddr: "127.0.0.1:8081", lifetimePipeFD: fd}
		vErr := validateEffectiveConfig(cfg)
		if vErr == nil {
			t.Fatalf("--lifetime-pipe-fd %d = nil, want it rejected as a standard stream", fd)
		}
		if !strings.Contains(vErr.Error(), "stdin") {
			t.Errorf("rejection %q does not explain why a standard stream is wrong", vErr)
		}
	}

	// A descriptor that passes flag validation but is not actually open must be a
	// startup ERROR, not an immediate EOF. Read as EOF it would mean "the parent
	// died", so the daemon would start, publish its ready file, and vanish
	// milliseconds later with nothing but a WARN — much harder to diagnose than a
	// refusal naming the flag.
	closedFD := closedDescriptor(t)
	if _, err := openLifetimePipe(closedFD); err == nil {
		t.Fatalf("openLifetimePipe(%d) = nil for a closed descriptor, want a startup error", closedFD)
	} else if !strings.Contains(err.Error(), "--lifetime-pipe-fd") {
		t.Errorf("error %q does not name the flag", err)
	}
}

// TestSDKServerEnablers_Scenario8_LifetimePipeRejectsANonPipeDescriptor pins that
// being OPEN is not enough.
//
// openLifetimePipe runs after bindListeners, so a stale or mistyped fd number can
// name a descriptor the daemon already owns. os.NewFile+Stat accepts a listening
// socket happily; reading one yields ENOTCONN, which watch() maps to "parent
// exited". The daemon would publish its ready file, shut down immediately, and
// close that descriptor out from under its real owner on the way out — the same
// undiagnosable failure the closed-fd rejection exists to prevent, reached
// through a different door.
func TestSDKServerEnablers_Scenario8_LifetimePipeRejectsANonPipeDescriptor(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	dup, err := lis.(*net.TCPListener).File()
	if err != nil {
		t.Fatalf("duplicate the listener descriptor: %v", err)
	}
	defer dup.Close()

	_, err = openLifetimePipe(int(dup.Fd()))
	if err == nil {
		t.Fatal("openLifetimePipe accepted a listening socket, want a startup error: a socket is not a parent-liveness signal")
	}
	if !strings.Contains(err.Error(), "--lifetime-pipe-fd") || !strings.Contains(err.Error(), "not a pipe") {
		t.Errorf("error %q must name the flag and say the descriptor is not a pipe", err)
	}

	// The descriptor must survive the rejection, and it must survive a GC.
	//
	// The GC is the assertion, not a formality. os.NewFile attaches a cleanup that
	// CLOSES the descriptor when its wrapper is collected, so validating through an
	// os.File and dropping it on the error path lets the runtime close this fd at
	// an arbitrary later moment — long after the number has been handed to an
	// unrelated socket. That is not a visible failure here; it surfaces as some
	// other component's connection dying for no reason, which is exactly how it
	// was found (a ten-minute hang in an unrelated http.Get). Without forcing the
	// collection this assertion passes whether or not the bug is present.
	for range 5 {
		runtime.GC()
	}
	if _, statErr := dup.Stat(); statErr != nil {
		t.Errorf("the rejected descriptor was closed after a GC (%v); openLifetimePipe must not construct an owning os.File before it commits to adopting the fd", statErr)
	}
}

// TestSDKServerEnablers_Scenario8_LifetimePipeIsQuietOnCleanShutdown pins that an
// ordinary exit does not log a fault.
//
// serve() defers p.Close(), so a ctx.Done() shutdown closes the descriptor under
// the blocked Read and it returns os.ErrClosed rather than io.EOF. Warning there
// prints "lifetime pipe read failed" on every clean exit of a pipe-configured
// daemon — and non-deterministically, since Close() does not join the watcher. A
// line that cries wolf on the happy path is how a real one gets skimmed past.
func TestSDKServerEnablers_Scenario8_LifetimePipeIsQuietOnCleanShutdown(t *testing.T) {
	logs := captureLogs(t)

	fd, w := rawPipe(t)
	defer w.Close() // the "parent" stays alive throughout: only our Close ends the watch

	p, err := openLifetimePipe(fd)
	if err != nil {
		t.Fatalf("openLifetimePipe: %v", err)
	}
	if !p.Enabled() {
		t.Fatal("a configured lifetime pipe must report Enabled")
	}
	p.Close()

	select {
	case <-p.Closed():
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not observe its own Close within 10s")
	}
	if got := logs(); strings.Contains(got, "lifetime pipe read failed") {
		t.Errorf("a clean shutdown logged a read failure:\n%s", got)
	}
}

// closedDescriptor returns a descriptor number that was open and is now closed,
// so nothing else in the process can be holding it.
func closedDescriptor(t *testing.T) int {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	fd := int(r.Fd())
	if err := r.Close(); err != nil {
		t.Fatalf("close read end: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}
	return fd
}

// TestSDKServerEnablers_Scenario8_LifetimePipeIgnoresParentBytes pins that the
// descriptor is a LIVENESS signal and never a control channel.
//
// A parent that writes a heartbeat must be tolerated rather than mistaken for a
// dead one — and, more importantly, bytes on the pipe must never be interpreted:
// treating them as commands would hand a local writer a way to steer the daemon
// with no authentication at all.
func TestSDKServerEnablers_Scenario8_LifetimePipeIgnoresParentBytes(t *testing.T) {
	fd, w := rawPipe(t)
	p, err := openLifetimePipe(fd)
	if err != nil {
		t.Fatalf("openLifetimePipe: %v", err)
	}
	t.Cleanup(p.Close)

	if _, err := w.Write([]byte("shutdown\nstop\n")); err != nil {
		t.Fatalf("write to the pipe: %v", err)
	}
	select {
	case <-p.Closed():
		t.Fatal("the lifetime pipe reported parent exit after a mere write; only EOF means the parent is gone")
	case <-time.After(200 * time.Millisecond):
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}
	select {
	case <-p.Closed():
	case <-time.After(10 * time.Second):
		t.Fatal("the lifetime pipe did not report EOF after the write end closed")
	}
}

// ---------------------------------------------------------------------------
// AC8.6
// ---------------------------------------------------------------------------

// TestSDKServerEnablers_Scenario8_StaleSocketCleanup is AC8.6.
//
// The filesystem cannot tell the two cases apart — a UNIX socket inode looks
// identical whether or not anyone is listening — so the test is behavioural, and
// so is the implementation: dial it. The case that matters most is the LIVE one:
// unlinking a live peer's socket silently steals its address, and the second
// daemon then answers RPCs the first one's clients meant for it.
func TestSDKServerEnablers_Scenario8_StaleSocketCleanup(t *testing.T) {
	t.Run("a stale socket from a dead process is removed and rebound", func(t *testing.T) {
		sock := socketPathForTest(t)
		if err := os.MkdirAll(filepath.Dir(sock), socketDirMode); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Bind then close WITHOUT unlinking, which is what a killed process
		// leaves behind. net.UnixListener unlinks on Close, so the file is
		// re-created by hand to reproduce the corpse faithfully.
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatalf("seed a socket: %v", err)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("close seed listener: %v", err)
		}
		reseedStaleSocket(t, sock)

		lis, err := listenUnixSocket(sock)
		if err != nil {
			t.Fatalf("listenUnixSocket over a stale socket: %v", err)
		}
		defer func() { _ = lis.Close() }()
		if got := lis.Addr().String(); got != sock {
			t.Errorf("bound address = %q, want %q", got, sock)
		}
	})

	t.Run("a socket a live process is accepting on refuses the start", func(t *testing.T) {
		sock := socketPathForTest(t)
		if err := os.MkdirAll(filepath.Dir(sock), socketDirMode); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		live, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatalf("seed a live listener: %v", err)
		}
		defer func() { _ = live.Close() }()

		_, err = listenUnixSocket(sock)
		if err == nil {
			t.Fatal("listenUnixSocket = nil over a LIVE socket: removing it would silently steal the running daemon's address")
		}
		if !strings.Contains(err.Error(), "live") {
			t.Errorf("refusal %q does not say the socket is live", err)
		}
		// The live peer's socket must still be there and still accepting.
		if _, statErr := os.Stat(sock); statErr != nil {
			t.Fatalf("the live peer's socket was removed: %v", statErr)
		}
		conn, dialErr := net.DialTimeout("unix", sock, staleSocketDialTimeout)
		if dialErr != nil {
			t.Fatalf("the live peer stopped accepting after the refused start: %v", dialErr)
		}
		_ = conn.Close()
	})

	t.Run("a path that is not a socket is refused, never removed", func(t *testing.T) {
		dir := t.TempDir()
		regular := filepath.Join(dir, "important.txt")
		if err := os.WriteFile(regular, []byte("operator data"), 0o600); err != nil {
			t.Fatalf("seed a regular file: %v", err)
		}
		err := reclaimStaleSocket(regular)
		if err == nil {
			t.Fatal("reclaimStaleSocket = nil on a regular file: deleting an operator's file because a flag pointed at it is data loss, not cleanup")
		}
		if !strings.Contains(err.Error(), "not a socket") {
			t.Errorf("refusal %q does not name the reason", err)
		}
		if body, readErr := os.ReadFile(regular); readErr != nil || string(body) != "operator data" {
			t.Fatalf("the regular file was modified or removed (body=%q err=%v)", body, readErr)
		}
	})

	t.Run("a socket that cannot be probed refuses the start", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions, so the unprobeable case cannot be constructed")
		}
		dir := filepath.Join(t.TempDir(), "sealed")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		sock := filepath.Join(dir, "g.sock")
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		// The rule under test is fail-CLOSED: only a REFUSED connect proves the
		// inode has no listener. Every other outcome — an unreadable directory
		// here, a timeout because a live peer's accept backlog is full in
		// production — is ambiguous, and reading ambiguity as "stale" would
		// unlink a live daemon's socket through the back door.
		if err := reclaimStaleSocket(sock); err == nil {
			t.Fatal("reclaimStaleSocket = nil for a path it could not probe, want a refusal")
		}
	})

	t.Run("a missing socket path is not an error", func(t *testing.T) {
		if err := reclaimStaleSocket(filepath.Join(t.TempDir(), "absent.sock")); err != nil {
			t.Fatalf("reclaimStaleSocket on a missing path = %v, want nil (the first start is the common case)", err)
		}
	})
}

// reseedStaleSocket recreates the socket inode a killed process would have left
// behind. net.UnixListener unlinks the path on Close, so a faithful corpse has
// to be re-made — and it has to be a SOCKET, since reclaimStaleSocket rightly
// refuses to remove anything else.
func reseedStaleSocket(t *testing.T, sock string) {
	t.Helper()
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("recreate the socket inode: %v", err)
	}
	// Drop the unlink-on-close behaviour so the inode survives: a *net.UnixListener
	// with SetUnlinkOnClose(false) leaves the file exactly as a SIGKILL would.
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close the recreated listener: %v", err)
	}
	info, err := os.Lstat(sock)
	if err != nil {
		t.Fatalf("the stale socket did not survive: %v", err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("seeded path %q is mode %s, want a socket", sock, info.Mode())
	}
}

// ---------------------------------------------------------------------------
// AC8.7
// ---------------------------------------------------------------------------

// TestSDKServerEnablers_Scenario8_SocketPermissionsOwnerOnly is AC8.7.
//
// It asserts BOTH defences, because only one of them actually closes the window:
//
//   - the socket file is owner-only, set at CREATION by the bind-time umask
//     rather than by a chmod that follows bind. A chmod after bind leaves a real
//     interval in which the socket is connectable, and on a socket carrying an
//     unauthenticated local harness API that interval is command execution.
//   - the directory this process created for it is 0700, which is what makes the
//     socket unreachable even during that interval.
func TestSDKServerEnablers_Scenario8_SocketPermissionsOwnerOnly(t *testing.T) {
	sock := socketPathForTest(t)
	lis, err := listenUnixSocket(sock)
	if err != nil {
		t.Fatalf("listenUnixSocket: %v", err)
	}
	defer func() { _ = lis.Close() }()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("socket mode = %s, want owner-only (no group or other bits)", perm)
	}

	dir, err := os.Stat(filepath.Dir(sock))
	if err != nil {
		t.Fatalf("stat socket directory: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != socketDirMode {
		t.Errorf("created socket directory mode = %s, want %s: the directory, not a post-bind chmod, is what leaves no window", perm, socketDirMode)
	}
}

// TestSDKServerEnablers_Scenario8_ExistingSocketDirIsNotChmodded pins the
// deliberate restraint in ensureSocketDir.
//
// A directory that already exists is left alone. Chmod'ing an operator's /tmp,
// XDG runtime directory, or systemd RuntimeDirectory to 0700 would be a far
// worse outcome than the risk it closes — so a merely group/world-READABLE one
// is accepted with a WARN. Others can stat the socket but, since it is 0700,
// cannot connect to it, and since unlink(2) checks the DIRECTORY's write bit,
// cannot remove it either.
func TestSDKServerEnablers_Scenario8_ExistingSocketDirIsNotChmodded(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := ensureSocketDir(dir); err != nil {
		t.Fatalf("ensureSocketDir on an existing directory = %v, want nil", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o755 {
		t.Fatalf("ensureSocketDir changed an existing directory's mode to %s; it must not chmod a directory the operator owns", perm)
	}

	// A path that exists and is not a directory is a configuration error, not
	// something to overwrite.
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := ensureSocketDir(file); err == nil {
		t.Error("ensureSocketDir = nil for a non-directory path, want an error")
	}
}

// TestSDKServerEnablers_Scenario8_WritableSocketDirIsRefused pins the case where
// the socket's own mode defends nothing.
//
// unlink(2) checks write permission on the DIRECTORY, not on the file. So in a
// group/world-writable, non-sticky directory any local user can unlink the
// daemon's socket and bind their own listener at the same path: the daemon keeps
// serving the now-unlinked inode while every NEW client — the spawning parent
// included, since it dials the path it read from the ready file — reaches the
// impostor. On an unauthenticated local API that is interception of prompts,
// tool calls, and tool output, so it is refused rather than warned about.
//
// The sticky bit is the exemption that keeps /tmp usable: with it set, only the
// owner may unlink their own socket.
func TestSDKServerEnablers_Scenario8_WritableSocketDirIsRefused(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o707, 0o770} {
		dir := t.TempDir()
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatalf("chmod %s: %v", mode, err)
		}
		err := ensureSocketDir(dir)
		if err == nil {
			t.Errorf("ensureSocketDir(mode %s) = nil, want a refusal: a non-sticky writable directory lets any local user unlink the socket and bind their own", mode)
			continue
		}
		if !strings.Contains(err.Error(), "sticky") || !strings.Contains(err.Error(), "0700") {
			t.Errorf("refusal %q must name both remedies (an owner-only directory, or a sticky one)", err)
		}
	}

	// Sticky is accepted: /tmp is a legitimate socket home precisely because the
	// sticky bit restores the unlink restriction.
	sticky := t.TempDir()
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("chmod sticky: %v", err)
	}
	if err := ensureSocketDir(sticky); err != nil {
		t.Errorf("ensureSocketDir on a sticky world-writable directory = %v, want nil: the sticky bit is what makes /tmp usable", err)
	}
}

// ---------------------------------------------------------------------------
// flag-surface wiring
// ---------------------------------------------------------------------------

// TestSDKServerEnablers_Scenario8_FlagsAreRegisteredAndDefaultOff pins that the
// three flags reach cfg through the REAL parseFlags path, and that their absence
// is byte-identical to the pre-Scenario-8 daemon: no socket, no ready file, no
// pipe.
func TestSDKServerEnablers_Scenario8_FlagsAreRegisteredAndDefaultOff(t *testing.T) {
	base, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if base.grpcUnixSocket != "" || base.readyFile != "" || base.lifetimePipeFD != 0 {
		t.Fatalf("daemon-hosting flags are not off by default: socket=%q ready=%q fd=%d",
			base.grpcUnixSocket, base.readyFile, base.lifetimePipeFD)
	}
	if base.tcpGRPCConfigured() {
		t.Error("tcpGRPCConfigured() is true with no --grpc-addr passed; the default must not read as a request")
	}

	cfg, err := parseFlags([]string{
		"--grpc-unix-socket", "/tmp/mecated/g.sock",
		"--ready-file", "/tmp/mecated/ready.json",
		"--lifetime-pipe-fd", "7",
		"--http-addr", "",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.grpcUnixSocket != "/tmp/mecated/g.sock" {
		t.Errorf("grpcUnixSocket = %q", cfg.grpcUnixSocket)
	}
	if cfg.readyFile != "/tmp/mecated/ready.json" {
		t.Errorf("readyFile = %q", cfg.readyFile)
	}
	if cfg.lifetimePipeFD != 7 {
		t.Errorf("lifetimePipeFD = %d, want 7", cfg.lifetimePipeFD)
	}
	if cfg.httpAddr != "" {
		t.Errorf("httpAddr = %q, want the explicit empty value to survive", cfg.httpAddr)
	}
	if cfg.tcpGRPCConfigured() {
		t.Error("tcpGRPCConfigured() is true without --grpc-addr")
	}
	if err := validateEffectiveConfig(cfg); err != nil {
		t.Fatalf("the spawned-daemon flag combination must validate: %v", err)
	}

	explicit, err := parseFlags([]string{"--grpc-addr", "127.0.0.1:9999"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !explicit.tcpGRPCConfigured() {
		t.Error("tcpGRPCConfigured() is false after an explicit --grpc-addr")
	}
}

// withExplicit marks flagName as explicitly passed on the command line, the way
// recordExplicitFlags would.
func withExplicit(cfg config, flagName string) config {
	if cfg.cliExplicit == nil {
		cfg.cliExplicit = map[string]bool{}
	}
	cfg.cliExplicit[flagName] = true
	return cfg
}

// withFileGRPCAddr marks grpc_addr as supplied by a daemon config file, the way
// mergeDaemonConfig would.
func withFileGRPCAddr(cfg config) config {
	cfg.grpcAddrFromFile = true
	return cfg
}

// offlineServiceWithDeployment is newOfflineService plus an operator-set
// deployment label, so a test can assert the label reaches the ready file
// through the CompatibilityInfo projection rather than from cfg.
func offlineServiceWithDeployment(t *testing.T, deploymentID string) *server.Service {
	t.Helper()
	llm := mockllm.New(mockllm.TextTurn("ok"))
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     llm,
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
			Model:   "test-model",
		}),
		Store: memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		DeploymentID:        deploymentID,
		PlacementProvider:   offlinePlacementProvider{},
		PlacementScope:      "test",
		SharedEngineRoot:    "/ws",
	})
	if err != nil {
		t.Fatalf("new offline service: %v", err)
	}
	return svc
}
