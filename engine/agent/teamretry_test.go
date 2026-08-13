package agent_test

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// teamretry_test.go covers the BOUNDED MEMBER RETRY — the last acceptance bullet of
// issue #318 ("a team member that hits one transient stall still participates in later
// rounds"), which ADR 0077 shipped its recovery half of and explicitly deferred. See
// docs/adr/0077-resume-a-failed-subagent.md.
//
// The three mechanisms it pins, all in engine/agent/teamsupervisor.go:
//
//   - runTurn leaves a RECOVERED StopError member SCHEDULABLE while it is under
//     WithMemberErrorRetries, instead of benching it (stopped + StopReasonError).
//   - the retried member RELEASES its in-progress task claim, so the work is not
//     stranded behind the member that just failed at it, and planRound force-schedules
//     it once even with no message and no claimable task.
//   - MemberOutcome.ErrorRounds keeps the disposition honest: a member that failed a
//     round and then finished is DispositionDone with no Reason, so the count is the
//     only signal the run was not clean.

// TestSupervisorRetriedMemberContributesInLaterRound is the HEADLINE case. A member's
// round 0 ends in a run-level error; the supervisor recovers its session, releases
// nothing (it holds no task), leaves it schedulable, and force-schedules one retry turn
// in which the member does real, observable work — it appends to the team findings
// ledger, the deterministic contribution channel.
//
// The two tiers run the SAME script so the retry is the only variable: with retry
// DISABLED the member is benched at round 0 and the finding never appears, which is what
// gives the enabled case its teeth (a supervisor that scheduled failed members
// regardless, or a test that would pass on any wiring, could not produce this split).
func TestSupervisorRetriedMemberContributesInLaterRound(t *testing.T) {
	tests := []struct {
		name        string
		retries     int
		wantFinding bool
		wantStopped bool
		wantRounds  int
	}{
		{name: "one retry (default): the member comes back and contributes",
			retries: 1, wantFinding: true, wantStopped: false, wantRounds: 2},
		{name: "retry disabled: the member is benched and contributes nothing",
			retries: 0, wantFinding: false, wantStopped: true, wantRounds: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tm := team.New("retry")
			rec := newPromptRecorder()
			scripts := map[string][]mockllm.Turn{
				"worker": {
					// Round 0: an empty turn carrying a terminal error stop → terminate() →
					// Fail() → StateFailed, the state only Recover returns to idle.
					mockllm.EmptyTurnWithStop(session.StopError),
					// The RETRY round: real work. A tool call keeps the turn loop going...
					mockllm.ToolCallTurn(session.NewToolCall("f1", "RecordFinding",
						json.RawMessage(`{"finding":"CONTRIBUTED AFTER THE STALL"}`))),
					// ...and this closes the retry round cleanly.
					mockllm.TextTurn("recovered and finished"),
				},
			}
			sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
				recordingFactory(t, tm, rec, scripts),
				agent.WithTeamGoal("goal"), agent.WithMaxRounds(5),
				agent.WithMemberErrorRetries(tc.retries))
			// No lead: the member's own disposition and the ledger are the whole oracle,
			// with no synthesis turn to launder either.
			mustAdd(t, sup, agent.MemberSpec{Name: "worker", InitialPrompt: "investigate"})

			out := sup.Run(context.Background(), nil)

			m := singleMember(t, out)
			if m.Stopped != tc.wantStopped {
				t.Fatalf("Stopped = %v, want %v (outcome %+v)", m.Stopped, tc.wantStopped, m)
			}
			if m.ErrorRounds != 1 {
				t.Errorf("ErrorRounds = %d, want 1 — the errored round is counted at BOTH tiers", m.ErrorRounds)
			}
			if out.Rounds != tc.wantRounds {
				t.Errorf("Rounds = %d, want %d", out.Rounds, tc.wantRounds)
			}

			var gotFinding bool
			for _, f := range out.Findings {
				if f.Member == "worker" && strings.Contains(f.Body, "CONTRIBUTED AFTER THE STALL") {
					gotFinding = true
				}
			}
			if gotFinding != tc.wantFinding {
				t.Fatalf("finding recorded = %v, want %v (findings %+v)", gotFinding, tc.wantFinding, out.Findings)
			}

			prompts := rec.turns("worker")
			if !tc.wantFinding {
				// Benched: exactly the one round-0 prompt ever reached the provider.
				if len(prompts) != 1 {
					t.Fatalf("a benched member must receive exactly 1 prompt, got %d: %q", len(prompts), prompts)
				}
				return
			}
			// The retry turn must SAY it is a retry. Without the note the member sees a
			// fresh prompt on top of a transcript that stops mid-thought and cannot tell a
			// crash from its own decision to stop.
			if len(prompts) < 2 {
				t.Fatalf("want at least 2 prompts (round 0 + the retry), got %d: %q", len(prompts), prompts)
			}
			if !strings.Contains(prompts[1], "your previous turn in this team run FAILED") {
				t.Errorf("the retry turn must carry the harness retry note; prompt was:\n%s", prompts[1])
			}
			if strings.Contains(prompts[0], "your previous turn in this team run FAILED") {
				t.Errorf("the FIRST (non-retry) turn must not carry the retry note:\n%s", prompts[0])
			}
		})
	}
}

