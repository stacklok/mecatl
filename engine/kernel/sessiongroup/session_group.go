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

// Close proposes one terminal shutdown: reject new loads/execution, signal
// cancellation, maintain ownership through bounded existing-work drain, then
// release. Actual ownership loss retires all members without waiting for blocked
// I/O. Neither caller cancellation nor a report proves external effects stopped.
//
// The first call starts group-owned shutdown, even if its context is canceled.
// Config.CloseTimeout bounds that shutdown, including Config.DrainTimeout; caller
// contexts bound only their own waits. Concurrent and repeated calls observe the
// same shutdown and final result, never restart it or extend its budget. Each
// returned final report owns its Unresolved slice independently of other results.
//
// A completed shutdown returns its final report and nil error, including when
// release is uncertain or work remains: callers must inspect the report. A wait
// canceled before completion returns zero CloseReport and the context error,
// not a partial report or evidence of release. Later calls may await completion.
// This observation contract remains provisional pending lifecycle implementation.
// Currently it always returns zero CloseReport and ErrNotImplemented, performs
// no shutdown, and releases nothing, even for a canceled context or nil receiver.
func (*SessionGroup) Close(context.Context) (CloseReport, error) {
	return CloseReport{}, ErrNotImplemented
}
