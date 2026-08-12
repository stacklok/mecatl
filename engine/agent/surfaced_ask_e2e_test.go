package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestE2E_SurfacedAskAllowed drives a REAL interactive parent Engine whose Subagent child
// (isolated, but with a substitution that is NOT worktree-auto-approvable) surfaces a
// Bash ask. A scripted approver goroutine reads the parent stream and APPROVES via the
// surfaced (child) askID; the child then executes the command and the parent run
// completes cleanly.
func TestE2E_SurfacedAskAllowed(t *testing.T) {
	bash := &fakeBash{}
	// `cat $(zap)`: substitution with a non-read-only inner stand-in → NOT
	// IsolationApprovable → surfaces even though the child is isolated.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: command output processed"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run the command"}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	evs := drainApproving(r, session.VerdictAllowOnce, nil)

	if got := bash.ran(); len(got) != 1 || !strings.Contains(got[0], "cat $(zap)") {
		t.Fatalf("approved surfaced ask: child should have executed the command once; ran=%v", got)
	}
	// The parent's terminal result reflects the child's real summary folding back.
	res := lastResult(t, evs)
	if res.Stop == session.StopError {
		t.Fatalf("parent run failed: %q", res.Error)
	}
	// The parent's single Subagent tool result is the child's summary (not an error).
	var taskResult *session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			taskResult = ev.ToolResult
		}
	}
	if taskResult == nil || taskResult.IsError {
		t.Fatalf("expected a non-error Subagent result after approval; got %+v", taskResult)
	}
}

// TestE2E_SurfacedChildAskCarriesChildGatedCallID (#148): the SURFACED child ask
// propagates the CHILD's gated ToolCall.ID onto PendingAsk.Call (the child
// engine's own authorize populated it, and the surfacing closure forwards it), so
// a host sees the same opaque correlation id on a surfaced ask as on a direct one.
func TestE2E_SurfacedChildAskCarriesChildGatedCallID(t *testing.T) {
	bash := &fakeBash{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: command output processed"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run the command"}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	var surfaced *session.PendingAsk
	drainApproving(r, session.VerdictAllowOnce, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && surfaced == nil {
			ask := *ev.Ask
			surfaced = &ask
		}
	})
	if surfaced == nil {
		t.Fatal("no surfaced child ask was observed")
	}
	if surfaced.Call != "k1" {
		t.Fatalf("surfaced child ask .Call = %q, want the child's gated ToolCall.ID %q", surfaced.Call, "k1")
	}
}

// TestE2E_SurfacedAskDenied drives the same surfaced-ask path but the scripted approver
// DENIES. The child gets a denied tool result and degrades gracefully — the run still
// terminates with a deliverable, never hangs.
func TestE2E_SurfacedAskDenied(t *testing.T) {
	bash := &fakeBash{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: proceeding without that command"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	evs := drainApproving(r, session.VerdictDeny, nil)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("denied surfaced ask: child must NOT execute the command; ran=%v", got)
	}
	res := lastResult(t, evs)
	if res.Stop == session.StopError {
		t.Fatalf("parent run should degrade gracefully, not fail: %q", res.Error)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatalf("parent must still produce a deliverable after a denied subagent ask")
	}
}

// TestE2E_HeadlessAutoDeny drives a HEADLESS parent (Interactive=false): the same
// surfaced-class ask is auto-denied with the accurate "non-interactive subagent shell"
// operator diagnostic, the child does not execute it, and the run does not hang.
func TestE2E_HeadlessAutoDeny(t *testing.T) {
	bash := &fakeBash{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: adapted"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	diag := newRecordingDiag()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent: done"),
	)
	// newEngine ⇒ Interactive=false (headless).
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task), Diagnostics: diag})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("headless auto-deny: child must not execute; ran=%v", got)
	}
	if line, ok := diag.findLine("non-interactive shell"); !ok {
		t.Fatalf("expected the accurate non-interactive auto-deny diagnostic; got %+v", diag.base().lines)
	} else if line.argValue("agent") == nil {
		t.Fatalf("auto-deny diagnostic must carry agent=<role>; args=%v", line.args)
	}
	if lastResult(t, evs).Stop == session.StopError {
		t.Fatalf("headless run must not fail; the denied child degrades")
	}
}

