package agent

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestAcceptedChildApprovalRequeuesAfterEmitAbort(t *testing.T) {
	r, _ := newDrainRun(1)
	r.childAsks = newChildAskRouter()
	r.hardAbort = make(chan struct{})
	defer close(r.hardAbort)
	r.events <- session.Event{Type: session.EvTurnStart}
	sink := &recordingSink{}
	e := &Engine{deps: Deps{Sink: sink}}
	child := &Run{asks: newAskRegistry()}
	ask := session.PendingAsk{AskID: "child-ask", Tool: "Shell", Origin: session.ApprovalOriginPermission}
	child.asks.registerAsk(ask)
	r.childAsks.registerSurfaced(ask, child, 1)
	if got := r.ResolveOrdinaryAsk(ask.AskID, session.VerdictAllowOnce); got != AskResolutionResolved {
		t.Fatalf("resolved ask = %v", got)
	}
	caps := e.parentCaps(r, nil, 1)
	done := make(chan struct{})
	go func() {
		caps.emitChildApprovals(child)
		close(done)
	}()
	deadline := time.After(10 * time.Second)
	for {
		r.childAsks.mu.Lock()
		taken := len(r.childAsks.accepted[child]) == 0
		r.childAsks.mu.Unlock()
		if taken {
			break
		}
		select {
		case <-deadline:
			t.Fatal("child drain did not take accepted approval")
		default:
			runtime.Gosched()
		}
	}
	r.children.abortEmits()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("child drain did not stop on the child emit abort alone")
	}
	if queued := r.childAsks.takeAccepted(child); len(queued) != 1 || queued[0].Approval == nil || queued[0].Approval.AskID != ask.AskID {
		t.Fatalf("aborted approval was not requeued: %+v", queued)
	}
	if mirrored := sink.snapshotEvents(); len(mirrored) != 0 {
		t.Fatalf("aborted approval reached sink without stream delivery: %+v", mirrored)
	}
}

// orderedApprovalSink parks after a child approval reaches the sink, before
// recording it. A terminal result may not overtake that already-delivered
// approval, even when parent drain races the child sink callback.
type orderedApprovalSink struct {
	approvalEntered chan struct{}
	releaseApproval chan struct{}
	seen            chan session.Event
}

func (s *orderedApprovalSink) Emit(_ context.Context, ev session.Event) {
	if ev.Type == session.EvApproval {
		close(s.approvalEntered)
		<-s.releaseApproval
	}
	s.seen <- ev
}

