package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// countingEchoTool is a trivial read-only member tool that counts executions and
// closes reached once the threshold is hit — the volume probe the stalled-team
// test sequences on (every execution implies ~4 member events headed at the
// parent stream).
type countingEchoTool struct {
	count     atomic.Int64
	threshold int64
	reached   chan struct{}
	once      sync.Once
}

func newCountingEchoTool(threshold int64) *countingEchoTool {
	return &countingEchoTool{threshold: threshold, reached: make(chan struct{})}
}

func (*countingEchoTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Echo", Description: "returns ok", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*countingEchoTool) ReadOnly() bool { return true }
func (e *countingEchoTool) Execute(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	if e.count.Add(1) >= e.threshold {
		e.once.Do(func() { close(e.reached) })
	}
	return session.NewToolResult(in.ID, "ok"), nil
}

// echoTurns scripts n Echo tool-call turns followed by a closing text turn.
func echoTurns(prefix string, n int) []mockllm.Turn {
	turns := make([]mockllm.Turn, 0, n+1)
	for i := 0; i < n; i++ {
		turns = append(turns, mockllm.ToolCallTurn(
			session.NewToolCall(session.ToolCallID(fmt.Sprintf("%s-%d", prefix, i)), "Echo", json.RawMessage(`{}`))))
	}
	return append(turns, mockllm.TextTurn(prefix+" done"))
}

// setHardAbortGrace shortens the Cancel→hardAbort grace for a test and restores
// the real value on cleanup — the same test-seam pattern as setDrainCaps over
// childDrainCap/childDrainGrace (these tests are not parallel, so the global
// write is safe).
func setHardAbortGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := hardAbortGrace
	hardAbortGrace = d
	t.Cleanup(func() { hardAbortGrace = old })
}

// waitForCondition is a bounded condition wait (an exact state predicate over
// atomics/channel lengths, not a sleep-as-sync): it polls cond until true or
// fails the test at the deadline.
func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

