package source

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// THIS IS THE ONLY FILE IN THE REPO ALLOWED TO IMPORT github.com/stacklok/toolhive.
// The ToolHive Go library pulls in a Docker client, ~154 k8s packages, the AWS
// SDK, and OTel; confining the import to this single adapter file keeps the
// layering audit trivial (domain/agent/port import none of it) and gives the rest
// of the package a tiny, fakeable seam (workloadLister) to test against offline.
//
// CONSTRAINT — NO STDIO MCP, NEVER SPAWN: this source only *lists already-running
// workloads* and reads their HTTP proxy URLs. It never starts, stops, or spawns a
// workload, and it never connects over stdio.
//
// A workload's *backend* transport (core.Workload.TransportType: stdio/sse/
// streamable-http) is NOT what mecatl connects to. ToolHive runs an HTTP proxy in
// front of every workload — for a stdio backend the proxy bridges stdio<->HTTP and
// exposes a streamable-HTTP (or SSE) endpoint at w.URL. So mecatl filters on the
// *effective proxy mode* (core.Workload.ProxyMode, what clients actually speak),
// not the backend transport: a stdio-backed server is reachable and IS mapped; only
// an SSE proxy is skipped (mecatl's MCP client is streamable-HTTP only).

// effectiveDefaultGroup is the group name used when the operator did not name one
// (mirroring ToolHive's own notion of a "default" group). Empty group -> "default".
const (
	effectiveDefaultGroup = "default"
	// ToolHive's current listing API materializes the returned slice before the
	// caller can filter it. Reject it immediately, before group filtering or any
	// derived server/diagnostic allocation; the dependency-owned slice allocation
	// is the unavoidable residual until ToolHive exposes a bounded listing API.
	maxToolHiveWorkloads = 128
)

// workloadLister is the minimal slice of the ToolHive library mecatl needs: list
// the running workloads. Defining our own interface (rather than depending on
// workloads.Manager directly) lets tests inject a fake with zero ToolHive
// construction — no Docker client, no container runtime socket.
type workloadLister interface {
	// ListWorkloads lists workloads. listAll=false restricts to running ones. It
	// returns an ERROR (not an empty slice) when the container runtime is absent.
	ListWorkloads(ctx context.Context, listAll bool, labelFilters ...string) ([]core.Workload, error)
}

// ToolHiveSource discovers MCP servers from the running ToolHive workloads in a
// group, reading each workload's already-populated HTTP proxy URL. Construction of
// the underlying ToolHive manager is lazy (first Servers call). Runtime/listing
// infrastructure failures are returned as consultation errors so reconciliation
// can retain the source's last-known-good snapshot; a successful empty list is
// therefore distinguishable and authoritative.
type ToolHiveSource struct {
	// group is the ToolHive group to filter workloads to. Empty means the
	// effective "default" group.
	group string
	// newLister constructs the workload lister. The default wraps
	// workloads.NewManager; tests inject a fake. It is consulted lazily on the
	// first Servers call.
	newLister func(context.Context) (workloadLister, error)
}

// NewToolHiveSource builds a ToolHiveSource for the given group (empty -> the
// effective "default" group), backed by the real ToolHive manager. The manager is
// not constructed until the first Servers call.
func NewToolHiveSource(group string) ToolHiveSource {
	return ToolHiveSource{group: group, newLister: defaultNewLister}
}

// defaultNewLister wraps workloads.NewManager, which auto-detects the container
// runtime (Podman then Docker) and ERRORS when no runtime socket is reachable.
// *DefaultManager satisfies workloadLister via its ListWorkloads method.
func defaultNewLister(ctx context.Context) (workloadLister, error) {
	mgr, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, err
	}
	return mgr, nil
}

// effectiveGroup is the group name actually filtered on.
func (s ToolHiveSource) effectiveGroup() string {
	if s.group == "" {
		return effectiveDefaultGroup
	}
	return s.group
}

