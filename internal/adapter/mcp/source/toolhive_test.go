package source

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// fakeLister is an in-memory workloadLister for offline tests. It records the
// listAll arg it was called with so a test can assert running-only listing, and
// returns its canned workloads (or error). No ToolHive manager, no Docker.
type fakeLister struct {
	workloads   []core.Workload
	err         error
	gotListAll  bool
	listAllSeen bool
	calls       int // number of times ListWorkloads was invoked
}

func (f *fakeLister) ListWorkloads(_ context.Context, listAll bool, _ ...string) ([]core.Workload, error) {
	f.gotListAll = listAll
	f.listAllSeen = true
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.workloads, nil
}

// sourceWith builds a ToolHiveSource wired to a fake lister (no runtime).
func sourceWith(group string, lister workloadLister, newErr error) ToolHiveSource {
	return ToolHiveSource{
		group: group,
		newLister: func(context.Context) (workloadLister, error) {
			if newErr != nil {
				return nil, newErr
			}
			return lister, nil
		},
	}
}

// running builds a running workload. proxyMode is the effective proxy transport
// clients speak (what ToolHive populates on core.Workload.ProxyMode); for direct
// transports it equals the transport type, for stdio it is the bridge protocol.
func running(name, url string, tt types.TransportType, proxyMode types.ProxyMode, group string) core.Workload {
	return core.Workload{
		Name:          name,
		URL:           url,
		TransportType: tt,
		ProxyMode:     proxyMode.String(),
		Status:        runtime.WorkloadStatusRunning,
		Group:         group,
	}
}

func TestToolHiveMapsRunningStreamable(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("github", "http://127.0.0.1:8080/mcp", types.TransportTypeStreamableHTTP, types.ProxyModeStreamableHTTP, "default"),
		running("fetch", "http://127.0.0.1:8081/mcp", types.TransportTypeStreamableHTTP, types.ProxyModeStreamableHTTP, "default"),
	}}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if !f.listAllSeen || f.gotListAll {
		t.Errorf("expected ListWorkloads called with listAll=false, gotListAll=%v seen=%v", f.gotListAll, f.listAllSeen)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 servers, got %d: %v", len(got), got)
	}
	if got[0].Name != "github" || got[0].URL != "http://127.0.0.1:8080/mcp" {
		t.Errorf("first server wrong: %+v", got[0])
	}
}

func TestToolHiveSkipsSSE(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("legacy", "http://127.0.0.1:9000/sse", types.TransportTypeSSE, types.ProxyModeSSE, "default"),
	}}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SSE workload must not be mapped, got %v", got)
	}
	if len(skips) != 1 || skips[0].Server != "legacy" || !strings.Contains(skips[0].Reason, "not streamable-HTTP") {
		t.Fatalf("expected one non-streamable skip for legacy, got %v", skips)
	}
}

// A stdio *backend* is bridged to streamable-HTTP by the ToolHive proxy, so it is
// reachable over HTTP and MUST be mapped — the backend transport is irrelevant; the
// proxy mode is what mecatl's client speaks.
func TestToolHiveMapsStdioBackedStreamable(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("github", "http://127.0.0.1:8080/mcp", types.TransportTypeStdio, types.ProxyModeStreamableHTTP, "default"),
	}}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("stdio-backed streamable-HTTP workload must not be skipped, got %v", skips)
	}
	if len(got) != 1 || got[0].Name != "github" || got[0].URL != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("stdio-backed streamable-HTTP workload must be mapped, got %v", got)
	}
}

// A stdio backend proxied as SSE is NOT reachable (mecatl has no SSE client).
func TestToolHiveSkipsStdioSSEProxy(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("legacy", "http://127.0.0.1:9000/sse", types.TransportTypeStdio, types.ProxyModeSSE, "default"),
	}}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SSE-proxied workload must not be mapped, got %v", got)
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "not streamable-HTTP") {
		t.Fatalf("expected one non-streamable skip for SSE-proxied stdio, got %v", skips)
	}
}

