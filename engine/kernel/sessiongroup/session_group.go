package sessiongroup

import (
	"context"
	"errors"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	"github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
	"github.com/stacklok/mecatl/engine/kernel/sessionruntime"
)

// ErrNotImplemented identifies the nonoperational hosting skeleton. An error
// from a skeleton method is not evidence of membership, absence, or release.
var ErrNotImplemented = errors.New("session group hosting is not implemented")

// SessionGroup is the hosting residency and shared ownership boundary for its
// permanent members. Obtain it through Open; zero is unusable and must not be
// copied after use. No constructor currently returns a usable group.
//
// Its intended responsibility is to own the loaded-runtime registry and group
// lifecycle, not to execute member loops or duplicate History's storage API.
// Registry, synchronization, ownership dependencies, and capacity/timeout policy
// are not implemented or represented by placeholder mutable state.
type SessionGroup struct {
	id id.GroupID
}

// Open proposes acquiring hosting residency for an existing group. It must
// eventually return a fully initialized owner or nil and an error; parsing an ID
// or loading member history is not authority. Opening is distinct from creating
// the group or its first Session. Membership/ownership dependencies and opening
// budgets will be added with their contracts, not inferred from global state.
// Currently it always returns nil, ErrNotImplemented, even on cancellation.
func Open(context.Context, id.GroupID) (*SessionGroup, error) {
	return nil, ErrNotImplemented
}

// GroupID returns the stable identity, or zero for a zero/nil group. It is not
// evidence that this host owns residency or that the group exists in storage.
func (g *SessionGroup) GroupID() id.GroupID {
	if g == nil {
		return id.GroupID{}
	}
	return g.id
}

// Members proposes enumerating authoritative permanent membership, including
// unloaded Sessions, without activating them. The eventual result owns its slice;
// order is unspecified. A successful empty result must mean an empty group,
// never a failed catalog lookup or an empty loaded set. Enumeration bounds and
// catalog consistency remain provisional until membership storage is selected.
// Currently it always returns nil, ErrNotImplemented.
func (*SessionGroup) Members(context.Context) ([]sessionid.SessionID, error) {
	return nil, ErrNotImplemented
}

// LoadedRuntimes proposes a point-in-time residency snapshot, not membership or
// an authority grant. The eventual result owns its slice of controlled runtime
// handles; order is unspecified, and retaining a handle must not pin residency
// or allow use after unload/retirement. No mutable Session is exposed.
// Currently it always returns nil, ErrNotImplemented.
func (*SessionGroup) LoadedRuntimes() ([]*sessionruntime.SessionRuntime, error) {
	return nil, ErrNotImplemented
}

// Load proposes lazy activation of one existing member under group authority.
// It must reject nonmembers, converge concurrent loads, and never publish a
// partially reconstructed runtime or reopen a closing/retired group. It neither
// creates a Session nor starts a run. Failure must release any capacity reserved
// for that attempt; capacity bounds belong to the future construction policy.
// Currently it always returns nil, ErrNotImplemented.
func (*SessionGroup) Load(context.Context, sessionid.SessionID) (*sessionruntime.SessionRuntime, error) {
	return nil, ErrNotImplemented
}

// Unload proposes ending an idle member's local runtime residency. Membership
// and group ownership remain unchanged. Unresolved append acceptance or external
// work is not idle; unload must refuse it without a supported recovery transfer.
// In-flight loads and previously returned handles must be coordinated by the
// eventual registry. This is not a forced cancellation or deletion operation.
// Currently it always returns ErrNotImplemented.
func (*SessionGroup) Unload(context.Context, sessionid.SessionID) error {
	return ErrNotImplemented
}

// Close proposes terminally closing hosting: reject new loads/execution, signal
// cancellation, maintain ownership through bounded existing-work drain, then
// release. Caller timeout cannot reopen admission or prove that effects stopped.
// Actual ownership loss retires all members without waiting for blocked I/O.
// The bounded drain/release report and repeated-close observation contract need
// refinement before implementation; error alone must not hide unresolved work.
// Currently it always returns ErrNotImplemented and closes/releases nothing.
func (*SessionGroup) Close(context.Context) error {
	return ErrNotImplemented
}
