package agent

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
)

// approval is a client's resolution of a permission.ask. It carries the full
// three-way verdict (deny / allow-once / allow-always) rather than a bool, so
// the loop can both authorize the current call AND, on allow-always, ask the
// policy to LEARN a rule. session.VerdictDeny is the zero value (fail-safe).
type approval struct {
	verdict session.ApprovalVerdict
	// denyReason, when non-empty AND verdict is VerdictDeny, REPLACES the default
	// "denied by user: <reason>" message authorize would otherwise synthesize. It is the
	// accurate-message seam for a HEADLESS subagent auto-deny ("not permitted in a
	// non-interactive subagent shell: …"), where no user was ever asked, so "denied by
	// user" would be a lie. Empty (the default) keeps the existing wording — so a real
	// user/surfaced deny still reads "denied by user".
	denyReason string
}

// askRegistry brokers the blocking-and-resume handshake between the loop
// goroutine (which pauses on a permission.ask and waits) and Run.Approve (which
// the API calls out-of-band to resolve it). Each pending ask owns a single
// buffered channel; Approve writes the verdict, await reads it. It is safe for
// concurrent use.
type askRegistry struct {
	mu      sync.Mutex
	pending map[string]pendingApproval
}

// pendingApproval keeps the resolution channel and the one provenance bit the
// ordinary-ask control must inspect at the same linearization point. Plan asks
// remain owned by the dedicated plan-resolution choreography.
type pendingApproval struct {
	ch             chan approval
	planOriginated bool
}

// newAskRegistry constructs an empty registry.
func newAskRegistry() *askRegistry {
	return &askRegistry{pending: make(map[string]pendingApproval)}
}

// register creates and stores a resolution channel for askID before the loop
// emits the permission.ask Event, so an Approve that races in immediately after
// the event is observed cannot be lost. It returns the channel the loop awaits.
func (r *askRegistry) register(askID string) <-chan approval {
	return r.registerPending(session.PendingAsk{AskID: askID})
}

// registerPending is the provenance-carrying registration path used by the
// live ask spine. The legacy ID-only helper remains for internal callers that
// construct an ordinary ask directly.
func (r *askRegistry) registerPending(ask session.PendingAsk) <-chan approval {
	ch := make(chan approval, 1)
	r.mu.Lock()
	r.pending[ask.AskID] = pendingApproval{ch: ch, planOriginated: ask.PlanOriginated}
	r.mu.Unlock()
	return ch
}

// resolve delivers a verdict for askID if one is pending. It is non-blocking and
// idempotent: a second resolution (or one for an unknown ask) is dropped. It
// removes the ask from the registry so a stale Approve cannot resolve a later,
// distinct ask that happens to reuse an id.
func (r *askRegistry) resolve(askID string, v session.ApprovalVerdict) {
	r.resolveWith(askID, approval{verdict: v})
}

// resolveOrdinary atomically classifies and resolves askID. A plan-originated
// ask is deliberately left in the registry for the dedicated plan control.
func (r *askRegistry) resolveOrdinary(askID string, v session.ApprovalVerdict) AskResolution {
	r.mu.Lock()
	pending, ok := r.pending[askID]
	if !ok {
		r.mu.Unlock()
		return AskResolutionNotPending
	}
	if pending.planOriginated {
		r.mu.Unlock()
		return AskResolutionPlanOriginated
	}
	delete(r.pending, askID)
	r.mu.Unlock()

	// The channel is buffered (cap 1) and this entry was removed while locked,
	// so exactly one caller can reach this send and it cannot block.
	pending.ch <- approval{verdict: v}
	return AskResolutionResolved
}

