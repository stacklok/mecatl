package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestEffectiveStatusWaitClamp pins the A3 cap: nil / non-positive → no wait; a
// positive value passes through; anything above maxSubagentStatusWaitMs (120000)
// is CLAMPED to it, never honoured verbatim.
func TestEffectiveStatusWaitClamp(t *testing.T) {
	intp := func(v int) *int { return &v }
	cases := []struct {
		name string
		in   *int
		want time.Duration
	}{
		{"nil means no wait", nil, 0},
		{"zero means no wait", intp(0), 0},
		{"negative means no wait", intp(-5), 0},
		{"in-range passes through", intp(250), 250 * time.Millisecond},
		{"cap boundary", intp(maxSubagentStatusWaitMs), 120 * time.Second},
		{"over the cap is clamped", intp(99999999), 120 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveStatusWait(tc.in); got != tc.want {
				t.Fatalf("effectiveStatusWait(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestChildRegistryCollectOutcomes pins the collect decision table: unknown,
// running (no body), foreground-done (body returned inline elsewhere), then the
// background exactly-once pair (OK delivers + marks; second is already).
func TestChildRegistryCollectOutcomes(t *testing.T) {
	reg := newChildRunRegistry()
	if _, _, outcome := reg.collect("nope"); outcome != collectUnknown {
		t.Fatalf("unknown id must be collectUnknown, got %v", outcome)
	}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.register("bg", childFamilySubagent, "g", cancel, true)
	if _, st, outcome := reg.collect("bg"); outcome != collectRunning || st.state == childDone {
		t.Fatalf("live entry must be collectRunning, got %v (%+v)", outcome, st)
	}

	body := session.NewToolResult("p1", "agentId: bg\n\nfindings")
	reg.markDoneResult("bg", session.StopEndTurn, &body)
	res, st, outcome := reg.collect("bg")
	if outcome != collectOK || res == nil || res.Content != body.Content || !st.delivered {
		t.Fatalf("first collect must deliver and mark delivered, got %v res=%+v st=%+v", outcome, res, st)
	}
	if _, _, outcome := reg.collect("bg"); outcome != collectAlready {
		t.Fatalf("second collect must be collectAlready, got %v", outcome)
	}

	reg.register("fg", childFamilySubagent, "g", cancel, false)
	reg.markDone("fg", session.StopEndTurn)
	if _, _, outcome := reg.collect("fg"); outcome != collectForeground {
		t.Fatalf("a done foreground child must be collectForeground, got %v", outcome)
	}
}

// TestStatusToolsDisjointProjections pins the shared-registry / disjoint-
// projections contract: a background-Bash (bash-cmd) entry is ABSENT from the
// SubagentStatus roster and an unknown-id miss to its collect path, while the
// SAME registry hands it to BashStatus — and a delegation child is the mirror
// miss there.
func TestStatusToolsDisjointProjections(t *testing.T) {
	reg := newChildRunRegistry()
	noCancel := func() {}
	// One delegation child (done, result stored) and one bash job (done).
	body := session.NewToolResult("subagent-a", "agentId: subagent-a\n\nfindings")
	reg.register("subagent-a", childFamilySubagent, "explore", noCancel, true)
	reg.markDoneResult("subagent-a", session.StopEndTurn, &body)
	job := session.NewToolResult("bashcmd-j", "tail\n[exit code: 0]")
	reg.register("bashcmd-j", childFamilyBashCmd, "make serve", noCancel, true)
	reg.markDoneResult("bashcmd-j", session.StopEndTurn, &job)

	// SubagentStatus roster: the subagent, never the bash job.
	res, err := NewSubagentStatusTool().(childCapableTool).ExecuteWithParent(
		context.Background(), session.ToolCall{ID: "s1", Name: subagentStatusToolName, Args: json.RawMessage(`{}`)},
		bashEnv, nil, parentCaps{children: reg})
	if err != nil {
		t.Fatalf("ExecuteWithParent err = %v", err)
	}
	if !strings.Contains(res.Content, "subagent-a") || strings.Contains(res.Content, "bashcmd-j") {
		t.Fatalf("SubagentStatus roster = %q, want the subagent only (no bash job)", res.Content)
	}

	// SubagentStatus collect of the bash job is the same miss as an unknown id.
	res, err = NewSubagentStatusTool().(childCapableTool).ExecuteWithParent(
		context.Background(), session.ToolCall{ID: "s2", Name: subagentStatusToolName,
			Args: json.RawMessage(`{"agent_id":"bashcmd-j"}`)},
		bashEnv, nil, parentCaps{children: reg})
	if err != nil {
		t.Fatalf("ExecuteWithParent err = %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, `no subagent "bashcmd-j" in this run`) {
		t.Fatalf("SubagentStatus collect of a bash job = %+v, want the unknown-id miss", res)
	}

	// BashStatus sees the job; its collect delivers the stored body once.
	res = execBashStatus(t, reg, "s3", map[string]any{"job_id": "bashcmd-j"})
	if res.IsError || res.Content != "tail\n[exit code: 0]" {
		t.Fatalf("BashStatus collect = %+v, want the stored body", res)
	}
	res = execBashStatus(t, reg, "s4", nil)
	if !strings.Contains(res.Content, "bashcmd-j") || strings.Contains(res.Content, "subagent-a") {
		t.Fatalf("BashStatus roster = %q, want the bash job only", res.Content)
	}

	// childKindLabel renders the bash family as a background command, never a
	// "background subagent".
	if got := childKindLabel(childStatus{id: "bashcmd-j", family: childFamilyBashCmd, background: true}); got != "background command" {
		t.Fatalf("childKindLabel(bash-cmd) = %q, want %q", got, "background command")
	}
}

// TestChildRegistryRemoveSemantics pins the A5 pre-start abort: remove deletes a
// not-done entry, closes its doneCh (a parked waiter wakes), bumps the terminal
// generation (an any-waiter wakes), and is a deliberate NO-OP for a done entry
// (never erase a meaningful terminal).
func TestChildRegistryRemoveSemantics(t *testing.T) {
	reg := newChildRunRegistry()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.register("c1", childFamilySubagent, "g", cancel, true)

	doneCh, ok := reg.doneChFor("c1")
	if !ok {
		t.Fatalf("registered entry must expose its doneCh")
	}
	gen, live := reg.liveGeneration()
	if !live {
		t.Fatalf("a queued entry must report live")
	}
	reg.remove("c1")
	select {
	case <-doneCh:
	default:
		t.Fatalf("remove must close the doneCh so a parked waiter wakes")
	}
	select {
	case <-gen:
	default:
		t.Fatalf("remove must bump the terminal generation so an any-waiter wakes")
	}
	if _, _, outcome := reg.collect("c1"); outcome != collectUnknown {
		t.Fatalf("removed entry must be gone, got %v", outcome)
	}

	// remove on a DONE entry is a no-op: the meaningful terminal survives.
	reg.register("c2", childFamilySubagent, "g", cancel, false)
	reg.markDone("c2", session.StopCancelled)
	reg.remove("c2")
	if _, st, outcome := reg.collect("c2"); outcome == collectUnknown || st.stop != session.StopCancelled {
		t.Fatalf("remove must not erase a done entry, got %v (%+v)", outcome, st)
	}
}

// failingForkerInt always fails Fork (the internal-package twin of the external
// tests' erroringForker).
type failingForkerInt struct{}

func (failingForkerInt) Fork(_ context.Context, _ tool.Environment, _ string) (tool.Environment, func() error, string, error) {
	return tool.Environment{}, nil, "", errors.New("worktree add failed")
}

// multiLineFailingForker fails Fork with a MULTI-LINE error, the shape a real `git worktree
// add` failure has (git writes several lines to stderr). It exists so the pre-run
// subagent.end emit site can be held to the same line-oriented Cause contract as the other
// two — see TestBackgroundPreRunFailureEmitsCause.
type multiLineFailingForker struct{}

func (multiLineFailingForker) Fork(_ context.Context, _ tool.Environment, _ string) (tool.Environment, func() error, string, error) {
	return tool.Environment{}, nil, "", errors.New("worktree add failed:\n\tfatal: could not create leading directories\n\thint: check permissions")
}

// TestSubagentForegroundForkFailureAbortsEntry pins the A5 foreground ghost fix:
// a fork failure (the error returned inline; the child never drove) leaves NO
// registry entry — previously a done+StopNone phantom.
func TestSubagentForegroundForkFailureAbortsEntry(t *testing.T) {
	childEngine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.TextTurn("never runs")),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "child-model",
	})
	tl := NewSubagentTool(childEngine, WithChildForker(failingForkerInt{})).(*SubagentTool)
	reg := newChildRunRegistry()

	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x"}`)),
		memEnv("/ws"), nil, parentCaps{children: reg})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "workspace isolation failed") {
		t.Fatalf("fork failure must surface inline, got %+v", res)
	}
	reg.mu.Lock()
	n := len(reg.entries)
	reg.mu.Unlock()
	if n != 0 {
		t.Fatalf("a never-started child must leave NO registry entry (A5), got %d entries", n)
	}
}

// TestBackgroundForkFailureIsCollectible is the background twin: the model
// already holds the started-result, so a post-spawn fork failure must NOT be a
// silent abort — it lands as a collectible done(StopError) entry whose stored
// body is the error.
func TestBackgroundForkFailureIsCollectible(t *testing.T) {
	childEngine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.TextTurn("never runs")),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "child-model",
	})
	tl := NewSubagentTool(childEngine, WithChildForker(failingForkerInt{})).(*SubagentTool)
	reg := newChildRunRegistry()

	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","background":true}`)),
		memEnv("/ws"), nil, parentCaps{children: reg})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "started in the background") {
		t.Fatalf("the started-result must have been returned before the fork ran, got %+v", res)
	}
	doneCh, ok := reg.doneChFor("subagent-p1")
	if !ok {
		t.Fatalf("background entry must exist")
	}
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("background goroutine did not reach its terminal")
	}
	stored, st, outcome := reg.collect("subagent-p1")
	if outcome != collectOK || st.stop != session.StopError {
		t.Fatalf("post-spawn failure must be a collectible StopError, got %v (%+v)", outcome, st)
	}
	if stored == nil || !stored.IsError || !strings.Contains(stored.Content, "workspace isolation failed") {
		t.Fatalf("stored body must carry the failure, got %+v", stored)
	}
}

