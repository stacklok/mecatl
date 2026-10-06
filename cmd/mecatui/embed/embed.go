// Package embed lets mecatui host its OWN mecated server in-process when no
// external one is running, so a single `mecatui` binary "just works" with no
// separately-spawned daemon and no TCP port.
//
// It assembles the harness via the SHARED composition layer (internal/app) — the
// exact same engine, tools, permission policy, and service the standalone mecated
// binary builds — and serves it over a per-process UNIX socket in a private temp
// directory. The TUI then dials that socket as an ordinary gRPC client, so the
// ui/theme/client packages stay pure: they never learn the server is in-process.
//
// Architectural boundary: this package — like cmd/mecatui/client and the
// cmd/mecatui main — is the ONLY place in the TUI tree allowed to import
// contracts/gen, grpc, internal/app, internal/adapter/*, and the server adapter.
// The render packages (ui, theme) and the client package import none of it.
package embed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/trace"
	"time"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpperf"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// socketName is the fixed socket filename inside the per-process temp directory.
// The directory is randomised (os.MkdirTemp), so the filename can be stable.
const socketName = "mecated.sock"

// gracefulStopTimeout bounds s.grpc.GracefulStop() in Close: after it elapses the
// server is hard-stopped via s.grpc.Stop(). Package-level var so white-box tests
// can override to a short value.
var gracefulStopTimeout = 30 * time.Second

// compositionCloseTimeout bounds s.appstop() in Close: after it elapses Close
// proceeds to os.RemoveAll(s.dir) without waiting further. Package-level var so
// white-box tests can override to a short value.
var compositionCloseTimeout = 10 * time.Second

// grpcServer exposes the two gRPC shutdown methods Close needs: GracefulStop and
// Stop. *grpc.Server satisfies it, and white-box tests can inject a mock.
type grpcServer interface {
	GracefulStop()
	Stop()
}

// adminSocketName is the fixed admin socket filename beside the embedded gRPC
// socket. The containing directory is randomised per instance.
const adminSocketName = "admin.sock"

// PerfConfig is the opt-in perf-observability configuration for the embedded
// server (decision 7 in docs/adr/0018-perf-observability.md). It is OFF by default
// (the zero value): mecatui hosts a bare gRPC socket with no telemetry, exactly
// as before. When Enabled, Start arms the SAME runtime-introspection surface
// mecated exposes — pprof, expvar, the runtime/RSS snapshot, and the execution
// FlightRecorder — on a loopback HTTP listener, plus the domain-metrics EventSink
// wired into the embedded engine so turn/tool/latency series render at /metrics.
//
// The motivating incident (a render-starvation + memory-growth freeze in a
// since-removed tool) was a mecatui process freeze, so goroutines, RSS, pprof, and the flight
// recorder are exactly the instruments it needed — hence covering the embedded
// server, not just the standalone daemon.
type PerfConfig struct {
	// Enabled turns the whole perf surface on. The zero value (false) means no
	// telemetry, no admin listener, no flight recorder, no watchdog.
	Enabled bool
	// Addr is an explicit loopback TCP listen address for the admin mux. When
	// empty, the admin surface uses a private UNIX socket beside the embedded
	// gRPC socket; MCP instead uses ephemeral 127.0.0.1 TCP because the current
	// streaming-HTTP MCP client transport cannot dial HTTP over UNIX.
	Addr string
	// MCP mounts the read-only perf MCP server (internal/adapter/mcpperf) at /mcp on
	// the embedded admin mux, so an agent can introspect THIS process's
	// runtime/latency/profile state over MCP. Only meaningful with Enabled.
	// Explicit addresses are loopback-only; with no address MCP uses ephemeral
	// loopback TCP. A slow-turn ring is wired into the embedded engine's sink.
	MCP bool
	// GoroutineWarnThreshold arms the live goroutine-leak watchdog (decision 10):
	// a background sampler logs slog.Warn whenever runtime.NumGoroutine() exceeds
	// this count. 0 (default) disables the alarm; the runtime collector still
	// exports the goroutine count as a /metrics series regardless.
	GoroutineWarnThreshold int
	// GoroutineWarnInterval is how often the watchdog samples NumGoroutine. <= 0
	// falls back to the watchdog's own 30s default.
	GoroutineWarnInterval time.Duration
	// Logger receives the perf-surface startup/teardown lines and the watchdog
	// alarms. Nil falls back to slog.Default().
	Logger *slog.Logger
}