// Empty ProxyMode on a stdio backend defaults to streamable-HTTP (ToolHive's
// documented default), so it is reachable and mapped.
func TestToolHiveStdioEmptyProxyModeDefaultsStreamable(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("github", "http://127.0.0.1:8080/mcp", types.TransportTypeStdio, "", "default"),
	}}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("stdio with empty proxy mode must default to streamable-HTTP and map, got skips %v", skips)
	}
	if len(got) != 1 || got[0].Name != "github" {
		t.Fatalf("stdio with empty proxy mode must be mapped, got %v", got)
	}
}

func TestToolHiveSkipsDoubleUnderscoreName(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("bad__name", "http://127.0.0.1:8080/mcp", types.TransportTypeStreamableHTTP, types.ProxyModeStreamableHTTP, "default"),
	}}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("__-named workload must not be mapped, got %v", got)
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "__") {
		t.Fatalf("expected one __ skip, got %v", skips)
	}
}

func TestToolHiveSkipsNonRunning(t *testing.T) {
	stopped := core.Workload{
		Name: "stopped", URL: "http://127.0.0.1:8080/mcp",
		TransportType: types.TransportTypeStreamableHTTP,
		Status:        runtime.WorkloadStatus("stopped"), Group: "default",
	}
	f := &fakeLister{workloads: []core.Workload{stopped}}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("stopped workload must not be mapped, got %v", got)
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "not running") {
		t.Fatalf("expected one not-running skip, got %v", skips)
	}
}

func TestToolHiveGroupFilterApplied(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("a", "http://127.0.0.1:1/mcp", types.TransportTypeStreamableHTTP, types.ProxyModeStreamableHTTP, "team-x"),
		running("b", "http://127.0.0.1:2/mcp", types.TransportTypeStreamableHTTP, types.ProxyModeStreamableHTTP, "default"),
	}}
	got, _, err := sourceWith("team-x", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("group filter not applied, got %v", got)
	}
}

func TestToolHiveEmptyGroupDefaults(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("a", "http://127.0.0.1:1/mcp", types.TransportTypeStreamableHTTP, types.ProxyModeStreamableHTTP, "default"),
		running("b", "http://127.0.0.1:2/mcp", types.TransportTypeStreamableHTTP, types.ProxyModeStreamableHTTP, "other"),
	}}
	src := sourceWith("", f, nil) // empty group -> "default"
	if src.Name() != "toolhive(default)" {
		t.Errorf("empty group should label default, got %q", src.Name())
	}
	got, _, err := src.Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("empty group should filter to default, got %v", got)
	}
}

func TestToolHiveNoRuntimeIsConsultationFailure(t *testing.T) {
	got, skips, err := sourceWith("default", nil, errors.New("no socket")).Servers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "runtime unavailable") {
		t.Fatalf("no-runtime error = %v, want consultation failure", err)
	}
	if got != nil || skips != nil {
		t.Fatalf("failed consultation must not masquerade as authoritative empty: got=%v skips=%v", got, skips)
	}
}

func TestToolHiveListErrorIsConsultationFailure(t *testing.T) {
	f := &fakeLister{err: errors.New("daemon down")}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listing ToolHive workloads") {
		t.Fatalf("list error = %v, want consultation failure", err)
	}
	if got != nil || skips != nil {
		t.Fatalf("failed consultation must not masquerade as authoritative empty: got=%v skips=%v", got, skips)
	}
}

func TestToolHiveEmptyGroupNoMatches(t *testing.T) {
	f := &fakeLister{workloads: []core.Workload{
		running("a", "http://127.0.0.1:1/mcp", types.TransportTypeStreamableHTTP, types.ProxyModeStreamableHTTP, "other"),
	}}
	got, skips, err := sourceWith("default", f, nil).Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("no workloads in group -> empty, got %v", got)
	}
	if len(skips) != 0 {
		t.Fatalf("filtered-out-by-group workloads should not produce skips, got %v", skips)
	}
}
