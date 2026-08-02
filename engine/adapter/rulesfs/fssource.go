package rulesfs

import (
	"context"

	"github.com/stacklok/mecatl/engine/prompt"
)

// FSSource is the FILESYSTEM implementation of the prompt.RulesSource port: a
// snapshot of the rules discovered from the composed Source list (the
// conventional project + user locations). The port carries no path/dir/root
// concept; the locator business the composition layer's diagnostics still need
// is exposed as adapter-public NON-PORT methods (Discovered/Detail), mirroring
// the agentfs FSSource split.
//
// SNAPSHOT SEMANTICS: discovery runs ONCE at construction (NewMultiSource over
// the given sources) and the rules are retained in memory, so ListRules is
// stable for the life of the source — the build-once trust-gate invariant (an
// untrusted workspace's project tier is gated at SOURCE CONSTRUCTION, in
// ResolveSources).
type FSSource struct {
	list    []Discovered // name-sorted snapshot (rule + adapter-private detail)
	details map[string]string
}

// compile-time assertion that FSSource satisfies the port.
var _ prompt.RulesSource = (*FSSource)(nil)

// NewFSSource resolves the given sources ONCE (highest-precedence first, the
// NewMultiSource collision rule) and returns the snapshot source plus the
// aggregated discovery diagnostics. A genuine discovery fault returns a
// non-nil error (with the diagnostics gathered so far); an absent dir is
// simply "no rules".
func NewFSSource(ctx context.Context, sources ...RuleSource) (*FSSource, []SkipError, error) {
	discovered, skips, err := NewMultiSource(sources...).Rules(ctx)
	if err != nil {
		return nil, skips, err
	}
	src := &FSSource{
		list:    discovered, // MultiSource returns name-sorted, de-duplicated entries
		details: make(map[string]string, len(discovered)),
	}
	for i := range src.list {
		if src.list[i].Rule.Origin == "" {
			// A hand-constructed Source that did not stamp a tier: a personal
			// location.
			src.list[i].Rule.Origin = prompt.RuleOriginUser
		}
		if src.list[i].Detail != "" {
			src.details[src.list[i].Rule.Name] = src.list[i].Detail
		}
	}
	return src, skips, nil
}

// ListRules returns the name-sorted, unique rule snapshot.
func (s *FSSource) ListRules(_ context.Context) ([]prompt.Rule, error) {
	out := make([]prompt.Rule, 0, len(s.list))
	for _, d := range s.list {
		out = append(out, d.Rule)
	}
	return out, nil
}

// --- Adapter-public NON-PORT detail API (composition-only consumers) --------

// Discovered returns the name-sorted Discovered snapshot (rule +
// adapter-private Detail). Adapter-public, NON-PORT: it backs the composition
// layer's diagnostics, never the port.
func (s *FSSource) Discovered() []Discovered {
	out := make([]Discovered, len(s.list))
	copy(out, s.list)
	return out
}

// Detail returns the named rule's adapter-private locator ("<label>: <path>")
// and whether the snapshot holds one. NON-PORT, diagnostics only.
func (s *FSSource) Detail(name string) (string, bool) {
	d, ok := s.details[name]
	return d, ok
}
