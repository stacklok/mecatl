package agent

import (
	"context"
	"sort"
	"sync"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// childFamily discriminates which delegation family a registered child belongs
// to. The registry indexes all three families in ONE flat map keyed by the child
// SESSION id (the agentId trailer / overlay ChildID / store key — the single
// handle convention); the families' id prefixes ("subagent-"/"parallel-"/
// "team-") are disjoint by the existing convention, which the registry inherits
// rather than re-engineers.
type childFamily string

const (
	// childFamilySubagent is a flat-fleet Subagent child ("subagent-<callID>").
	childFamilySubagent childFamily = "subagent"
	// childFamilyParallelBranch is one Parallel fork-join branch
	// ("parallel-<callID>-<i>").
	childFamilyParallelBranch childFamily = "parallel-branch"
	// childFamilyTeamMember is one long-lived team member ("team-<teamID>-<member>",
	// MemberSessionID). Unlike the one-shot families its entry stays live ACROSS
	// rounds (idle-between-rounds is still cancellable) and is marked done when the
	// supervisor stops it or the team ends.
	//
	// CAUTION — for THIS family, done/doneCh means DE-SCHEDULED, not quiescent: a
	// budget-stopped LEAD is marked done by the supervisor's stop path yet is
	// deliberately driven ONE more time for the synthesis turn (its per-member ctx
	// is not cancelled on stop for exactly that reason — see Supervisor.runTurn /
	// cleanupAll). Any reader of done state (the SubagentStatus tool, a
	// background-team future) must NOT treat a team-member done entry as
	// fully-terminal; only the TEAM's own end (cleanupAll) is.
	childFamilyTeamMember childFamily = "team-member"
	// childFamilyShellCmd is a background-Shell job ("bashcmd-<callID>") — a
	// detached shell command, NOT a delegation family: no child session, no
	// engine, no observability events. It rides the registry only for its
	// run-scoped cancel-at-end drain, the background job-count gate, and the
	// notice/collect machinery; its result body is the retained output tail.
	childFamilyShellCmd childFamily = "bash-cmd"
)

// The delegation families' child-session id PREFIXES — the single exported
// source for the id-minting convention. Each constant is the literal prefix of
// a child SESSION id; the three are disjoint, and every consumer derives from
// them rather than re-spelling the literals: the minting sites
// (SubagentTool's default child prefix, ParallelTool's default branch prefix,
// MemberSessionID's team scheme), the InspectSubagent prefix gate (subagent- ∪
// parallel-, issue #30), and the composition layer's child-session retention GC
// (internal/app/childgc.go's childSessionPrefixes). A deployment overriding a
// prefix (WithChildSessionPrefix / WithParallelChildSessionPrefix /
// WithMemberSessionPrefix) departs from this convention and from everything
// keyed on it — see those options' docs.
const (
	// SubagentSessionPrefix prefixes flat-fleet Subagent children:
	// "subagent-<callID>" (SubagentTool's default; WithChildSessionPrefix
	// overrides the stem).
	SubagentSessionPrefix = "subagent-"
	// ParallelSessionPrefix prefixes Parallel fork-join branches:
	// "parallel-<callID>-<i>" (ParallelTool's default;
	// WithParallelChildSessionPrefix overrides the stem). Issue #30: the
	// InspectSubagent prefix gate ALSO consumes this prefix, so a persisted branch
	// (WithParallelStore) is loadable by its surfaced "branch id:".
	ParallelSessionPrefix = "parallel-"
	// TeamSessionPrefix prefixes team members: "team-<teamID>-<member>" —
	// MemberSessionID's scheme (memberSessionIDPrefix aliases this constant).
	TeamSessionPrefix = "team-"
	// ShellCmdJobPrefix prefixes background-Shell job ids: "bashcmd-<callID>".
	// Unlike the three delegation prefixes above it names NO session (the job
	// is a bare process, not a child loop), so the InspectSubagent prefix gate
	// and the child-session retention GC must NOT learn it — it exists only so
	// the job-id spelling has one source.
	ShellCmdJobPrefix = "bashcmd-"
)

// childState is a registered child's lifecycle position: queued (registered,
// possibly waiting on the concurrency gate), running (its drive has started; a
// team member stays running across rounds), or done (terminal; doneCh closed).
type childState int

const (
	childQueued childState = iota
	childRunning
	childDone
)

// String renders the state as the enum-ish label the SubagentStatus roster shows
// (ids and labels only — A9).
func (s childState) String() string {
	switch s {
	case childRunning:
		return "running"
	case childDone:
		return "done"
	default:
		return "queued"
	}
}

// childRunRegistry tracks every child run spawned under one parent Run, keyed by
// the child session id. It is the shared foundation for per-child cancel (cancel
// consumes cancel/clientCancelled/askIDs) and background subagents (background
// consumes state/result/delivered/doneCh and the seal). It is owned by the parent
// Run exactly as childAsks is, but created UNCONDITIONALLY (cancel arrives only on
// interactive surfaces, but background bookkeeping must work headless too, and
// the registry is a mutex + map — negligible).
//
// LOCKING: two mutexes for two concerns, never held together by the registry.
// `mu` guards the entries map (registration/markDone/recordAsk/requestCancel —
// always short, never across a channel send). `emitMu` guards the seal flag AND
// every guarded send (safeEmit) as ONE locked section (A4a: no TOCTOU between
// the sealed check and the send). The split means a guarded send that waits on
// the events channel can never wedge entry bookkeeping (markDone, a sibling's
// registration, a later drain) behind it.
//
// STATE VOCABULARY (A5 — the ghost-entry resolution). An entry describes a child
// that RAN, is running, or is queued-and-cancellable. Two deliberate rules keep
// the roster honest:
//
//   - PRE-START ABORT (remove): a child that NEVER started driving — a
//     post-registration validation/fork/session-build failure whose error already
//     returned inline to the model, the background fail-fast gate-full path, or a
//     team member torn down by an enrolment failure before any round drove it —
//     is REMOVED from the registry (the one exception to "entries are never
//     removed"). There is nothing to observe, collect, resume, or inspect, so a
//     done+StopNone (or fabricated StopEndTurn) entry would be a phantom in the
//     SubagentStatus roster. remove closes the entry's doneCh (a parked waiter
//     wakes) and only ever applies to a NOT-done entry — a pre-start abort by
//     definition precedes any real terminal.
//
//   - MEANINGFUL PRE-START TERMINAL (markDone): a child cancelled while QUEUED
//     (CancelChild / parent-ctx death during the gate wait) keeps a done entry
//     with StopCancelled — the cancellation is a real, attributable disposition,
//     not a phantom.
//
// Otherwise entries are never removed during the run — done entries are what
// SubagentStatus and background delivery read — and the whole map dies with the
// Run.
type childRunRegistry struct {
	// mu guards entries (and the per-entry fields) plus gen. Never held across emit.
	mu      sync.Mutex
	entries map[string]*childEntry
	// gen is the terminal-generation broadcast channel: closed and replaced (under
	// mu) on every markDone/remove, so an "any child" waiter (SubagentStatus with
	// wait_ms and no agent_id) can park on the CURRENT generation and wake when the
	// next terminal lands.
	gen chan struct{}

	// emitMu guards sealed + every guarded send in one locked section (A4a), so a
	// late emit can never race seal/close into a send-on-closed-channel panic.
	// seal() therefore also serialises against an in-flight emit. Distinct from
	// mu so a send waiting on the events channel never blocks entry bookkeeping.
	emitMu sync.Mutex
	// sealed is set (via seal) after the run-end drain and before the run's events
	// channel closes. Every later safeEmit is a silent no-op.
	sealed bool
	// sealOnce guards the emitAbort close (seal may be called twice: the loop's
	// pre-terminal drain hook AND the run goroutine's deferred belt).
	sealOnce sync.Once
	// emitAbort is closed at seal-INTENT, BEFORE emitMu is acquired, so a guarded
	// send blocked on a full events channel (a consumer that stopped draining)
	// aborts and releases emitMu rather than deadlocking seal. Run.emitOrAbort
	// selects on it alongside the send.
	emitAbort chan struct{}
	// emit publishes an event toward the parent Run's stream. Engine.Run binds
	// it to Run.emitOrAbort (+ the sink mirror): a BLOCKING send (loop-style
	// backpressure; a cancelled run's in-flight child events still reach the
	// draining consumer) that gives up only when emitAbort closes (seal), so a
	// wedged consumer cannot park a child goroutine (or readControl inside
	// CancelChild) past the run's own teardown. It is set once before the run
	// goroutine starts and only read after; emitMu guards each call. nil (tests)
	// silently drops.
	emit func(session.Event)
	// unregisterAsk is the ANSWERED-vs-PENDING gate for ask retraction: bound by
	// Engine.Run (alongside emit) to Run.unregisterChildAsk (the parent run's
	// childAskRouter.unregister), it reports whether the askID was still
	// registered (pending) and removed. route() deletes an answered ask's entry,
	// so false means the verdict already resolved the ask (or it never surfaced)
	// and retractAsks must NOT emit a permission.retract for it. Like emit it is
	// set once before the run goroutine starts and only read after. nil on an
	// unbound registry (unit tests) ⇒ retracts are skipped, never a panic. A
	// bound headless/child run has no router, so its gate returns false for every
	// id — correct: nothing was ever surfaced, so there is nothing to retract.
	unregisterAsk func(askID string) bool
	// liveness is the process-wide exclusion seam shared with Service/retention.
	// Only delegation families register; background Shell has no child session.
	liveness port.SessionLiveness
}

// childEntry is one registered child's control block.
type childEntry struct {
	family childFamily
	// goal is the clamped, plain-text label registered for overlay/status
	// rendering (never child content). The SubagentStatus ROSTER deliberately does
	// not render it (ids and enum labels only — A9).
	goal string
	// cancel is the per-CHILD context cancel covering the whole call (gate wait,
	// fork, drive, structured-output re-drives). Invoked OUTSIDE the registry lock.
	cancel context.CancelFunc
	// background marks a detached-delivery child: its tool call returned an
	// immediate "started" result and its rendered result is stored here at terminal
	// for SubagentStatus collection.
	background bool
	// clientCancelled is set by Run.CancelChild — it disambiguates the child's
	// StopCancelled terminal (cancelled BY THE USER vs parent-run cancel/timeout).
	clientCancelled bool
	// askIDs are the surfaced permission asks currently owned by this child,
	// recorded at the single surfacing seam (surfaceAsk → recordAsk). They are
	// SNAPSHOT (not cleared) by requestCancel — the eager CancelChild retract —
	// and snapshot+CLEARED (takeAsksLocked) by the child's registry terminal
	// (markDoneResult — the retraction chokepoint) and the drain's abandoned
	// sweep. Exactly-once across those attempts is enforced by the unregister
	// gate, consumed atomically with the emit (retractAsksVia), NOT by clearing:
	// the eager attempt can lose a scheduling race to the seal, and clearing
	// would strand the ask (zero retracts). Verdict routing leaves the set
	// slightly stale (an answered ask is not removed); harmless — retractAsks
	// gates each id on childAskRouter.unregister's answered-vs-pending bool, so a
	// stale already-answered id emits nothing.
	askIDs map[string]struct{}
	state  childState
	// releaseLiveness ends this attempt's process-wide maintenance exclusion.
	// It is idempotent and belongs to the entry pointer, so a same-id resume can
	// replace the map entry without releasing the new attempt from an old terminal.
	releaseLiveness func()
	// stop is the child's terminal stop reason (set by markDone).
	stop session.StopReason
	// displaced is the DONE entry this registration OVERWROTE (a `resume` of an
	// already-run id within the same run — register stashes it). If the new
	// attempt ABORTS pre-start (gate full, resume-load/fork failure), remove
	// REINSTATES it, so the prior terminal — and its possibly-undelivered
	// background result — survives a failed resume attempt instead of being
	// erased. It is cleared the moment the new attempt genuinely proceeds
	// (markRunning) or lands its own terminal (markDone): from then on the
	// overwrite is real and permanent.
	displaced *childEntry
	// result is a BACKGROUND child's rendered terminal ToolResult (the exact text
	// the foreground call would have returned, stored by markDoneResult), awaiting
	// collection via SubagentStatus — the SOLE body channel (A2). nil for
	// foreground children (their result returned inline).
	result *session.ToolResult
	// delivered marks a background result as collected via SubagentStatus
	// (exactly-once: a second collect reports "already delivered").
	delivered bool
	// noticed marks a finished background child as already announced by the
	// turn-boundary completion NOTICE (A2 — the injected harness-note user
	// message). It is DELIBERATELY separate from delivered: the notice carries
	// only harness-authored metadata (id + stop label), so a noticed result is
	// never re-noticed but REMAINS collectible — the body still has exactly one
	// channel (SubagentStatus → delivered).
	noticed bool
	// doneCh is closed exactly once at the child's terminal (markDone); the
	// run-end drain and SubagentStatus wait_ms park on it.
	doneCh chan struct{}
	// outputTail is a background-Shell job's bounded live output sink
	// (bash-cmd entries only): the detached drive streams stdout+stderr into
	// it from spawn to terminal, so ShellStatus (or the collected result) can
	// render the RECENT tail. nil for every other family.
	outputTail *tailBuffer
	// exitCode is a background-Shell job's process exit status, valid only
	// once the entry is done (bash-cmd entries only; 0 placeholder before).
	exitCode int
}

// childStatus is the read-only snapshot of one entry the SubagentStatus tool
// renders (plain values; no channels, no cancel funcs).
type childStatus struct {
	id         string
	family     childFamily
	goal       string
	background bool
	state      childState
	stop       session.StopReason
	delivered  bool
}

// backgroundJoin is one live background child the run-end drain cancels and joins.
type backgroundJoin struct {
	id     string
	doneCh <-chan struct{}
}

// newChildRunRegistry constructs an empty registry.
func newChildRunRegistry() *childRunRegistry {
	return &childRunRegistry{
		entries:   make(map[string]*childEntry),
		gen:       make(chan struct{}),
		emitAbort: make(chan struct{}),
	}
}

// register records a child under its session id BEFORE the child acquires its
// concurrency slot, so a child queued on the gate is already cancellable.
// Re-registration of an existing id (a `resume` of an already-run child within
// the SAME parent run) OVERWRITES the old entry with a fresh doneCh — the old
// entry's doneCh was closed at its terminal and is never touched again, so a
// double-close is structurally impossible. The displaced DONE entry is STASHED
// on the new one so a pre-start abort of the resume attempt can reinstate it
// (see childEntry.displaced); it is dropped for good once the new attempt
// genuinely starts (markRunning) or terminates (markDone).
func (g *childRunRegistry) register(childID string, family childFamily, goal string, cancel context.CancelFunc, background bool) {
	_ = g.registerProtected(context.Background(), childID, family, goal, cancel, background)
}

func (g *childRunRegistry) registerProtected(ctx context.Context, childID string, family childFamily, goal string, cancel context.CancelFunc, background bool) error {
	e := &childEntry{
		family:     family,
		goal:       goal,
		cancel:     cancel,
		background: background,
		askIDs:     make(map[string]struct{}),
		state:      childQueued,
		doneCh:     make(chan struct{}),
	}
	if g.liveness != nil && family != childFamilyShellCmd {
		release, err := g.liveness.Register(ctx, session.SessionID(childID), cancel)
		if err != nil {
			return err
		}
		e.releaseLiveness = release
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if old, ok := g.entries[childID]; ok && old.state == childDone {
		e.displaced = old
	}
	g.entries[childID] = e
	return nil
}

// attachOutputTail stores a background-Shell job's live output sink on its entry
// (bash-cmd registrations only). It is a separate seam from register rather than
// a register parameter because only the ONE family ever carries a tail — the
// delegation families' five-field registration stays untouched.
func (g *childRunRegistry) attachOutputTail(childID string, buf *tailBuffer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.entries[childID]; ok && e.state != childDone {
		e.outputTail = buf
	}
}

// outputTailSnapshot reads a background-Shell job's retained output tail plus its
// truncation flag (ShellStatus's live-job view). ok is false for an unknown id or
// an entry with no tail attached (every non-bash-cmd family).
func (g *childRunRegistry) outputTailSnapshot(childID string) (tail string, truncated, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, found := g.entries[childID]
	if !found || e.outputTail == nil {
		return "", false, false
	}
	return e.outputTail.Snapshot(), e.outputTail.Truncated(), true
}

// setExitCode records a background-Shell job's process exit status on its (live)
// entry. Unknown or already-done ids are ignored — the drive sets it strictly
// before its markDoneResult terminal.
func (g *childRunRegistry) setExitCode(childID string, code int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.entries[childID]; ok && e.state != childDone {
		e.exitCode = code
	}
}

// markRunning advances a queued entry to running (the child's drive has actually
// started — its concurrency slot is held / its first round is being driven).
// Unknown or already-done ids are ignored. From here the registration's
// overwrite of a displaced prior entry is permanent (the stash is dropped).
func (g *childRunRegistry) markRunning(childID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[childID]
	if !ok || e.state == childDone {
		return
	}
	e.state = childRunning
	e.displaced = nil
}

// markDone records the child's terminal stop and closes its doneCh. It is
// idempotent: a second markDone for the same entry (or one for an unknown id)
// is a no-op, so doneCh is never double-closed.
func (g *childRunRegistry) markDone(childID string, stop session.StopReason) {
	g.markDoneResult(childID, stop, nil)
}

// markDoneResult is markDone plus the BACKGROUND child's rendered terminal
// ToolResult, stored for SubagentStatus collection (A2: the result body's sole
// channel). Same idempotence as markDone.
//
// It is ALSO the ask-retraction chokepoint: every child that ran lands exactly
// one terminal here (the spawning tools all defer it), and by then the child's
// drive has returned — a surfaced permission ask that is STILL pending is dead
// by definition (the parked await unwound via ctx; no verdict can ever resolve
// it). So any unanswered surfaced asks the child owns are taken
// (snapshot-and-cleared, the requestCancel pattern) and retracted HERE,
// strictly BEFORE the done-transition closes doneCh: the run-end drain joins on
// doneCh and only then seals, and the terminate paths emit the terminal
// EvResult after the drain returns — so a joined child's permission.retract
// always sequences before the seal AND before EvResult on the stream. This
// covers EVERY ctx-driven unwind (run-end drain, per-call timeout_ms, a
// parallel join=first loser, whole-run cancel, team teardown), not only
// Run.CancelChild — and it is the GUARANTEED leg of a client-cancelled child's
// retract: requestCancel only SNAPSHOTS the ask set (never clears it), so even
// when CancelChild's eager retract loses its scheduling race to the seal, the
// take here still holds the ask and delivers the retract pre-seal. The retract
// is a GUARDED emit (it gives up on emitAbort/hardAbort like every child
// emit), not an ownership release, so it correctly precedes the doneCh join
// signal. Idempotence/exactly-once: a second call takes an empty ask set and
// no-ops on the done check, and the unregisterAsk gate — consumed atomically
// with the emit in retractAsksVia — keeps an ask the eager CancelChild path
// (or a routed verdict: route deletes an answered ask's router entry) already
// consumed from ever drawing a second or spurious retract.
func (g *childRunRegistry) markDoneResult(childID string, stop session.StopReason, result *session.ToolResult) {
	// Capture the entry POINTER and take its asks under ONE mu hold, then
	// retract OUTSIDE mu (retractAsks crosses into the router mutex and emitMu —
	// the mu-never-across-a-send rule), then transition THAT pointer — never a
	// re-lookup by id after the retract. The pointer capture is load-bearing:
	// the retract emit can PARK on consumer backpressure, and the in-flight
	// child id is released BEFORE this terminal lands (the ownership-before-
	// done-signal ordering), so a same-run `resume` can re-register the id into
	// that gap — register's replace-when-not-done semantics would then hand a
	// re-lookup the NEW attempt's entry and the OLD child's terminal would land
	// on it (premature done + the old result corrupting the new attempt).
	// Transitioning the captured pointer stays correct when the entry was
	// replaced (or removed) meanwhile: the old and new entries are DISTINCT
	// structs, so the new one is untouched; the old pointer's doneCh close is
	// exactly what the old child's joiners (the drain's join snapshot, a parked
	// SubagentStatus wait) captured and need; and the old doneCh cannot already
	// be closed — remove operates on the CURRENT map entry (the old pointer left
	// the map at re-register), and for one child's own lifecycle remove
	// (pre-start abort) and this terminal are mutually exclusive. The replaced
	// old entry's stored result is unreachable via the map afterwards — the same
	// disposition as any register-over-a-live-id overwrite.
	e, askIDs := g.takeEntryAsks(childID)
	g.retractAsks(askIDs)
	if e == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if e.state == childDone {
		return
	}
	e.state = childDone
	e.stop = stop
	e.result = result
	e.displaced = nil
	if e.family != childFamilyTeamMember && e.releaseLiveness != nil {
		e.releaseLiveness()
	}
	close(e.doneCh)
	g.bumpGenLocked()
}

// remove deletes a NEVER-STARTED entry — the pre-start abort of the A5 state
// vocabulary (see the type comment): the child's failure already surfaced inline
// (or its member never ran a round), so a lingering done+StopNone entry would be
// a roster phantom. It closes the doneCh so a parked waiter wakes, and is a
// deliberate no-op for a done entry (a pre-start abort precedes any real
// terminal; never erase a meaningful one). If the aborted registration had
// DISPLACED a prior done entry (a failed `resume` attempt on an already-run id),
// that entry is REINSTATED — the prior terminal and its possibly-undelivered
// background result must survive the failed attempt.
func (g *childRunRegistry) remove(childID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[childID]
	if !ok || e.state == childDone {
		return
	}
	if e.releaseLiveness != nil {
		e.releaseLiveness()
	}
	close(e.doneCh)
	if e.displaced != nil {
		g.entries[childID] = e.displaced
	} else {
		delete(g.entries, childID)
	}
	g.bumpGenLocked()
}

// releaseLiveness ends a long-lived team's process-wide exclusion at team
// teardown. Team entries may be marked done when de-scheduled before the lead's
// final synthesis, so their ordinary markDone cannot mean lifecycle completion.
func (g *childRunRegistry) releaseLiveness(childID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.entries[childID]; e != nil && e.releaseLiveness != nil {
		e.releaseLiveness()
	}
}

// bumpGenLocked wakes every "any child" waiter parked on the current terminal
// generation. Callers hold mu.
func (g *childRunRegistry) bumpGenLocked() {
	close(g.gen)
	g.gen = make(chan struct{})
}

// liveGeneration returns, under ONE lock hold, whether any registered child is
// still live AND the terminal-generation channel CONSISTENT with that liveness
// snapshot (closed when the next markDone/remove lands). The single hold is
// load-bearing: separate anyLive()/generationCh() calls had a TOCTOU window — a
// terminal landing between them handed the waiter the FRESH generation channel,
// parking an "any child" wait for its full capped duration even though the
// terminal it was waiting for had already happened.
func (g *childRunRegistry) liveGeneration() (gen <-chan struct{}, live bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.entries {
		if e.state != childDone {
			live = true
			break
		}
	}
	return g.gen, live
}

// doneChFor returns the entry's terminal channel (closed iff the child is done),
// or ok=false for an unknown id. The channel survives a later remove (remove
// closes it first), so a parked waiter never hangs.
func (g *childRunRegistry) doneChFor(childID string) (<-chan struct{}, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[childID]
	if !ok {
		return nil, false
	}
	return e.doneCh, true
}

// statusSnapshot returns a stable (id-sorted) value snapshot of every entry for
// the SubagentStatus roster.
func (g *childRunRegistry) statusSnapshot() []childStatus {
	return g.statusSnapshotMatching(nil)
}

// statusSnapshotMatching is statusSnapshot filtered to entries whose family is
// NOT in exclude (nil exclude ⇒ every entry): ONE registry walk serves the two
// disjoint roster projections — SubagentStatus (excludes bash-cmd, the three
// delegation families) and ShellStatus (bash-cmd only) — over the shared map,
// so neither tool can drift its view of an entry.
func (g *childRunRegistry) statusSnapshotMatching(exclude map[childFamily]bool) []childStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]childStatus, 0, len(g.entries))
	for id, e := range g.entries {
		if exclude[e.family] {
			continue
		}
		out = append(out, childStatus{
			id: id, family: e.family, goal: e.goal, background: e.background,
			state: e.state, stop: e.stop, delivered: e.delivered,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// collectOutcome classifies one SubagentStatus collection attempt.
type collectOutcome int

const (
	// collectUnknown — no such child in this run.
	collectUnknown collectOutcome = iota
	// collectRunning — the child has not reached its terminal yet (no body).
	collectRunning
	// collectForeground — done, but a FOREGROUND child: its result already
	// returned inline on its own tool call; there is no stored body.
	collectForeground
	// collectAlready — a background result that was already delivered (never
	// re-bloat the context with a second copy).
	collectAlready
	// collectOK — a background result delivered NOW (marked delivered by this
	// call; exactly-once).
	collectOK
)

// collect attempts to deliver one background child's stored result body,
// marking it delivered on success (exactly-once). The returned status is valid
// for every outcome except collectUnknown.
func (g *childRunRegistry) collect(childID string) (res *session.ToolResult, st childStatus, outcome collectOutcome) {
	return g.collectMatching(childID, nil)
}

// collectMatching is collect restricted to entries whose family is NOT in
// exclude (nil exclude ⇒ every entry): an entry of an excluded family reports
// collectUnknown, so ShellStatus (bash-cmd only) and SubagentStatus (the three
// delegation families) can never deliver through the other's projection — one
// stored body, exactly-once delivery, two disjoint doors.
func (g *childRunRegistry) collectMatching(childID string, exclude map[childFamily]bool) (res *session.ToolResult, st childStatus, outcome collectOutcome) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[childID]
	if !ok || exclude[e.family] {
		return nil, childStatus{}, collectUnknown
	}
	st = childStatus{id: childID, family: e.family, goal: e.goal, background: e.background,
		state: e.state, stop: e.stop, delivered: e.delivered}
	switch {
	case e.state != childDone:
		return nil, st, collectRunning
	case !e.background:
		return nil, st, collectForeground
	case e.delivered:
		return nil, st, collectAlready
	}
	e.delivered = true
	st.delivered = true
	return e.result, st, collectOK
}

// noticeFinishedBackground returns the (id-sorted) statuses of every BACKGROUND
// child that reached its terminal and has neither been NOTICED nor DELIVERED
// yet, marking every scanned candidate noticed — the loop's turn-boundary
// completion-notice source (A2). Semantics, all deliberate:
//
//   - noticed ≠ delivered: marking noticed leaves the stored result fully
//     collectible via SubagentStatus; only `collect` ever sets delivered.
//   - exactly-once notice: every candidate is flipped to noticed here, so a
//     child can never appear in two notices.
//   - an ALREADY-DELIVERED result is marked noticed but NOT returned: the model
//     collected the body itself (e.g. a SubagentStatus wait_ms in the same
//     turn), so announcing "finished — collect it" would only instruct it to
//     re-collect into an "already delivered" reply. There is nothing left to
//     announce.
//   - foreground/team/parallel entries are excluded (background==false): their
//     outcomes were delivered inline on their own tool calls.
//
// A child that lands its terminal AFTER this scan is simply picked up at the
// NEXT turn boundary — the scan window needs no locking beyond the registry's.
func (g *childRunRegistry) noticeFinishedBackground() []childStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []childStatus
	for id, e := range g.entries {
		if !e.background || e.state != childDone || e.noticed {
			continue
		}
		e.noticed = true
		if e.delivered {
			continue
		}
		out = append(out, childStatus{
			id: id, family: e.family, goal: e.goal, background: e.background,
			state: e.state, stop: e.stop, delivered: e.delivered,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// liveBackgroundIDs returns the sorted ids of every background child that has not
// reached its terminal — the gate-full fail-fast error's "currently running" list
// and the background-pending nudge's id list (ids ONLY — A9).
func (g *childRunRegistry) liveBackgroundIDs() []string {
	return g.liveBackgroundIDsMatching(nil)
}

// liveBackgroundIDsMatching is liveBackgroundIDs filtered to entries whose
// family is NOT in exclude (nil exclude ⇒ every live background entry): the
// run-end nudge partitions its id list by family over this one walk.
func (g *childRunRegistry) liveBackgroundIDsMatching(exclude map[childFamily]bool) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var ids []string
	for id, e := range g.entries {
		if e.background && e.state != childDone && !exclude[e.family] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// cancelLiveBackground cancels every live BACKGROUND entry's per-call context
// (outside the lock) and returns their (id, doneCh) join handles, id-sorted, for
// the run-end drain. Foreground/team/parallel children cannot be live at a run
// terminal (their tool calls are synchronous), so only background entries are
// touched.
func (g *childRunRegistry) cancelLiveBackground() []backgroundJoin {
	g.mu.Lock()
	var joins []backgroundJoin
	var cancels []context.CancelFunc
	for id, e := range g.entries {
		if e.background && e.state != childDone {
			joins = append(joins, backgroundJoin{id: id, doneCh: e.doneCh})
			if e.cancel != nil {
				cancels = append(cancels, e.cancel)
			}
		}
	}
	g.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	sort.Slice(joins, func(i, j int) bool { return joins[i].id < joins[j].id })
	return joins
}

// recordAsk records that the (live) child owns a surfaced ask, so a later
// CancelChild can retract it. Unknown or already-done ids are ignored (a child
// driven without parent caps, or a terminal race).
func (g *childRunRegistry) recordAsk(childID, askID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[childID]
	if !ok || e.state == childDone {
		return
	}
	e.askIDs[askID] = struct{}{}
}

// requestCancel marks the child client-cancelled and returns its cancel func
// plus a SNAPSHOT of its owned askIDs — deliberately NOT cleared from the
// entry. The snapshot feeds CancelChild's EAGER retract (prompt modal
// dismissal), but that emit runs on the CALLER's goroutine, unsynchronized
// with the run's terminate path: if it loses a scheduling race to the seal it
// is silently dropped (safeEmit's post-seal no-op — the once-in-CI
// TestCancelChildWhileParkedOnAsk flake). Leaving the set intact keeps the
// GUARANTEED emitter armed: the child's registry terminal (markDoneResult —
// pre-doneCh-close ⇒ pre-seal) takes the same asks and retracts whatever the
// eager path didn't deliver. Exactly-once across the two paths is the
// unregister gate's job (retractAsksVia consumes the router entry atomically
// with the emit), not a snapshot-clear's.
// It returns ok=false for an unknown or already-done id — CancelChild is a
// no-op then. The returned cancel MUST be invoked outside the registry lock
// (it may synchronously unwind code paths that re-enter the registry, e.g.
// markDone).
func (g *childRunRegistry) requestCancel(childID string) (cancel context.CancelFunc, askIDs []string, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, found := g.entries[childID]
	if !found || e.state == childDone {
		return nil, nil, false
	}
	e.clientCancelled = true
	return e.cancel, snapshotAsksLocked(e), true
}

// takeAsks snapshots and CLEARS the surfaced askIDs owned by childID (the
// requestCancel pattern), id-sorted, under one mu hold. Unknown ids return nil;
// a DONE entry is deliberately NOT skipped — a markDoneResult race may have
// flipped the state first (its own take already emptied the set, so this take
// is then empty: the clear is the exactly-once mechanism, not the state check).
// Callers retract the returned set OUTSIDE the registry lock (retractAsks).
func (g *childRunRegistry) takeAsks(childID string) []string {
	_, askIDs := g.takeEntryAsks(childID)
	return askIDs
}

// takeEntryAsks is takeAsks plus the entry POINTER, captured under the SAME mu
// hold as the ask snapshot, so markDoneResult's later done-transition operates
// on the exact entry that owned the taken asks — never on whatever a re-lookup
// finds after the (parkable) retract emit. nil entry ⇔ unknown id.
func (g *childRunRegistry) takeEntryAsks(childID string) (*childEntry, []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[childID]
	if !ok {
		return nil, nil
	}
	return e, takeAsksLocked(e)
}

// takeAsksLocked is the locked snapshot+CLEAR core of takeAsks/takeEntryAsks —
// the chokepoint take. Callers hold mu. The ids are sorted
// (cancelLiveBackground's determinism discipline).
func takeAsksLocked(e *childEntry) []string {
	ids := snapshotAsksLocked(e)
	clear(e.askIDs)
	return ids
}

// snapshotAsksLocked is the non-clearing snapshot requestCancel uses: the eager
// CancelChild retract gets the ids while the entry KEEPS them for the
// guaranteed child-terminal take (see requestCancel). Callers hold mu.
func snapshotAsksLocked(e *childEntry) []string {
	if len(e.askIDs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(e.askIDs))
	for id := range e.askIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// retractAsks unregisters each taken askID from the parent's router and emits a
// permission.retract for the ones that were still PENDING: the bound
// unregisterAsk gate reports whether the router actually removed an entry —
// route() already deletes an answered ask's, so a stale already-answered id
// fails the gate and emits nothing (no spurious retract racing a just-delivered
// verdict). An UNBOUND registry (nil unregisterAsk — unit tests that never ran
// Engine.Run) skips the retracts entirely, never panics. NO registry lock
// is held here — the mu-never-across-a-send rule; see retractAsksVia for the
// {sealed-check, unregister, emit} atomic section and its lock order. Each
// retract is a guarded send that gives up on emitAbort/hardAbort like every
// child emit.
func (g *childRunRegistry) retractAsks(askIDs []string) {
	g.retractAsksVia(g.unregisterAsk, askIDs)
}

// retractAsksVia is retractAsks' core with an EXPLICIT gate: Run.CancelChild
// passes its own Run.unregisterChildAsk method value so the unregister-BEFORE-
// emit ordering holds even on a Run whose registry was built outside
// Engine.Run (the binding and the method are the same function in
// production). A nil gate skips entirely (do-not-retract, fail-safe); a nil
// EMIT likewise leaves the gate unconsumed — an unbound registry must not eat
// the router entry it can never announce.
//
// The {sealed-check, unregister, emit} triple is ONE emitMu section per id —
// the same A4a no-TOCTOU discipline as safeEmit, with the GATE pulled inside.
// That atomicity is load-bearing for exactly-once-with-delivery across the TWO
// retract attempts a client-cancelled child's ask gets (CancelChild's eager
// snapshot + the markDoneResult chokepoint): the gate consume and the emit
// cannot be split by the seal, so either this attempt runs pre-seal and its
// emit is genuinely delivered (or aborted only by emitAbort — the documented
// stalled-consumer abandon), or it runs post-seal and SKIPS WITHOUT consuming
// the gate, leaving the askID for whichever attempt ran pre-seal (the child
// terminal always does for a joined child). The old shape — unregister, then a
// separate safeEmit — let a preempted eager attempt win the gate and then drop
// the emit against a sealed registry, starving the chokepoint: zero retracts
// (the TestCancelChildWhileParkedOnAsk CI flake).
//
// Lock order inside the section: emitMu → router mu (inside the gate). No code
// path acquires them in the reverse order (the router's own methods take only
// its mu; surfaceAsk's registerChild releases the router mu before safeEmit).
// NO registry mu is held here (the mu-never-across-a-send rule).
func (g *childRunRegistry) retractAsksVia(unregister func(askID string) bool, askIDs []string) {
	if unregister == nil {
		return
	}
	for _, id := range askIDs {
		g.emitMu.Lock()
		// Operand order is load-bearing: the gate (unregister) is consumed LAST,
		// only once both emit preconditions hold — the registry is unsealed AND an
		// emitter is bound — so a consumed gate ALWAYS corresponds to a real emit
		// attempt. Consuming it first (unregister before the emit-nil check, or
		// before the sealed check) silently removes the router entry with no
		// retract delivered, starving the other retract leg.
		// TestRetractSealedOrUnboundLeavesGate pins both orderings.
		if !g.sealed && g.emit != nil && unregister(id) {
			g.emit(session.Event{Type: session.EvPermissionRetract, Ask: &session.PendingAsk{AskID: id}})
		}
		g.emitMu.Unlock()
	}
}

// clientCancelled reports whether CancelChild was requested for the child. The
// spawning tools read it (via parentCaps.children) to attribute the kill: the
// Subagent tool renders the "[subagent cancelled by user]" terminal note and a
// Parallel branch flips its failReason to "cancelled by user", disambiguating a
// client cancel from a parent-run cancel or a per-call timeout.
func (g *childRunRegistry) clientCancelled(childID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[childID]
	return ok && e.clientCancelled
}

// abortEmits closes the emit-abort channel (idempotent — it shares sealOnce with
// seal), unblocking every guarded send parked on a full events channel WITHOUT
// yet barring future emits. The run-end drain calls it between its two join
// phases: a child blocked inside safeEmit (consumer stopped draining) cannot
// reach markDone until its send aborts, so aborting emits BEFORE the grace
// re-join lets such a child unwind and join — instead of burning the whole cap
// and being misreported as wedged.
func (g *childRunRegistry) abortEmits() {
	g.sealOnce.Do(func() { close(g.emitAbort) })
}

// seal marks the registry closed for emission. It is called twice per run —
// once by the loop's pre-terminal drain hook (so an ABANDONED background child's
// residual emits are safe no-ops before the terminal EvResult goes out) and once
// by the run goroutine's deferred belt just before the events channel closes.
//
// Ordering (deadlock-free by construction): seal first closes emitAbort (via
// abortEmits; the drain may already have closed it), which unblocks any guarded
// send parked on a full events channel — even with a LIVE run ctx (a consumer
// that stopped draining) — releasing emitMu; only then does it take emitMu and
// set sealed. Because the emit path holds emitMu across its sealed-check+send
// (A4a), an in-flight emit completes (or aborts) before seal returns, and every
// later emit is a safe no-op. This abort-before-emitMu ordering is THE deadlock
// prevention mechanic; TestSealUnblocksEmitParkedSend pins it.
func (g *childRunRegistry) seal() {
	g.abortEmits()
	g.emitMu.Lock()
	g.sealed = true
	g.emitMu.Unlock()
}

// sealWithFinal atomically closes child-ask routing and seals child emission.
// It returns the final approvals/retractions for the parent loop to emit after
// releasing emitMu. The parent emitter may wait for a draining consumer; it
// must never hold emitMu while doing so, or the seal escape can deadlock.
func (g *childRunRegistry) sealWithFinal(final func() []session.Event) []session.Event {
	g.abortEmits()
	g.emitMu.Lock()
	defer g.emitMu.Unlock()
	if g.sealed {
		return nil
	}
	var events []session.Event
	if final != nil {
		events = final()
	}
	g.sealed = true
	return events
}

// safeEmit publishes one event on the parent stream through the seal guard: the
// sealed check AND the send happen inside one emitMu section (no TOCTOU against
// seal/close — A4a); the entries mutex is NOT held here, so a send waiting on
// the events channel never wedges entry bookkeeping. EVERY child-originated emit
// (subagent.start/tool/end, the surfaced-ask EvPermissionAsk) routes through
// here (A4c) — permission.retract uses the same emitMu+sealed section inlined
// in retractAsksVia (the gate must sit INSIDE it) — so a post-seal emit from an
// abandoned background goroutine is a silent no-op, never a
// send-on-closed-channel panic. The bound
// emit (Run.emitOrAbort) blocks until delivered and gives up when seal aborts
// it — child observability is a UX courtesy; dropping an event against a
// wedged/teardown consumer is acceptable.
func (g *childRunRegistry) safeEmit(ev session.Event) {
	g.emitMu.Lock()
	defer g.emitMu.Unlock()
	if g.sealed || g.emit == nil {
		return
	}
	g.emit(ev)
}

// emitAccepted takes accepted child verdicts while the parent stream is open.
// Sends use the child abort signal, so drain can release a stalled consumer.
// Any send that aborts is restored under router.mu before emitMu is released;
// sealWithFinal then picks it up for parent emission before EvResult. A caller
// with an EventSink mirrors successful sends inside send, before emitMu releases
// and a parent terminal can overtake them.
func (g *childRunRegistry) emitAccepted(take func() []session.Event, send func(session.Event) bool, requeue func([]session.Event)) {
	g.emitMu.Lock()
	defer g.emitMu.Unlock()
	if g.sealed || take == nil || send == nil || requeue == nil {
		return
	}
	queued := take()
	for i, ev := range queued {
		if !send(ev) {
			requeue(queued[i:])
			break
		}
	}
}

// emitRetract publishes a permission.retract event for one withdrawn askID on
// the parent stream, through the same seal guard as every child emit. The
// payload is server-authored and carries the AskID only. Production retracts
// flow through retractAsksVia (which inlines this emit so the unregister gate
// shares its emitMu section); this stays the gate-less primitive for tests.
func (g *childRunRegistry) emitRetract(askID string) {
	g.safeEmit(session.Event{Type: session.EvPermissionRetract, Ask: &session.PendingAsk{AskID: askID}})
}