// Server is a mecated server hosted in the current process, listening on a UNIX
// socket. Close it to stop serving and release the socket, temp dir, and any
// composition-owned resources (the MCP manager) plus, when perf is enabled, the
// admin listener, the watchdog, the flight recorder, and the telemetry providers.
// It is safe to call Close once.
type Server struct {
	target  string // gRPC dial target, e.g. "unix:///run/user/1000/mecatui-123/mecated.sock"
	dir     string // private temp dir holding the socket
	grpc    grpcServer
	appstop func() // app.Built.Close — tears down MCP etc.

	// Perf teardown (all nil/no-op when PerfConfig.Enabled is false). adminSrv is
	// the private UNIX or loopback TCP admin HTTP server; perfStop cancels the
	// watchdog context and stops the flight recorder; perfShutdown flushes providers.
	adminNetwork string
	adminAddr    string
	adminSrv     *http.Server
	perfStop     func()
	perfShutdown func(context.Context) error

	// recorderArmed is true only when THIS server armed the process FlightRecorder
	// (ProcessFlightRecorder returned nil) — i.e. it owns it and will Stop it on
	// Close. False when perf is off, the recorder failed, or this server coalesced
	// onto an instance another owner armed. It exists so a test can tell whether
	// /debug/flightrecorder will serve a live snapshot (armed) versus a stopped
	// process-singleton (the sync.Once limitation; see ProcessFlightRecorder).
	recorderArmed bool
}

// registerLocalSessionContextServer registers ADR 0296's privileged projection only
// on this package's owner-private Unix socket. It intentionally accepts no general
// opt-in flag: starting embedded Mecatui is the v1 opt-in.
func registerLocalSessionContextServer(grpcSrv *grpc.Server, lis net.Listener, local *server.LocalSessionContextServer) error {
	if grpcSrv == nil || local == nil {
		return errors.New("local session context requires a server and service")
	}
	if err := verifyEmbeddedPrivateUnixListener(lis); err != nil {
		return fmt.Errorf("local session context requires embedded private listener: %w", err)
	}
	mecatlv1.RegisterLocalSessionContextServiceServer(grpcSrv, local)
	return nil
}

// verifyEmbeddedPrivateUnixListener admits only a filesystem Unix socket beneath
// an owner-only directory. TCP and even loopback are deliberately insufficient:
// they do not attest a single local user can reach the privileged projection.
func verifyEmbeddedPrivateUnixListener(lis net.Listener) error {
	unixLis, ok := lis.(*net.UnixListener)
	if !ok || unixLis == nil {
		return errors.New("listener is not a Unix socket")
	}
	addr, ok := unixLis.Addr().(*net.UnixAddr)
	if !ok || addr == nil || addr.Net != "unix" || addr.Name == "" || !filepath.IsAbs(addr.Name) {
		return errors.New("listener is not a filesystem Unix socket")
	}
	dir, err := os.Stat(filepath.Dir(addr.Name))
	if err != nil {
		return fmt.Errorf("stat socket directory: %w", err)
	}
	if !dir.IsDir() || dir.Mode().Perm() != 0o700 {
		return errors.New("socket directory is not owner-only")
	}
	socket, err := os.Lstat(addr.Name)
	if err != nil {
		return fmt.Errorf("stat socket: %w", err)
	}
	if socket.Mode()&os.ModeSocket == 0 || socket.Mode().Perm() != 0o600 {
		return errors.New("socket is not owner-only")
	}
	return nil
}

