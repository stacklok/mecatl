// Package mcpperf serves this Go process's runtime performance data over a
// read-only Model Context Protocol (MCP) server, so an agent can introspect the
// harness's own latency, memory, goroutine, and profile state through MCP tools
// and resources instead of a human reading raw /metrics or /debug/pprof.
//
// # What it exposes
//
// Every tool and resource returns a REDUCED, numeric summary — never a raw blob.
// Histograms become count + p50/p90/p99 bucket UPPER BOUNDS; profiles become a
// ranked top-N of function names; the runtime snapshot is a small JSON DTO. Raw
// artifacts (a full pprof CPU profile, a flight-recorder trace) are offered ONLY
// as user-audience resource_links pointing at the existing loopback admin
// endpoints (/debug/pprof/*, /debug/flightrecorder) — this server never holds an
// artifact store and never streams a multi-MB blob into the model's context.
//
// # Transport
//
// Streamable HTTP only (a pure http.Handler), per the project's hard no-stdio
// constraint: no MCP server process is ever spawned and the stdio transport is
// never imported. Handler is built with the SDK's default options, which leaves
// the SDK's localhost / DNS-rebinding protection ON — a request that arrives on
// a loopback listener with a non-loopback Host header is rejected with 403. The
// surface is UNAUTHENTICATED by design: it is meant to be mounted on the same
// loopback admin listener as the rest of the perf surface, never exposed
// publicly. Its output can embed goroutine-derived function names and timing, so
// loopback-only is a security requirement.
//
// # Layering
//
// This is an edge adapter constructed by dependency injection (the Deps struct):
// it imports the MCP SDK, the telemetry adapter (only for the RuntimeSnapshot DTO
// type), google/pprof for profile parsing, and the prometheus client model — and
// NOTHING from the domain, port, or agent layers, none of which may import it. It
// is not wired into any composition root by this commit; that is a later step.
package mcpperf

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stacklok/mecatl/internal/adapter/telemetry"
)

// serverName / serverVersion identify this perf MCP server to clients in the
// initialize handshake.
const (
	serverName    = "mecatl-perf"
	serverVersion = "v1"
)

// instructions is the short, declarative server-level guidance returned in the
// initialize result. It tells the model the shape and cost of what it will get:
// reduced numeric summaries, with raw artifacts only as user-audience links.
const instructions = "Read-only introspection of THIS Go process's runtime performance. " +
	"Tools return reduced numeric summaries — latency-histogram counts and p50/p90/p99 bucket upper bounds, " +
	"top-N function names from CPU/heap profiles, goroutine and memory counts. " +
	"Raw profiles and execution traces are never returned inline; they are offered as user-audience resource links " +
	"to the loopback admin endpoints. CPU-profiling tools perturb the process and are rate limited to one capture per cooldown window."

// FlightRecorder is the read seam over the execution-trace flight recorder.
// *telemetry.FlightRecorder satisfies it. It is nil-able: when the recorder was
// never armed, capture_flight_recorder returns a tool error rather than failing.
type FlightRecorder interface {
	// SnapshotBytes returns the current trace window as parseable trace bytes.
	SnapshotBytes() ([]byte, error)
	// Enabled reports whether the recorder is currently running.
	Enabled() bool
}

// Profiler is the seam over runtime/pprof, so tests inject a deterministic
// profile rather than profiling the test binary. The production implementation
// (defaultProfiler) wraps runtime/pprof: Lookup for named heap/goroutine/etc.
// profiles, and a start/stop pair for the CPU profile.
type Profiler interface {
	// Lookup returns the named profile's pprof bytes (debug=0, gzipped protobuf)
	// for name in {heap, goroutine, allocs, mutex, block}, or an error if the
	// name is unknown or the profile cannot be written.
	Lookup(name string) ([]byte, error)
	// CPUProfile runs a CPU profile for d and returns its pprof bytes. It blocks
	// for the duration. Only one CPU profile may run process-wide at a time; the
	// caller (the cpuGate) serializes access.
	CPUProfile(d time.Duration) ([]byte, error)
}

// SlowTurn is one entry of the per-turn slow-turn history. It carries ONLY
// numerics and timestamps — never prompt text, tool arguments, or session IDs —
// so list_slow_turns can never leak conversation content.
type SlowTurn struct {
	// TurnIndex is the monotonic index of the turn within its run.
	TurnIndex int `json:"turn_index"`
	// DurationMs is the turn's model-call wall-clock duration in milliseconds.
	DurationMs int64 `json:"duration_ms"`
	// TTFTMs is the time-to-first-token in milliseconds (0 if not measured).
	TTFTMs int64 `json:"ttft_ms"`
	// InterTokenMaxMs is the worst inter-token gap in milliseconds (0 if not measured).
	InterTokenMaxMs int64 `json:"inter_token_max_ms"`
	// EndedAt is the wall-clock time the turn ended, RFC3339.
	EndedAt time.Time `json:"ended_at"`
	// Role is the BOUNDED engine role family that produced the turn
	// (main|subagent|member|parallel|usermodel|child). It is a closed enum
	// label — never a def/member name or session id — so redaction by shape holds.
	Role string `json:"role,omitempty"`
}