// TestSupervisorBenchesMemberAtErrorRetryCap pins the BOUND. A member that errors in
// every round it is given is benched the moment its errored-round count EXCEEDS the cap,
// with the unchanged disposition (stopped + StopReasonError) and the honest count —
// and it runs exactly cap+1 rounds, never more.
//
// Sweeping the cap is what makes this a bound rather than a single data point: an
// off-by-one or a cap read from the wrong place shows up as the wrong round count at one
// of the three tiers.
func TestSupervisorBenchesMemberAtErrorRetryCap(t *testing.T) {
	for _, retries := range []int{0, 1, 2} {
		t.Run(capName(retries), func(t *testing.T) {
			tm := team.New("cap")
			// One more error turn than the member can possibly consume, so a member that
			// over-retried would NOT run out of script — it would keep erroring and the
			// round assertion below is what catches it.
			var turns []mockllm.Turn
			for i := 0; i < retries+3; i++ {
				turns = append(turns, mockllm.EmptyTurnWithStop(session.StopError))
			}
			prov := mockllm.New(turns...)
			sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
				memberFactory(t, tm, map[string]*mockllm.Provider{"worker": prov}),
				agent.WithMaxRounds(20), agent.WithMemberErrorRetries(retries))
			mustAdd(t, sup, agent.MemberSpec{Name: "worker", InitialPrompt: "go"})

			out := sup.Run(context.Background(), nil)

			m := singleMember(t, out)
			if m.Disposition != agent.DispositionStopped || m.Reason != agent.StopReasonError {
				t.Fatalf("disposition = %q/%q, want stopped/error", m.Disposition, m.Reason)
			}
			if want := retries + 1; m.ErrorRounds != want {
				t.Errorf("ErrorRounds = %d, want %d (cap %d ⇒ one benching round after %d retries)",
					m.ErrorRounds, want, retries, retries)
			}
			if want := retries + 1; out.Rounds != want {
				t.Errorf("Rounds = %d, want %d — the retry must be BOUNDED by the cap", out.Rounds, want)
			}
			if got := prov.Calls(); got != retries+1 {
				t.Errorf("provider calls = %d, want %d — a benched member must not be re-driven", got, retries+1)
			}
		})
	}
}

func capName(retries int) string {
	switch retries {
	case 0:
		return "retries=0 benches on the first errored round"
	case 1:
		return "retries=1 (default) benches on the second"
	default:
		return "retries=2 benches on the third"
	}
}

// alwaysFailingProvider is a port.LLMProvider whose every stream fails mid-flight — the
// PERMANENT analogue of the transient stall the retry exists for. mockllm cannot express
// it (an exhausted script yields an empty, benign turn, which the loop nudges rather than
// fails), and an "always fails" provider is what makes the termination proof independent
// of how long a script happens to be.
type alwaysFailingProvider struct{ calls atomic.Int64 }