// Start builds the harness from cfg via internal/app and serves it over a fresh
// UNIX socket in a private temp directory. The returned Server's Target() is a
// gRPC dial string a client can connect to immediately (the listener is open
// before Start returns; serving runs on a background goroutine).
//
// When perf.Enabled, Start ALSO installs the perf-observability surface
// (decision 7): it builds a telemetry MeterProvider + prometheus registry via the
// same telemetry.Setup path mecated uses, wires the domain-metrics EventSink into
// the embedded engine, registers the runtime collector + process-RSS gauge, arms
// the process-singleton FlightRecorder, optionally arms the goroutine watchdog,
// and serves the admin mux (/metrics, /debug/pprof/*, /debug/vars,
// /debug/flightrecorder) on a loopback HTTP listener. All of it is torn down by
// Server.Close, so an enabled perf surface never leaks a listener, a watchdog
// goroutine, or the flight recorder's runtime-trace subscription.
//
// ctx governs the lifetime of composition-owned background work (MCP manager,
// memory consolidation) AND the perf watchdog; cancelling it does NOT stop the
// gRPC or admin servers — call Close for that. On any setup error Start cleans up
// everything it created before returning, so the caller never leaks a socket,
// temp dir, or telemetry resource.
func Start(ctx context.Context, cfg app.Config, perf PerfConfig) (*Server, error) {
	// This private Unix-socket server is the one explicit compatibility authority
	// for local single-user storage management. Remote roots never set this bit.
	cfg.LocalStorageManagement = true
	// Reserve the private per-instance directory first. The default perf admin
	// socket is a sibling of this gRPC socket, so both share the same ownership,
	// Darwin path fallback, and cleanup lifecycle.
	lis, dir, sock, err := newUnixSocketListener()
	if err != nil {
		return nil, err
	}
	cleanupRuntime := func() {
		_ = lis.Close()
		_ = os.RemoveAll(dir)
	}

	// Perf setup happens BEFORE app.Build so the domain-metrics EventSink can be
	// injected into the engine via cfg.Sink/cfg.ToolCallRecorder.
	ps, err := setupPerf(ctx, perf, &cfg, dir)
	if err != nil {
		cleanupRuntime()
		return nil, err
	}

	cfg, err = app.ConfigureExecution(cfg)
	if err != nil {
		ps.teardown(ctx)
		cleanupRuntime()
		return nil, err
	}
	built, err := app.Build(ctx, cfg)
	if err != nil {
		ps.teardown(ctx)
		cleanupRuntime()
		return nil, err
	}

	// No auth/TLS interceptors: the socket lives in a private, user-owned temp dir
	// (0700 via MkdirTemp) and only this process knows its path — the same
	// single-user loopback trust model mecated uses for 127.0.0.1, with a tighter
	// blast radius (filesystem perms, no network surface at all).
	grpcSrv := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcSrv, server.NewHarnessServer(built.Service))
	mecatlv1.RegisterScheduleServiceServer(grpcSrv, server.NewScheduleServer(built.Service))
	if err := registerLocalSessionContextServer(grpcSrv, lis, server.NewLocalSessionContextServer(built.Service)); err != nil {
		grpcSrv.Stop()
		built.Close()
		ps.teardown(ctx)
		cleanupRuntime()
		return nil, err
	}

	// Mount the standard gRPC health service so orchestration tooling can confirm
	// readiness over the same socket (the TUI client itself dials + creates a
	// session as its readiness check).
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("mecatl.v1.HarnessService", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("mecatl.v1.ScheduleService", healthpb.HealthCheckResponse_SERVING)

	go func() { _ = grpcSrv.Serve(lis) }() // returns when GracefulStop is called

	srv := &Server{
		target:        "unix://" + sock,
		dir:           dir,
		grpc:          grpcSrv,
		appstop:       built.Close,
		adminNetwork:  ps.adminNetwork,
		adminAddr:     ps.adminAddr,
		adminSrv:      ps.adminSrv,
		perfStop:      ps.stop,
		perfShutdown:  ps.shutdown,
		recorderArmed: ps.recorderArmed,
	}
	return srv, nil
}

