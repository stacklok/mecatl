package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// configChildEngine builds a child engine over a CONFIG-shaped policy
// (issue #32): the child allow-all floor PLUS caller-supplied configured rules,
// resolved with the AudienceSubagent pin — exactly the shape composition's
// childPermPolicy assembles (engine tests cannot import internal adapters, so
// the governance.Rule values are constructed directly).
func configChildEngine(llm port.LLMProvider, bash tool.Tool, configured ...governance.Rule) *agent.Engine {
	rules := append([]governance.Rule{{Scope: governance.ScopeBuiltinDefault, Effect: governance.Allow}}, configured...)
	return agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: bashCatalog(bash),
		Policy:  permpolicy.NewPolicy(rules, nil, governance.WithAudience(governance.AudienceSubagent)),
		Model:   "child-model",
	})
}

// requestRecorder captures every LLM request a child makes, so a test can
// assert on the MODEL-FACING tool-result text (the denied result's content).
type requestRecorder struct {
	mu   sync.Mutex
	reqs []port.LLMRequest
}

func (rr *requestRecorder) observe(req port.LLMRequest) {
	rr.mu.Lock()
	rr.reqs = append(rr.reqs, req)
	rr.mu.Unlock()
}

// get returns the i-th recorded request (by model-call order). The steer tests
// index per turn; callers must ensure at least i+1 calls were made.
func (rr *requestRecorder) get(i int) port.LLMRequest {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.reqs[i]
}

// count reports how many requests (model calls) were recorded.
func (rr *requestRecorder) count() int {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return len(rr.reqs)
}

// toolResultContents flattens every tool-result body seen across the recorded
// requests.
func (rr *requestRecorder) toolResultContents() []string {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	var out []string
	for _, req := range rr.reqs {
		for _, m := range req.Messages {
			if m.ToolResult != nil {
				out = append(out, m.ToolResult.Content)
			}
		}
	}
	return out
}

// TestConfiguredChildAskSurfacedNotAutoApproved (issue #32 (a)): a CONFIGURED
// subagent Ask on `go test*` gates an ISOLATED child's `go test ./...` — the
// isolation auto-approve (A2) would otherwise clear it, but a configured Ask is
// NEVER suppressed: it SURFACES to the interactive parent, and only the
// approval runs it.
func TestConfiguredChildAskSurfacedNotAutoApproved(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go test ./..."}`)),
		mockllm.TextTurn("child done"),
	)
	child := configChildEngine(childLLM, bash,
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Ask, Audience: governance.AudienceSubagent})
	// Forker ⇒ ISOLATED child: without the configured Ask, A2 would auto-approve
	// `go test ./...` (pinned by TestIsolatedSubagentAutoApprovesWorktreeSafe).
	task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run tests"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var sawAsk bool
	var ranBeforeAsk bool
	var surfacedReason string
	evs := drainApproving(r, session.VerdictAllowOnce, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			sawAsk = true
			surfacedReason = ev.Ask.Reason
			if len(bash.ran()) != 0 {
				ranBeforeAsk = true
			}
		}
	})
	if !sawAsk {
		t.Fatalf("a configured subagent Ask must SURFACE, never auto-approve (A2 skipped)")
	}
	// The surfaced reason must carry the POLICY reason (the rule that gated it),
	// so the operator can tell their own configured rule from a substitution
	// floor (UX should-fix 4). The policy reason names the matched rule.
	if !strings.Contains(surfacedReason, "approval required by rule for Shell") {
		t.Fatalf("surfaced ask must include the configured-rule reason so the operator knows WHY it surfaced; got %q", surfacedReason)
	}
	if ranBeforeAsk {
		t.Fatalf("the command ran BEFORE the surfaced ask was approved — A2 auto-approved a configured Ask")
	}
	if got := bash.ran(); len(got) != 1 || !strings.Contains(got[0], "go test") {
		t.Fatalf("approval should run the command exactly once; ran=%v", got)
	}
	if res := lastResult(t, evs); res.Stop == session.StopError {
		t.Fatalf("run failed: %q", res.Error)
	}
}