// TestCancelUnwedgesStalledTeamRun reproduces the wedged-pipeline shape: a team
// run whose CONSUMER stops draining after the first team event. The member
// drives keep producing events; the chain of blocking sends fills up
// (parent events channel → forwarder parked in safeEmit/emitOrAbort → evCh full
// → member goroutines parked at the evCh send). The seal/abort escape lives in
// the terminate paths, which are themselves unreachable while dispatch is
// blocked inside the Team tool — so without an explicit unwedge signal,
// Run.Cancel (a bare ctx cancel) can never terminate the run.
//
// The test synchronizes (channel signals + exact state predicates) until the
// forwarder is GENUINELY parked inside the guarded send and the members have
// produced strictly more events than the pipeline can hold, then calls
// run.Cancel() and asserts the run terminates (drive returns, the session lands
// cancelled — Interrupt-recoverable, identical to a healthy esc-cancel) within
// a watchdog bound.
//
// Verified failing at HEAD before the hardAbort fix landed (the failure-first
// proof): against the pre-fix code this test died on the watchdog with
// "run remained WEDGED after Cancel: drive never returned" — Cancel was a bare
// ctx cancel, which no parked send watched.
func TestCancelUnwedgesStalledTeamRun(t *testing.T) {
	setHardAbortGrace(t, 20*time.Millisecond)
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	// Two workers x 10 Echo turns ≈ 80+ member events — strictly more than the
	// small parent buffer + the supervisor's evCh (64) can hold, so the member
	// goroutines park at the evCh send once the forwarder is parked. Threshold 20
	// = all worker Echo calls executed (the engines themselves never park: each
	// worker's own run buffer holds its ~45 events).
	echo := newCountingEchoTool(20)
	providers := map[string]*mockllm.Provider{
		"lead": mockllm.New(
			mockllm.TextTurn("lead coordinating"),
			mockllm.TextTurn("CONSOLIDATED: stalled-run report"),
		),
		"w1": mockllm.New(echoTurns("w1", 10)...),
		"w2": mockllm.New(echoTurns("w2", 10)...),
	}
	factory := func(tm *team.Team, spec MemberSpec, _ string) MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(echo)
		return MemberBuild{Engine: NewEngine(Deps{LLM: prov, Catalog: cat, Policy: allow, Model: "member-model"})}
	}

	parentCat := tool.NewCatalog()
	parentCat.MustRegister(NewTeamTool(factory))
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Team", json.RawMessage(
			`{"goal":"investigate","members":[{"name":"lead","role":"coordinate"},{"name":"w1","role":"work"},{"name":"w2","role":"work"}]}`))),
		mockllm.TextTurn("parent done"),
	)
	e := NewEngine(Deps{LLM: parentLLM, Catalog: parentCat, Policy: allow, Model: "parent-model"})
	sess := session.New("wedge-parent", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	ws := memfs.NewWorkspace("/ws")

	// Construct the Run by hand (mirroring Engine.Run) so the events channel
	// gets a SMALL test cap — the wedge would otherwise need 64+ buffered parent
	// events to manifest. Keep this in sync with Engine.Run.
	ctx, cancel := context.WithCancel(context.Background())
	r := &Run{
		events:    make(chan session.Event, 4),
		asks:      newAskRegistry(),
		cancel:    cancel,
		ctx:       ctx,
		hardAbort: make(chan struct{}),
		serial:    runSerial.Add(1),
		diag:      e.bindRunDiag(sess.ID),
	}
	r.children = newChildRunRegistry()
	// The production emit binding, instrumented: entered>exited while the events
	// buffer is full means the forwarder is genuinely parked inside the guarded
	// send (it cannot deliver into a full channel nobody drains).
	var entered, exited atomic.Int64
	r.children.emit = func(ev session.Event) {
		entered.Add(1)
		r.emitOrAbort(ev, r.children.emitAbort)
		exited.Add(1)
	}

	driveDone := make(chan struct{})
	go func() {
		// Mirror Engine.Run's goroutine: cancel, seal, close(events) — LIFO —
		// then signal the test.
		defer close(driveDone)
		defer close(r.events)
		defer r.children.seal()
		defer cancel()
		e.drive(ctx, r, sess, ws, "investigate", nil)
	}()

	// The consumer drains until the first team event, then STOPS FOREVER — the
	// dead-client shape (a relay whose Send failed and that never reads again).
	stalled := make(chan struct{})
	go func() {
		for ev := range r.events {
			if ev.Type == session.EvTeamStart {
				close(stalled)
				return
			}
		}
	}()

	<-stalled
	// All worker Echo calls executed: the members produced their full event
	// volume, strictly more than the pipeline holds.
	select {
	case <-echo.reached:
	case <-time.After(10 * time.Second):
		t.Fatalf("workers never produced their event volume (echo executions = %d)", echo.count.Load())
	}
	// The forwarder is genuinely parked inside the guarded send: buffer full,
	// one entry not exited.
	waitForCondition(t, "forwarder parked in the guarded send", func() bool {
		return len(r.events) == cap(r.events) && entered.Load() > exited.Load()
	})

	r.Cancel()

	select {
	case <-driveDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("run remained WEDGED after Cancel: drive never returned (consumer dead, sends parked)")
	}
	if sess.State != session.StateCancelled {
		t.Fatalf("after cancel state = %q, want %q (Interrupt-recoverable)", sess.State, session.StateCancelled)
	}
}