// RecorderArmed reports whether THIS server armed (and therefore owns + will stop)
// the process FlightRecorder. It is false when perf is disabled, when the recorder
// failed to start, or when this server coalesced onto a recorder another owner
// armed — including a second perf-enabled run in the same process after the first
// run stopped the process-singleton (the sync.Once cannot be re-armed). Tests use
// it to decide whether /debug/flightrecorder will serve a live snapshot.
func (s *Server) RecorderArmed() bool { return s.recorderArmed }

// Target returns the gRPC dial string for the hosted server (a "unix://" target).
func (s *Server) Target() string { return s.target }

// AdminNetwork returns "unix" for the collision-free default or "tcp" for an
// explicit TCP address and for the streaming-HTTP MCP fallback.
func (s *Server) AdminNetwork() string { return s.adminNetwork }

// AdminAddr returns the resolved admin listener address: a private socket path
// for UNIX or host:port for TCP. It is empty when perf is disabled.
func (s *Server) AdminAddr() string { return s.adminAddr }

// Close stops the gRPC server with a bounded graceful-stop window, tears down
// composition-owned resources with a separate bounded window, and (when perf was
// enabled) the admin listener, the watchdog, the flight recorder, and the
// telemetry providers, then removes the socket and its temp directory. It is safe
// to call once.
func (s *Server) Close() error {
	// 1. Bounded gRPC shutdown: GracefulStop with a timeout, hard-stop fallback.
	stopped := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(gracefulStopTimeout):
		s.grpc.Stop()
		<-stopped
	}

	// 2. Tear down the perf surface in reverse order of construction: stop the admin
	//    listener, then the watchdog + flight recorder, then flush the providers.
	if s.adminSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.adminSrv.Shutdown(shutdownCtx)
		cancel()
	}
	if s.perfStop != nil {
		s.perfStop()
	}
	if s.perfShutdown != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.perfShutdown(shutdownCtx)
		cancel()
	}

	// 3. Bounded composition teardown: run appstop in a goroutine with a timeout.
	if s.appstop != nil {
		done := make(chan struct{})
		go func() {
			s.appstop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(compositionCloseTimeout):
			// Embedded path: log nothing — proceed to dir cleanup.
		}
	}

	return os.RemoveAll(s.dir)
}

// perfState gathers the teardown handles for an armed perf surface so Start can
// unwind cleanly on a later error and Close can release everything in one place.
type perfState struct {
	adminNetwork  string
	adminAddr     string
	adminSrv      *http.Server
	stop          func()                      // cancels the watchdog ctx + stops the flight recorder
	shutdown      func(context.Context) error // flushes the telemetry providers
	recorderArmed bool                        // this server armed (and owns) the process FlightRecorder
}

// teardown releases everything a partially- or fully-built perfState holds. It is
// safe on a zero perfState (perf disabled), so Start's error paths can call it
// unconditionally.
func (p perfState) teardown(ctx context.Context) {
	if p.adminSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = p.adminSrv.Shutdown(shutdownCtx)
		cancel()
	}
	if p.stop != nil {
		p.stop()
	}
	if p.shutdown != nil {
		_ = p.shutdown(ctx)
	}
}