// TestE2E_AdversarialSubstitutionHidesDestructiveStillDenied proves the security crux:
// an ISOLATED child whose substitution hides a NON-read-only inner (innocuous stand-in
// `zap`) is STILL not auto-approved by A2 — even when isolated. With a headless parent
// (no surface), it auto-denies; the child never runs the command. We assert on the
// EFFECT (the command did not run), never on a destructive literal.
func TestE2E_AdversarialSubstitutionHidesDestructiveStillDenied(t *testing.T) {
	bash := &fakeBash{}
	// A worktree-safe-LOOKING outer (`go test`) hiding a non-read-only inner via
	// substitution — must NOT auto-approve under A2.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":"go test $(zap)"}`)),
		mockllm.TextTurn("child: adapted"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent: done"),
	)
	// Headless: no surface, so A2 is the ONLY thing that could clear it — and it must not.
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	_ = drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("adversarial hidden-non-read-only substitution must NOT auto-approve even when isolated; ran=%v", got)
	}
}

// TestE2E_SurfacedTeamMemberAskDoesNotBlockPeers drives a REAL team via the Team tool
// with an interactive parent: the lead surfaces a Bash ask and parks; a read-only
// worker keeps running and finishes; the scripted approver answers the lead's surfaced
// ask; the team converges. It proves a parked member does not block its peers (the
// supervisor drains members on independent errgroup goroutines).
func TestE2E_SurfacedTeamMemberAskDoesNotBlockPeers(t *testing.T) {
	leadBash := &fakeBash{}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)

	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		switch spec.Name {
		case "lead":
			cat.MustRegister(leadBash)
			llm := mockllm.New(
				// Round 0: surface a Bash ask (non-isolation-approvable substitution).
				mockllm.ToolCallTurn(toolCall("a1", "Bash", `{"command":"cat $(zap)"}`)),
				mockllm.TextTurn("lead: command done"),
				// Synthesis turn.
				mockllm.TextTurn("CONSOLIDATED: worker reported, command run"),
			)
			eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "m"})
			// IsolateReadOnly: a read-only member granted a shell (isolated worktree).
			return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
		default:
			llm := mockllm.New(mockllm.TextTurn("worker: finished its independent work"))
			eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "m"})
			return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
		}
	}

	roFk := &recordingSubagentForker{}
	tt := agent.NewTeamTool(factory, agent.WithTeamToolReadOnlyForker(roFk))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"do work","members":[{"name":"lead","role":"run a command"},{"name":"worker","role":"do independent work"}]}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, tt)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	// T6: pin CONCURRENCY, not just convergence. We HOLD the lead's surfaced ask until the
	// WORKER has produced one of its own events, then approve. If the supervisor drained
	// members sequentially (lead first, parked on the ask), the worker would NEVER emit
	// before the approval and this would deadlock the 10s watchdog → fail. Observing a
	// worker event WHILE the lead is parked proves peers keep running.
	var (
		workerEmittedBeforeApproval bool
		approved                    bool
	)
	evs := drainTeamPinningConcurrency(t, r, func(ev session.Event, workerSeen bool) (approve bool, verdict session.ApprovalVerdict) {
		if !approved && ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			// Only approve ONCE the worker has already emitted (proves concurrency).
			if workerSeen {
				approved = true
				workerEmittedBeforeApproval = true
				return true, session.VerdictAllowOnce
			}
			// Worker hasn't emitted yet — defer; we'll approve on a later poll once it has.
			return false, session.VerdictDeny
		}
		return false, session.VerdictDeny
	})

	if !workerEmittedBeforeApproval {
		t.Fatalf("concurrency not pinned: the worker did not emit an event before the lead's ask was approved (a sequential drain would have)")
	}
	if got := leadBash.ran(); len(got) != 1 {
		t.Fatalf("lead's surfaced ask should be approved and run once; ran=%v", got)
	}
	res := lastResult(t, evs)
	if res.Stop == session.StopError {
		t.Fatalf("team run failed: %q", res.Error)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatalf("team must produce a deliverable")
	}
}

