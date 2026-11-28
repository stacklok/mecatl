package resourcegroup

import (
	"context"
	"errors"

	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

// ErrNotImplemented identifies the nonoperational residency skeleton. It proves
// neither existence, absence, authority, nor release.
var ErrNotImplemented = errors.New("resource group residency is not implemented")

// Manager is the process-local factory registry and Group residency owner.
// Obtain it through NewManager; zero is unusable and must not be copied after use.
// No registry, synchronization, or operational state is implemented here.
type Manager struct{}

// NewManager proposes constructing a Manager with explicit configuration.
// Currently it returns nil, ErrNotImplemented without validation or retention.
func NewManager(ManagerConfig) (*Manager, error) { return nil, ErrNotImplemented }

// Register proposes atomically adding factories, rejecting duplicate Kind or
// ResourceType both within the batch and against the registry. Typed nil factories
// are invalid. Metadata must be stable, local, and non-I/O; its declarations are
// not proof that a factory implements a particular Loader. Registrations are
// additive, including after group loading, and never replace existing factories.
// Drain synchronously bars registration. Currently it returns ErrNotImplemented,
// without inspecting factories or calling their metadata methods.
func (*Manager) Register(...Factory) error { return ErrNotImplemented }

// Load proposes converging concurrent openings of an existing group. ctx governs
// only this caller's wait, not shared construction, which uses OpeningTimeout.
// Opening resolves storage, verifies its GroupID, claims authority, then checks a
// snapshot of supported stored member kinds, taken after the claim, before
// publication. Unknown kinds block activation without eagerly loading members;
// later registration permits a retry, not retroactive activation. A failed opening
// makes best-effort cleanup of its fresh acquisition; late results cannot revive
// retired authority. Neither identity parsing nor registration grants authority.
// Drain bars new loads synchronously; already in-flight openings join shutdown
// or unwind within their bounds. Currently it returns nil, ErrNotImplemented.
func (*Manager) Load(context.Context, groupid.GroupID) (*Group, error) {
	return nil, ErrNotImplemented
}

// ResidentGroups proposes a detached local snapshot including opening Groups,
// with IDs and scalar state only, no handles. It is not authoritative membership
// or placement; ordering is unspecified. Currently it returns nil, ErrNotImplemented.
func (*Manager) ResidentGroups() ([]GroupStatus, error) { return nil, ErrNotImplemented }

// Drain proposes promptly and idempotently barring registrations and new group
// loads, then coordinating all resident and opening Groups' bounded shutdowns.
// It borrows no caller context and never waits for user or storage code here.
// The first shutdown trigger fixes the Manager's absolute shutdown bound, including
// opening work; existing Group budgets may be shortened but never extended.
// Currently it returns ErrNotImplemented and starts nothing.
func (*Manager) Drain() error { return ErrNotImplemented }

// Done proposes a stable channel closing when bounded coordination and the final
// report are complete, not when every goroutine or external effect has stopped.
// Currently it returns nil, including on nil/zero receivers.
func (*Manager) Done() <-chan struct{} { return nil }

// Report proposes nonblocking final accounting, or ErrNotComplete before Done.
// Each successful call returns detached slices, including openings never claimed;
// availability is not proof of clean shutdown or ended dependency lifetimes.
// Currently it returns zero ManagerShutdownReport, ErrNotImplemented.
func (*Manager) Report() (ManagerShutdownReport, error) {
	return ManagerShutdownReport{}, ErrNotImplemented
}