// setupPerf installs the perf-observability surface when perf.Enabled, mutating
// cfg to inject the domain-metrics EventSink/ToolCallRecorder into the engine BEFORE
// app.Build runs. It returns a perfState carrying the teardown handles (a zero
// perfState when perf is disabled). On any setup error it unwinds whatever it has
// already built and returns the error.
func setupPerf(ctx context.Context, perf PerfConfig, cfg *app.Config, runtimeDir string) (perfState, error) {
	if !perf.Enabled {
		return perfState{}, nil
	}
	logger := perf.Logger
	if logger == nil {
		// Discard, not slog.Default(): the embedded TUI never wants a perf line on
		// stderr/the alt-screen. The real caller (cmd/mecatui) always injects a
		// file-backed Logger AND redirects the global default to the same file, so
		// this fallback only fires for a caller that wired no Logger at all.
		logger = slog.New(slog.DiscardHandler)
	}

	// An explicit address always selects TCP. The entire admin mux is sensitive,
	// not just /mcp, so reject non-loopback binds before creating telemetry.
	if perf.Addr != "" && !isLoopbackHostPort(perf.Addr) {
		return perfState{}, fmt.Errorf("perf admin refuses non-loopback Addr=%s: it exposes unauthenticated runtime data; bind loopback or add auth (future work)", perf.Addr)
	}

	// Telemetry providers: metrics ALWAYS on (no OTLP endpoint needed — the
	// embedded server only serves loopback Prometheus + introspection), the same
	// Setup path mecated uses. The runtime collector (go.goroutine.count, GC, heap)
	// is started against this MeterProvider by Setup.
	providers, err := telemetry.Setup(ctx, telemetry.OTLPConfig{ServiceName: "mecatui-embedded"})
	if err != nil {
		return perfState{}, fmt.Errorf("setup telemetry: %w", err)
	}
	var ps perfState
	ps.shutdown = providers.Shutdown

	// Process-RSS gauge (mecatl.process.rss): Linux-only, no-op elsewhere; rides
	// the same MeterProvider so it renders on /metrics (decision 9).
	if rerr := telemetry.RegisterProcessGauges(providers.Meter, slogdiag.NewFromLogger(logger)); rerr != nil {
		ps.teardown(ctx)
		return perfState{}, fmt.Errorf("setup process gauges: %w", rerr)
	}

	// Domain-metrics EventSink: wire turn/tool/latency instruments into the
	// embedded engine so the embedded server's domain series show up at /metrics
	// too (app.Config.Sink/ToolCallRecorder are optional injection — the engine
	// nil-guards both). app.Build already supports this seam, so we use it.
	metrics, err := telemetry.NewMetrics(providers.Meter)
	if err != nil {
		ps.teardown(ctx)
		return perfState{}, fmt.Errorf("setup metrics: %w", err)
	}
	tracing := telemetry.NewTracing(otel.GetTracerProvider())

	// Domain sinks: the role-scoped main pair, the optional slow-turn ring, and
	// the child role scoper (issue #47) — wired by the shared helper below. With
	// perf disabled, setupPerf returns before this point and cfg.Sink /
	// cfg.MetricsRoleScoper stay nil (children unmetered, the no-perf path).
	slowTurns := wirePerfSinks(cfg, metrics, tracing, perf.MCP)

	// FlightRecorder: arm the bounded execution-trace ring buffer via the
	// process-singleton accessor (only one may be active process-wide). We may
	// either ARM it (rerr == nil ⇒ this call created+started the singleton) or
	// COALESCE onto an instance another owner already armed
	// (ErrFlightRecorderAlreadyActive ⇒ the recorder is still usable for snapshots,
	// but we did NOT start it). Only the owner may Stop it: ownsRecorder is true
	// solely in the arming case, mirroring mecated's owner-only-stops pattern (it
	// `defer recorder.Stop()`s only in its default arm branch). Stopping a recorder
	// we merely coalesced onto would yank the runtime-trace subscription out from
	// under its real owner (a co-running mecated, or a second embed in this
	// process). NOTE the sync.Once singleton: once ANY owner Stops the process
	// recorder it cannot be re-armed in the same process — fine for the
	// one-embed-per-process production wiring, but a hard constraint for a test
	// binary or any future multi-embed (see ProcessFlightRecorder's doc).
	var recorder *telemetry.FlightRecorder
	var ownsRecorder bool
	rec, rerr := telemetry.ProcessFlightRecorder(trace.FlightRecorderConfig{})
	switch {
	case errors.Is(rerr, telemetry.ErrFlightRecorderAlreadyActive):
		recorder = rec // usable for snapshots, but another owner armed it
		logger.Info("flight recorder already active process-wide; reusing the shared instance (will NOT stop it — not our recorder)")
	case rerr != nil:
		logger.Warn("flight recorder failed to start; continuing without it", "err", rerr)
	default:
		recorder = rec
		ownsRecorder = true // this call armed it ⇒ this server stops it on Close
	}
	ps.recorderArmed = ownsRecorder

	// Goroutine-leak watchdog (decision 10): bound to a child ctx we cancel on
	// teardown so the alarm itself never leaks.
	watchdogCtx, cancelWatchdog := context.WithCancel(ctx)
	if perf.GoroutineWarnThreshold > 0 {
		telemetry.StartGoroutineWatchdog(watchdogCtx, perf.GoroutineWarnThreshold, perf.GoroutineWarnInterval, runtime.NumGoroutine, logger)
		logger.Info("goroutine-leak watchdog armed (embedded server)",
			"threshold", perf.GoroutineWarnThreshold, "interval", perf.GoroutineWarnInterval)
	}

	// stop cancels the watchdog and — ONLY if this server armed it — stops the
	// flight recorder. A coalesced recorder belongs to another owner, so we must
	// not Stop it here (that would tear down the shared runtime-trace subscription
	// the real owner still relies on). The provider shutdown stays separate
	// (ps.shutdown) so Close can order it after the listener drain.
	ps.stop = func() {
		cancelWatchdog()
		if recorder != nil && ownsRecorder {
			recorder.Stop()
		}
	}

	// Bind eagerly so callers can report the resolved endpoint. Plain perf defaults
	// to a collision-free private UNIX socket. The current MCP SDK's streaming-HTTP
	// client transport has no HTTP-over-UNIX dial hook, so MCP defaults to ephemeral
	// loopback TCP instead. An explicit --perf-addr always selects loopback TCP.
	var (
		lis     net.Listener
		lerr    error
		network string
	)
	switch {
	case perf.Addr != "":
		network = "tcp"
		lis, lerr = net.Listen(network, perf.Addr)
	case perf.MCP:
		network = "tcp"
		lis, lerr = net.Listen(network, "127.0.0.1:0")
	default:
		network = "unix"
		lis, lerr = listenPrivateUnix(filepath.Join(runtimeDir, adminSocketName))
	}
	if lerr != nil {
		ps.teardown(ctx)
		return perfState{}, fmt.Errorf("listen perf admin %s: %w", network, lerr)
	}
	ps.adminNetwork = network
	ps.adminAddr = lis.Addr().String()
	adminMux := telemetry.NewAdminMux(providers.Registry, recorder)
	adminPaths := "/metrics /debug/pprof /debug/vars /debug/flightrecorder"
	if perf.MCP {
		adminMux.Handle("/mcp", mcpperf.Handler(mcpperf.Deps{
			Snapshot:  telemetry.Snapshot,
			Gatherer:  providers.Registry,
			Recorder:  recorder, // nil-able
			Profiler:  mcpperf.NewProfiler(),
			SlowTurns: slowTurnSource(slowTurns),
			Clock:     time.Now,
			Logger:    logger,
		}))
		adminPaths += " /mcp"
	}
	ps.adminSrv = &http.Server{
		Handler:           adminMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if serveErr := ps.adminSrv.Serve(lis); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Warn("perf admin server stopped", "err", serveErr)
		}
	}()
	logger.Info("perf admin server listening (private, UNAUTHENTICATED — single-user trust model)",
		"network", ps.adminNetwork, "addr", ps.adminAddr, "paths", adminPaths)

	return ps, nil
}