// drainTeamPinningConcurrency drains a team run, tracking whether the WORKER member has
// emitted any event, and calls decide for each event with the current worker-seen flag.
// When decide returns approve=true it routes the verdict via r.Approve(ask.AskID). It is
// bounded by a watchdog so a sequential-drain regression (worker never runs before the
// parked ask) fails as a deadlock rather than hanging the suite. It holds EVERY pending
// ask (a map, not a single slot — concurrent members can surface asks concurrently) and
// re-evaluates each on EVERY subsequent event, sweeping until a pass makes no progress —
// so resolving one ask can unblock another decided in the same pass (the two-concurrent-
// asks test holds both until both are in flight).
func drainTeamPinningConcurrency(t *testing.T, r *agent.Run, decide func(ev session.Event, workerSeen bool) (bool, session.ApprovalVerdict)) []session.Event {
	t.Helper()
	var evs []session.Event
	var workerSeen bool
	pending := map[string]session.PendingAsk{}
	deadline := time.After(10 * time.Second)
	ch := r.Events()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
			if ev.Type == session.EvTeamMember && ev.Team != nil && ev.Team.Member == "worker" {
				workerSeen = true
			}
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				pending[ev.Ask.AskID] = *ev.Ask
			}
			if len(pending) == 0 {
				_, _ = decide(ev, workerSeen)
				continue
			}
			// Try to resolve every held ask now that state may have advanced; an
			// approval can unblock another held ask, so re-sweep until quiescent.
			for progressed := true; progressed; {
				progressed = false
				for id, a := range pending {
					ask := a
					if approve, verdict := decide(session.Event{Type: session.EvPermissionAsk, Ask: &ask}, workerSeen); approve {
						r.Approve(id, verdict)
						delete(pending, id)
						progressed = true
					}
				}
			}
		case <-deadline:
			r.Cancel()
			t.Fatalf("team run did not converge within the deadline (possible sequential-drain wedge: a parked member blocked its peers)")
			return evs
		}
	}
}

// TestE2E_TwoConcurrentSurfacedAsksBothResolved drives a REAL team via the Team tool
// with an interactive parent where TWO members (alpha = lead, beta) each surface a Bash
// ask in round 0 (substitutions — not isolation-approvable). Both asks are HELD until
// both have been observed in flight SIMULTANEOUSLY (pinning that concurrent members
// really do surface concurrent asks — the situation the client-side FIFO ask queue
// exists for), then each is approved via its own surfaced (child) askID. Both members'
// commands run exactly once and the team still converges to a deliverable — proving the
// server keeps every concurrently-parked ask answerable, none clobbered or lost.
//
// Honest scope note: this pins the SERVER-side precondition (the askRegistry's routing
// of concurrent surfaced asks, which already worked); the client-side single-slot
// clobber bug itself is pinned by the reducer tests in cmd/mecatui/ui/ask_queue_test.go.
func TestE2E_TwoConcurrentSurfacedAsksBothResolved(t *testing.T) {
	alphaBash := &fakeBash{}
	betaBash := &fakeBash{}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)

	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		switch spec.Name {
		case "alpha": // the lead (first member): surfaces an ask, then synthesises.
			cat.MustRegister(alphaBash)
			llm := mockllm.New(
				mockllm.ToolCallTurn(toolCall("a1", "Bash", `{"command":"cat $(zap-a)"}`)),
				mockllm.TextTurn("alpha: command done"),
				// Synthesis turn.
				mockllm.TextTurn("CONSOLIDATED: both commands ran"),
			)
			eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "m"})
			return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
		default: // beta: surfaces its own ask concurrently.
			cat.MustRegister(betaBash)
			llm := mockllm.New(
				mockllm.ToolCallTurn(toolCall("b1", "Bash", `{"command":"cat $(zap-b)"}`)),
				mockllm.TextTurn("beta: command done"),
			)
			eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "m"})
			return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
		}
	}

	roFk := &recordingSubagentForker{}
	tt := agent.NewTeamTool(factory, agent.WithTeamToolReadOnlyForker(roFk))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"run both commands","members":[{"name":"alpha","role":"run command a"},{"name":"beta","role":"run command b"}]}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, tt)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	// HOLD every surfaced ask until BOTH have been observed; only then approve. Until
	// the second ask arrives neither is answered, so when the threshold trips both
	// children are parked simultaneously — pinning the concurrency. The drain's sweep
	// then approves both (resolving one re-evaluates the other in the same pass).
	seen := map[string]struct{}{}
	var bothHeldInFlight bool
	evs := drainTeamPinningConcurrency(t, r, func(ev session.Event, _ bool) (bool, session.ApprovalVerdict) {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			seen[ev.Ask.AskID] = struct{}{}
			if len(seen) >= 2 {
				bothHeldInFlight = true
				return true, session.VerdictAllowOnce
			}
		}
		return false, session.VerdictDeny
	})

	if !bothHeldInFlight {
		t.Fatalf("concurrency not pinned: both members' asks were never in flight simultaneously (asks seen: %d)", len(seen))
	}
	if got := alphaBash.ran(); len(got) != 1 {
		t.Fatalf("alpha's surfaced ask should be approved and run exactly once; ran=%v", got)
	}
	if got := betaBash.ran(); len(got) != 1 {
		t.Fatalf("beta's surfaced ask should be approved and run exactly once; ran=%v", got)
	}
	res := lastResult(t, evs)
	if res.Stop == session.StopError {
		t.Fatalf("team run failed: %q", res.Error)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatalf("team must produce a deliverable")
	}
}