func TestStudioChatApprovals_Scenario1_ChildApprovalSinkPrecedesTerminal(t *testing.T) {
	r, _ := newDrainRun(2)
	r.runID = "parent-run"
	r.childAsks = newChildAskRouter()
	sink := &orderedApprovalSink{
		approvalEntered: make(chan struct{}),
		releaseApproval: make(chan struct{}),
		seen:            make(chan session.Event, 2),
	}
	releaseApproval := sync.OnceFunc(func() { close(sink.releaseApproval) })
	defer releaseApproval()
	e := &Engine{deps: Deps{Sink: sink}}
	child := &Run{asks: newAskRegistry()}
	ask := session.PendingAsk{AskID: "child-ask", Tool: "Shell", Origin: session.ApprovalOriginPermission}
	child.asks.registerAsk(ask)
	r.childAsks.registerSurfaced(ask, child, 1)
	if got := r.ResolveOrdinaryAsk(ask.AskID, session.VerdictAllowOnce); got != AskResolutionResolved {
		t.Fatalf("resolved ask = %v, want accepted", got)
	}
	childDone := make(chan struct{})
	go func() {
		e.parentCaps(r, nil, 1).emitChildApprovals(child)
		close(childDone)
	}()
	select {
	case <-sink.approvalEntered:
	case <-time.After(time.Second):
		t.Fatal("child approval never reached the sink")
	}
	parentDone := make(chan struct{})
	go func() {
		e.drainChildren(context.Background(), r)
		e.emit(r, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
		close(parentDone)
	}()
	select {
	case <-r.children.emitAbort:
	case <-time.After(time.Second):
		t.Fatal("parent drain never began sealing")
	}
	var early session.Event
	select {
	case early = <-sink.seen:
	case <-time.After(100 * time.Millisecond):
	}
	releaseApproval()
	select {
	case <-childDone:
	case <-time.After(time.Second):
		t.Fatal("child approval mirror did not finish")
	}
	select {
	case <-parentDone:
	case <-time.After(time.Second):
		t.Fatal("parent terminal did not finish")
	}
	if early.Type != "" {
		t.Fatalf("sink observed %s before the accepted child approval", early.Type)
	}
	readEvent := func(ch <-chan session.Event, source string) session.Event {
		t.Helper()
		select {
		case ev := <-ch:
			return ev
		case <-time.After(time.Second):
			t.Fatalf("%s did not deliver its event", source)
			return session.Event{}
		}
	}
	firstSink, secondSink := readEvent(sink.seen, "sink"), readEvent(sink.seen, "sink")
	firstStream, secondStream := readEvent(r.events, "stream"), readEvent(r.events, "stream")
	if firstSink.Type != session.EvApproval || secondSink.Type != session.EvResult {
		t.Fatalf("sink order = %s, %s; want approval before result", firstSink.Type, secondSink.Type)
	}
	assertApprovalSinkParity(t, []session.Event{firstSink, secondSink}, firstStream, secondStream)
}

// TestFinalChildApprovalSealDoesNotHoldEmitMuBehindStream proves that a parent
// stream waiting for a consumer cannot prevent child-registry seal. The final
// approval still reaches the stream after the consumer makes room.
func TestFinalChildApprovalSealDoesNotHoldEmitMuBehindStream(t *testing.T) {
	r, _ := newDrainRun(1)
	r.childAsks = newChildAskRouter()
	r.events <- session.Event{Type: session.EvTurnStart}
	child := &Run{asks: newAskRegistry()}
	ask := session.PendingAsk{AskID: "child-ask", Tool: "Shell", Origin: session.ApprovalOriginPermission}
	child.asks.registerAsk(ask)
	r.childAsks.registerSurfaced(ask, child, 1)
	if got := r.ResolveOrdinaryAsk(ask.AskID, session.VerdictAllowOnce); got != AskResolutionResolved {
		t.Fatalf("resolved ask = %v", got)
	}
	drainDone := make(chan struct{})
	go func() {
		(&Engine{}).drainChildren(context.Background(), r)
		close(drainDone)
	}()
	deadline := time.After(10 * time.Second)
	for {
		r.childAsks.mu.Lock()
		closed := r.childAsks.closed
		r.childAsks.mu.Unlock()
		if closed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("terminal router never closed")
		default:
			runtime.Gosched()
		}
	}
	sealDone := make(chan struct{})
	go func() {
		r.children.seal()
		close(sealDone)
	}()
	sealBlocked := false
	select {
	case <-sealDone:
	case <-time.After(time.Second):
		sealBlocked = true
	}
	<-r.events // consumer makes room after seal was attempted
	select {
	case ev := <-r.events:
		if ev.Type != session.EvApproval || ev.Approval == nil || ev.Approval.AskID != ask.AskID {
			t.Fatalf("final event = %+v, want approval", ev)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("final approval did not reach parent stream")
	}
	select {
	case <-drainDone:
	case <-time.After(10 * time.Second):
		t.Fatal("terminal drain did not finish")
	}
	select {
	case <-sealDone:
	case <-time.After(10 * time.Second):
		t.Fatal("child registry did not seal")
	}
	if sealBlocked {
		t.Fatal("final approval held emitMu while parent stream was full")
	}
}

// setDrainCaps shortens the run-end drain phases for a test and restores the
// real values on cleanup. The caps are vars ONLY as this test seam; the drain
// tests are not parallel, so the global write is safe.
func setDrainCaps(t *testing.T, phase1, grace time.Duration) {
	t.Helper()
	oldCap, oldGrace := childDrainCap, childDrainGrace
	childDrainCap, childDrainGrace = phase1, grace
	t.Cleanup(func() { childDrainCap, childDrainGrace = oldCap, oldGrace })
}

// newDrainRun builds the minimal Run shape drainChildren operates on: a real
// events channel, a registry with the PRODUCTION emit binding (Run.emitOrAbort
// over the registry's seal-abort channel), and a capturing diagnostics sink.
func newDrainRun(eventsCap int) (*Run, *internalCapturingDiag) {
	diag := newInternalCapturingDiag()
	r := &Run{
		events:   make(chan session.Event, eventsCap),
		ctx:      context.Background(),
		diag:     diag,
		children: newChildRunRegistry(),
	}
	r.children.emit = func(ev session.Event) { r.emitOrAbort(ev, r.children.emitAbort) }
	return r, diag
}

// TestDrainAcceptedAbandonedChildApprovalBeforeParentResult pins the terminal
// fallback: even a background child that misses the bounded join cannot leave
// an accepted ask unanswered in the parent log. The sweep emits its approval
// before seal and therefore before the caller's parent EvResult.
func TestDrainAcceptedAbandonedChildApprovalBeforeParentResult(t *testing.T) {
	setDrainCaps(t, 5*time.Millisecond, 5*time.Millisecond)
	r, _ := newDrainRun(4)
	sink := &recordingSink{}
	e := &Engine{deps: Deps{Sink: sink}}
	r.runID = "parent-run"
	r.childAsks = newChildAskRouter()
	r.children.unregisterAsk = r.unregisterChildAsk
	child := &Run{asks: newAskRegistry()}
	ask := session.PendingAsk{AskID: "child-ask", Tool: "Shell", Call: "collision", Origin: session.ApprovalOriginPermission}
	child.asks.registerAsk(ask)
	r.childAsks.registerSurfaced(ask, child, 3)
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.children.register("background", childFamilySubagent, "task", cancel, true)
	r.children.markRunning("background")
	r.children.recordAsk("background", ask.AskID)
	if got := r.ResolveOrdinaryAsk(ask.AskID, session.VerdictDeny); got != AskResolutionResolved {
		t.Fatalf("resolved ask = %v", got)
	}
	e.drainChildren(context.Background(), r)
	e.emit(r, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopCancelled}})
	first, second := <-r.events, <-r.events
	if first.Type != session.EvApproval || first.RunID != r.runID || first.Approval == nil ||
		first.Approval.AskID != ask.AskID || first.Approval.Call != "" || second.Type != session.EvResult ||
		first.Seq >= second.Seq {
		t.Fatalf("terminal order = %+v, %+v; want parent approval before result", first, second)
	}
	if queued := r.childAsks.takeAccepted(child); len(queued) != 0 {
		t.Fatalf("accepted approvals left after pre-seal sweep: %+v", queued)
	}
	assertApprovalSinkParity(t, sink.snapshotEvents(), first, second)
}

