package agent

import (
	"context"
	"fmt"
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
	pending map[string]chan approval
	scopes  map[string]session.PendingAsk
}

// newAskRegistry constructs an empty registry.
func newAskRegistry() *askRegistry {
	return &askRegistry{pending: make(map[string]chan approval), scopes: make(map[string]session.PendingAsk)}
}

func (r *askRegistry) registerAsk(ask session.PendingAsk) <-chan approval {
	ch := make(chan approval, 1)
	r.mu.Lock()
	r.pending[ask.AskID] = ch
	r.scopes[ask.AskID] = ask
	r.mu.Unlock()
	return ch
}

// register creates and stores a resolution channel for askID before the loop
// emits the permission.ask Event, so an Approve that races in immediately after
// the event is observed cannot be lost. It returns the channel the loop awaits.
func (r *askRegistry) register(askID string) <-chan approval {
	return r.registerAsk(session.PendingAsk{AskID: askID, Origin: session.ApprovalOriginPermission})
}

// resolveOrdinary atomically classifies and resolves askID. Any non-permission
// ask is deliberately left in the registry for its purpose-specific control.
func (r *askRegistry) resolveOrdinary(askID string, v session.ApprovalVerdict) AskResolution {
	r.mu.Lock()
	ch, ok := r.pending[askID]
	ask := r.scopes[askID]
	if !ok {
		r.mu.Unlock()
		return AskResolutionNotPending
	}
	if ask.Origin != session.ApprovalOriginPermission {
		r.mu.Unlock()
		return AskResolutionPlanOriginated
	}
	delete(r.pending, askID)
	delete(r.scopes, askID)
	r.mu.Unlock()

	// The channel is buffered (cap 1) and this entry was removed while locked,
	// so exactly one caller can reach this send and it cannot block.
	ch <- approval{verdict: v}
	return AskResolutionResolved
}

// resolvePlan checks provenance and consumes the verdict at one lock point.
func (r *askRegistry) resolvePlan(askID string, verdict session.ApprovalVerdict) AskResolution {
	r.mu.Lock()
	ch, ok := r.pending[askID]
	ask := r.scopes[askID]
	if !ok {
		r.mu.Unlock()
		return AskResolutionNotPending
	}
	if ask.Origin != session.ApprovalOriginPlan || ask.Guardrail != nil {
		r.mu.Unlock()
		return AskResolutionNotPlan
	}
	delete(r.pending, askID)
	delete(r.scopes, askID)
	r.mu.Unlock()
	ch <- approval{verdict: verdict}
	return AskResolutionResolved
}

// resolveWith delivers a full approval (verdict + optional accurate deny message) for
// askID. It is the message-bearing variant resolve delegates to; the headless subagent
// auto-deny uses it to carry childAutoDenyMessage so the model sees the accurate cause
// rather than the misleading "denied by user".
func (r *askRegistry) resolveWith(askID string, a approval) {
	_ = r.resolveChecked(askID, a, nil)
}

