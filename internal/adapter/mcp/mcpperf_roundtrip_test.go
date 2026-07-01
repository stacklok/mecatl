package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpperf"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
)

// mcpperfFlightRecorder is a minimal, in-process FlightRecorder for the #223
// round-trip test: it is armed and returns a tiny canned trace window, so the
// mcpperf `capture_flight_recorder` tool — the cheapest mcpperf tool that
// emits a user-audience ResourceLink (no real CPU profiling, no pprof parsing)
// — succeeds deterministically without pulling the heavier mcpperf Deps fakes
// (which are unexported in the mcpperf test package).
type mcpperfFlightRecorder struct{}

func (mcpperfFlightRecorder) SnapshotBytes() ([]byte, error) {
	return []byte("trace bytes"), nil
}
func (mcpperfFlightRecorder) Enabled() bool { return true }

// TestMcpperfRoundTripResourceLinkPreserved is the #223 end-to-end acceptance
// test: mecatl's OWN mcpperf server deliberately emits user-audience
// ResourceLink content (mcpperf/tools.go:367 via userResourceLink with
// Annotations.Audience=["user"]). It stands the real mcpperf server up
// in-process over Streamable HTTP, dials it with the mecatl MCP client (the
// real newRemoteTool/mapContent adapter path), calls capture_flight_recorder,
// and asserts the broken round-trip is FIXED: the client preserves the typed
// BlockResourceLink with full metadata + audience instead of discarding it to
// a bare `[resource link: <uri>]` placeholder.
//
// capture_flight_recorder is chosen over capture_cpu_profile because it is the
// cheapest mcpperf tool that returns a user-audience resource_link — it needs
// only an armed FlightRecorder (no CPU-profiling window, no pprof parse), so
// the test is fast and deterministic while still exercising the exact
// server-emits-user-resource-link → client-preserves-typed-block path.
func TestMcpperfRoundTripResourceLinkPreserved(t *testing.T) {
	// Build the real mcpperf MCP server in-process with minimal real Deps.
	// Snapshot/Gatherer/Profiler are required by Deps.validate but are NOT
	// touched by capture_flight_recorder; the armed Recorder is the only seam
	// the tool exercises.
	d := mcpperf.Deps{
		Snapshot: telemetry.Snapshot,
		Gatherer: prometheus.NewRegistry(),
		Profiler: mcpperf.NewProfiler(),
		Recorder: mcpperfFlightRecorder{},
	}
	srv := mcpperf.NewServer(d)

	// Serve it over Streamable HTTP on a loopback listener, mirroring mcp_test's
	// newTestServer (registering the listener close via t.Cleanup so the SDK's
	// standalone SSE reader unwinds before the listener closes).
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv },
		nil,
	)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)

	// Connect the mecatl MCP client — this exercises the real adapter path
	// (newRemoteTool, mapContent).
	s := connectTest(t, ServerConfig{Name: "mcpperf", URL: httpSrv.URL})
	tool := toolsByName(s.Tools())["mcp__mcpperf__capture_flight_recorder"]
	if tool == nil {
		t.Fatalf("capture_flight_recorder tool not advertised; got %v", keys(toolsByName(s.Tools())))
	}

	call := session.NewToolCall("call-223", "mcp__mcpperf__capture_flight_recorder", json.RawMessage(`{}`))
	res, err := tool.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	// Parts must be non-empty and contain a BlockResourceLink.
	if len(res.Parts) == 0 {
		t.Fatalf("expected non-empty Parts, got none")
	}
	blk := findBlock(t, res.Parts, session.BlockResourceLink)

	const wantURI = "/debug/flightrecorder" // mcpperf.flightRecorderURI
	const wantName = "flight-recorder-trace"
	const wantDesc = "Loopback execution-trace snapshot (human download)"

	if blk.URL != wantURI {
		t.Errorf("block URL = %q, want %q", blk.URL, wantURI)
	}
	if len(blk.Audience) != 1 || blk.Audience[0] != "user" {
		t.Errorf("block Audience = %v, want [user]", blk.Audience)
	}
	if blk.Name != wantName {
		t.Errorf("block Name = %q, want %q", blk.Name, wantName)
	}
	if blk.Description != wantDesc {
		t.Errorf("block Description = %q, want %q", blk.Description, wantDesc)
	}

	// The model-facing Content must NOT be the bare `[resource link: <uri>]`
	// placeholder — it includes the name (the "not a useless bare placeholder"
	// acceptance check). mapContent emits `[resource link: <uri> (<name>)]`.
	if !strings.Contains(res.Content, wantURI) {
		t.Errorf("Content missing the resource URI: %q", res.Content)
	}
	if !strings.Contains(res.Content, wantName) {
		t.Errorf("Content missing the resource name: %q", res.Content)
	}
	if res.Content == "[resource link: "+wantURI+"]" {
		t.Errorf("Content is the bare placeholder (no name): %q", res.Content)
	}
}