func TestChildDrainApprovalSinkMatchesParentStream(t *testing.T) {
	r, _ := newDrainRun(2)
	r.runID = "parent-run"
	r.childAsks = newChildAskRouter()
	sink := &recordingSink{}
	e := &Engine{deps: Deps{Sink: sink}}
	child := &Run{asks: newAskRegistry()}
	ask := session.PendingAsk{AskID: "child-ask", Tool: "Shell", Origin: session.ApprovalOriginPermission}
	child.asks.registerAsk(ask)
	r.childAsks.registerSurfaced(ask, child, 2)
	if got := r.ResolveOrdinaryAsk(ask.AskID, session.VerdictAllowOnce); got != AskResolutionResolved {
		t.Fatalf("resolved ask = %v", got)
	}
	caps := e.parentCaps(r, nil, 2)
	caps.emitChildApprovals(child)
	approval := <-r.events
	if approval.Type != session.EvApproval || approval.Approval == nil || approval.Approval.AskID != ask.AskID {
		t.Fatalf("parent stream approval = %+v", approval)
	}
	assertApprovalSinkParity(t, sink.snapshotEvents(), approval)
}

func assertApprovalSinkParity(t *testing.T, mirrored []session.Event, streamed ...session.Event) {
	t.Helper()
	if !reflect.DeepEqual(mirrored, streamed) {
		t.Fatalf("sink events = %+v, stream events = %+v", mirrored, streamed)
	}
}

func TestSealedChildDrainDoesNotConsumeAcceptedApproval(t *testing.T) {
	r, _ := newDrainRun(1)
	r.childAsks = newChildAskRouter()
	child := &Run{asks: newAskRegistry()}
	ask := session.PendingAsk{AskID: "child-ask", Tool: "Shell", Origin: session.ApprovalOriginPermission}
	child.asks.registerAsk(ask)
	r.childAsks.registerSurfaced(ask, child, 1)
	if got := r.ResolveOrdinaryAsk(ask.AskID, session.VerdictAllowOnce); got != AskResolutionResolved {
		t.Fatalf("resolved ask = %v", got)
	}
	r.children.seal()
	r.children.emitAccepted(
		func() []session.Event { return r.childAsks.takeAccepted(child) },
		func(session.Event) bool { return true },
		func(events []session.Event) { r.childAsks.requeueAccepted(child, events) },
	)
	if queued := r.childAsks.takeAccepted(child); len(queued) != 1 || queued[0].Approval == nil || queued[0].Approval.AskID != ask.AskID {
		t.Fatalf("post-seal drain consumed accepted approval: %+v", queued)
	}
}