// TestBackgroundPreRunFailureEmitsCause covers the THIRD subagent.end emit site —
// driveBackground's endOnError, which fires when the fork or the session build fails BEFORE
// the child ever drove. It is a StopError terminal like the other two, so
// session.SubagentPayload.Cause's contract ("non-empty whenever Stop is StopError") has to
// hold here as well; and this is the one path where the event is a client's ONLY channel,
// since a background child's failure never reaches an inline Subagent card (the model
// already holds the started-result). Before this guard the emit set Stop with no Cause and
// nothing noticed.
//
// It is an internal test because the emit needs a real emit sink threaded straight into
// ExecuteWithParent; its external sibling (TestBackgroundSubagentFailureCarriesCause)
// covers the DRIVE-failure emit.
func TestBackgroundPreRunFailureEmitsCause(t *testing.T) {
	childEngine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.TextTurn("never runs")),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "child-model",
	})
	tl := NewSubagentTool(childEngine, WithChildForker(multiLineFailingForker{})).(*SubagentTool)
	reg := newChildRunRegistry()

	var mu sync.Mutex
	var ends []session.SubagentPayload
	emit := func(ev session.Event) {
		if ev.Type != session.EvSubagentEnd || ev.Subagent == nil {
			return
		}
		mu.Lock()
		ends = append(ends, *ev.Subagent)
		mu.Unlock()
	}

	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","background":true}`)),
		memEnv("/ws"), emit, parentCaps{children: reg})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("the started-result must have been returned before the fork ran, got %+v", res)
	}
	doneCh, ok := reg.doneChFor("subagent-p1")
	if !ok {
		t.Fatalf("background entry must exist")
	}
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("background goroutine did not reach its terminal")
	}

	mu.Lock()
	got := append([]session.SubagentPayload(nil), ends...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("a pre-run failure must still close its client lane with exactly one subagent.end, got %d", len(got))
	}
	if got[0].Stop != session.StopError {
		t.Fatalf("a pre-run failure is a StopError terminal, got %q", got[0].Stop)
	}
	if !strings.Contains(got[0].Cause, "workspace isolation failed") {
		t.Fatalf("stop=error with an empty Cause breaks the field's contract on the one path that has no other channel; got %q", got[0].Cause)
	}
	if n := len([]rune(got[0].Cause)); n > maxSubagentCausePreview+1 { // +1 for clampRunes' ellipsis
		t.Fatalf("the pre-run cause must be clamped like the other emit sites: %d runes", n)
	}
	// And COLLAPSED like the other two emit sites: a real `git worktree add` failure is
	// multi-line, while every consumer of this field is a single-line surface. This is emit
	// site 3 of 3, and the field's doc-comment promises that every one of them normalises
	// through the same helper.
	if strings.ContainsAny(got[0].Cause, "\n\r\t") {
		t.Fatalf("the pre-run emit site must normalise the cause to ONE line through subagentCausePayload, got %q", got[0].Cause)
	}
	if !strings.Contains(got[0].Cause, "could not create leading directories") {
		t.Fatalf("collapsing must not lose the continuation lines of a multi-line fork error, got %q", got[0].Cause)
	}
}