// TestCancelAbortNoChildLeakAfterSeal pins the sticky all-or-nothing semantics of
// the hardAbort unwedge: once Cancel fires it, a child emit PARKED behind a dead
// consumer gives up UNDELIVERED (no late child event sneaks onto the parent
// stream), a SECOND would-park emit ALSO gives up promptly (the closed channel
// is a per-run signal, never a one-shot token), and after the registry seals a
// residual child emit is a silent no-op — never a delivery, never a panic.
func TestCancelAbortNoChildLeakAfterSeal(t *testing.T) {
	setHardAbortGrace(t, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &Run{
		events:    make(chan session.Event, 1),
		asks:      newAskRegistry(),
		cancel:    cancel,
		ctx:       ctx,
		hardAbort: make(chan struct{}),
	}
	r.children = newChildRunRegistry()
	delivered := make(chan bool, 8)
	entered := make(chan struct{}, 8)
	r.children.emit = func(ev session.Event) {
		entered <- struct{}{}
		delivered <- r.emitOrAbort(ev, r.children.emitAbort)
	}

	// Fill the buffer; nobody drains — the dead-consumer shape.
	r.events <- session.Event{Type: session.EvTurnStart}

	go r.children.safeEmit(session.Event{Type: session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{ChildID: "subagent-x", Stop: session.StopCancelled}})
	<-entered // the child emit is inside the guarded send (buffer full → it parks)

	r.Cancel()

	select {
	case ok := <-delivered:
		if ok {
			t.Fatalf("a child emit parked behind a dead consumer must give up UNDELIVERED after Cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the parked child emit never unwound after Cancel (hardAbort did not fire)")
	}
	if len(r.events) != 1 {
		t.Fatalf("no child event may reach the parent stream after the abort fired; buffered = %d, want 1", len(r.events))
	}

	// Stickiness (mutation finding M8): a SECOND would-park emit after the first
	// gave up must ALSO give up promptly — hardAbort is a closed channel every
	// later send selects on, not a one-shot token consumed by the first unwind.
	go r.children.safeEmit(session.Event{Type: session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{ChildID: "subagent-y", Stop: session.StopCancelled}})
	<-entered
	select {
	case ok := <-delivered:
		if ok {
			t.Fatalf("a second would-park emit after hardAbort must give up UNDELIVERED")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the second would-park emit never gave up: hardAbort must be sticky, not one-shot")
	}
	if len(r.events) != 1 {
		t.Fatalf("the second post-abort emit must deliver nothing; buffered = %d, want 1", len(r.events))
	}

	// After seal, a residual child emit is a silent no-op: nothing enters the
	// guarded send at all (sealed-check first), nothing is delivered.
	r.children.seal()
	r.children.safeEmit(session.Event{Type: session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{ChildID: "subagent-x", Stop: session.StopCancelled}})
	select {
	case <-entered:
		t.Fatalf("a post-seal child emit must not enter the guarded send")
	default:
	}
	if len(r.events) != 1 {
		t.Fatalf("a post-seal child emit must deliver nothing; buffered = %d, want 1", len(r.events))
	}
	// Drain the pre-abort event so nothing lingers (and prove it is the original).
	if ev := <-r.events; ev.Type != session.EvTurnStart {
		t.Fatalf("buffered event = %q, want the pre-abort %q", ev.Type, session.EvTurnStart)
	}
}

// TestEmitDeliversWithBufferRoomAfterHardAbort pins the try-send-first shape of
// emit AND emitOrAbort (mutation finding M5/M5b — nothing else enforces it):
// with hardAbort ALREADY fired and room in the buffer — the cancelled-but-
// DRAINED consumer shape — EVERY emit must deliver. This is the executable form
// of the "a cancelled run with a draining consumer still delivers in-flight
// events (the terminal StopCancelled EvResult included)" rationale the emit
// comments cite: a plain two-arm blocking select picks RANDOMLY between the
// ready send and the closed abort channel, dropping each event with ~50%
// probability — the odds of such a regression surviving these 200 sends are
// ~2^-200.
func TestEmitDeliversWithBufferRoomAfterHardAbort(t *testing.T) {
	const n = 100
	r := &Run{
		events:    make(chan session.Event, 2*n),
		hardAbort: make(chan struct{}),
	}
	close(r.hardAbort) // Cancel fired and the grace elapsed

	for i := 0; i < n; i++ {
		r.emit(session.Event{Type: session.EvMessageDelta})
	}
	if got := len(r.events); got != n {
		t.Fatalf("emit delivered %d/%d events with buffer room after hardAbort (try-send-first regressed)", got, n)
	}

	abort := make(chan struct{}) // the registry seal-abort, NOT fired
	for i := 0; i < n; i++ {
		if !r.emitOrAbort(session.Event{Type: session.EvSubagentTool}, abort) {
			t.Fatalf("emitOrAbort dropped event %d with buffer room after hardAbort (try-send-first regressed)", i)
		}
	}
	if got := len(r.events); got != 2*n {
		t.Fatalf("emitOrAbort delivered %d/%d events with buffer room after hardAbort", got-n, n)
	}

	// Seq stays monotonic across both delivery paths.
	var prev int64
	for i := 0; i < 2*n; i++ {
		ev := <-r.events
		if ev.Seq <= prev {
			t.Fatalf("Seq not monotonic: %d after %d", ev.Seq, prev)
		}
		prev = ev.Seq
	}
}

// signallingMemberStore wraps a SessionStore and closes saved the first time a
// session whose id matches is persisted — the race-free "this member's drive
// unwound" signal (runTurn persists the member right after driveOneTurn
// returns, on the member's own goroutine).
type signallingMemberStore struct {
	port.SessionStore
	match func(session.SessionID) bool
	saved chan struct{}
	once  sync.Once
}

func (s *signallingMemberStore) Save(ctx context.Context, sess *session.Session) error {
	err := s.SessionStore.Save(ctx, sess)
	if s.match(sess.ID) {
		s.once.Do(func() { close(s.saved) })
	}
	return err
}

// TestCancelMemberUnparksEvChSend pins the GUARDED member→evCh forward in
// driveOneTurn (mutation finding M3b — nothing else enforces it): with the
// supervisor's consumer (the sink) blocked forever — the stalled-consumer shape
// on the supervisor-direct/gRPC RunTeam path, where there is NO parent hardAbort
// (zero caps → nil channel) — the worker parks at the evCh send once evCh
// fills. CancelMember must still unwind that member's drive via the select's
// driveCtx.Done() arm, which is LOAD-BEARING for member liveness: the member
// run's OWN hardAbort is never armed (nobody calls Run.Cancel on a member run —
// per-member cancel is m.ctx). A bare `evCh <- te` send reverts this to a
// permanent park: the member never terminates and is never persisted.
func TestCancelMemberUnparksEvChSend(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	// The threshold is the kill arithmetic, not just a started signal: with the
	// sink parked after its FIRST event, the TOTAL evCh throughput available for
	// the whole run is ~65 events (evCh cap 64 + the one the sink holds), shared
	// with the lead's ~5. By 20 Echo executions the worker has produced ≥80
	// events (~4 per tool turn), i.e. STRICTLY more than can ever drain — so a
	// bare `evCh <- te` can never finish forwarding, cancelled or not, and only
	// the guarded select's driveCtx arm lets the drive unwind. (A lower threshold
	// lets the post-cancel residual fit in evCh and the mutant survive; 20 stays
	// safely below the ~31-execution park plateau, so it is always reached.)
	echo := newCountingEchoTool(20)
	providers := map[string]*mockllm.Provider{
		"lead":   mockllm.New(mockllm.TextTurn("lead briefed"), mockllm.TextTurn("CONSOLIDATED")),
		"worker": mockllm.New(echoTurns("worker", 40)...),
	}
	tm := team.New("evch-park")
	factory := func(spec MemberSpec, _ string) MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(echo)
		return MemberBuild{Engine: NewEngine(Deps{LLM: prov, Catalog: cat, Policy: allow, Model: "member-model"})}
	}
	store := &signallingMemberStore{
		SessionStore: memstore.New(),
		match:        func(id session.SessionID) bool { return strings.HasSuffix(string(id), "-worker") },
		saved:        make(chan struct{}),
	}
	sup := NewSupervisor(tm, memfs.NewWorkspace("/ws"), factory,
		WithTeamGoal("investigate"),
		// Raise the per-round limits so the worker's 40 Echo turns (~160 events)
		// strictly exceed the evCh(64)+forwarder(1) capacity and it genuinely parks.
		WithTeamLimits(session.Limits{MaxTurns: 45, MaxToolCalls: 200}),
		WithMemberStore(store),
	)
	ctx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := sup.AddMember(ctx, MemberSpec{Name: "lead", Lead: true, InitialPrompt: "coordinate"}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	if err := sup.AddMember(ctx, MemberSpec{Name: "worker", InitialPrompt: "work"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	// The sink parks the forwarder until teardown: evCh fills behind it and the
	// worker parks at the evCh send.
	unblock := make(chan struct{})
	sink := func(TeamEvent) { <-unblock }
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		sup.Run(ctx, sink)
	}()

	<-echo.reached // the worker is genuinely mid-drive

	if !sup.CancelMember("worker") {
		t.Fatalf("CancelMember must return true for an enrolled member")
	}
	// The unwound drive persists the member (runTurn → persistMember) — the
	// race-free unpark signal. With a bare evCh send this parks forever: the
	// forwarder is in the sink, not in emitOrAbort, so nothing ever drains evCh.
	select {
	case <-store.saved:
	case <-time.After(10 * time.Second):
		t.Fatalf("worker drive never unwound after CancelMember: the member is parked at the evCh send (guarded select regressed)")
	}

	// Teardown: stop scheduling and unblock the forwarder so Run drains and
	// returns (goleak-clean).
	cancelRun()
	close(unblock)
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("supervisor Run never returned after teardown")
	}
}
