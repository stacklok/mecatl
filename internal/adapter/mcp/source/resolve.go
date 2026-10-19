package source

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// ResolveOptions configures the conventional MCP-source resolver. The zero value
// resolves NOTHING (no static servers, ToolHive disabled), so MCP stays opt-in
// unless the operator configured a server or left ToolHive on.
type ResolveOptions struct {
	// StaticServers are the operator-configured --mcp-server entries, already
	// parsed. Highest precedence.
	StaticServers []mcp.ServerConfig
	// ToolHiveEnabled adds the live ToolHive workload source (lower precedence than
	// the static list). Default-on is decided by the composition root's flag, not
	// here.
	ToolHiveEnabled bool
	// ToolHiveGroup is the ToolHive group to filter to. Empty -> the effective
	// "default" group. Only consulted when ToolHiveEnabled.
	ToolHiveGroup string
}

// ResolveSources builds the ORDERED, highest-precedence-first Source list:
//
//	static (explicit --mcp-server, in flag order)   [highest]
//	  > toolhive(<group or 'default'>) when enabled  [lowest]
//
// so an explicit --mcp-server always wins a name collision against a discovered
// ToolHive workload. The static source is always included (it is harmless when
// empty); the ToolHive source is included only when ToolHiveEnabled. The result is
// ready to hand to NewMultiSource.
func ResolveSources(opts ResolveOptions) []Source {
	sources := []Source{StaticSource{Configs: opts.StaticServers}}
	if opts.ToolHiveEnabled {
		sources = append(sources, NewToolHiveSource(opts.ToolHiveGroup))
	}
	return sources
}

// --- Inspection types (Stage C) ---------------------------------------------
//
// These SDK-free value types let the composition root expose WHAT the resolver
// produced — the sources, their effective servers, and their diagnostics — to a
// later stage (the gRPC server adapter) WITHOUT that stage needing to re-run
// resolution or import the ToolHive library. They are a read-only inventory
// snapshot built by Inspect; they carry no live connections.

// ServerInfo is one resolved MCP server candidate (post-merge, post-shadow).
type ServerInfo struct {
	Name      string
	URL       string
	Transport string
	Group     string
}

// SourceInfo is the inventory for one resolved Source: its identity, the servers
// it contributed, and any diagnostics it raised. The name is deliberate (Stage C,
// the gRPC adapter, consumes source.SourceInfo) even though it reads as a stutter.
//
//nolint:revive // intentional name: source.SourceInfo is the Stage C contract
type SourceInfo struct {
	// Name is the source's Name(), e.g. "static" or "toolhive(default)".
	Name string
	// Kind is the coarse source kind: "static" or "toolhive".
	Kind string
	// Enabled reports whether this source was active in the resolution.
	Enabled bool
	// Group is the ToolHive group (empty for the static source).
	Group string
	// Servers are the configs this source contributed (before cross-source
	// shadowing — these are the source's own view).
	Servers []ServerInfo
	// Diagnostics are this source's per-server skip reasons.
	Diagnostics []string
}

const (
	kindStatic   = "static"
	kindToolHive = "toolhive"
)

// Resolve walks the resolved sources EXACTLY ONCE and returns three views built
// from that single pass:
//
//   - merged: the de-duplicated, cross-source-shadowed server configs, sorted by
//     name — the SAME result MultiSource.Servers produces (earlier sources win a
//     name collision; the dropped lower-precedence config becomes a shadow
//     SkipError). This is the set the composition root connects.
//   - inventory: the per-source SourceInfo snapshot (identity, Kind, Group, the
//     servers each source contributed pre-shadow, and its diagnostics) for the
//     gRPC ListMcpSources / ListToolHiveGroups RPCs.
//   - skips: the aggregated cross-source diagnostics (each source's own skips,
//     plus the shadow notices), in source order.
//
// It exists to avoid a double walk: calling both InspectSources AND
// NewMultiSource(...).Servers consults every source twice — for the live
// ToolHive source that is two ListWorkloads container round-trips and, worse,
// two independent snapshots (so the reported inventory could differ from the
// connected servers). Resolve consults each source once,
// so both views are derived from the same snapshot.
//
// Fail-soft contract is preserved: a source's fatal error is recorded as a
// diagnostic on that source's SourceInfo (and appended to skips) and that source
// contributes no servers to the merge, rather than aborting the whole resolution
// — matching the read-only, non-fatal philosophy our real sources already follow.
func Resolve(ctx context.Context, sources []Source) (merged []mcp.ServerConfig, inventory []SourceInfo, skips []SkipError) {
	inventory = make([]SourceInfo, 0, len(sources))
	winner := map[string]string{} // server name -> source Name() that first claimed it
	for _, src := range sources {
		info := SourceInfo{Name: src.Name(), Enabled: true}
		switch s := src.(type) {
		case StaticSource:
			info.Kind = kindStatic
		case ToolHiveSource:
			info.Kind = kindToolHive
			info.Group = s.effectiveGroup()
		}

		cfgs, srcSkips, err := src.Servers(ctx)
		// Preserve each source's own diagnostics, in source order.
		skips = append(skips, srcSkips...)
		for _, sk := range srcSkips {
			info.Diagnostics = append(info.Diagnostics, sk.Error())
		}
		if err != nil {
			diag := "source consultation failed: " + err.Error()
			info.Diagnostics = append(info.Diagnostics, diag)
			skips = append(skips, SkipError{Server: src.Name(), Reason: diag})
			inventory = append(inventory, info)
			continue
		}

		for _, c := range cfgs {
			// The inventory records the source's OWN view (pre cross-source shadow).
			info.Servers = append(info.Servers, ServerInfo{
				Name:      c.Name,
				URL:       c.URL,
				Transport: "streamable-http", // the only transport these sources map
				Group:     info.Group,
			})
			if prevSrc, taken := winner[c.Name]; taken {
				// An earlier (higher-precedence) source already claimed this name; the
				// current, lower-precedence config is shadowed: drop it and note why.
				skips = append(skips, SkipError{
					Server: c.Name,
					Reason: fmt.Sprintf(
						"shadowed by a higher-precedence source (kept the one from %q, dropped the one from %q)",
						prevSrc, src.Name()),
				})
				continue
			}
			winner[c.Name] = src.Name()
			merged = append(merged, c)
		}
		inventory = append(inventory, info)
	}
	slices.SortFunc(merged, func(a, b mcp.ServerConfig) int { return cmp.Compare(a.Name, b.Name) })
	return merged, inventory, skips
}

// InspectSources consults each resolved Source and returns the per-source
// inventory snapshot. It is now a thin wrapper over Resolve (which walks once and
// also produces the merged view); kept for existing callers/tests. opts is no
// longer consulted — Kind/Group are derived from the concrete source types — but
// retained in the signature for source compatibility.
func InspectSources(ctx context.Context, sources []Source, _ ResolveOptions) []SourceInfo {
	_, inventory, _ := Resolve(ctx, sources)
	return inventory
}