// TestConfiguredChildAskHeadlessAutoDenyMessage (issue #32 (b)): the same
// configured Ask on a HEADLESS parent auto-denies with the RULE-ORIENTED
// message — never the substitution-rephrase advice (a lie for a configured
// rule) and never "denied by user" (no user denied anything) — plus the
// correlated operator diagnostic.
func TestConfiguredChildAskHeadlessAutoDenyMessage(t *testing.T) {
	bash := &fakeShell{}
	rec := &requestRecorder{}
	childLLM := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(rec.observe)},
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go test ./..."}`)),
		mockllm.TextTurn("child adapted"),
	)
	child := configChildEngine(childLLM, bash,
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Ask, Audience: governance.AudienceSubagent})
	task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))

	diag := newRecordingDiag()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run tests"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task), Diagnostics: diag}) // headless
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("headless configured Ask must auto-deny, not execute; ran=%v", got)
	}
	// The MODEL-FACING denied tool result carries the accurate, rule-oriented text.
	var denied string
	for _, c := range rec.toolResultContents() {
		if strings.Contains(c, "not permitted in a non-interactive subagent shell") {
			denied = c
		}
	}
	if denied == "" {
		t.Fatalf("expected the child to see the accurate non-interactive deny; tool results: %v", rec.toolResultContents())
	}
	if !strings.Contains(denied, "configured permission rule") {
		t.Fatalf("configured-ask deny must carry the rule-oriented suffix; got %q", denied)
	}
	if strings.Contains(denied, "rephrase to avoid") {
		t.Fatalf("configured-ask deny must NOT carry the substitution-rephrase advice; got %q", denied)
	}
	if strings.Contains(denied, "denied by user") {
		t.Fatalf("auto-deny must never claim a user denied it; got %q", denied)
	}
	// The correlated operator diagnostic fired.
	line, ok := diag.findLine("auto-denied")
	if !ok {
		t.Fatalf("expected the auto-deny operator diagnostic; got %+v", diag.base().lines)
	}
	if line.argValue("agent") == nil {
		t.Fatalf("auto-deny diagnostic must be agent-correlated; args=%v", line.args)
	}
}

// TestFlooredConfiguredAllowExecutesWithoutSurfacing (issue #32 (c)): a
// substitution-floored command — READ-ONLY inner (`$(ls)`), NON-read-only outer
// (`go generate`, so A1 cannot clear it) — covered by a CONFIGURED subagent
// Allow resolves AllowOnce and executes — no surfaced ask, no auto-deny — even
// on a HEADLESS parent with a NON-isolated child (so neither surfacing nor A2
// could have cleared it; the configured allow did). The inner MUST be
// positively read-only: the hidden-inner adversarial twin
// (TestAdversarialConfiguredAllowHiddenInnerStillGated) pins the complement.
func TestFlooredConfiguredAllowExecutesWithoutSurfacing(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go generate $(ls)"}`)),
		mockllm.TextTurn("child done"),
	)
	child := configChildEngine(childLLM, bash,
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go generate*", Effect: governance.Allow, Audience: governance.AudienceSubagent})
	task := agent.NewSubagentTool(child) // NO forker: not isolated, A2 unavailable.

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)}) // headless
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 1 || !strings.Contains(got[0], "go generate $(ls)") {
		t.Fatalf("a floored configured-allow command (read-only inner) must execute without surfacing; ran=%v", got)
	}
	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatalf("no ask should surface for a floored configured allow")
		}
	}
}