func TestDrainClosesLateSurfacedChildAskBeforeParentResult(t *testing.T) {
	r, _ := newDrainRun(3)
	r.asks = newAskRegistry()
	r.childAsks = newChildAskRouter()
	child := &Run{asks: newAskRegistry()}
	ask := session.PendingAsk{AskID: "late-child-ask", Tool: "Shell", Origin: session.ApprovalOriginPermission}
	child.asks.registerAsk(ask)
	r.childAsks.registerSurfaced(ask, child, 2)
	(&Engine{}).drainChildren(context.Background(), r)
	r.emit(session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopCancelled}})
	first, second := <-r.events, <-r.events
	if first.Type != session.EvPermissionRetract || first.Ask == nil || first.Ask.AskID != ask.AskID ||
		second.Type != session.EvResult || first.Seq >= second.Seq {
		t.Fatalf("terminal events = %+v, %+v; want retract before result", first, second)
	}
	if got := r.ResolveOrdinaryAsk(ask.AskID, session.VerdictAllowOnce); got != AskResolutionNotPending {
		t.Fatalf("post-terminal verdict = %v, want not pending", got)
	}
}

func TestFinalChildApprovalWaitsForDrainingParentStream(t *testing.T) {
	r, _ := newDrainRun(1)
	r.childAsks = newChildAskRouter()
	r.events <- session.Event{Type: session.EvTurnStart} // transiently full
	child := &Run{asks: newAskRegistry()}
	ask := session.PendingAsk{AskID: "child-ask", Tool: "Shell", Origin: session.ApprovalOriginPermission}
	child.asks.registerAsk(ask)
	r.childAsks.registerSurfaced(ask, child, 1)
	if got := r.ResolveOrdinaryAsk(ask.AskID, session.VerdictAllowOnce); got != AskResolutionResolved {
		t.Fatalf("resolved ask = %v", got)
	}
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		for _, ev := range r.children.sealWithFinal(r.childAsks.closeEvents) {
			close(entered)
			r.emit(ev)
		}
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("terminal sweep did not reach final emitter")
	}
	<-r.events // release backpressure
	select {
	case ev := <-r.events:
		if ev.Type != session.EvApproval || ev.Approval == nil || ev.Approval.AskID != ask.AskID {
			t.Fatalf("delivered final event = %+v, want approval", ev)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("final approval did not reach draining parent stream")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("terminal sweep did not finish after approval delivery")
	}
}

// warnsOf filters the captured records down to LevelWarn.
func warnsOf(diag *internalCapturingDiag) []internalDiagRecord {
	var warns []internalDiagRecord
	for _, rec := range diag.snapshot() {
		if rec.level == port.LevelWarn {
			warns = append(warns, rec)
		}
	}
	return warns
}