// wirePerfSinks wires the domain-metrics observability onto cfg (issue #47):
//
//   - the MAIN engine's Sink/ToolCallRecorder become the role="main" scoped view
//     over the shared instruments (every series carries the role label uniformly),
//     fanned out with tracing and — when the perf MCP server is mounted — the
//     slow-turn ring buffer (main turns recorded with role="main");
//   - cfg.MetricsRoleScoper hands each CHILD engine a role-scoped (EventSink,
//     ToolCallRecorder) pair keyed on the BOUNDED family label internal/app's
//     roleFamily already resolved; the child sink also fans into the SAME
//     slow-turn ring so child turns carry their role in list_slow_turns.
//
// It returns the slow-turn buffer (nil when the perf MCP server is off) for the
// mcpperf Deps wiring.
func wirePerfSinks(cfg *app.Config, metrics *telemetry.Metrics, tracing port.EventSink, mountMCP bool) *telemetry.SlowTurnBuffer {
	// Capture whatever cfg.Sink/cfg.ToolCallRecorder ALREADY held before either
	// field is reassigned below — the product-metrics tap main.go wired onto
	// composition BEFORE Start (and thus before setupPerf/wirePerfSinks ran),
	// when perf is also enabled. Folding it in here (rather than overwriting)
	// keeps the tap alive alongside the perf metrics; a nil oldSink/
	// oldToolCallRecorder (perf-only, no product metrics) is the byte-identical
	// prior behaviour.
	oldSink := cfg.Sink
	oldToolCallRecorder := cfg.ToolCallRecorder
	mainScoped := metrics.WithRole(telemetry.RoleMain)
	var slowTurns *telemetry.SlowTurnBuffer
	sinks := []port.EventSink{mainScoped, tracing}
	if oldSink != nil {
		sinks = append(sinks, oldSink)
	}
	if mountMCP {
		// The ring stores scalars only (redaction by shape) and spawns no
		// goroutine — goleak-clean. Built only when the MCP server will read it.
		slowTurns = telemetry.NewSlowTurnBuffer(telemetry.DefaultSlowTurnCapacity, time.Now)
		sinks = append(sinks, slowTurns.WithRole(telemetry.RoleMain))
	}
	cfg.Sink = telemetry.NewSink(sinks...)
	cfg.ToolCallRecorder = cliconfig.TeeToolCallRecorder(mainScoped, oldToolCallRecorder)
	// Schedule metrics (issue #233, Phase 2b): wire the metrics callback over the
	// telemetry adapter's EmitSchedule, mirroring MetricsRoleScoper. Schedule
	// metrics are NOT a role-family; this is a separate schedule-lifecycle
	// dimension. EmitSchedule is nil-safe, so a nil metrics (perf off) stays the
	// byte-identical metrics-silent path — the embed builds metrics only under
	// perf-on, so this closure is a no-op there until metrics is non-nil.
	cfg.ScheduleMetricsEmitter = metrics.EmitSchedule
	cfg.SessionLoadFailureMetricsEmitter = metrics.EmitSessionLoadFailure
	cfg.MetricsRoleScoper = func(familyRole string) (port.EventSink, port.ToolCallRecorder) {
		scoped := metrics.WithRole(familyRole)
		childSinks := []port.EventSink{scoped}
		if slowTurns != nil {
			childSinks = append(childSinks, slowTurns.WithRole(familyRole))
		}
		return telemetry.NewSink(childSinks...), scoped
	}
	return slowTurns
}