// TestAdversarialConfiguredAllowHiddenInnerStillGated (issue #32 ship-blocker
// bound, ADVERSARIAL): a configured subagent allow on `go test*` must NOT clear
// `go test $(touch SAFE_MARKER)` — the configured Allow vouches only for the
// OUTER literal; the hidden inner is not read-only, so the floored ask stays
// unresolved. Headless ⇒ auto-denied, never executed (even ISOLATED — A2 also
// refuses, the inner is not worktree-safe); interactive ⇒ it SURFACES and a
// deny keeps it unexecuted. Innocuous stand-ins per the no-destructive-literals
// rule; asserted on the EFFECT (the command never ran).
func TestAdversarialConfiguredAllowHiddenInnerStillGated(t *testing.T) {
	allowRule := governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Allow, Audience: governance.AudienceSubagent}
	const cmd = `{"command":"go test $(touch SAFE_MARKER)"}`

	t.Run("headless: auto-denied, never executed", func(t *testing.T) {
		bash := &fakeShell{}
		childLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("k1", "Shell", cmd)),
			mockllm.TextTurn("child adapted"),
		)
		child := configChildEngine(childLLM, bash, allowRule)
		task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))
		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
			mockllm.TextTurn("parent done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		_ = drainWithTimeout(t, r)
		if got := bash.ran(); len(got) != 0 {
			t.Fatalf("a configured outer allow must NOT auto-run a hidden non-read-only inner; ran=%v", got)
		}
	})
	t.Run("interactive: surfaces, deny keeps it unexecuted", func(t *testing.T) {
		bash := &fakeShell{}
		childLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("k1", "Shell", cmd)),
			mockllm.TextTurn("child adapted"),
		)
		child := configChildEngine(childLLM, bash, allowRule)
		task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))
		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
			mockllm.TextTurn("parent done"),
		)
		e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		var sawAsk bool
		_ = drainApproving(r, session.VerdictDeny, func(ev session.Event) {
			if ev.Type == session.EvPermissionAsk {
				sawAsk = true
			}
		})
		if !sawAsk {
			t.Fatalf("the hidden-inner command should SURFACE on an interactive parent")
		}
		if got := bash.ran(); len(got) != 0 {
			t.Fatalf("denied surfaced ask must not execute; ran=%v", got)
		}
	})
}

// TestAdversarialSubagentAllowGitPushStillGated (issue #32 (d), ADVERSARIAL): a
// configured subagent allow on `git push*` must NOT clear `git push $(x)` — the
// escape bound (flooredAllowSafe -> escapeRejectionsFree) refuses the worktree-escape verb, so the
// floored ask stays unresolved: headless ⇒ auto-denied, never executed, even
// for an ISOLATED child.
func TestAdversarialSubagentAllowGitPushStillGated(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"git push $(x)"}`)),
		mockllm.TextTurn("child adapted"),
	)
	child := configChildEngine(childLLM, bash,
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "git push*", Effect: governance.Allow, Audience: governance.AudienceSubagent})
	// Isolated AND headless: neither A2 (escape verb) nor the floored-allow bit
	// (escape bound) may clear it, and there is no human to surface to.
	task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("a configured allow must NOT clear a worktree-escape substitution; ran=%v", got)
	}
}

// TestBareFloorChildSubstitutionAskStillSurfaces is the GUARD for the exact
// regression the issue-#32 work once introduced (exposed as a test hang): a
// child under the DEFAULT posture — the bare allow-all FLOOR
// (permpolicy.AllowAllFloorRules), NO configured rules — must still SURFACE its
// substitution-floored Shell ask to an interactive parent. The blanket floor
// allow-all must never register as a "configured Allow" and trip
// FlooredConfiguredAllow into a silent auto-approve. Asserted POSITIVELY (the
// ask event is observed, and the command runs only after the approval), not via
// a drain timeout.
func TestBareFloorChildSubstitutionAskStillSurfaces(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child done"),
	)
	// bashChildEngine IS the canonical bare-floor fixture (AllowAllFloorRules).
	child := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var sawAsk, ranBeforeAsk bool
	_ = drainApproving(r, session.VerdictAllowOnce, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			sawAsk = true
			if len(bash.ran()) != 0 {
				ranBeforeAsk = true
			}
		}
	})
	if !sawAsk {
		t.Fatalf("a bare-floor child's substitution ask must SURFACE — the floor allow-all must not auto-approve it (FlooredConfiguredAllow misfire)")
	}
	if ranBeforeAsk {
		t.Fatalf("the command executed before the surfaced ask was approved — the floor allow-all auto-approved it")
	}
	if got := bash.ran(); len(got) != 1 {
		t.Fatalf("the approved ask should run the command exactly once; ran=%v", got)
	}
}

// TestAdversarialSubagentAllowGitPushSurfacesWhenInteractive is the interactive
// half of (d): the gated escape command SURFACES to the human (it is approvable
// — just never auto-approvable) and a DENY keeps it unexecuted.
func TestAdversarialSubagentAllowGitPushSurfacesWhenInteractive(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"git push $(x)"}`)),
		mockllm.TextTurn("child adapted"),
	)
	child := configChildEngine(childLLM, bash,
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "git push*", Effect: governance.Allow, Audience: governance.AudienceSubagent})
	task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var sawAsk bool
	_ = drainApproving(r, session.VerdictDeny, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk {
			sawAsk = true
		}
	})
	if !sawAsk {
		t.Fatalf("the escape-gated command should SURFACE on an interactive parent")
	}
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("denied surfaced ask must not execute; ran=%v", got)
	}
}

