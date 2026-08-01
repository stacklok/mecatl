package agentfs

import (
	"context"

	"github.com/stacklok/mecatl/engine/tool"
)

// FSSource is the FILESYSTEM implementation of the tool.AgentDefSource port: a
// snapshot of the agent definitions discovered from the composed Source list
// (explicit dirs + conventional locations). The port carries no path/dir/root
// concept; the locator business the composition layer's diagnostics still need
// is exposed as adapter-public NON-PORT methods (Discovered/Detail), mirroring
// the skills FSSource's AssetDir/AssetDirs split.
//
// SNAPSHOT SEMANTICS: discovery runs ONCE at construction (NewMultiSource over
// the given sources) and the defs are retained in memory, so ListAgentDefs is
// stable for the life of the source — the build-once trust-gate invariant (an
// untrusted workspace's project tier is gated at SOURCE CONSTRUCTION, in
// ResolveSources).
type FSSource struct {
	list    []Discovered // name-sorted snapshot (def + adapter-private detail)
	details map[string]string
}

// compile-time assertion that FSSource satisfies the port.
var _ tool.AgentDefSource = (*FSSource)(nil)

// NewFSSource resolves the given sources ONCE (highest-precedence first, the
// NewMultiSource collision rule) and returns the snapshot source plus the
// aggregated discovery diagnostics. A genuine discovery fault returns a
// non-nil error (with the diagnostics gathered so far); an absent dir is
// simply "no defs" (opt-in), exactly as before.
func NewFSSource(ctx context.Context, sources ...AgentSource) (*FSSource, []SkipError, error) {
	discovered, skips, err := NewMultiSource(sources...).Agents(ctx)
	if err != nil {
		return nil, skips, err
	}
	src := &FSSource{
		list:    discovered, // MultiSource returns name-sorted, de-duplicated entries
		details: make(map[string]string, len(discovered)),
	}
	for i := range src.list {
		if src.list[i].Def.Origin == "" {
			// A hand-constructed Source that did not stamp a tier: an
			// operator-configured location.
			src.list[i].Def.Origin = tool.AgentOriginExplicit
		}
		if src.list[i].Detail != "" {
			src.details[src.list[i].Def.Name] = src.list[i].Detail
		}
	}
	return src, skips, nil
}

// ListAgentDefs returns the name-sorted, unique definition snapshot.
func (s *FSSource) ListAgentDefs(_ context.Context) ([]AgentDef, error) {
	out := make([]AgentDef, 0, len(s.list))
	for _, d := range s.list {
		out = append(out, d.Def)
	}
	return out, nil
}

// --- Adapter-public NON-PORT detail API (composition-only consumers) --------

// Discovered returns the name-sorted Discovered snapshot (def + adapter-private
// Detail). Adapter-public, NON-PORT: it backs NewRegistryDiscovered in the
// composition layer, never the port.
func (s *FSSource) Discovered() []Discovered {
	out := make([]Discovered, len(s.list))
	copy(out, s.list)
	return out
}

// Detail returns the named def's adapter-private locator ("<label>: <path>")
// and whether the snapshot holds one. NON-PORT, diagnostics only.
func (s *FSSource) Detail(name string) (string, bool) {
	d, ok := s.details[name]
	return d, ok
}