func (*alwaysFailingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *alwaysFailingProvider) Stream(_ context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls.Add(1)
	return func(yield func(port.Chunk, error) bool) {
		yield(port.Chunk{}, errPermanentProviderOutage)
	}, nil
}

// errPermanentProviderOutage is the in-stream failure alwaysFailingProvider yields. It is
// a plain sentinel, not a wrapped transient class, so nothing in the loop can mistake it
// for something worth its own retry.
var errPermanentProviderOutage = errPermanent("provider is permanently unavailable")

type errPermanent string

func (e errPermanent) Error() string { return string(e) }

// TestSupervisorPermanentlyFailingMemberTerminates is the TERMINATION proof the bounded
// retry owes. A member whose provider ALWAYS fails must bench after its cap and the round
// loop must still reach quiescence — well short of the round cap, which is the backstop
// this must not be relying on.
//
// The round cap is set far above cap+1 deliberately: if the retry ever failed to bound
// itself (an errorRounds reset, a cap re-read per round, a retryPending that re-arms), the
// member would keep being rescheduled and Rounds would run to WithMaxRounds instead.
func TestSupervisorPermanentlyFailingMemberTerminates(t *testing.T) {
	const maxRounds = 12
	tm := team.New("perma")
	prov := &alwaysFailingProvider{}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock",
		})}
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory,
		agent.WithMaxRounds(maxRounds)) // DEFAULT retry cap — the shipped configuration.
	mustAdd(t, sup, agent.MemberSpec{Name: "worker", InitialPrompt: "go"})

	out := sup.Run(context.Background(), nil)

	m := singleMember(t, out)
	if m.Disposition != agent.DispositionStopped || m.Reason != agent.StopReasonError {
		t.Fatalf("a permanently failing member must end stopped/error, got %q/%q", m.Disposition, m.Reason)
	}
	// defaultMemberErrorRetries is 1, so: round 0 errors (retried), round 1 errors (benched).
	if m.ErrorRounds != 2 {
		t.Errorf("ErrorRounds = %d, want 2 (one retry then the bench)", m.ErrorRounds)
	}
	if out.Rounds != 2 {
		t.Fatalf("Rounds = %d, want 2 — the retry did not bound itself (round cap was %d)", out.Rounds, maxRounds)
	}
	if got := prov.calls.Load(); got != 2 {
		t.Errorf("provider stream calls = %d, want 2 — one per driven round, no unbounded re-drive", got)
	}
}