// SlowTurnSource is the read seam over the slow-turn ring buffer. It is nil-able:
// the concrete buffer lands in a later commit, so when nil, list_slow_turns
// reports that per-turn history is not enabled. The implementation owns the
// window/eviction policy; the buffer is intrinsically bounded (a fixed-size
// ring), so returning its whole contents is cheap.
type SlowTurnSource interface {
	// Recent returns recent slow turns, newest first. thresholdMs, if > 0, filters
	// to turns at least that slow; 0 means "use the source default threshold". It
	// returns the whole (bounded) matching set so the tool can report an accurate
	// totalCount and paginate over a stable snapshot — the buffer's fixed size is
	// the cap, not a per-call limit argument.
	Recent(thresholdMs int64) []SlowTurn
}

// Deps is the dependency seam for the perf MCP server. Every external capability
// is an interface or a func so the adapter never reaches into a composition root
// and tests substitute fakes. *telemetry.FlightRecorder, *prometheus.Registry,
// and telemetry.Snapshot already satisfy the respective fields.
type Deps struct {
	// Snapshot returns the current runtime snapshot DTO. Required.
	Snapshot func() telemetry.RuntimeSnapshot
	// Gatherer gathers the prometheus metric families backing the latency
	// histograms and counters. *prometheus.Registry satisfies it. Required.
	Gatherer prometheus.Gatherer
	// Recorder is the flight recorder seam. Nil-able (recorder not armed).
	Recorder FlightRecorder
	// Profiler is the pprof seam. Required (defaultProfiler in production).
	Profiler Profiler
	// SlowTurns is the slow-turn history seam. Nil-able (history not enabled).
	SlowTurns SlowTurnSource
	// Clock returns the current time, for cooldown accounting and timestamps.
	// Nil falls back to time.Now.
	Clock func() time.Time
	// Logger is used for low-volume operational logging. It MUST NOT be used to
	// log tool inputs or outputs at info level. Nil falls back to a discard logger.
	Logger *slog.Logger
}

// now returns the current time honoring an injected Clock, defaulting to
// time.Now.
func (d Deps) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

// logger returns the Deps logger or a discard logger so call sites never
// nil-check.
func (d Deps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// validate fails loud on a missing REQUIRED dependency. Snapshot, Gatherer, and
// Profiler are required: a tool/resource call would otherwise nil-panic deep in a
// handler, far from the wiring mistake. This is a programmer/composition error
// (not runtime input), so a clear panic naming the missing dep is the right
// failure mode — the constructor cannot meaningfully proceed without them.
// Recorder and SlowTurns stay nil-able by design (optional → isError at call time).
func (d Deps) validate() {
	var missing []string
	if d.Snapshot == nil {
		missing = append(missing, "Snapshot")
	}
	if d.Gatherer == nil {
		missing = append(missing, "Gatherer")
	}
	if d.Profiler == nil {
		missing = append(missing, "Profiler")
	}
	if len(missing) > 0 {
		panic("mcpperf: missing required Deps: " + strings.Join(missing, ", "))
	}
}

// NewServer builds the perf MCP server: it registers the read-only tools and the
// runtime/metrics/pprof resources against a fresh *mcpsdk.Server carrying the
// "mecatl-perf" identity and the declarative instructions. A cpuGate is created
// per server so the CPU-profiling tools share one cooldown + in-flight guard.
//
// It panics if a required dependency (Snapshot, Gatherer, Profiler) is nil — a
// wiring error the caller must fix, not a runtime condition to be handled.
func NewServer(d Deps) *mcpsdk.Server {
	d.validate()
	srv := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: serverName, Version: serverVersion},
		&mcpsdk.ServerOptions{
			Instructions: instructions,
			// Leave Logger nil: never log tool I/O. Operational logging goes through
			// Deps.Logger at low volume, not the SDK's per-message logger.
		},
	)

	gate := newCPUGate(d.now)
	registerTools(srv, d, gate)
	registerResources(srv, d)
	// Operational, low-volume: never logs tool I/O, only that the server was built.
	d.logger().Debug("mcpperf: perf MCP server constructed",
		"recorder_armed", d.Recorder != nil && d.Recorder.Enabled(),
		"slow_turns_enabled", d.SlowTurns != nil,
	)
	return srv
}

// Handler builds the Streamable HTTP handler for the perf MCP server. It uses the
// SDK default options (nil), which keeps the SDK's localhost / DNS-rebinding
// protection ON — NEVER set DisableLocalhostProtection. The returned handler is a
// pure http.Handler meant to be mounted on the loopback admin listener; it
// spawns no process and opens no outbound connection, satisfying the no-stdio
// constraint by construction.
//
// The server is constructed once and shared across requests via the getServer
// closure: this server is stateless across calls (every tool reads live process
// state), so one instance is correct and avoids per-request allocation.
func Handler(d Deps) http.Handler {
	srv := NewServer(d)
	return mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv },
		nil,
	)
}
