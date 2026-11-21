package sessiongroup

// Phase describes local shutdown progress, not ownership. It is a provisional,
// closed in-memory vocabulary; zero and unknown values establish no usable state.
type Phase uint8

const (
	// PhaseUnknown is unset or unavailable lifecycle information.
	PhaseUnknown Phase = iota
	// PhaseActive permits new work only while ownership remains provable.
	PhaseActive
	// PhaseDraining rejects new work and cooperatively finishes/checkpoints
	// admitted work, quiesces/detaches environments, then releases ownership.
	PhaseDraining
	// PhaseEmergencyCleanup rejects new work and forcibly quiesces/cleans up
	// within the remaining budget. Actions depend on whether authority survives;
	// this phase may be entered with ownership still held or already gone.
	PhaseEmergencyCleanup
	// PhaseComplete means bounded shutdown coordination finished and its final
	// report is available. It proves neither clean release nor ceased effects.
	PhaseComplete
)

// Authority describes observed group ownership separately from shutdown progress.
// This provisional closed in-memory vocabulary grants no authority. Zero and
// unknown values cannot justify work; enforcement belongs at each acceptance point.
type Authority uint8

const (
	// AuthorityUnknown means ownership information is unset or unavailable.
	AuthorityUnknown Authority = iota
	// AuthorityHeld means the latest observation supports ownership. It may
	// already be stale; operations still require current incarnation fencing.
	AuthorityHeld
	// AuthorityReleased means voluntary release was established.
	AuthorityReleased
	// AuthorityLost means terminal expiry or supersession was observed.
	AuthorityLost
	// AuthorityUnproven means local validity can no longer be established,
	// without claiming that expiry or takeover was observed. It retires local
	// admission just as loss does; later evidence must not revive this owner.
	AuthorityUnproven
)

// LifecycleStatus is a detached, scalar observation of one group's local phase
// and ownership evidence. Its zero value establishes neither admission nor
// completion. Callers may copy it and read copies concurrently; observations
// can become stale immediately and never authorize storage or environment work.
//
// Provisional paths:
//
//	Active -> Draining -> Complete
//	Active / Draining -> EmergencyCleanup -> Complete
//
// Grace expiry enters emergency cleanup while ownership may still be held.
// Ownership loss or unprovable validity skips grace and retires all member
// admission immediately, even if a participant is blocked. Emergency cleanup
// cannot return to graceful draining. A successful drain detaches environments
// before lease release; there is no graceful post-release cleanup phase.
//
// Revisit the vocabulary and transitions against real hibernation/handoff,
// grace expiry, ownership loss during handoff, and uncooperative-operation cases.
// Participants may need different graceful/emergency and pre/post-loss actions;
// no callback framework, runtime shutdown API, or handoff guarantee is selected.
// Admission retirement is immediate, distinct from completion of bounded cleanup.
type LifecycleStatus struct {
	// Phase is the observed progress, independent of whether ownership remains.
	Phase Phase
	// Authority is the latest ownership observation, not a usable lease Hold.
	Authority Authority
}