// TestBackgroundGateFullAbortsPhantomAndListsIDs drives the fail-fast path at
// the registry level: the gate is full, the pre-gate registration is REMOVED
// (A5 — no phantom queued entry) and the error lists the live background ids
// (ids only — A9).
func TestBackgroundGateFullAbortsPhantomAndListsIDs(t *testing.T) {
	childEngine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.TextTurn("never runs")),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "child-model",
	})
	tl := NewSubagentTool(childEngine, WithMaxConcurrentChildren(1)).(*SubagentTool)
	tl.childGate <- struct{}{} // occupy the only slot

	reg := newChildRunRegistry()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A live background sibling holding the slot — the id the error must list.
	reg.register("subagent-zzz", childFamilySubagent, "g", cancel, true)

	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p2", "Subagent", json.RawMessage(`{"prompt":"x","background":true}`)),
		memEnv("/ws"), nil, parentCaps{children: reg})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "concurrency limit") {
		t.Fatalf("gate-full background must fail fast, got %+v", res)
	}
	if !strings.Contains(res.Content, "subagent-zzz") {
		t.Fatalf("error must list the live background ids, got %q", res.Content)
	}
	// The failing call's OWN id must be ABSENT: the abort (which removes the
	// pre-gate registration) runs BEFORE the live-ids read — read first, the
	// error claimed the very child that failed to start was "currently running".
	if strings.Contains(res.Content, "subagent-p2") {
		t.Fatalf("the gate-full error must not list the failing call's own id, got %q", res.Content)
	}
	reg.mu.Lock()
	_, phantom := reg.entries["subagent-p2"]
	reg.mu.Unlock()
	if phantom {
		t.Fatalf("the failed-fast registration must be removed (A5)")
	}
}

