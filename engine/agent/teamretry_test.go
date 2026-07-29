package agent_test

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
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
// docs/adr/0077-resume-a-failed-subagent.md (Amendment).
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
			sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
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
// with the pre-amendment disposition (stopped + StopReasonError) and the honest count —
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
			sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
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
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"), factory,
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
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
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
	alpha := memberByName(t, out, "alpha")
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
func TestSupervisorCancelledMemberIsNeverRetried(t *testing.T) {
	tm := team.New("cancel")
	ctx, cancel := context.WithCancel(context.Background())
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	// Cancel the run as soon as the member's round-0 turn reaches the provider, so the
	// in-flight run observes it and the session lands in StateCancelled — the state whose
	// Reopen fails. Extra scripted turns are present so a bypassed gate would have
	// something to drive (the test cannot pass merely because the script ran dry).
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		prov := mockllm.NewWith(
			[]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { cancel() })},
			mockllm.TextTurn("never reached cleanly"),
			mockllm.TextTurn("PHANTOM RETRY OF A CANCELLED MEMBER"),
		)
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock",
		})}
	}
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"), factory,
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
	if strings.Contains(m.LastText, "PHANTOM RETRY") {
		t.Errorf("a cancelled member must not be retried; LastText = %q", m.LastText)
	}
}

// memberByName returns the named member's outcome, failing the test when it is absent.
func memberByName(t *testing.T, out agent.TeamOutcome, name string) agent.MemberOutcome {
	t.Helper()
	for _, m := range out.Members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no outcome for member %q: %+v", name, out.Members)
	return agent.MemberOutcome{}
}
