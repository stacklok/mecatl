package agent_test

import (
	"context"
	"errors"
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

// adjOutcome is one scripted reviewer response.
type adjOutcome struct {
	review agent.ChildAskReview
	err    error
}

// scriptedAdjudicator is a deterministic agent.ChildAskReviewer: it returns
// the scripted outcomes in order (and a default deny once exhausted) and counts
// invocations, so a test can assert exactly when the reviewer was (not)
// consulted — the breaker / configured-rules / interactive gates.
type scriptedAdjudicator struct {
	mu       sync.Mutex
	script   []adjOutcome
	calls    int
	isolated []bool
	roots    []session.SessionID
}

func (s *scriptedAdjudicator) Review(ctx context.Context, req agent.ChildAskReviewRequest) (agent.ChildAskReview, session.AuxiliaryUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	s.isolated = append(s.isolated, req.Isolated)
	root, _ := port.RootSessionIDFromContext(ctx)
	s.roots = append(s.roots, root)
	if i < len(s.script) {
		return s.script[i].review, session.AuxiliaryUsage{}, s.script[i].err
	}
	return agent.ChildAskReview{Allowed: false, Reason: "scripted default deny"}, session.AuxiliaryUsage{}, nil
}

func (s *scriptedAdjudicator) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *scriptedAdjudicator) capturedRoots() []session.SessionID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.SessionID(nil), s.roots...)
}

func allow(reason string) adjOutcome {
	return adjOutcome{review: agent.ChildAskReview{Allowed: true, Reason: reason}}
}

func deny(reason string) adjOutcome {
	return adjOutcome{review: agent.ChildAskReview{Allowed: false, Reason: reason}}
}

// substitutionAskTurns scripts a child that issues n substitution-floored Shell
// calls (`cat $(zap)` — read-only outer, unknown inner, so neither A1 nor A2 nor
// any configured rule resolves it) and then finishes.
func substitutionAskTurns(n int) []mockllm.Turn {
	turns := make([]mockllm.Turn, 0, n+1)
	for i := 0; i < n; i++ {
		turns = append(turns, mockllm.ToolCallTurn(toolCall("k"+string(rune('1'+i)), "Shell", `{"command":"cat $(zap)"}`)))
	}
	return append(turns, mockllm.TextTurn("child done"))
}

// TestAdjudicatorAllowRunsHeadlessSubagentCommand: a HEADLESS parent with the
// reviewer wired — the child's substitution-floored Shell ask is adjudicated
// ALLOW and EXECUTES, with NOTHING learned: a second identical command asks (and
// is adjudicated) again, proving the verdict was AllowOnce, never a learned
// rule. The allow rides the child-ask diagnostic chokepoint as a correlated
// INFO with the decision/verdict fields.
func TestAdjudicatorAllowRunsHeadlessSubagentCommand(t *testing.T) {
	bash := &fakeShell{}
	child := shellChildEngine(mockllm.New(substitutionAskTurns(2)...), bash)
	task := agent.NewSubagentTool(child)

	stub := &scriptedAdjudicator{script: []adjOutcome{allow("read-only"), allow("read-only")}}
	diag := newRecordingDiag()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task),
		ChildAskReviewer: stub, Diagnostics: diag}) // headless
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 2 || !strings.Contains(got[0], "cat $(zap)") {
		t.Fatalf("an adjudicated allow must execute both commands; ran=%v", got)
	}
	if stub.count() != 2 {
		t.Fatalf("each identical ask must be adjudicated again (AllowOnce, nothing learned); calls=%d", stub.count())
	}
	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatalf("a headless adjudicated ask must never surface on the parent stream")
		}
	}
	line, ok := diag.findLine("allowed by the automated policy reviewer")
	if !ok {
		t.Fatalf("expected the adjudicated-allow operator INFO; got %+v", diag.base().lines)
	}
	if line.argValue("decision") != "reviewed-allow" || line.argValue("verdict_reason") != "read-only" {
		t.Fatalf("allow INFO must carry decision/verdict_reason; args=%v", line.args)
	}
	if res := lastResult(t, evs); res.Stop == session.StopError {
		t.Fatalf("run failed: %q", res.Error)
	}
}