func (r *askRegistry) resolveChecked(askID string, a approval, check func(session.PendingAsk) error) error {
	r.mu.Lock()
	ch, ok := r.pending[askID]
	ask := r.scopes[askID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: ask is unknown, stale, or already resolved", ErrApprovalNotPending)
	}
	if check != nil {
		if err := check(ask); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	delete(r.pending, askID)
	delete(r.scopes, askID)
	r.mu.Unlock()
	ch <- a
	return nil
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
		delete(r.scopes, askID)
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
	mu       sync.Mutex
	byAskID  map[string]childAskRoute
	accepted map[*Run][]session.Event
	closed   bool
}

type childAskRoute struct {
	child    *Run
	turn     int
	approval session.ApprovalPayload
	surfaced bool
}

// newChildAskRouter constructs an empty router.
func newChildAskRouter() *childAskRouter {
	return &childAskRouter{byAskID: make(map[string]childAskRoute), accepted: make(map[*Run][]session.Event)}
}

// registerChild records that askID is owned by child, BEFORE the parent emits the
// surfaced EvPermissionAsk, so a fast ResumeApproval cannot race ahead of registration
// (mirrors askRegistry.register-before-emit).
func (r *childAskRouter) registerChild(askID string, child *Run) {
	r.mu.Lock()
	if !r.closed {
		r.byAskID[askID] = childAskRoute{child: child}
	}
	r.mu.Unlock()
}

// registerSurfaced retains only the metadata the parent may safely record on
// verdict acceptance. The child call ID is deliberately omitted: it can collide
// with a call in the parent conversation and must never drive parent rule replay.
func (r *childAskRouter) registerSurfaced(ask session.PendingAsk, child *Run, turn int) {
	r.mu.Lock()
	if !r.closed {
		r.byAskID[ask.AskID] = childAskRoute{child: child, turn: turn, surfaced: true,
			approval: session.ApprovalPayload{AskID: ask.AskID, Tool: ask.Tool, Origin: ask.Origin}}
	}
	r.mu.Unlock()
}

// route delivers verdict v to the child that owns askID and returns true; it returns
// false (the parent then resolves its own ask) when askID is unknown. The owning child
// is unregistered on a hit so a stale verdict cannot resolve a later ask. child.Approve
// is itself idempotent and safe on an already-resolved/unknown id, so a verdict that
// arrives after the child moved on is a harmless no-op.
func (r *childAskRouter) route(askID string, v session.ApprovalVerdict) bool {
	owned, _ := r.routeResolution(ApprovalResolution{AskID: askID, Verdict: v})
	return owned
}

func (r *childAskRouter) routeResolution(resolution ApprovalResolution) (bool, error) {
	// Child runs have no child router. ResolveApproval takes only their ask
	// registry mutex and sends to a capacity-one channel; it never emits to the
	// parent. Keeping router.mu across that short submit makes acceptance atomic
	// with retraction and marker recording. Parent emissions take emitMu first,
	// then router.mu, only after this method returns.
	r.mu.Lock()
	defer r.mu.Unlock()
	route, ok := r.byAskID[resolution.AskID]
	if !ok {
		return false, nil
	}
	if err := route.child.ResolveApproval(resolution); err != nil {
		// A validation failure leaves both the child ask and route pending.
		return true, err
	}
	delete(r.byAskID, resolution.AskID)
	r.recordAccepted(route, resolution.Verdict)
	return true, nil
}

// resolveOrdinary resolves one surfaced child ask while holding the route and
// child-registry decision in a single critical section. A plan-originated ask
// leaves both entries pending so the compatibility plan choreography can still
// reach it. The found result distinguishes a child route from a parent-registry
// miss even when the child ask was resolved concurrently.
func (r *childAskRouter) resolveOrdinary(askID string, v session.ApprovalVerdict) (result AskResolution, found bool) {
	r.mu.Lock()
	route, ok := r.byAskID[askID]
	if !ok {
		r.mu.Unlock()
		return AskResolutionNotPending, false
	}

	result = route.child.asks.resolveOrdinary(askID, v)
	if result != AskResolutionPlanOriginated {
		delete(r.byAskID, askID)
	}
	if result == AskResolutionResolved {
		r.recordAccepted(route, v)
	}
	r.mu.Unlock()
	return result, true
}

// recordAccepted runs under mu after the child registry consumes a real verdict.
// A child drain later emits the parent event without holding this mutex or the
// server's control locks, including when cancellation prevents the child's own
// EvApproval.
func (r *childAskRouter) recordAccepted(route childAskRoute, verdict session.ApprovalVerdict) {
	if !route.surfaced {
		return
	}
	payload := route.approval
	payload.Verdict = session.VerdictString(verdict)
	payload.AllowAlways = verdict == session.VerdictAllowAlways
	r.accepted[route.child] = append(r.accepted[route.child], session.Event{
		Type: session.EvApproval, Turn: route.turn, Approval: &payload,
	})
}

func (r *childAskRouter) takeAccepted(child *Run) []session.Event {
	r.mu.Lock()
	events := r.accepted[child]
	delete(r.accepted, child)
	r.mu.Unlock()
	return events
}

// closeEvents is the parent's final answer to every still-routed ask. It runs
// under childRunRegistry.emitMu immediately before stream seal: accepted
// verdicts become approvals, and any pending routes become retractions. A
// verdict racing this lock either lands in accepted before close or is rejected
// after close; no answer can appear in the sweep-to-seal gap.
func (r *childAskRouter) closeEvents() []session.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var events []session.Event
	for child, queued := range r.accepted {
		events = append(events, queued...)
		delete(r.accepted, child)
	}
	for id, route := range r.byAskID {
		if route.surfaced {
			events = append(events, session.Event{Type: session.EvPermissionRetract,
				Ask: &session.PendingAsk{AskID: id}})
		}
		delete(r.byAskID, id)
	}
	return events
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
