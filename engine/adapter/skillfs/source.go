package skillfs

import (
	"context"
	"fmt"
	"sort"
)

// Source is the pluggable EXTENSIBILITY POINT for where skills come from. A
// Source produces a set of Skill value objects together with non-fatal
// diagnostics (SkipError), and a fatal error only for a genuine infrastructure
// fault that prevented the source from being consulted at all.
//
// LAYERING: Source lives in this ADAPTER package, not the domain. Nothing in the
// domain or the agent loop consumes skills — they are packaged into a tool.Tool
// at composition time (NewTool/Register) — so a domain port would be the wrong
// home. The seam is scoped to where it is consumed (the composition root),
// mirroring how MCP, repo-map, and the theme search-path are scoped to their
// adapters. The local-OS-filesystem layout is just ONE implementation
// (DirSource); embedded defaults, a remote registry, or a multi-directory
// aggregate (MultiSource) all satisfy this same interface and slot in without
// touching the consumer or the Skill value object.
//
// Skills(ctx) returns:
//   - the discovered skills (deterministically ordered by the implementation),
//   - the non-fatal diagnostics (skipped/duplicate/truncated/shadowed entries),
//   - a non-nil error ONLY for a hard fault (e.g. an I/O error the source could
//     not classify as "simply absent"). An absent source (e.g. a missing
//     directory) is "no skills", not an error — skills are opt-in.
type Source interface {
	Skills(ctx context.Context) ([]Skill, []SkipError, error)
}

// SkipError records one diagnostic from discovery: either a skill that could not
// be loaded (a fatal SKIP — the skill is excluded), or a non-fatal WARNING about
// a skill that WAS kept (e.g. its description or body was truncated), or a skill
// dropped because it was SHADOWED by a higher-precedence source. In all cases
// discovery keeps scanning rather than aborting, and returns the collected
// diagnostics so the composition root can surface them. It is never returned as
// a Source's fatal error.
type SkipError struct {
	// Path is the SKILL.md (or directory) the problem was found at.
	Path string
	// Reason is a short, human-readable description of the problem.
	Reason string
}

func (e SkipError) Error() string {
	return fmt.Sprintf("skipped skill at %q: %s", e.Path, e.Reason)
}

// MultiSource composes an ORDERED list of Sources into one, with a defined
// precedence on name collisions and aggregated diagnostics. It is what makes
// "keep adding sources" easy: future embedded/remote sources implement Source and
// slot into the ordered list — the consumer (Register/NewTool) is unchanged.
//
// PRECEDENCE (collision rule): EARLIER sources win. When two sources both produce
// a skill with the same effective name, the one from the earlier source is kept
// and the later one is SHADOWED — dropped, with a SkipError "shadowed by a
// higher-precedence source" notice so the operator can see it happened. Callers
// order the slice highest-precedence-first; the conventional resolver
// (ResolveSources) builds it as explicit paths > project > user.
//
// Diagnostics from every source are concatenated in source order, with each
// shadow notice appended at the point the collision is detected. A fatal error
// from ANY source is returned immediately (with the diagnostics gathered so far)
// — a source that genuinely could not be consulted is a configuration fault worth
// surfacing, unlike a merely-absent one which its own implementation reports as
// "no skills".
type MultiSource struct {
	sources []Source
}

// NewMultiSource builds a MultiSource over the given ordered sources
// (highest-precedence first). nil entries are ignored so callers can assemble the
// slice conditionally without sprinkling nil checks.
func NewMultiSource(sources ...Source) MultiSource {
	filtered := make([]Source, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			filtered = append(filtered, s)
		}
	}
	return MultiSource{sources: filtered}
}

// Skills aggregates every composed source, applies the earlier-wins precedence on
// name collisions, and returns the merged skills sorted by name for deterministic,
// cache-stable output. Shadowed lower-precedence skills are dropped and reported.
func (m MultiSource) Skills(ctx context.Context) ([]Skill, []SkipError, error) {
	var (
		out    []Skill
		skips  []SkipError
		winner = map[string]Skill{} // effective name -> the kept (higher-precedence) skill
	)
	for _, src := range m.sources {
		got, srcSkips, err := src.Skills(ctx)
		// Preserve each source's own diagnostics, in source order.
		skips = append(skips, srcSkips...)
		if err != nil {
			return nil, skips, err
		}
		for _, sk := range got {
			if prev, taken := winner[sk.Name]; taken {
				// An earlier (higher-precedence) source already claimed this name. The
				// current, lower-precedence skill is shadowed: drop it and note why.
				skips = append(skips, SkipError{
					Path: sk.Path,
					Reason: fmt.Sprintf(
						"skill %q shadowed by a higher-precedence source (kept %q)",
						sk.Name, prev.Path),
				})
				continue
			}
			winner[sk.Name] = sk
			out = append(out, sk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, skips, nil
}