// TestCancelChildWhileParkedOnConfiguredAsk drives the cancel-while-parked
// unwind for an ask that parked via the NEW skip-A2 branch (a CONFIGURED
// subagent Ask on an otherwise isolation-approvable command): the child
// surfaces and parks, CancelChild unwinds it to a cancelled-by-user note,
// the surfaced askID is retracted on the parent stream, and the command never
// executes. This pins the seal-race interplay for the issue-#32 landing —
// exactly where the floor-scope fixture hang lived. It does NOT touch
// TestCancelChildWhileParkedOnAsk (the substitution-floor parked path).
func TestCancelChildWhileParkedOnConfiguredAsk(t *testing.T) {
	bash := &fakeShell{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go test ./..."}`)),
		mockllm.TextTurn("child: never reached"),
	)
	// Configured subagent Ask on go test*: without it, the ISOLATED child's
	// `go test ./...` would A2-auto-approve and nothing would park.
	child := configChildEngine(childLLM, bash,
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Ask, Audience: governance.AudienceSubagent})
	task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run tests"}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	askCh := make(chan string, 1)
	var childID string
	var cancelOK bool
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		<-askCh
		cancelOK = r.CancelChild(childID)
	}()

	var surfacedAskID string
	var retracts []string
	evs := drainObserving(t, r, func(ev session.Event) {
		switch {
		case ev.Type == session.EvSubagentStart && ev.Subagent != nil:
			childID = ev.Subagent.ChildID
		case ev.Type == session.EvPermissionAsk && ev.Ask != nil:
			surfacedAskID = ev.Ask.AskID
			select {
			case askCh <- ev.Ask.AskID:
			default:
			}
		case ev.Type == session.EvPermissionRetract && ev.Ask != nil:
			retracts = append(retracts, ev.Ask.AskID)
		}
	})
	cancelDone.Wait()

	if !cancelOK {
		t.Fatalf("CancelChild must return true for a child parked on a configured ask")
	}
	if surfacedAskID == "" {
		t.Fatalf("expected the configured ask to SURFACE (skip-A2 branch) and park")
	}
	if len(retracts) != 1 || retracts[0] != surfacedAskID {
		t.Fatalf("expected exactly one permission.retract for the surfaced askID %q, got %v", surfacedAskID, retracts)
	}
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("the cancelled parked command must never execute; ran=%v", got)
	}
	res := subagentResultOf(t, evs)
	if res == nil || res.IsError || !strings.Contains(res.Content, "[subagent cancelled by user]") {
		t.Fatalf("cancelled child must yield the success-with-note result; got %+v", res)
	}
	if got := lastResult(t, evs); got.Stop == session.StopError || got.Stop == session.StopCancelled {
		t.Fatalf("the PARENT run must complete cleanly after the per-child cancel, got stop %q", got.Stop)
	}
}

// TestTeamMemberConfiguredAskSurfaces pins family parity (issue #32): a TEAM
// MEMBER posture honours a configured subagent Ask exactly like a Subagent
// child — the isolated member's otherwise A2-approvable `go test ./...` SKIPS
// the isolation auto-approve, surfaces to the interactive parent, and runs only
// after approval.
func TestTeamMemberConfiguredAskSurfaces(t *testing.T) {
	leadShell := &fakeShell{}
	memberPolicy := permpolicy.NewPolicy(
		append(permpolicy.AllowAllFloorRules(),
			governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Ask, Audience: governance.AudienceSubagent}),
		nil, governance.WithAudience(governance.AudienceSubagent))

	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(leadShell)
		llm := mockllm.New(
			mockllm.ToolCallTurn(toolCall("a1", "Shell", `{"command":"go test ./..."}`)),
			mockllm.TextTurn("lead: tests run"),
			mockllm.TextTurn("CONSOLIDATED: done"),
		)
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: memberPolicy, Hooks: noopHooks{}, Model: "m"})
		// IsolateReadOnly: the member is ISOLATED, so absent the configured Ask
		// its `go test ./...` would A2-auto-approve and never surface.
		return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
	}
	tt := agent.NewTeamTool(factory, agent.WithTeamToolReadOnlyForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"run the tests","members":[{"name":"lead","role":"run go test"}]}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, tt)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var sawAsk, ranBeforeAsk bool
	evs := drainApproving(r, session.VerdictAllowOnce, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			sawAsk = true
			if len(leadShell.ran()) != 0 {
				ranBeforeAsk = true
			}
		}
	})
	if !sawAsk {
		t.Fatalf("a team member's configured Ask must SURFACE (A2 skipped), never auto-approve")
	}
	if ranBeforeAsk {
		t.Fatalf("the member command ran before the surfaced ask was approved")
	}
	if got := leadShell.ran(); len(got) != 1 {
		t.Fatalf("approval should run the member command exactly once; ran=%v", got)
	}
	if res := lastResult(t, evs); res.Stop == session.StopError {
		t.Fatalf("team run failed: %q", res.Error)
	}
}

