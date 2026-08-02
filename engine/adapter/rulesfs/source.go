package rulesfs

import (
	"context"
	"fmt"
	"sort"

	"github.com/stacklok/mecatl/engine/prompt"
)

// Rule is the pure value object for one rule, re-aliased from engine/prompt so
// the adapter and the port speak one type (the agentfs.AgentDef precedent).
type Rule = prompt.Rule

// Discovered is one discovered rule together with its adapter-private locator
// string. The PORT value object (prompt.Rule) carries no path/dir/root concept;
// Detail is the NON-PORT diagnostics channel a reviewer traces a rule back
// through — "<label>: <path>" for a filesystem source, "driver: <target>" for
// a remote driver. It never crosses the prompt.RulesSource port.
type Discovered struct {
	Rule   Rule
	Detail string
}

// RuleSource is the pluggable EXTENSIBILITY POINT for where rules come from. A
// Source produces a set of Discovered entries (the Rule value object + its
// adapter-private Detail) together with non-fatal diagnostics (SkipError), and
// a fatal error only for a genuine infrastructure fault that prevented the
// source from being consulted at all.
//
// It is the verbatim shape of agentfs.AgentSource: the local-OS-filesystem
// layout (DirSource) is just ONE implementation; an embedded default set or a
// remote registry would satisfy the same interface and slot in via MultiSource
// without touching the consumer or the Rule value object.
//
// Rules(ctx) returns:
//   - the discovered rules (deterministically ordered by the implementation),
//   - the non-fatal diagnostics (skipped/duplicate/truncated/shadowed entries),
//   - a non-nil error ONLY for a hard fault. An absent source (e.g. a missing
//     directory) is "no rules", not an error.
type RuleSource interface {
	Rules(ctx context.Context) ([]Discovered, []SkipError, error)
}

// SkipError records one diagnostic from discovery. It carries a structural
// two-way split via Fatal — NOT a string-matched one — so the composition root
// can word the log honestly instead of overloading "skipped":
//
//   - Fatal == true: the rule was DROPPED (excluded from the set). Causes: it
//     could not be read, its frontmatter was malformed, a duplicate name within
//     a directory, or it was SHADOWED by a higher-precedence source.
//   - Fatal == false (the zero value): the rule was KEPT but ADJUSTED — a
//     non-fatal modification was applied (its body was truncated to the cap).
//     The rule is still in the set.
//
// Discovery keeps scanning rather than aborting and returns the collected
// diagnostics so the composition root can surface them. A SkipError is never
// returned as a Source's fatal error.
type SkipError struct {
	// Path is the <name>.md the problem was found at.
	Path string
	// Reason is a short, human-readable description of the problem.
	Reason string
	// Fatal reports whether the rule was DROPPED (true) or KEPT-but-ADJUSTED
	// (false, the zero value). See the type doc-comment.
	Fatal bool
}

func (e SkipError) Error() string {
	return fmt.Sprintf("skipped rule at %q: %s", e.Path, e.Reason)
}

// MultiSource composes an ORDERED list of Sources into one, with a defined
// precedence on name collisions and aggregated diagnostics.
//
// PRECEDENCE (collision rule): EARLIER sources win. When two sources both
// produce a rule with the same effective name, the one from the earlier source
// is kept and the later one is SHADOWED — dropped, with a SkipError notice.
// Callers order the slice highest-precedence-first; ResolveSources builds it as
// project > user.
//
// A fatal error from ANY source is returned immediately (with the diagnostics
// gathered so far). Output is sorted by name for deterministic, cache-stable
// ordering.
type MultiSource struct {
	sources []RuleSource
}

// NewMultiSource builds a MultiSource over the given ordered sources
// (highest-precedence first). nil entries are ignored so callers can assemble
// the slice conditionally without sprinkling nil checks.
func NewMultiSource(sources ...RuleSource) MultiSource {
	filtered := make([]RuleSource, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			filtered = append(filtered, s)
		}
	}
	return MultiSource{sources: filtered}
}

// Rules aggregates every composed source, applies the earlier-wins precedence
// on name collisions, and returns the merged rules sorted by name. Shadowed
// lower-precedence rules are dropped and reported.
func (m MultiSource) Rules(ctx context.Context) ([]Discovered, []SkipError, error) {
	var (
		out    []Discovered
		skips  []SkipError
		winner = map[string]Discovered{} // effective name -> the kept (higher-precedence) rule
	)
	for _, src := range m.sources {
		got, srcSkips, err := src.Rules(ctx)
		skips = append(skips, srcSkips...)
		if err != nil {
			return nil, skips, err
		}
		for _, d := range got {
			if prev, taken := winner[d.Rule.Name]; taken {
				skips = append(skips, SkipError{
					Path: d.Detail,
					Reason: fmt.Sprintf(
						"rule %q shadowed by a higher-precedence source (kept %q)",
						d.Rule.Name, prev.Detail),
					Fatal: true,
				})
				continue
			}
			winner[d.Rule.Name] = d
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule.Name < out[j].Rule.Name })
	return out, skips, nil
}