// isLoopbackHostPort reports whether a "host:port" listen address binds the
// loopback interface (127.0.0.0/8, ::1, or "localhost"). It is the fail-closed
// gate for mounting the UNAUTHENTICATED perf MCP server (decision 6 / CWE-306). A
// malformed address (no port) is treated as the bare host; an unparseable host is
// NOT loopback (fail safe).
func isLoopbackHostPort(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// slowTurnSource bridges the telemetry slow-turn ring buffer to the
// mcpperf.SlowTurnSource read seam. The dependency points inward (embed →
// telemetry, embed → mcpperf); telemetry never imports the adapter, so the tiny
// field-copy adapter lives here at the composition boundary. A nil buffer yields
// a nil source so list_slow_turns reports "history not enabled".
func slowTurnSource(b *telemetry.SlowTurnBuffer) mcpperf.SlowTurnSource {
	if b == nil {
		return nil
	}
	return slowTurnBridge{b}
}

// slowTurnBridge maps telemetry.SlowTurn (scalars) to mcpperf.SlowTurn at the
// composition boundary. The shapes are identical by design (a 1:1 copy), but
// keeping the types distinct is what lets telemetry stay ignorant of the adapter.
type slowTurnBridge struct{ b *telemetry.SlowTurnBuffer }

func (s slowTurnBridge) Recent(thresholdMs int64) []mcpperf.SlowTurn {
	src := s.b.Recent(thresholdMs)
	out := make([]mcpperf.SlowTurn, len(src))
	for i, t := range src {
		out[i] = mcpperf.SlowTurn{
			TurnIndex:       t.TurnIndex,
			DurationMs:      t.DurationMs,
			TTFTMs:          t.TTFTMs,
			InterTokenMaxMs: t.InterTokenMaxMs,
			EndedAt:         t.EndedAt,
			Role:            t.Role,
		}
	}
	return out
}

// runtimeDir picks the base directory for the per-process socket dir: the
// XDG_RUNTIME_DIR (a user-private tmpfs on Linux desktops) when set, else the OS
// temp dir. An empty return makes os.MkdirTemp fall back to os.TempDir itself.
func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return ""
}