// TestAdjudicatorAllowRunsTeamMemberCommand pins family parity: a TEAM MEMBER's
// substitution-floored ask routes through the SAME resolveChildAsk chokepoint,
// so the headless reviewer adjudicates it identically.
func TestAdjudicatorAllowRunsTeamMemberCommand(t *testing.T) {
	leadShell := &fakeShell{}
	memberPolicy := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil,
		governance.WithAudience(governance.AudienceSubagent))

	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(leadShell)
		llm := mockllm.New(
			mockllm.ToolCallTurn(toolCall("a1", "Shell", `{"command":"cat $(zap)"}`)),
			mockllm.TextTurn("lead: inspected"),
			mockllm.TextTurn("CONSOLIDATED: done"),
		)
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: memberPolicy, Hooks: noopHooks{}, Model: "m"})
		// IsolateReadOnly + forker: the read-only-member mutating-tool backstop
		// otherwise strips the (non-read-only) fakeShell. `cat $(zap)` is NOT
		// IsolationApprovable (unknown inner), so the ask still reaches the reviewer.
		return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
	}
	tt := agent.NewTeamTool(factory, agent.WithTeamToolReadOnlyForker(&recordingSubagentForker{}))

	stub := &scriptedAdjudicator{script: []adjOutcome{allow("read-only")}}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"inspect","members":[{"name":"lead","role":"inspect the tree"}]}`)),
		mockllm.TextTurn("parent: done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, tt), ChildAskReviewer: stub}) // headless
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainWithTimeout(t, r)

	if got := leadShell.ran(); len(got) != 1 || !strings.Contains(got[0], "cat $(zap)") {
		t.Fatalf("the member's adjudicated allow must execute; ran=%v", got)
	}
	if stub.count() != 1 {
		t.Fatalf("the member ask must consult the reviewer exactly once; calls=%d", stub.count())
	}
	if res := lastResult(t, evs); res.Stop == session.StopError {
		t.Fatalf("team run failed: %q", res.Error)
	}
}

// TestAdjudicatorDenyCarriesReviewedMessage: a reviewed DENY reaches the child
// MODEL as childReviewedDenyMessage verbatim — naming the automated reviewer and
// carrying its CLAMPED rationale (control bytes scrubbed) — never "denied by
// user" and never the substitution-rephrase advice of the not-reviewed path.
func TestAdjudicatorDenyCarriesReviewedMessage(t *testing.T) {
	bash := &fakeShell{}
	rec := &requestRecorder{}
	childLLM := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(rec.observe)},
		substitutionAskTurns(1)...)
	child := shellChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(child)

	stub := &scriptedAdjudicator{script: []adjOutcome{deny("touches the\x1bnetwork")}}
	diag := newRecordingDiag()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task),
		ChildAskReviewer: stub, Diagnostics: diag})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("a reviewed deny must not execute; ran=%v", got)
	}
	var denied string
	for _, c := range rec.toolResultContents() {
		if strings.Contains(c, "not permitted in a non-interactive subagent shell") {
			denied = c
		}
	}
	if denied == "" {
		t.Fatalf("expected the child to see the reviewed deny; tool results: %v", rec.toolResultContents())
	}
	// The verbatim reviewed-deny shape: reviewer named, rationale clamped (the
	// ESC byte became a space), actionable advice present.
	if !strings.Contains(denied, "an automated policy reviewer declined this command (touches the network)") {
		t.Fatalf("reviewed deny must carry the reviewer phrasing + clamped rationale; got %q", denied)
	}
	if !strings.Contains(denied, "rephrase to a read-only form or report back that approval is required") {
		t.Fatalf("reviewed deny must carry the actionable suffix; got %q", denied)
	}
	if strings.Contains(denied, "denied by user") || strings.Contains(denied, "rephrase to avoid command substitution") {
		t.Fatalf("reviewed deny must not reuse the user-deny or not-reviewed phrasing; got %q", denied)
	}
	line, ok := diag.findLine("auto-denied")
	if !ok {
		t.Fatalf("expected the deny INFO at the child-ask chokepoint; got %+v", diag.base().lines)
	}
	if line.argValue("decision") != "reviewed-deny" || line.argValue("verdict_reason") != "touches the network" {
		t.Fatalf("deny INFO must carry decision/verdict_reason (clamped); args=%v", line.args)
	}
}

// TestAdjudicatorErrorFallsBackToPlainAutoDeny: a reviewer ERROR is NOT a
// verdict — the ask falls through to the EXISTING childAutoDenyMessage, with no
// false "reviewer declined" claim.
func TestAdjudicatorErrorFallsBackToPlainAutoDeny(t *testing.T) {
	bash := &fakeShell{}
	rec := &requestRecorder{}
	childLLM := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(rec.observe)},
		substitutionAskTurns(1)...)
	child := shellChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(child)

	stub := &scriptedAdjudicator{script: []adjOutcome{{err: errors.New("reviewer wedged")}}}
	diag := newRecordingDiag()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task), ChildAskReviewer: stub, Diagnostics: diag})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("a reviewer error must keep the call denied; ran=%v", got)
	}
	var denied string
	for _, c := range rec.toolResultContents() {
		if strings.Contains(c, "not permitted in a non-interactive subagent shell") {
			denied = c
		}
	}
	if denied == "" {
		t.Fatalf("expected the plain auto-deny; tool results: %v", rec.toolResultContents())
	}
	if !strings.Contains(denied, "rephrase to avoid command substitution") {
		t.Fatalf("a not-reviewed deny must keep the EXISTING message; got %q", denied)
	}
	if strings.Contains(denied, "policy reviewer") {
		t.Fatalf("a not-reviewed deny must not claim a reviewer declined; got %q", denied)
	}
	// The distinct reviewer-FAILURE INFO fired (so a flaky reviewer is visible),
	// carrying the clamped err and command.
	line, ok := diag.findLine("failed to produce a verdict")
	if !ok {
		t.Fatalf("expected the reviewer-failure INFO; got %+v", diag.base().lines)
	}
	if line.argValue("err") == nil || line.argValue("command") == nil {
		t.Fatalf("reviewer-failure INFO must carry err + command; args=%v", line.args)
	}
}

// TestAdjudicatorBreakerOpensAfterConsecutiveDenies: with MaxDenies=2, the third
// (and every later) ask in the run skips the reviewer entirely — call count
// stays 2 — and falls through to the plain auto-deny. The child gets relaxed
// failure limits so the run, not the child's failure cap, is what bounds it.
func TestAdjudicatorBreakerOpensAfterConsecutiveDenies(t *testing.T) {
	bash := &fakeShell{}
	child := shellChildEngine(mockllm.New(substitutionAskTurns(4)...), bash)
	task := agent.NewSubagentTool(child, agent.WithChildLimits(session.Limits{
		MaxTurns: 50, MaxToolCalls: 200, MaxConsecutiveFailures: 10,
	}))

	stub := &scriptedAdjudicator{} // script empty: every consulted ask default-denies
	diag := newRecordingDiag()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task),
		ChildAskReviewer: stub, ChildAskReviewMaxDenies: 2, Diagnostics: diag})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drainWithTimeout(t, r)

	if got := stub.count(); got != 2 {
		t.Fatalf("the breaker must stop consulting the reviewer after 2 consecutive denies; calls=%d", got)
	}
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("nothing may execute; ran=%v", got)
	}
	// The one-time breaker-opened INFO fired exactly once across the run.
	opened := 0
	for _, l := range diag.base().lines {
		if strings.Contains(l.msg, "circuit breaker opened") {
			opened++
		}
	}
	if opened != 1 {
		t.Fatalf("the breaker-opened INFO must fire exactly once per run; got %d", opened)
	}
}

// TestAdjudicatorAbstainDoesNotCountTowardBreaker: a reviewer that ABSTAINS
// (ErrNotReviewable) on every ask falls through to auto-deny WITHOUT opening the
// breaker — every ask is still consulted (the stub is called once per ask), so a
// reviewer that legitimately cannot judge some asks never silences review for the
// rest. Contrast TestAdjudicatorBreakerOpensAfterConsecutiveDenies (real denies
// DO open it). Nothing executes either way.
func TestAdjudicatorAbstainDoesNotCountTowardBreaker(t *testing.T) {
	bash := &fakeShell{}
	child := shellChildEngine(mockllm.New(substitutionAskTurns(4)...), bash)
	task := agent.NewSubagentTool(child, agent.WithChildLimits(session.Limits{
		MaxTurns: 50, MaxToolCalls: 200, MaxConsecutiveFailures: 10,
	}))

	stub := &scriptedAdjudicator{script: []adjOutcome{
		{err: agent.ErrNotReviewable}, {err: agent.ErrNotReviewable},
		{err: agent.ErrNotReviewable}, {err: agent.ErrNotReviewable},
	}}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task),
		ChildAskReviewer: stub, ChildAskReviewMaxDenies: 2})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drainWithTimeout(t, r)

	if got := stub.count(); got != 4 {
		t.Fatalf("an abstention must NOT open the breaker — every ask stays consulted; calls=%d (want 4)", got)
	}
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("an abstention falls through to auto-deny; ran=%v", got)
	}
}

// TestAdjudicatorAllowResetsBreaker: an ALLOW resets the consecutive-deny count,
// so the breaker opens only after MaxDenies denies in a row.
func TestAdjudicatorAllowResetsBreaker(t *testing.T) {
	bash := &fakeShell{}
	child := shellChildEngine(mockllm.New(substitutionAskTurns(5)...), bash)
	task := agent.NewSubagentTool(child, agent.WithChildLimits(session.Limits{
		MaxTurns: 50, MaxToolCalls: 200, MaxConsecutiveFailures: 10,
	}))

	// deny(1) → allow(reset) → deny(1) → deny(2 = open). The 5th ask must NOT
	// invoke the stub (breaker open).
	stub := &scriptedAdjudicator{script: []adjOutcome{deny("d1"), allow("ok"), deny("d2"), deny("d3")}}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task),
		ChildAskReviewer: stub, ChildAskReviewMaxDenies: 2})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drainWithTimeout(t, r)

	if got := stub.count(); got != 4 {
		t.Fatalf("the allow must reset the breaker (4 consultations, 5th skipped); calls=%d", got)
	}
	if got := bash.ran(); len(got) != 1 {
		t.Fatalf("exactly the allowed command may execute; ran=%v", got)
	}
}

// TestAdjudicatorConfiguredRulesStillWin: configured permission rules are NEVER
// delegated to the reviewer — a configured Ask auto-denies rule-oriented with
// the stub UNCALLED; a configured Deny resolves in the fold (no ask at all);
// FlooredConfiguredAllow and A2 auto-approve upstream of the reviewer.
func TestAdjudicatorConfiguredRulesStillWin(t *testing.T) {
	parentTurns := func() []mockllm.Turn {
		return []mockllm.Turn{
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
			mockllm.TextTurn("parent done"),
		}
	}

	t.Run("configured Ask: rule-oriented deny, stub uncalled", func(t *testing.T) {
		bash := &fakeShell{}
		rec := &requestRecorder{}
		childLLM := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(rec.observe)},
			mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go test ./..."}`)),
			mockllm.TextTurn("child adapted"),
		)
		child := configChildEngine(childLLM, bash,
			governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Ask, Audience: governance.AudienceSubagent})
		task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))
		stub := &scriptedAdjudicator{script: []adjOutcome{allow("would have allowed")}}
		e := newEngine(agent.Deps{LLM: mockllm.New(parentTurns()...), Catalog: catalogWith(t, task), ChildAskReviewer: stub})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		_ = drainWithTimeout(t, r)

		if stub.count() != 0 {
			t.Fatalf("a CONFIGURED Ask must never consult the reviewer; calls=%d", stub.count())
		}
		if got := bash.ran(); len(got) != 0 {
			t.Fatalf("the gated command must not execute; ran=%v", got)
		}
		var denied string
		for _, c := range rec.toolResultContents() {
			if strings.Contains(c, "not permitted in a non-interactive subagent shell") {
				denied = c
			}
		}
		if !strings.Contains(denied, "configured permission rule") {
			t.Fatalf("configured-ask deny must stay rule-oriented; got %q", denied)
		}
	})

	t.Run("configured Deny: resolves in the fold, no ask, stub uncalled", func(t *testing.T) {
		bash := &fakeShell{}
		childLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go test ./..."}`)),
			mockllm.TextTurn("child adapted"),
		)
		child := configChildEngine(childLLM, bash,
			governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Deny, Audience: governance.AudienceSubagent})
		task := agent.NewSubagentTool(child)
		stub := &scriptedAdjudicator{}
		e := newEngine(agent.Deps{LLM: mockllm.New(parentTurns()...), Catalog: catalogWith(t, task), ChildAskReviewer: stub})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		_ = drainWithTimeout(t, r)

		if stub.count() != 0 || len(bash.ran()) != 0 {
			t.Fatalf("a configured Deny resolves in the fold; calls=%d ran=%v", stub.count(), bash.ran())
		}
	})

	t.Run("FlooredConfiguredAllow: auto-approves upstream, stub uncalled", func(t *testing.T) {
		bash := &fakeShell{}
		childLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go generate $(ls)"}`)),
			mockllm.TextTurn("child done"),
		)
		child := configChildEngine(childLLM, bash,
			governance.Rule{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go generate*", Effect: governance.Allow, Audience: governance.AudienceSubagent})
		task := agent.NewSubagentTool(child)
		stub := &scriptedAdjudicator{}
		e := newEngine(agent.Deps{LLM: mockllm.New(parentTurns()...), Catalog: catalogWith(t, task), ChildAskReviewer: stub})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		_ = drainWithTimeout(t, r)

		if stub.count() != 0 {
			t.Fatalf("a floored configured allow resolves before the reviewer; calls=%d", stub.count())
		}
		if got := bash.ran(); len(got) != 1 {
			t.Fatalf("the floored-allow command must execute; ran=%v", got)
		}
	})

	t.Run("A2 isolation auto-approve: resolves upstream, stub uncalled", func(t *testing.T) {
		bash := &fakeShell{}
		childLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go test ./..."}`)),
			mockllm.TextTurn("child done"),
		)
		child := shellChildEngine(childLLM, bash)
		task := agent.NewSubagentTool(child, agent.WithChildForker(&recordingSubagentForker{}))
		stub := &scriptedAdjudicator{}
		e := newEngine(agent.Deps{LLM: mockllm.New(parentTurns()...), Catalog: catalogWith(t, task), ChildAskReviewer: stub})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		_ = drainWithTimeout(t, r)

		if stub.count() != 0 {
			t.Fatalf("A2 resolves before the reviewer; calls=%d", stub.count())
		}
		if got := bash.ran(); len(got) != 1 {
			t.Fatalf("the A2-approvable command must execute; ran=%v", got)
		}
	})
}

