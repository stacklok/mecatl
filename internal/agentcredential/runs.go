package agentcredential

import (
	"context"
	"slices"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Run is the run a root session's main agent is in: the server's run ID and
// the run's owner, the user who started it. The server puts it on the
// context of every run it hands the engine; the engine passes context values
// to tool calls and to the runs of helpers it starts, background ones
// included.
type Run struct {
	ID    string
	Owner identitybroker.Owner
}

// IsZero reports whether r names no run.
func (r Run) IsZero() bool { return r.ID == "" }

type runKey struct{}

// WithRun returns ctx carrying run as the main agent's run.
func WithRun(ctx context.Context, run Run) context.Context {
	return context.WithValue(ctx, runKey{}, run)
}

// RunFromContext returns the main agent's run ctx carries, and whether it
// carries one.
func RunFromContext(ctx context.Context) (Run, bool) {
	run, ok := ctx.Value(runKey{}).(Run)
	return run, ok && !run.IsZero()
}

// Delegation is a helper agent's delegation as reported to the identity
// broker: the helper's run, delegated to it by a run of its parent agent.
type Delegation struct {
	// ID is the delegation ID the helper's calls name.
	ID string
	// Run is the helper's own run, and its owner.
	Run Run
	// DelegatingRun is the run that delegated, and DelegatingAgentSession the
	// session of the agent that run is bound to: the root session for the
	// main agent, or a helper's own session.
	DelegatingRun          string
	DelegatingAgentSession string
}

// HelperDelegation is a delegation and the helper session it delegates to.
type HelperDelegation struct {
	AgentSessionID string
	Delegation
}

// maxDelegationsPerRoot bounds how many delegations a root keeps for
// reporting again; the oldest is forgotten first, as the broker forgets its
// records.
const maxDelegationsPerRoot = 4096

// SetDelegation records d as the current delegation of key's helper, once
// the broker accepted it. It returns ErrNoCredential, recording nothing, when
// no credential is held for key. The design gives a delegation no end, so
// each is kept, and reported again after a reopen, until its root is dropped.
func (h *Holder) SetDelegation(key Key, d Delegation) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.roots[key.RootSessionID]
	if r == nil || r.entries[key.AgentSessionID] == nil {
		return ErrNoCredential
	}
	if r.current == nil {
		r.current = make(map[string]Delegation)
	}
	r.current[key.AgentSessionID] = d
	if len(r.delegations) == maxDelegationsPerRoot {
		r.delegations = r.delegations[1:]
	}
	r.delegations = append(r.delegations, HelperDelegation{AgentSessionID: key.AgentSessionID, Delegation: d})
	return nil
}

// ClearDelegation forgets the current delegation of key's helper, so its
// gateway calls name no run until it is delegated again. Delegations already
// recorded stay listed for reporting again.
func (h *Holder) ClearDelegation(key Key) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.roots[key.RootSessionID]; r != nil {
		delete(r.current, key.AgentSessionID)
	}
}

// Delegation returns the current delegation of key's helper, and whether one
// is held.
func (h *Holder) Delegation(key Key) (Delegation, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.roots[key.RootSessionID]; r != nil {
		d, ok := r.current[key.AgentSessionID]
		return d, ok
	}
	return Delegation{}, false
}

// Delegations returns every delegation recorded for root's session tree in
// the order they were recorded, so each delegating run comes before the runs
// it delegated to: the order a reopened broker session needs them reported
// again in.
func (h *Holder) Delegations(root string) []HelperDelegation {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.roots[root]; r != nil {
		return slices.Clone(r.delegations)
	}
	return nil
}