// TestDrainTwoPhaseJoinsEmitParkedChild is the architect's two-phase test: a
// child blocked in its own end-emit (full events channel, NON-draining consumer
// — the run's OWN backpressure) cannot reach markDone until emits abort. With a
// short phase-1 cap, drainChildren must NOT misattribute that backpressure as a
// wedged child: phase 2's abortEmits unblocks the send, the child reaches its
// terminal within the grace, it JOINS, and NO abandon WARN fires.
func TestDrainTwoPhaseJoinsEmitParkedChild(t *testing.T) {
	setDrainCaps(t, 100*time.Millisecond, 2*time.Second)
	r, diag := newDrainRun(1)
	r.events <- session.Event{} // buffer FULL; nobody drains — the wedged-consumer shape

	// Re-bind the emit with an entered-signal so the test can deterministically
	// wait until the child goroutine is genuinely parked INSIDE the guarded send
	// (emitMu held, send blocked) before draining.
	entered := make(chan struct{})
	var once sync.Once
	inner := r.children.emit
	r.children.emit = func(ev session.Event) {
		once.Do(func() { close(entered) })
		inner(ev)
	}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.children.register("subagent-bg", childFamilySubagent, "g", cancel, true)
	r.children.markRunning("subagent-bg")

	go func() {
		// The emit-blocked child: its end event parks on the full channel; only
		// after the send aborts can it land its terminal.
		r.children.safeEmit(session.Event{Type: session.EvSubagentEnd,
			Subagent: &session.SubagentPayload{ChildID: "subagent-bg", Stop: session.StopCancelled}})
		r.children.markDone("subagent-bg", session.StopCancelled)
	}()
	<-entered

	done := make(chan struct{})
	go func() {
		(&Engine{}).drainChildren(context.Background(), r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("drainChildren wedged")
	}

	if _, st, outcome := r.children.collect("subagent-bg"); outcome == collectUnknown || st.state != childDone {
		t.Fatalf("the emit-parked child must have JOINED once emits aborted, got %v (%+v)", outcome, st)
	}
	if warns := warnsOf(diag); len(warns) != 0 {
		t.Fatalf("consumer backpressure must NOT be misattributed as a wedged child; got WARNs %+v", warns)
	}
}

// TestDrainTwoPhaseJoinsRetractParkedChild pins the NEW park point the
// chokepoint introduced: markDoneResult's retract emit sits BEFORE the doneCh
// close, so against a non-draining consumer the child's terminal can park
// INSIDE the retract send — phase 2's abortEmits must unpark it so the child
// reaches its done-transition, JOINS within the grace, and is never flipped to
// abandoned (no WARN). Mirrors TestDrainTwoPhaseJoinsEmitParkedChild with the
// park moved from the end-emit to the recorded ask's retract.
func TestDrainTwoPhaseJoinsRetractParkedChild(t *testing.T) {
	setDrainCaps(t, 100*time.Millisecond, 2*time.Second)
	r, diag := newDrainRun(1)
	r.events <- session.Event{} // buffer FULL; nobody drains — the wedged-consumer shape
	const askID = "subagent-bg:0:k1:r1"
	router := newChildAskRouter()
	r.childAsks = router
	r.children.unregisterAsk = r.unregisterChildAsk

	// Entered-signal rebind so the test deterministically waits until the
	// terminal goroutine is parked INSIDE the guarded retract send.
	entered := make(chan struct{})
	var once sync.Once
	inner := r.children.emit
	r.children.emit = func(ev session.Event) {
		once.Do(func() { close(entered) })
		inner(ev)
	}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.children.register("subagent-bg", childFamilySubagent, "g", cancel, true)
	r.children.markRunning("subagent-bg")
	r.children.recordAsk("subagent-bg", askID)
	router.registerChild(askID, &Run{asks: newAskRegistry()})

	go func() {
		// The retract-parked terminal: the ask's retract parks on the full
		// channel BEFORE doneCh closes; only after emits abort can the done
		// transition land.
		r.children.markDoneResult("subagent-bg", session.StopCancelled, nil)
	}()
	<-entered

	done := make(chan struct{})
	go func() {
		(&Engine{}).drainChildren(context.Background(), r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("drainChildren wedged")
	}

	if _, st, outcome := r.children.collect("subagent-bg"); outcome == collectUnknown || st.state != childDone {
		t.Fatalf("the retract-parked child must have JOINED once emits aborted, got %v (%+v)", outcome, st)
	}
	if warns := warnsOf(diag); len(warns) != 0 {
		t.Fatalf("a retract parked on consumer backpressure must NOT be misattributed as a wedged child; got WARNs %+v", warns)
	}
	// The dropped retract leaves the router entry behind only if unregister never
	// ran — it DID run (unregister-before-emit), so the entry is gone even though
	// the event itself was aborted: the stale-modal residual is the consumer's
	// own wedge, not a routing leak.
	router.mu.Lock()
	_, present := router.byAskID[askID]
	router.mu.Unlock()
	if present {
		t.Fatalf("the parked retract's askID must still have been unregistered before the emit")
	}
}

// TestDrainAbandonsGenuinelyWedgedChildWithOneWarn pins the abandon path: a
// child whose doneCh NEVER closes (even after emits abort) is abandoned with
// exactly ONE LevelWarn whose attrs carry the ids ONLY (the child's goal label
// must not leak — A9), and a residual post-seal emit against the CLOSED events
// channel is a safe no-op, never a panic.
func TestDrainAbandonsGenuinelyWedgedChildWithOneWarn(t *testing.T) {
	setDrainCaps(t, 50*time.Millisecond, 50*time.Millisecond)
	r, diag := newDrainRun(8)

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	const secretGoal = "label-that-must-not-leak"
	r.children.register("subagent-wedged", childFamilySubagent, secretGoal, cancel, true)
	r.children.markRunning("subagent-wedged")
	// No goroutine: the doneCh genuinely never closes.

	(&Engine{}).drainChildren(context.Background(), r)

	warns := warnsOf(diag)
	if len(warns) != 1 {
		t.Fatalf("a genuinely wedged child must produce exactly ONE abandon WARN, got %d (%+v)", len(warns), warns)
	}
	if ids, _ := warns[0].attrs["ids"].(string); ids != "subagent-wedged" {
		t.Fatalf("the WARN must carry the abandoned ids, got attrs %+v", warns[0].attrs)
	}
	if strings.Contains(warns[0].msg, secretGoal) {
		t.Fatalf("the WARN must carry ids only — the goal label leaked into the message")
	}
	for k, v := range warns[0].attrs {
		if s, ok := v.(string); ok && strings.Contains(s, secretGoal) {
			t.Fatalf("the WARN must carry ids only — the goal label leaked into attr %q", k)
		}
	}

	// Run-teardown shape: the channel closes after seal; the abandoned child's
	// residual emit must be a silent no-op.
	close(r.events)
	r.children.safeEmit(session.Event{Type: session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{ChildID: "subagent-wedged"}})
}

// drainEventsOf empties the run's buffered events channel non-blockingly and
// returns what was delivered (the drain tests have no live consumer).
func drainEventsOf(r *Run) []session.Event {
	var evs []session.Event
	for {
		select {
		case ev := <-r.events:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

// TestDrainAbandonedChildAskRetractedPreSeal pins the drain's abandoned-only ask
// sweep: a genuinely WEDGED child (doneCh never closes) never reaches its
// registry terminal, so the chokepoint retraction cannot fire — drainChildren
// must sweep its still-pending surfaced ask itself, BEFORE the seal: the
// permission.retract is delivered (a post-seal emit would have been dropped),
// the router entry is gone, the abandon WARN stays exactly ONE line, and the
// abandoned child's eventual LATE markDoneResult emits nothing (its ask set was
// taken and the registry is sealed).
func TestDrainAbandonedChildAskRetractedPreSeal(t *testing.T) {
	setDrainCaps(t, 50*time.Millisecond, 50*time.Millisecond)
	r, diag := newDrainRun(8)
	const askID = "subagent-wedged:0:k1:r1"
	router := newChildAskRouter()
	r.childAsks = router
	r.children.unregisterAsk = r.unregisterChildAsk

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.children.register("subagent-wedged", childFamilySubagent, "g", cancel, true)
	r.children.markRunning("subagent-wedged")
	r.children.recordAsk("subagent-wedged", askID)
	router.registerChild(askID, &Run{asks: newAskRegistry()})
	// No goroutine: the doneCh genuinely never closes — the child is abandoned.

	(&Engine{}).drainChildren(context.Background(), r)

	if warns := warnsOf(diag); len(warns) != 1 {
		t.Fatalf("the abandoned child must still produce exactly ONE abandon WARN, got %d (%+v)", len(warns), warns)
	}
	var retracts []string
	for _, ev := range drainEventsOf(r) {
		if ev.Type == session.EvPermissionRetract && ev.Ask != nil {
			retracts = append(retracts, ev.Ask.AskID)
		}
	}
	if len(retracts) != 1 || retracts[0] != askID {
		t.Fatalf("the abandoned child's parked ask must be retracted pre-seal exactly once, got %v (want [%s])", retracts, askID)
	}
	router.mu.Lock()
	_, present := router.byAskID[askID]
	router.mu.Unlock()
	if present {
		t.Fatalf("the swept ask must be unregistered from the router")
	}

	// The abandoned child's LATE terminal: the ask set was already taken and the
	// registry is sealed — no retract, no panic, nothing on the channel.
	r.children.markDoneResult("subagent-wedged", session.StopCancelled, nil)
	if late := drainEventsOf(r); len(late) != 0 {
		t.Fatalf("the late markDoneResult must emit nothing post-seal, got %+v", late)
	}
}

// TestDrainCleanNoWarn pins the healthy path: children already at their terminal
// drain instantly — no WARN, registry sealed.
func TestDrainCleanNoWarn(t *testing.T) {
	r, diag := newDrainRun(8)
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.children.register("subagent-done", childFamilySubagent, "g", cancel, true)
	body := session.NewToolResult("p1", "agentId: subagent-done\n\nok")
	r.children.markDoneResult("subagent-done", session.StopEndTurn, &body)

	(&Engine{}).drainChildren(context.Background(), r)

	if warns := warnsOf(diag); len(warns) != 0 {
		t.Fatalf("a clean drain must emit NO warn, got %+v", warns)
	}
	r.children.emitMu.Lock()
	sealed := r.children.sealed
	r.children.emitMu.Unlock()
	if !sealed {
		t.Fatalf("drainChildren must seal the registry")
	}
}

// TestClampPreviewFastPathZeroAlloc verifies the fast-path is zero-alloc AND
// correct: a clean short ASCII string is returned verbatim with no allocation,
// while control bytes, UTF-8 multi-byte sequences, and long strings fall through
// to the slow path and are scrubbed/capped correctly. This is the regression guard
// for the inlining/escape bug: if isCleanASCII stops being inlined or the gc
// starts escaping the result, this benchmark assertion catches it.
func TestClampPreviewFastPathZeroAlloc(t *testing.T) {
	// Fast path — zero allocs, returns input.
	clean := "Background check complete: slice 7 is intact."
	allocTest := func(input string) float64 {
		res := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = clampPreview(input)
			}
		})
		return float64(res.AllocsPerOp())
	}
	if allocs := allocTest(clean); allocs != 0.0 {
		t.Fatalf("clampPreview on a clean short ASCII string allocated %.0f alloc/op; want 0 — isCleanASCII not inlined or result escapes", allocs)
	}
	if got := clampPreview(clean); got != clean {
		t.Fatalf("clampPreview on clean returned %q, want input verbatim", got)
	}

	// Dirty — control byte scrubbed.
	dirty := "Back\x1bground"
	if got := clampPreview(dirty); got != "Back ground" {
		t.Fatalf("clampPreview on dirty returned %q, want 'Back ground'", got)
	}

	// Multi-byte UTF-8 — falls to slow path, returned correctly.
	unicode := "café résumé"
	if got := clampPreview(unicode); got != unicode {
		t.Fatalf("clampPreview on multi-byte UTF-8 returned %q, want verbatim %q", got, unicode)
	}

	// Overlong — clamped.
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	if got := clampPreview(long); len(got) != maxTeamPreview+3 { // 200 x + "…" (3 bytes)
		t.Fatalf("clampPreview on 300-byte string returned len %d (%q), want len %d", len(got), got, maxTeamPreview+3)
	}

	// Empty — fast path returns empty verbatim.
	if got := clampPreview(""); got != "" {
		t.Fatalf("clampPreview on empty returned %q, want empty", got)
	}
}

// TestIsCleanASCIICorrectness verifies the byte-level sentinel against the rune-aware
// slow path: for every input the sentinel reports, clampPreview MUST return the input
// verbatim (unchanged), so the two code paths produce the same output.
func TestIsCleanASCIICorrectness(t *testing.T) {
	// Build a long-ish ASCII string — the exact kind a child tool call or message
	// delta carries.
	ascii := "find the bug in src/parser/handler.go -- line 42 is the suspect"
	if !isCleanASCII(ascii) {
		t.Fatalf("isCleanASCII false on pure ASCII under cap")
	}

	// Verify product equivalence: the sentinel always precedes the slow path in
	// clampPreview, and clampPreview returns the input verbatim when isCleanASCII is
	// true. So the slow-path never runs against these inputs — we assert that the
	// result matches what the slow path WOULD have produced on the same input.
	if got := clampPreview(ascii); got != ascii {
		t.Fatalf("clampPreview fast path changed input: got %q, want %q", got, ascii)
	}

	// False: control byte.
	if isCleanASCII("a\x1bb") {
		t.Fatal("isCleanASCII true on control byte")
	}

	// False: over cap.
	long := ""
	for i := 0; i < 250; i++ {
		long += "x"
	}
	if isCleanASCII(long) {
		t.Fatal("isCleanASCII true on over-cap string")
	}

	// False: non-ASCII (multi-byte UTF-8 bytes > 0x7e).
	if isCleanASCII("café") {
		t.Fatal("isCleanASCII true on multi-byte UTF-8")
	}
}