// TestE2E_HeadlessTeamMemberDeniedResultIsAccurate proves the MODEL-facing message of a
// headless auto-denied subagent Bash ask is the ACCURATE "non-interactive subagent
// shell" text, not the misleading "denied by user". A team member's denied tool RESULT
// is forwarded on the team.member stream (clamped), so the parent stream carries it and
// the test can assert on it. The parent is headless (Interactive=false) so the member's
// substitution ask auto-denies.
func TestE2E_HeadlessTeamMemberDeniedResultIsAccurate(t *testing.T) {
	leadBash := &fakeBash{}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(leadBash)
		llm := mockllm.New(
			mockllm.ToolCallTurn(toolCall("a1", "Bash", `{"command":"cat $(zap)"}`)),
			mockllm.TextTurn("member: adapted after denial"),
			mockllm.TextTurn("CONSOLIDATED: done"),
		)
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "m"})
		return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
	}
	roFk := &recordingSubagentForker{}
	tt := agent.NewTeamTool(factory, agent.WithTeamToolReadOnlyForker(roFk))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"x","members":[{"name":"lead","role":"run a command"}]}`)),
		mockllm.TextTurn("parent: done"),
	)
	// Headless parent (newEngine ⇒ Interactive=false) so the member ask auto-denies.
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, tt)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainWithTimeout(t, r)

	var sawAccurate, sawMisleading bool
	for _, ev := range evs {
		if ev.Type == session.EvTeamMember && ev.Team != nil && ev.Team.IsError {
			if strings.Contains(ev.Team.Detail, "non-interactive subagent shell") {
				sawAccurate = true
			}
			if strings.Contains(ev.Team.Detail, "denied by user") {
				sawMisleading = true
			}
		}
	}
	if !sawAccurate {
		t.Fatalf("headless member's denied Bash result must carry the accurate 'non-interactive subagent shell' message")
	}
	if sawMisleading {
		t.Fatalf("headless auto-deny must NOT use the misleading 'denied by user' message")
	}
}

// drainWithTimeout drains a run to completion, failing the test (and cancelling) if it
// does not terminate within a generous bound — so a wedge surfaces as a failure, not a
// suite hang.
func drainWithTimeout(t *testing.T, r *agent.Run) []session.Event {
	t.Helper()
	var evs []session.Event
	deadline := time.After(10 * time.Second)
	ch := r.Events()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-deadline:
			r.Cancel()
			t.Fatalf("run did not terminate within the deadline (possible wedge)")
			return evs
		}
	}
}
