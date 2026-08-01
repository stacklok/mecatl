package agentfs

import (
	"context"
	"sort"
)

// Registry is an immutable, name-indexed view of the discovered agent
// definitions: the ONE registry both consumers (the Subagent tool now; the team
// member factory in a later slice) share. It is built once at composition time
// from a resolved Source and never mutated thereafter, so it is safe to read
// concurrently.
//
// Alongside the port-shaped defs it retains each def's adapter-private locator
// (Discovered.Detail), exposed via Detail for composition-layer diagnostics —
// the NON-PORT replacement for the old AgentDef.Path field.
type Registry struct {
	byName  map[string]AgentDef
	details map[string]string // def name -> adapter-private locator (may be empty)
	order   []string          // def names, sorted, for deterministic iteration
}

// NewRegistry builds a Registry from the given defs (typically the output of a
// MultiSource). On a duplicate name the FIRST occurrence wins (callers pass
// already-deduped, precedence-ordered defs from MultiSource, so this is only a
// defensive backstop). Iteration order is by name for determinism. The
// registry carries no per-def details — Detail returns "" for every name; use
// NewRegistryDiscovered to retain the locator channel.
func NewRegistry(defs []AgentDef) *Registry {
	discovered := make([]Discovered, len(defs))
	for i, d := range defs {
		discovered[i] = Discovered{Def: d}
	}
	return NewRegistryDiscovered(discovered)
}

// NewRegistryDiscovered builds a Registry from Discovered entries, retaining
// each def's adapter-private Detail for diagnostics. Same keep-first dedup and
// name-sorted iteration as NewRegistry.
func NewRegistryDiscovered(discovered []Discovered) *Registry {
	r := &Registry{
		byName:  make(map[string]AgentDef, len(discovered)),
		details: make(map[string]string, len(discovered)),
	}
	for _, d := range discovered {
		if _, exists := r.byName[d.Def.Name]; exists {
			continue
		}
		r.byName[d.Def.Name] = d.Def
		if d.Detail != "" {
			r.details[d.Def.Name] = d.Detail
		}
		r.order = append(r.order, d.Def.Name)
	}
	sort.Strings(r.order)
	return r
}

// ResolveRegistry runs the source to completion and builds a Registry from the
// resulting defs. It is the convenience the composition root uses: resolve the
// MultiSource, surface diagnostics, build the registry in one step. The returned
// SkipErrors are the source's diagnostics (shadowed/truncated/skipped); a non-nil
// error is a hard fault that prevented discovery.
func ResolveRegistry(ctx context.Context, src AgentSource) (*Registry, []SkipError, error) {
	if src == nil {
		return NewRegistry(nil), nil, nil
	}
	discovered, skips, err := src.Agents(ctx)
	if err != nil {
		return NewRegistry(nil), skips, err
	}
	return NewRegistryDiscovered(discovered), skips, nil
}

// Get returns the def registered under name and true, or a zero def and false.
func (r *Registry) Get(name string) (AgentDef, bool) {
	d, ok := r.byName[name]
	return d, ok
}

// Detail returns the adapter-private locator string the named def was
// discovered through ("<label>: <path>" for a filesystem source, "driver:
// <target>" for a remote driver), or "" when the registry carries none (an
// unknown name, or a NewRegistry-built registry). It is the NON-PORT
// diagnostics channel composition logs read; it never reaches the port or any
// model-facing surface.
func (r *Registry) Detail(name string) string {
	return r.details[name]
}

// List returns every def, ordered by name for determinism. The returned slice is
// a fresh copy; mutating it does not affect the Registry.
func (r *Registry) List() []AgentDef {
	out := make([]AgentDef, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.byName[name])
	}
	return out
}

// Len reports how many defs the Registry holds.
func (r *Registry) Len() int { return len(r.order) }
