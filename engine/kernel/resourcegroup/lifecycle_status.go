package resourcegroup

import (
	engineid "github.com/stacklok/mecatl/engine/id"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

// Phase is a provisional closed in-memory local lifecycle vocabulary, not
// ownership. Zero and unknown values establish no usable state.
type Phase uint8

const (
	// PhaseUnknown denotes unset or unavailable progress.
	PhaseUnknown Phase = iota
	// PhaseOpening denotes group opening or resource construction, not usability.
	PhaseOpening
	// PhaseActive permits ordinary work only with provable authority.
	PhaseActive
	// PhaseDraining denotes cooperative shutdown, still allowing counted draining
	// resources until atomic sealing after all constructors/completions terminate.
	PhaseDraining
	// PhaseEmergencyCleanup denotes irreversible emergency coordination, whether
	// authority is still held or already gone. New admission is barred.
	PhaseEmergencyCleanup
	// PhaseComplete denotes bounded coordination and available final accounting,
	// not proof of clean release or stopped external effects.
	PhaseComplete
)

// Authority is provisional closed in-memory ownership evidence, independent of
// Phase. No value grants authority; enforcement occurs at each acceptance point.
type Authority uint8

const (
	// AuthorityUnknown denotes unset or unavailable evidence.
	AuthorityUnknown Authority = iota
	// AuthorityHeld denotes the latest observation supporting authority, possibly
	// already stale; operations still require current generation fencing.
	AuthorityHeld
	// AuthorityReleased denotes established voluntary release.
	AuthorityReleased
	// AuthorityLost denotes observed supersession or retirement, not mere expiry.
	// Expiry makes takeover eligible; it is not terminal storage authority loss.
	AuthorityLost
	// AuthorityUnproven denotes locally unprovable validity. It retires local
	// admission without claiming takeover; later evidence cannot revive this owner.
	AuthorityUnproven
)

// LifecycleStatus is detached scalar progress and ownership evidence. Copies may
// be read concurrently but are immediately stale and never authorize work.
// Grace expiry enters emergency even if ownership survives; loss or unprovable
// validity skips grace independently of blocked code. Emergency cannot return to
// grace. Revisit these provisional phases with concrete factory consumers.
type LifecycleStatus struct {
	// Phase is local progress independent of ownership evidence.
	Phase Phase
	// Authority is the latest observation, not a storage capability.
	Authority Authority
}

// GroupStatus is a detached local residency observation, including openings.
// It exposes neither a Group handle nor authoritative placement information.
type GroupStatus struct {
	// GroupID correlates the opening or resident Group.
	GroupID groupid.GroupID
	// LifecycleStatus contains detached phase and authority observations.
	LifecycleStatus LifecycleStatus
}

// ResourceStatus is a detached local instance observation, including construction.
// It contains no resource handle and does not prove permanent membership.
type ResourceStatus struct {
	// ResourceID correlates the instance within the enclosing Group.
	ResourceID engineid.ID
	// Phase is local progress; PhaseOpening includes in-flight construction.
	Phase Phase
}