// TestChildRegistryRemoveReinstatesDisplacedEntry pins the failed-resume erase
// fix at the registry level: register-over-done STASHES the displaced terminal
// entry; a pre-start abort (remove) REINSTATES it — the prior result survives —
// while markRunning makes the overwrite permanent (a later remove then deletes
// outright).
func TestChildRegistryRemoveReinstatesDisplacedEntry(t *testing.T) {
	reg := newChildRunRegistry()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg.register("subagent-r", childFamilySubagent, "first", cancel, true)
	body := session.NewToolResult("p1", "agentId: subagent-r\n\noriginal findings")
	reg.markDoneResult("subagent-r", session.StopEndTurn, &body)

	// A resume attempt overwrites the done entry, then ABORTS pre-start (gate
	// full / load / fork failure): the displaced terminal must come back.
	reg.register("subagent-r", childFamilySubagent, "resume", cancel, true)
	reg.remove("subagent-r")
	res, st, outcome := reg.collect("subagent-r")
	if outcome != collectOK || res == nil || res.Content != body.Content || st.stop != session.StopEndTurn {
		t.Fatalf("a failed resume attempt must not erase the prior undelivered result, got %v res=%+v st=%+v", outcome, res, st)
	}

	// Once the new attempt genuinely STARTS (markRunning), the overwrite is
	// permanent: a later remove deletes outright (no stale reinstatement).
	reg.register("subagent-r", childFamilySubagent, "resume2", cancel, true)
	reg.markRunning("subagent-r")
	reg.remove("subagent-r")
	if _, _, outcome := reg.collect("subagent-r"); outcome != collectUnknown {
		t.Fatalf("after markRunning the displaced stash must be dropped; remove must delete outright, got %v", outcome)
	}
}

// TestFailedResumeAttemptPreservesUndeliveredResult is the panel's end-to-end
// scenario at the tool level: a background child finishes with its result NOT
// yet collected; a resume+background of the same id hits a full gate and fails
// fast — the error must not erase (or list as running) the prior child, and the
// ORIGINAL body must still be collectible afterwards.
func TestFailedResumeAttemptPreservesUndeliveredResult(t *testing.T) {
	childEngine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.TextTurn("original findings"), mockllm.TextTurn("never reached")),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "child-model",
	})
	tl := NewSubagentTool(childEngine,
		WithSubagentStore(memstore.New()),
		WithMaxConcurrentChildren(1)).(*SubagentTool)
	reg := newChildRunRegistry()
	caps := parentCaps{children: reg}

	// Background child runs to completion; its result is stored, UNDELIVERED.
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","background":true}`)),
		memEnv("/ws"), nil, caps)
	if err != nil || res.IsError {
		t.Fatalf("background start failed: %v %+v", err, res)
	}
	doneCh, ok := reg.doneChFor("subagent-p1")
	if !ok {
		t.Fatalf("background entry must exist")
	}
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("background child did not finish")
	}

	// Occupy the single slot, then resume+background the SAME id: fail-fast.
	tl.childGate <- struct{}{}
	res2, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p2", "Subagent", json.RawMessage(`{"prompt":"deeper","resume":"subagent-p1","background":true}`)),
		memEnv("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res2.IsError || !strings.Contains(res2.Content, "concurrency limit") {
		t.Fatalf("gate-full resume must fail fast, got %+v", res2)
	}
	// The reinstated entry is DONE, so it is not "currently running" — and a done
	// child must never be listed as one.
	if strings.Contains(res2.Content, "subagent-p1") {
		t.Fatalf("a done child must not be listed as currently running, got %q", res2.Content)
	}

	// The original, undelivered body survives the failed attempt.
	stored, st, outcome := reg.collect("subagent-p1")
	if outcome != collectOK || stored == nil || !strings.Contains(stored.Content, "original findings") {
		t.Fatalf("the prior undelivered result must survive a failed resume attempt, got %v (%+v) res=%+v", outcome, st, stored)
	}
}