// listenPrivateUnix binds a new owner-private UNIX socket without removing any
// pre-existing path. Even though callers normally pass a fresh private directory,
// the collision check is fail-closed against symlinks and arbitrary files.
func listenPrivateUnix(path string) (net.Listener, error) {
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("refusing existing admin socket path %q", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect admin socket path %q: %w", path, err)
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return nil, fmt.Errorf("restrict private socket %q: %w", path, err)
	}
	return lis, nil
}

// newUnixSocketListener creates the private socket directory and validates the
// final path before binding. Darwin's sockaddr_un leaves only 103 bytes for a
// pathname; a long XDG_RUNTIME_DIR or TMPDIR therefore falls back to a private
// directory directly beneath /tmp. Other platforms retain the existing
// runtime-directory selection unchanged.
func newUnixSocketListener() (net.Listener, string, string, error) {
	dir, err := os.MkdirTemp(runtimeDir(), "mecatui-")
	if err != nil {
		return nil, "", "", fmt.Errorf("create runtime dir: %w", err)
	}
	sock := filepath.Join(dir, socketName)
	if !unixSocketPathFits(sock) {
		if err := os.RemoveAll(dir); err != nil {
			return nil, "", "", fmt.Errorf("remove overlong runtime dir %q: %w", dir, err)
		}
		dir, err = os.MkdirTemp(darwinShortSocketBase, "mecatui-")
		if err != nil {
			return nil, "", "", fmt.Errorf("create short Darwin runtime dir: %w", err)
		}
		sock = filepath.Join(dir, socketName)
	}
	if !unixSocketPathFits(sock) {
		_ = os.RemoveAll(dir)
		return nil, "", "", fmt.Errorf(
			"unix socket path %q is %d bytes; Darwin supports at most %d",
			sock, len(sock), unixSocketPathLimit-1,
		)
	}

	lis, err := listenPrivateUnix(sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", "", fmt.Errorf("listen unix %q: %w", sock, err)
	}
	return lis, dir, sock, nil
}
