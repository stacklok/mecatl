package resourcegroup

import (
	"context"

	engineid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/groupstorage"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

// Group is the shared ownership and resource residency boundary. Obtain it from
// Manager.Load; zero is unusable and must not be copied after use. No constructor
// currently returns a usable Group. A resident Group will privately retain its
// owning Manager for live factory lookup. It exposes neither its Manager as a service
// locator nor a universal unload operation: instance completion is domain-owned.
type Group struct {
	id groupid.GroupID
}

// GroupID returns stable identity, or zero on nil/zero receivers. It establishes
// neither membership nor authority.
func (g *Group) GroupID() groupid.GroupID {
	if g == nil {
		return groupid.GroupID{}
	}
	return g.id
}

// Members proposes authoritative paged permanent membership, including unloaded
// resources, without activation. Page ownership, bounds, cursors, and cross-page
// consistency follow groupstorage.Backend.Members. A failed lookup must never be
// presented as empty membership. Currently it returns zero page, ErrNotImplemented.
func (*Group) Members(context.Context, engineid.Kind, groupstorage.MemberPageRequest) (groupstorage.MemberPage, error) {
	return groupstorage.MemberPage{}, ErrNotImplemented
}

// ResidentResources proposes a detached local snapshot including constructing
// instances, with IDs and scalar state only, no handles. It is not permanent
// membership; ordering is unspecified. Currently it returns nil, ErrNotImplemented.
func (*Group) ResidentResources() ([]ResourceStatus, error) { return nil, ErrNotImplemented }

// Drain proposes promptly and idempotently entering graceful draining. Draining
// remains an admission mode: new counted draining resources may be constructed
// with inherited deadlines. Only after all constructors and instance Completions
// are terminal does coordination atomically seal admission and then release
// authority. Renewal continues through grace while authority is provable.
//
// Grace expiry, ownership loss, or unprovable validity triggers emergency cleanup
// independently of blocked resource code or I/O. Emergency cannot return to grace;
// stale cleanup must not affect a successor. The first trigger fixes the shared
// shutdown budget; subsequent drains or new instances cannot reset it. Completion
// is bounded coordination, not proof that all effects have stopped.
// Currently it returns ErrNotImplemented and starts nothing.
func (*Group) Drain() error { return ErrNotImplemented }

// Status proposes a prompt detached scalar observation, not authority or an
// admission check, without waiting for storage or callbacks. Currently it returns
// zero LifecycleStatus, ErrNotImplemented.
func (*Group) Status() (LifecycleStatus, error) { return LifecycleStatus{}, ErrNotImplemented }

// Done proposes a stable channel closed when bounded shutdown coordination and
// its final report are complete, regardless of unresolved external effects.
// Currently it returns nil, including on nil/zero receivers.
func (*Group) Done() <-chan struct{} { return nil }

// Report proposes nonblocking final accounting, or ErrNotComplete before Done.
// Each successful call owns its Unresolved slice; nil error means available, not
// cleanly released. Currently it returns zero ShutdownReport, ErrNotImplemented.
func (*Group) Report() (ShutdownReport, error) { return ShutdownReport{}, ErrNotImplemented }