// TestAdjudicatorNotConsultedWhenInteractive: an INTERACTIVE parent surfaces the
// ask to the human; the reviewer is never consulted (surface wins).
func TestAdjudicatorNotConsultedWhenInteractive(t *testing.T) {
	bash := &fakeShell{}
	child := shellChildEngine(mockllm.New(substitutionAskTurns(1)...), bash)
	task := agent.NewSubagentTool(child)

	stub := &scriptedAdjudicator{script: []adjOutcome{allow("would have allowed")}}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task), ChildAskReviewer: stub})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var sawAsk bool
	_ = drainApproving(r, session.VerdictAllowOnce, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk {
			sawAsk = true
		}
	})
	if !sawAsk {
		t.Fatalf("an interactive parent must SURFACE the ask, reviewer or not")
	}
	if stub.count() != 0 {
		t.Fatalf("the reviewer must not be consulted when a human approver is attached; calls=%d", stub.count())
	}
	if got := bash.ran(); len(got) != 1 {
		t.Fatalf("the human approval should run the command exactly once; ran=%v", got)
	}
}

// TestAdjudicatorIsolationBitThreaded: the reviewer receives the child posture's
// REAL isolation bit (an isolated forked child reviews as isolated=true; a
// base-sharing child as false), so the O6 prompt line is honest.
func TestAdjudicatorIsolationBitThreaded(t *testing.T) {
	run := func(t *testing.T, forked bool) bool {
		t.Helper()
		bash := &fakeShell{}
		child := shellChildEngine(mockllm.New(substitutionAskTurns(1)...), bash)
		opts := []agent.SubagentOption{}
		if forked {
			opts = append(opts, agent.WithChildForker(&recordingSubagentForker{}))
		}
		task := agent.NewSubagentTool(child, opts...)
		stub := &scriptedAdjudicator{script: []adjOutcome{deny("no")}}
		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
			mockllm.TextTurn("parent done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task), ChildAskReviewer: stub})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		_ = drainWithTimeout(t, r)
		stub.mu.Lock()
		defer stub.mu.Unlock()
		if len(stub.isolated) != 1 {
			t.Fatalf("expected exactly one adjudication; got %d", len(stub.isolated))
		}
		return stub.isolated[0]
	}
	if !run(t, true) {
		t.Fatalf("a forked child must review as isolated=true")
	}
	if run(t, false) {
		t.Fatalf("a base-sharing child must review as isolated=false")
	}
}