// TestSupervisorRetriedMemberReleasesTaskForReclaim pins the TASK RELEASE half. A retried
// member that kept its in-progress claim would strand the work: planRound's auto-claim is
// short-circuited by InProgressFor, so nobody — not the member, not a peer — could pick it
// up, and the retry would buy a turn with nothing attached to it.
//
// The run is capped at ONE round so the assertions observe the team aggregate at exactly
// the point planRound would next read it, rather than after some later round has re-claimed
// and muddied it. Both halves are asserted directly on the aggregate: the task is back to
// pending and UNASSIGNED, and a PEER can actually claim it.
func TestSupervisorRetriedMemberReleasesTaskForReclaim(t *testing.T) {
	tm := team.New("release")
	taskID, err := tm.CreateTask("audit the login handler")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Neither member has an InitialPrompt, so planRound takes the ordinary path in round 0
	// and auto-claims for them: alpha (enrolled first) gets the only task, beta gets
	// nothing and is not scheduled.
	providers := map[string]*mockllm.Provider{
		"alpha": mockllm.New(mockllm.EmptyTurnWithStop(session.StopError)),
		"beta":  mockllm.New(mockllm.TextTurn("never scheduled in round 0")),
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, providers),
		// ONE round: alpha claims, fails, is recovered and retried. The loop then stops
		// before any re-claim, so what we inspect is the released state itself.
		agent.WithMaxRounds(1))
	mustAdd(t, sup, agent.MemberSpec{Name: "alpha"})
	mustAdd(t, sup, agent.MemberSpec{Name: "beta"})

	out := sup.Run(context.Background(), nil)

	// Preconditions: alpha really ran and really was retried (not benched, not skipped).
	if out.Rounds != 1 {
		t.Fatalf("Rounds = %d, want 1 — alpha's round must have run", out.Rounds)
	}
	alpha := memberOutcome(t, out, "alpha")
	if alpha.Stopped {
		t.Fatalf("alpha must be RETRIED, not benched: %+v", alpha)
	}
	if alpha.ErrorRounds != 1 {
		t.Fatalf("precondition: alpha's round must have ended in error, ErrorRounds = %d", alpha.ErrorRounds)
	}

	// (a) The claim was released: pending and unassigned, exactly what claimableLocked needs.
	var found bool
	for _, tk := range tm.Tasks() {
		if tk.ID != taskID {
			continue
		}
		found = true
		if tk.State != team.TaskPending || tk.Assignee != "" {
			t.Fatalf("a retried member's task must be released to pending/unassigned, got state=%q assignee=%q",
				tk.State, tk.Assignee)
		}
	}
	if !found {
		t.Fatalf("task %q vanished from the team: %+v", taskID, tm.Tasks())
	}

	// (b) …and it is genuinely re-claimable — by a PEER, not only by the member that
	//     dropped it. A release that left the assignee set would fail claimableLocked here.
	got, ok, err := tm.ClaimNext("beta")
	if err != nil {
		t.Fatalf("ClaimNext(beta): %v", err)
	}
	if !ok || got.ID != taskID {
		t.Fatalf("a peer must be able to claim the released task; ok=%v task=%+v", ok, got)
	}

	// (c) The member's AGGREGATE STATE is back to idle. The retry branch returns early
	//     while runTurn set MemberWorking at the top, so its SetMemberState(MemberIdle) is
	//     the ONLY thing that undoes that — and this run is the exact observation point,
	//     because it ENDS on a retry round. Without it the team reports a member that is
	//     forever `working`, which is what Quiescent and every roster projection read.
	var alphaState team.MemberState
	var seen bool
	for _, mem := range tm.Members() {
		if mem.Name == "alpha" {
			alphaState, seen = mem.State, true
		}
	}
	if !seen {
		t.Fatalf("alpha vanished from the team roster: %+v", tm.Members())
	}
	if alphaState != team.MemberIdle {
		t.Fatalf("a member queued for RETRY must be returned to idle in the aggregate, got %q", alphaState)
	}
}

// TestSupervisorBudgetExhaustedMemberIsNeverRetried pins the retry gate's
// `!budgetExhausted` conjunct — the one exclusion no test combined with an errored round.
//
// The member's LIFETIME turn budget is the operator's hard ceiling on how many turns it may
// consume; the retry must not reschedule it past that. Drop the conjunct and a member that
// blows its budget ON an errored round is retried anyway, and its Reason flips budget→error,
// mislabelling the terminal as well as overspending it.
func TestSupervisorBudgetExhaustedMemberIsNeverRetried(t *testing.T) {
	tm := team.New("budget-error")
	// Round 0 ends in a run-level error AND consumes the member's entire 1-turn lifetime
	// budget. Extra error turns follow so a bypassed gate would have something to drive —
	// the test must not pass merely because the script ran dry.
	prov := mockllm.New(
		mockllm.EmptyTurnWithStop(session.StopError),
		mockllm.EmptyTurnWithStop(session.StopError),
		mockllm.EmptyTurnWithStop(session.StopError),
	)
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, map[string]*mockllm.Provider{"worker": prov}),
		agent.WithMaxRounds(5),
		agent.WithMemberTurnBudget(1),
		// The DEFAULT retry cap: a retry WOULD be available if the budget conjunct
		// were not there to refuse it.
		agent.WithMemberErrorRetries(1))
	mustAdd(t, sup, agent.MemberSpec{Name: "worker", InitialPrompt: "go"})

	out := sup.Run(context.Background(), nil)

	m := singleMember(t, out)
	if !m.Stopped {
		t.Fatalf("a budget-exhausted member must be benched even on an errored round: %+v", m)
	}
	// The terminal keeps its most-specific classification: the round DID error, so error
	// is the honest reason — but the member must not have been rescheduled.
	if m.Reason != agent.StopReasonError {
		t.Errorf("Reason = %q, want %q (the round ended in error; budget is the residual class)", m.Reason, agent.StopReasonError)
	}
	if m.ErrorRounds != 1 {
		t.Errorf("ErrorRounds = %d, want 1 — the errored round is still counted", m.ErrorRounds)
	}
	if out.Rounds != 1 {
		t.Fatalf("Rounds = %d, want 1 — a budget-exhausted member must not earn a retry round", out.Rounds)
	}
	if got := prov.Calls(); got != 1 {
		t.Errorf("provider calls = %d, want 1 — the member must not be re-driven past its turn budget", got)
	}
}

