package agentfs

import (
	"context"
	"fmt"
	"sort"
)

// Discovered is one discovered agent definition together with its
// adapter-private locator string. The PORT value object (tool.AgentDef, the
// aliased AgentDef) carries no path/dir/root concept; Detail is the NON-PORT
// diagnostics channel a reviewer traces a def back through — "<label>: <path>"
// for a filesystem source, "driver: <target>" for a remote driver. It never
// crosses the tool.AgentDefSource port.
type Discovered struct {
	Def    AgentDef
	Detail string
}

// AgentSource is the pluggable EXTENSIBILITY POINT for where agent definitions
// come from. A Source produces a set of Discovered entries (the AgentDef value
// object + its adapter-private Detail) together with non-fatal diagnostics
// (SkipError), and a fatal error only for a genuine infrastructure fault that
// prevented the source from being consulted at all.
//
// It is the verbatim shape of skills.Source: the local-OS-filesystem layout
// (DirSource) is just ONE implementation; an embedded default set or a remote
// registry would satisfy the same interface and slot in via MultiSource without
// touching the consumer or the AgentDef value object.
//
// Agents(ctx) returns:
//   - the discovered defs (deterministically ordered by the implementation),
//   - the non-fatal diagnostics (skipped/duplicate/truncated/shadowed entries),
//   - a non-nil error ONLY for a hard fault. An absent source (e.g. a missing
//     directory) is "no defs", not an error — agent defs are opt-in.
type AgentSource interface {
	Agents(ctx context.Context) ([]Discovered, []SkipError, error)
}

// SkipError records one diagnostic from discovery. It carries a structural
// two-way split via Fatal — NOT a string-matched one — so the composition root
// can word the log honestly instead of overloading "skipped":
//
//   - Fatal == true: the def was DROPPED (excluded from the registry). Causes:
//     it could not be read, its frontmatter was malformed / missing a required
//     field, a duplicate name within a directory, or it was SHADOWED by a
//     higher-precedence source.
//   - Fatal == false (the zero value): the def was KEPT but ADJUSTED — a
//     non-fatal modification was applied (e.g. its description/body was
//     truncated to a cap, an mcpServers entry was skipped, or an unsupported
//     memory tier was ignored). The def is still in the registry.
//
// Discovery keeps scanning rather than aborting and returns the collected
// diagnostics so the composition root can surface them. A SkipError is never
// returned as a Source's fatal error.
type SkipError struct {
	// Path is the <name>.md (or directory) the problem was found at.
	Path string
	// Reason is a short, human-readable description of the problem.
	Reason string
	// Fatal reports whether the def was DROPPED (true) or KEPT-but-ADJUSTED
	// (false, the zero value). See the type doc-comment.
	Fatal bool
}

func (e SkipError) Error() string {
	return fmt.Sprintf("skipped agent def at %q: %s", e.Path, e.Reason)
}

// MultiSource composes an ORDERED list of Sources into one, with a defined
// precedence on name collisions and aggregated diagnostics.
//
// PRECEDENCE (collision rule): EARLIER sources win. When two sources both produce
// a def with the same effective name, the one from the earlier source is kept and
// the later one is SHADOWED — dropped, with a SkipError notice. Callers order the
// slice highest-precedence-first; ResolveSources builds it as
// explicit > project > user.
//
// A fatal error from ANY source is returned immediately (with the diagnostics
// gathered so far). Output is sorted by name for deterministic, cache-stable
// ordering.
type MultiSource struct {
	sources []AgentSource
}

// NewMultiSource builds a MultiSource over the given ordered sources
// (highest-precedence first). nil entries are ignored so callers can assemble the
// slice conditionally without sprinkling nil checks.
func NewMultiSource(sources ...AgentSource) MultiSource {
	filtered := make([]AgentSource, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			filtered = append(filtered, s)
		}
	}
	return MultiSource{sources: filtered}
}

// Agents aggregates every composed source, applies the earlier-wins precedence on
// name collisions, and returns the merged defs sorted by name. Shadowed
// lower-precedence defs are dropped and reported.
func (m MultiSource) Agents(ctx context.Context) ([]Discovered, []SkipError, error) {
	var (
		out    []Discovered
		skips  []SkipError
		winner = map[string]Discovered{} // effective name -> the kept (higher-precedence) def
	)
	for _, src := range m.sources {
		got, srcSkips, err := src.Agents(ctx)
		skips = append(skips, srcSkips...)
		if err != nil {
			return nil, skips, err
		}
		for _, d := range got {
			if prev, taken := winner[d.Def.Name]; taken {
				skips = append(skips, SkipError{
					Path: d.Detail,
					Reason: fmt.Sprintf(
						"agent def %q shadowed by a higher-precedence source (kept %q)",
						d.Def.Name, prev.Detail),
					Fatal: true,
				})
				continue
			}
			winner[d.Def.Name] = d
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Def.Name < out[j].Def.Name })
	return out, skips, nil
}