// TestLiveGenerationSnapshotConsistent pins the single-lock-hold contract: the
// returned generation channel is CONSISTENT with the returned liveness — when
// live, that exact channel closes on the next terminal; once nothing is live,
// live=false (a waiter never parks on a fresh generation for an event that
// already happened — the anyLive/generationCh TOCTOU).
func TestLiveGenerationSnapshotConsistent(t *testing.T) {
	reg := newChildRunRegistry()
	if _, live := reg.liveGeneration(); live {
		t.Fatalf("an empty registry must not report live")
	}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.register("c1", childFamilySubagent, "g", cancel, true)
	gen, live := reg.liveGeneration()
	if !live {
		t.Fatalf("a queued entry must report live")
	}
	reg.markDone("c1", session.StopEndTurn)
	select {
	case <-gen:
	default:
		t.Fatalf("the generation handed out WITH live=true must close when the terminal lands")
	}
	if _, live := reg.liveGeneration(); live {
		t.Fatalf("after the only entry's terminal, live must be false")
	}
}

// TestWaitForChildAnyReturnsPromptlyOnConcurrentTerminal is the behavioral
// TOCTOU pin: an any-child wait racing the child's terminal must return promptly
// (the consistent snapshot either sees not-live, or holds the generation that
// the terminal closes) — never park for the full capped wait.
func TestWaitForChildAnyReturnsPromptlyOnConcurrentTerminal(t *testing.T) {
	reg := newChildRunRegistry()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.register("c1", childFamilySubagent, "g", cancel, true)
	go func() {
		time.Sleep(20 * time.Millisecond)
		reg.markDone("c1", session.StopEndTurn)
	}()
	start := time.Now()
	waitForChild(context.Background(), reg, "", 30*time.Second)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("any-child wait parked %v despite the concurrent terminal (stale-generation park)", elapsed)
	}
}

// TestChildRegistrySafeEmitSealedFullSurface extends the I1 retract-only seal
// pin to the FULL child emit surface (A4c): after seal, safeEmit of
// subagent.start / subagent.tool / subagent.end / a surfaced EvPermissionAsk
// against a CLOSED channel binding is a silent no-op — never a panic.
func TestChildRegistrySafeEmitSealedFullSurface(t *testing.T) {
	reg := newChildRunRegistry()
	events := make(chan session.Event, 8)
	reg.emit = func(ev session.Event) {
		select {
		case events <- ev:
		case <-reg.emitAbort:
		}
	}

	pre := session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ChildID: "c", Background: true}}
	reg.safeEmit(pre)
	if len(events) != 1 {
		t.Fatalf("pre-seal safeEmit must deliver")
	}

	reg.seal()
	close(events) // the run-teardown shape: any post-seal send would panic

	for _, ev := range []session.Event{
		{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ChildID: "c"}},
		{Type: session.EvSubagentTool, Subagent: &session.SubagentPayload{ChildID: "c", ToolName: "Read"}},
		{Type: session.EvSubagentEnd, Subagent: &session.SubagentPayload{ChildID: "c", Stop: session.StopCancelled}},
		{Type: session.EvPermissionAsk, Ask: &session.PendingAsk{AskID: "a1"}},
	} {
		reg.safeEmit(ev) // must be a silent no-op, not a send-on-closed-channel panic
	}
	reg.emitRetract("a1") // and the retract path shares the same guard
}

// allowAllInt is the internal-package allow-all policy helper (twin of the
// external tests' allowAll).
func allowAllInt() port.PermissionPolicy {
	return permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
}