// TestSupervisorErrorRoundsIsIndependentOfTheTerminal pins the counter-example to the
// invariant MemberOutcome.ErrorRounds' doc-comment used to claim ("a member benched for
// cancellation or budget has 0"). The counter is MONOTONIC over the member's LIFETIME —
// that monotonicity is the retry cap's termination proof — so it does not follow from the
// terminal and the terminal does not follow from it.
//
// The reachable shape: round 0 errors and is retried, round 1 is cancelled. The member ends
// Reason "cancelled" with ErrorRounds 1. A consumer that read "cancelled ⇒ 0 errored rounds"
// off the old doc would render that member as a clean kill.
func TestSupervisorErrorRoundsIsIndependentOfTheTerminal(t *testing.T) {
	tm := team.New("mixed")
	ctx, cancel := context.WithCancel(context.Background())
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)

	var turnsSeen atomic.Int64
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		// Round 0: a terminal error stop (recovered + retried). Round 1: cancel the run as
		// the retry turn reaches the provider, so that round lands StateCancelled.
		prov := mockllm.NewWith(
			[]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) {
				if turnsSeen.Add(1) >= 2 {
					cancel()
				}
			})},
			mockllm.EmptyTurnWithStop(session.StopError),
			mockllm.TextTurn("the retry turn, cancelled mid-flight"),
			mockllm.TextTurn("never reached"),
		)
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock",
		})}
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory,
		agent.WithMaxRounds(5), agent.WithMemberErrorRetries(1))
	mustAdd(t, sup, agent.MemberSpec{Name: "worker", InitialPrompt: "go"})

	out := sup.Run(ctx, nil)

	m := singleMember(t, out)
	if m.Reason != agent.StopReasonCancelled {
		t.Fatalf("precondition: the member must end CANCELLED, got %q/%q (rounds=%d)", m.Disposition, m.Reason, out.Rounds)
	}
	if m.ErrorRounds != 1 {
		t.Fatalf("ErrorRounds = %d, want 1: the counter is a LIFETIME count, independent of the terminal — a cancelled member can carry errored rounds", m.ErrorRounds)
	}
}

// TestWithMemberErrorRetriesIgnoresNegative pins the option's documented guard ("a
// negative value is ignored"). Without it a negative cap would make `errorRounds <= cap`
// false on the very first errored round, silently turning the SHIPPED default (one retry)
// into fail-fast for any caller that passed a computed value that went negative.
func TestWithMemberErrorRetriesIgnoresNegative(t *testing.T) {
	tm := team.New("negative")
	// The retry-enabled script: an errored round 0, then real work. If the negative value
	// were applied, the member would be benched at round 0 and never reach the text turn.
	prov := mockllm.New(
		mockllm.EmptyTurnWithStop(session.StopError),
		mockllm.TextTurn("RECOVERED UNDER THE DEFAULT CAP"),
	)
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, map[string]*mockllm.Provider{"worker": prov}),
		agent.WithMaxRounds(5), agent.WithMemberErrorRetries(-1))
	mustAdd(t, sup, agent.MemberSpec{Name: "worker", InitialPrompt: "go"})

	out := sup.Run(context.Background(), nil)

	m := singleMember(t, out)
	if m.Stopped {
		t.Fatalf("a negative cap must be IGNORED (default 1 retry stands), but the member was benched: %+v", m)
	}
	if !strings.Contains(m.LastText, "RECOVERED UNDER THE DEFAULT CAP") {
		t.Fatalf("the member must have been retried under the default cap; LastText = %q", m.LastText)
	}
}