// TestChildAskYoloNonSubstitutionAutoApproves is the agent-layer e2e over the
// EXACT ruleset production childRules(Config{AllowAllTools:true}) produces (a
// ScopeCLI AudienceSubagent allow-all PREPENDED to the AllowAllFloorRules floor,
// pinned AudienceSubagent). It proves the --yolo child ruleset is BENIGN at the
// loop level: a plain non-read-only, non-substitution Shell command runs on a
// HEADLESS (non-interactive) parent with no surfaced ask and no auto-deny (the
// blanket child floor allows it — children have no mutate-ask floor), AND the
// paired substitution variant (non-read-only inner) STILL floors → headless ⇒
// auto-denied, never executed, since the substitution-floor loosening stays
// MAIN-only. Together they pin that the symmetric yolo rule does not weaken the
// child substitution floor. Innocuous stand-ins per the no-destructive-literals
// rule.
func TestChildAskYoloNonSubstitutionAutoApproves(t *testing.T) {
	yoloChildPolicy := func() port.PermissionPolicy {
		rules := append([]governance.Rule{{Scope: governance.ScopeCLI, Effect: governance.Allow, Audience: governance.AudienceSubagent}},
			permpolicy.AllowAllFloorRules()...)
		return permpolicy.NewPolicy(rules, nil, governance.WithAudience(governance.AudienceSubagent))
	}

	t.Run("plain mutate auto-approves without surfacing", func(t *testing.T) {
		bash := &fakeShell{}
		childLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"python3 script.py"}`)),
			mockllm.TextTurn("child done"),
		)
		child := agent.NewEngine(agent.Deps{LLM: childLLM, Catalog: bashCatalog(bash), Policy: yoloChildPolicy(), Model: "child-model"})
		task := agent.NewSubagentTool(child) // headless parent below ⇒ no surfacing path.

		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
			mockllm.TextTurn("parent done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)}) // headless
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		evs := drainWithTimeout(t, r)

		if got := bash.ran(); len(got) != 1 || !strings.Contains(got[0], "python3 script.py") {
			t.Fatalf("--yolo child should auto-approve a plain mutate without surfacing; ran=%v", got)
		}
		for _, ev := range evs {
			if ev.Type == session.EvPermissionAsk {
				t.Fatalf("no ask should surface for a --yolo child plain mutate")
			}
		}
	})

	t.Run("substitution with bad inner still auto-denies", func(t *testing.T) {
		bash := &fakeShell{}
		childLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"cat $(zap)"}`)),
			mockllm.TextTurn("child adapted"),
		)
		child := agent.NewEngine(agent.Deps{LLM: childLLM, Catalog: bashCatalog(bash), Policy: yoloChildPolicy(), Model: "child-model"})
		task := agent.NewSubagentTool(child)

		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
			mockllm.TextTurn("parent done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)}) // headless
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		evs := drainWithTimeout(t, r)

		if got := bash.ran(); len(got) != 0 {
			t.Fatalf("--yolo child substitution (bad inner) must auto-deny, not execute (loosening is main-only); ran=%v", got)
		}
		for _, ev := range evs {
			if ev.Type == session.EvPermissionAsk {
				t.Fatalf("headless parent has no surfacing path; the ask must auto-deny silently")
			}
		}
	})
}