// resolveWith delivers a full approval (verdict + optional accurate deny message) for
// askID. It is the message-bearing variant resolve delegates to; the headless subagent
// auto-deny uses it to carry childAutoDenyMessage so the model sees the accurate cause
// rather than the misleading "denied by user".
func (r *askRegistry) resolveWith(askID string, a approval) {
	r.mu.Lock()
	pending, ok := r.pending[askID]
	if ok {
		delete(r.pending, askID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	// ch is buffered (cap 1) and only ever written once per ask, so this never
	// blocks.
	pending.ch <- a
}

// discard drops a pending ask without resolving it and reports whether it was
// still pending. The loop calls this when an await is abandoned (e.g. ctx
// cancel) so the registry does not leak entries; Run.RetractPermissionAsk uses
// the result as the exactly-once gate for the matching retraction event.
func (r *askRegistry) discard(askID string) bool {
	r.mu.Lock()
	_, ok := r.pending[askID]
	if ok {
		delete(r.pending, askID)
	}
	r.mu.Unlock()
	return ok
}

// childAskRouter maps a CHILD run's askID to the child *Run that owns it, so the
// parent Run.Approve can route a verdict for a surfaced subagent ask down to the child
// whose authorize is parked awaiting it. A child askID is namespaced by the child's own
// session id (newAskID keys on sess.ID; a child session id is e.g. team-<id>-<member>
// or subagent-<callID>), so it is globally unique and never collides with the parent's
// own askIDs — the parent consults the router FIRST and falls through to its own
// registry on a miss. It is owned by the parent Run (one per interactive parent run);
// child Runs never surface further (subagents cannot recurse), so they carry no router.
// It is safe for concurrent use: register/unregister/route may be called from the
// drain/forwarder goroutine, the readControl goroutine, and the child's own loop.
type childAskRouter struct {
	mu      sync.Mutex
	byAskID map[string]*Run
}

// newChildAskRouter constructs an empty router.
func newChildAskRouter() *childAskRouter {
	return &childAskRouter{byAskID: make(map[string]*Run)}
}

// registerChild records that askID is owned by child, BEFORE the parent emits the
// surfaced EvPermissionAsk, so a fast ResumeApproval cannot race ahead of registration
// (mirrors askRegistry.register-before-emit).
func (r *childAskRouter) registerChild(askID string, child *Run) {
	r.mu.Lock()
	r.byAskID[askID] = child
	r.mu.Unlock()
}

// route delivers verdict v to the child that owns askID and returns true; it returns
// false (the parent then resolves its own ask) when askID is unknown. The owning child
// is unregistered on a hit so a stale verdict cannot resolve a later ask. child.Approve
// is itself idempotent and safe on an already-resolved/unknown id, so a verdict that
// arrives after the child moved on is a harmless no-op.
func (r *childAskRouter) route(askID string, v session.ApprovalVerdict) bool {
	r.mu.Lock()
	child, ok := r.byAskID[askID]
	if ok {
		delete(r.byAskID, askID)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	child.Approve(askID, v)
	return true
}

// resolveOrdinary resolves one surfaced child ask while holding the route and
// child-registry decision in a single critical section. A plan-originated ask
// leaves both entries pending so the compatibility plan choreography can still
// reach it. The found result distinguishes a child route from a parent-registry
// miss even when the child ask was resolved concurrently.
func (r *childAskRouter) resolveOrdinary(askID string, v session.ApprovalVerdict) (result AskResolution, found bool) {
	r.mu.Lock()
	child, ok := r.byAskID[askID]
	if !ok {
		r.mu.Unlock()
		return AskResolutionNotPending, false
	}

	result = child.asks.resolveOrdinary(askID, v)
	if result != AskResolutionPlanOriginated {
		delete(r.byAskID, askID)
	}
	r.mu.Unlock()
	return result, true
}

// unregister drops a surfaced child ask from the router without routing a
// verdict, reporting whether an entry was actually removed (a locked
// check-and-delete). The bool is the ANSWERED-vs-PENDING gate the retraction
// paths key on: route() already deletes an answered ask's entry, so false
// means the verdict was routed (or the ask never surfaced) and the caller
// must NOT emit a permission.retract — the client already resolved its modal.
// The retraction paths (childRunRegistry.retractAsks, reached from
// Run.CancelChild and from a cancelled child's registry terminal) call it
// BEFORE emitting the retract: a late ResumeApproval for the dropped askID
// then falls through to the parent's OWN registry, where it dies as an
// unknown-ask no-op — the documented stale-verdict behaviour. Idempotent;
// unknown ids return false.
func (r *childAskRouter) unregister(askID string) bool {
	r.mu.Lock()
	_, ok := r.byAskID[askID]
	if ok {
		delete(r.byAskID, askID)
	}
	r.mu.Unlock()
	return ok
}

// await blocks until the client resolves askID via resolve, or ctx is cancelled.
// It reports the verdict and ok=true on resolution; ok=false means the wait was
// abandoned (ctx cancelled), in which case the verdict is the zero value
// (VerdictDeny, fail-safe) and the caller should end the run as cancelled. The
// ask is removed from the registry on either path.
func (r *askRegistry) await(ctx context.Context, askID string, ch <-chan approval) (a approval, ok bool) {
	select {
	case got := <-ch:
		return got, true
	case <-ctx.Done():
		r.discard(askID)
		return approval{verdict: session.VerdictDeny}, false
	}
}