// TestSupervisorCancelledMemberIsNeverRetried pins the retry gate's exclusions: only a
// StopError round is retryable. A CANCELLED member keeps its D5 disposition
// (stopped/cancelled) and, because cancellation is not a run-level error, contributes
// NOTHING to ErrorRounds — so a kill can never be laundered into "retried and fine".
//
// It is also the only REACHABLE nonResumable shape, which is why the "a failed RECOVERY is
// never retried" rule is asserted here rather than on a synthetic failed-Recover: with the
// current state machine Recover-from-failed and Reopen-from-completed cannot fail (see
// ADR 0077's Consequences), so `reopenErr != nil` is reachable only via a cancelled
// member, whose Reopen is illegal by design. The `reopenErr == nil` conjunct in the retry
// gate is therefore fail-closed defence for a future state, and this test covers the shape
// that exists today: the member is benched, not retried, even though its round did end
// abnormally.
//
// The LastText check alone would be unfalsifiable and is deliberately not the headline:
// once the run ctx is cancelled a phantom retry drive would die on the cancelled ctx before
// reaching the provider, so the second scripted turn is unreachable whether the gate holds
// or not. The load-bearing assertions are therefore the ones a bypassed gate WOULD move —
// the member's state in the team AGGREGATE (retried ⇒ MemberIdle, benched ⇒ MemberStopped;
// that state is what Quiescent and every roster projection read) and its released task —
// plus the provider call count, which bounds how many turns it was actually given.
func TestSupervisorCancelledMemberIsNeverRetried(t *testing.T) {
	tm := team.New("cancel")
	if _, err := tm.CreateTask("work the cancelled member claims"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	// Cancel the run as soon as the member's round-0 turn reaches the provider, so the
	// in-flight run observes it and the session lands in StateCancelled — the state whose
	// Reopen fails. Extra scripted turns are present so a bypassed gate would have
	// something to drive (the test cannot pass merely because the script ran dry).
	//
	// Hoisted out of the factory so the call count is observable: a phantom extra turn
	// shows up here even when its text never lands on the outcome.
	prov := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { cancel() })},
		mockllm.TextTurn("never reached cleanly"),
		mockllm.TextTurn("PHANTOM RETRY OF A CANCELLED MEMBER"),
	)
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock",
		})}
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory,
		agent.WithMaxRounds(5)) // DEFAULT retry cap: a retry WOULD be available if the gate let it through.
	mustAdd(t, sup, agent.MemberSpec{Name: "worker", InitialPrompt: "go"})

	out := sup.Run(ctx, nil)

	m := singleMember(t, out)
	if m.Disposition != agent.DispositionStopped || m.Reason != agent.StopReasonCancelled {
		t.Fatalf("a cancelled member must stay stopped/cancelled, got %q/%q", m.Disposition, m.Reason)
	}
	if m.ErrorRounds != 0 {
		t.Errorf("cancellation is not a run-level error: ErrorRounds = %d, want 0", m.ErrorRounds)
	}
	// The AGGREGATE state is the falsifiable half: the retry branch sets MemberIdle, the
	// bench branch MemberStopped. A cancelled member must land on the bench.
	var state team.MemberState
	var seen bool
	for _, mem := range tm.Members() {
		if mem.Name == "worker" {
			state, seen = mem.State, true
		}
	}
	if !seen {
		t.Fatalf("worker vanished from the team roster: %+v", tm.Members())
	}
	if state != team.MemberStopped {
		t.Fatalf("a cancelled member must be BENCHED in the aggregate (MemberStopped), got %q — the retry branch would leave it idle", state)
	}
	if got := prov.Calls(); got != 1 {
		t.Errorf("provider calls = %d, want 1 — a cancelled member must not be given a second turn", got)
	}
	if strings.Contains(m.LastText, "PHANTOM RETRY") {
		t.Errorf("a cancelled member must not be retried; LastText = %q", m.LastText)
	}
}