// Name reports the source label, e.g. "toolhive(default)" or "toolhive(<group>)".
func (s ToolHiveSource) Name() string {
	return fmt.Sprintf("toolhive(%s)", s.effectiveGroup())
}

// Servers lists the running ToolHive workloads in the configured group and maps
// each streamable-http workload to an mcp.ServerConfig.
//
// Consultation contract:
//   - If the ToolHive manager cannot be constructed or the list call fails (no
//     container runtime), return an error. The ordered reconciler retains LKG;
//     startup remains fail-soft because composition treats this as stale.
//   - A workload whose name contains "__" is skipped (it would corrupt the
//     mcp__<server>__<tool> namespacing).
//   - A workload whose *effective proxy transport* is not streamable-http is
//     skipped with a diagnostic. This is the proxy mode clients speak (w.ProxyMode),
//     NOT the backend transport: a stdio-backed workload proxied as streamable-HTTP
//     is mapped; only an SSE proxy is skipped (mecatl's MCP client is
//     streamable-HTTP only — no SSE).
func (s ToolHiveSource) Servers(ctx context.Context) ([]mcp.ServerConfig, []SkipError, error) {
	lister, err := s.newLister(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("ToolHive container runtime unavailable: %w", err)
	}

	// listAll=false: running workloads only. The runtime returns an error (not an
	// empty slice) when it is absent, which we treat the same as a construction
	// fault: degrade, don't abort.
	list, err := lister.ListWorkloads(ctx, false)
	if err != nil {
		return nil, nil, fmt.Errorf("listing ToolHive workloads: %w", err)
	}
	if len(list) > maxToolHiveWorkloads {
		return nil, nil, fmt.Errorf("ToolHive workload count exceeds limit %d", maxToolHiveWorkloads)
	}

	// FilterByGroup is a pure in-memory filter over the already-listed workloads:
	// no extra manager construction and no runtime round-trip. (It is not a
	// dependency saving — pkg/workloads, which we already import, transitively
	// pulls in pkg/groups regardless.)
	inGroup, err := workloads.FilterByGroup(list, s.effectiveGroup())
	if err != nil {
		return nil, []SkipError{{
			Server: "toolhive",
			Reason: fmt.Sprintf("filtering ToolHive workloads by group %q failed: %v", s.effectiveGroup(), err),
		}}, nil
	}

	var (
		out   []mcp.ServerConfig
		skips []SkipError
	)
	for _, w := range inGroup {
		// Defensive: even with listAll=false, only map workloads the runtime
		// reports as running.
		if w.Status != runtime.WorkloadStatusRunning {
			skips = append(skips, SkipError{
				Server: w.Name,
				Reason: fmt.Sprintf("workload not running (status %q)", w.Status),
			})
			continue
		}
		if strings.Contains(w.Name, "__") {
			skips = append(skips, SkipError{
				Server: w.Name,
				Reason: `workload name contains "__", which would corrupt MCP tool namespacing; skipping`,
			})
			continue
		}
		// Filter on the EFFECTIVE PROXY transport, not the backend transport. The
		// ToolHive proxy fronts every workload over HTTP; w.ProxyMode is the protocol
		// clients speak to it (already resolved by ToolHive, with stdio defaulting to
		// streamable-http). A stdio backend proxied as streamable-HTTP is reachable
		// and mapped; only an SSE proxy is skipped (we have no SSE client).
		if mode := types.EffectiveProxyMode(w.TransportType, types.ProxyMode(w.ProxyMode)); mode != types.ProxyModeStreamableHTTP {
			skips = append(skips, SkipError{
				Server: w.Name,
				Reason: fmt.Sprintf("proxy transport %q is not streamable-HTTP; mecatl MCP is streamable-HTTP only", mode),
			})
			continue
		}
		if w.URL == "" {
			skips = append(skips, SkipError{
				Server: w.Name,
				Reason: "workload has no proxy URL; skipping",
			})
			continue
		}
		out = append(out, mcp.ServerConfig{Name: w.Name, URL: w.URL})
	}
	return out, skips, nil
}
