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
// the group or its first Session. Config supplies explicit lease dependencies,
// lifecycle bounds, and passive member construction inputs; its grouping remains
// provisional until runtime construction is exercised. The eventual owner keeps
// the acquired Hold private and gives runtimes only controlled member authority,
// never an unfenced backend or public access to the Hold. The private construction
// boundary and authoritative membership enumeration remain subsequent slices.
// Currently it always returns nil, ErrNotImplemented, even on cancellation or
// invalid configuration; it does not validate or retain Config or call dependencies.
func Open(context.Context, id.GroupID, Config) (*SessionGroup, error) {
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
// for that attempt; Config.MaxLoadedSessions bounds retained runtime residency.
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

// Drain proposes promptly and idempotently starting group-owned graceful
// shutdown, without waiting for external work or borrowing a caller context.
// It closes new load/execution admission, but lets admitted work finish or reach
// a safe checkpoint while ownership remains provable. Normal draining includes
// quiescing/detaching execution environments before releasing the lease; there
// is no separate graceful cleanup phase after release.
//
// Config.DrainTimeout expiry escalates to emergency cleanup. Observed ownership
// loss or inability to prove validity immediately retires admission and skips
// grace, including during an existing drain. Emergency actions may begin while
// ownership is held or after it is gone, and must respect that distinction:
// stale cleanup must never affect a successor's environment or authority.
// Lease supervision continues while authority remains usable; draining neither
// grants authority nor delays loss handling behind a blocked participant.
//
// The first shutdown trigger (Drain or ownership trouble) starts the single
// Config.ShutdownTimeout budget. Repeated Drain calls cannot reopen admission,
// restart/extend budgets, or downgrade emergency cleanup to graceful draining.
// The eventual integration must coordinate runtime/environment participants;
// callback shape and handoff guarantees await concrete consumers. These phase
// boundaries are provisional; see LifecycleStatus before adding consumers.
// Currently it always returns ErrNotImplemented and starts no shutdown.
func (*SessionGroup) Drain() error {
	return ErrNotImplemented
}

// Status proposes promptly capturing a concurrency-safe lifecycle observation.
// It must not wait for storage or participant callbacks. The returned value is
// detached scalar state, not authority or a substitute for admission checks.
// Currently it returns zero LifecycleStatus and ErrNotImplemented.
func (*SessionGroup) Status() (LifecycleStatus, error) {
	return LifecycleStatus{}, ErrNotImplemented
}

// Done proposes a stable channel closed once bounded shutdown coordination has
// completed and its final report is available. Closure proves neither lease
// release nor cessation of all external effects; unresolved work stays reported.
// Callers may select on Done and their own contexts without canceling shutdown.
// Currently it returns nil, including on a nil/zero group, and signals nothing.
func (*SessionGroup) Done() <-chan struct{} {
	return nil
}

// Report proposes nonblocking access to the final shutdown accounting. Before
// completion it returns zero ShutdownReport and ErrNotComplete. After Done closes,
// it returns the same final outcome with a separately owned Unresolved slice on
// every call. Nil error means the report is available, not that shutdown was clean;
// callers must inspect release and unresolved outcomes. Provisional reports do not
// transfer the lifetime obligation for outstanding dependencies.
// Currently it returns zero ShutdownReport and ErrNotImplemented.
func (*SessionGroup) Report() (ShutdownReport, error) {
	return ShutdownReport{}, ErrNotImplemented
}
