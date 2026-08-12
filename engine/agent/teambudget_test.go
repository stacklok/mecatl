package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// usageTurn builds a member turn that emits the given text and a known per-turn token
// usage (via UsageChunk on the turn.end) — the scripted-usage primitive the team-budget
// tests need, since the canned TextTurn/ToolCallTurn emit zero usage. The cumulative
// EvResult.Usage the supervisor reads is the sum of these per-turn UsageChunks, so a
// single-turn round's EvResult.Usage equals this turn's usage.
func usageTurn(text string, in, out int) mockllm.Turn {
	return mockllm.ChunksTurn(
		mockllm.TextChunk(text),
		mockllm.UsageChunk(session.Usage{InputTokens: in, OutputTokens: out}),
		mockllm.DoneChunk(session.StopEndTurn),
	)
}

// usageToolTurn builds a member turn that emits a tool call plus a known per-turn token
// usage, so a worker that must call a coordination tool (e.g. ping itself to be
// re-scheduled) still records spend.
func usageToolTurn(call session.ToolCall, in, out int) mockllm.Turn {
	return mockllm.ChunksTurn(
		mockllm.ToolCallChunk(call),
		mockllm.UsageChunk(session.Usage{InputTokens: in, OutputTokens: out}),
		mockllm.DoneChunk(session.StopEndTurn),
	)
}

// TestTeamTokenBudgetTripsAfterRoundInFlightCompletes is item 1: a 1-lead + 2-worker
// team whose round-0 per-member spend already meets the budget. The in-flight round-0
// MUST complete (all round-0 turns consumed), the budget trips at the round-1 boundary
// so no round-1 worker turn is ever consumed, and the lead's synthesis still runs. The
// outcome reports BudgetExhausted with Usage >= budget, and NO member is individually
// stopped / reasoned "budget" for the team trip.
func TestTeamTokenBudgetTripsAfterRoundInFlightCompletes(t *testing.T) {
	tm := team.New("budget")

	// The lead delegates in round 0 (one task per worker), then synthesises after the
	// loop. Each turn carries usage. Round-1+ lead turns are scripted but must never be
	// reached for the workers; the lead's THIRD scripted turn is the synthesis.
	addA := session.NewToolCall("la", "AddTask", json.RawMessage(`{"description":"task A"}`))
	addB := session.NewToolCall("lb", "AddTask", json.RawMessage(`{"description":"task B"}`))
	leadProv := mockllm.New(
		usageToolTurn(addA, 200, 0), // round 0: create task A
		usageToolTurn(addB, 200, 0), // (same round-0 run) create task B
		mockllm.ChunksTurn( // round 0: end the run cleanly
			mockllm.TextChunk("delegated; waiting"),
			mockllm.UsageChunk(session.Usage{InputTokens: 200, OutputTokens: 0}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		usageTurn("CONSOLIDATED: both workers reported.", 100, 0), // synthesis turn
		// Defensive extra turns: if the loop wrongly scheduled another round the lead
		// would consume these — but the budget must stop it first.
		usageTurn("EXTRA lead turn (should never run)", 100, 0),
	)

	// Each worker claims its task, completes it, records a finding, then in round 0
	// pings itself so it WOULD be re-scheduled in round 1 (proving the budget — not
	// quiescence — stops the next round). Its round-1 turns must never be consumed.
	workerProv := func(name string) *mockllm.Provider {
		complete := session.NewToolCall(session.ToolCallID(name+"c"), "CompleteTask", json.RawMessage(`{"task_id":"task-1"}`))
		ping := session.NewToolCall(session.ToolCallID(name+"p"), "SendMessage",
			json.RawMessage(`{"to":"`+name+`","body":"keep going"}`))
		return mockllm.New(
			usageToolTurn(complete, 250, 0),        // round 0 (or 1): complete a task
			usageToolTurn(ping, 250, 0),            // (same run) ping self to be re-scheduled
			usageTurn("round-0 work done", 250, 0), // end the run
			// round 1 turns — must NEVER be consumed if the budget trips correctly:
			usageTurn("ROUND 1 worker turn (should never run)", 1000, 0),
			usageTurn("ROUND 1 worker turn (should never run)", 1000, 0),
		)
	}

	w1 := workerProv("w1")
	w2 := workerProv("w2")
	providers := map[string]*mockllm.Provider{"lead": leadProv, "w1": w1, "w2": w2}

	// Round-0 total: lead 3×200 = 600; each worker 3×250 = 750 → 600+750+750 = 2100.
	// A budget of 1500 is well below that, so the trip fires at the round-1 boundary
	// (round-0 already overshot it), and the round-0 turns above are all consumed.
	const budget = 1500
	base := memfs.NewWorkspace("/ws")
	sup := agent.NewSupervisor(tm, base, memberFactory(t, tm, providers),
		agent.WithMaxRounds(10),
		agent.WithTeamTokenBudget(budget),
	)

	ctx := context.Background()
	if err := sup.AddMember(ctx, agent.MemberSpec{
		Name: "lead", Lead: true, InitialPrompt: "create one task per worker, then synthesise",
	}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "w1", InitialPrompt: "claim and do a task"}); err != nil {
		t.Fatalf("AddMember(w1): %v", err)
	}
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "w2", InitialPrompt: "claim and do a task"}); err != nil {
		t.Fatalf("AddMember(w2): %v", err)
	}

	// Sum the turn.end usage off the streamed member events — the SAME projection the
	// TeamTool sink (memberEventUsage) accumulates for the EvTeamEnd payload — so the
	// documented "equal by construction, never reconciled" invariant between the
	// supervisor's EvResult-based accumulator (TeamOutcome.Usage) and the sink's
	// turn.end sum is pinned below.
	var turnEndSum session.Usage
	out := sup.Run(ctx, func(ev agent.TeamEvent) {
		if ev.Event.Type == session.EvTurnEnd && ev.Event.TurnEnd != nil {
			turnEndSum = turnEndSum.Add(ev.Event.TurnEnd.Usage)
		}
	})

	if !out.BudgetExhausted {
		t.Errorf("outcome.BudgetExhausted = false, want true (budget %d should have tripped)", budget)
	}
	if out.Usage.TotalTokens() < budget {
		t.Errorf("outcome.Usage.TotalTokens() = %d, want >= budget %d", out.Usage.TotalTokens(), budget)
	}
	// Two-sums-equal: the supervisor accumulator (Σ per-drive EvResult.Usage) must equal
	// the turn.end sum the TeamTool sink would compute — a divergence means one side
	// started counting something the other does not (the double-count bug class).
	if turnEndSum == (session.Usage{}) {
		t.Fatalf("test setup: no turn.end usage streamed to sum")
	}
	if out.Usage != turnEndSum {
		t.Errorf("outcome.Usage = %+v, want the turn.end sum %+v (supervisor accumulator and sink sum must stay equal by construction)",
			out.Usage, turnEndSum)
	}
	// All round-0 turns were consumed: lead 3, each worker 3.
	if got := leadProv.Calls(); got != 4 { // 3 round-0 + 1 synthesis
		t.Errorf("lead consumed %d turns, want 4 (3 round-0 + 1 synthesis)", got)
	}
	if got := w1.Calls(); got != 3 {
		t.Errorf("w1 consumed %d turns, want exactly 3 (round-0 only; round-1 never scheduled)", got)
	}
	if got := w2.Calls(); got != 3 {
		t.Errorf("w2 consumed %d turns, want exactly 3 (round-0 only; round-1 never scheduled)", got)
	}
	// No member is individually stopped or reasoned "budget" for the TEAM trip.
	for _, m := range out.Members {
		if m.Stopped {
			t.Errorf("member %q ended Stopped; the team-wide budget trip must NOT stop members individually", m.Name)
		}
		if m.Reason == agent.StopReasonBudget {
			t.Errorf("member %q got Reason=budget; the lifetime turn budget is distinct from the team token budget", m.Name)
		}
	}
}

// TestTeamTokenBudgetSynthesisUsageAccumulates is item 2: the lead's synthesis-turn
// usage folds into the OUTCOME (it runs after the loop and cannot trip the gate). The
// final outcome.Usage must STRICTLY exceed the at-trip team total by the synthesis turn's
// usage.
func TestTeamTokenBudgetSynthesisUsageAccumulates(t *testing.T) {
	tm := team.New("synth")

	const synthIn = 777
	leadProv := mockllm.New(
		usageTurn("delegated; nothing to do", 500, 0),          // round 0 (also crosses budget)
		usageTurn("CONSOLIDATED synthesis report", synthIn, 0), // synthesis turn
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv}

	const budget = 400 // round-0 lead spend (500) already exceeds it → trips at round 1
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		memberFactory(t, tm, providers),
		agent.WithMaxRounds(10),
		agent.WithTeamTokenBudget(budget),
	)
	if err := sup.AddMember(context.Background(), agent.MemberSpec{
		Name: "lead", Lead: true, InitialPrompt: "do nothing then synthesise",
	}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}

	out := sup.Run(context.Background(), nil)
	if !out.BudgetExhausted {
		t.Fatalf("outcome.BudgetExhausted = false, want true")
	}
	// The at-trip total was the round-0 lead spend (500). The synthesis turn adds synthIn
	// on top, so the final Usage must STRICTLY exceed the at-trip sum.
	const atTrip = 500
	if out.Usage.TotalTokens() != atTrip+synthIn {
		t.Errorf("outcome.Usage.TotalTokens() = %d, want %d (round-0 %d + synthesis %d)",
			out.Usage.TotalTokens(), atTrip+synthIn, atTrip, synthIn)
	}
	if out.Usage.TotalTokens() <= atTrip {
		t.Errorf("synthesis usage must accumulate: total %d must strictly exceed the at-trip sum %d",
			out.Usage.TotalTokens(), atTrip)
	}
	if !strings.Contains(out.Report, "CONSOLIDATED synthesis report") {
		t.Errorf("synthesis turn must still run when the budget trips: report = %q", out.Report)
	}
}

// TestTeamTokenBudgetZeroDisabledIdenticalScheduling is item 3: a run with NO budget
// option and a run with WithTeamTokenBudget(0) must schedule identically (same Rounds /
// Report / member dispositions). BudgetExhausted is false in both; Usage is newly
// populated in both (fine).
func TestTeamTokenBudgetZeroDisabledIdenticalScheduling(t *testing.T) {
	run := func(withZeroOption bool) agent.TeamOutcome {
		tm := team.New("zero")
		addTask := session.NewToolCall("l1", "AddTask", json.RawMessage(`{"description":"investigate"}`))
		leadProv := mockllm.New(
			usageToolTurn(addTask, 10, 2),
			usageTurn("delegated; waiting", 10, 2),
			usageTurn("CONSOLIDATED report", 10, 2),
		)
		complete := session.NewToolCall("w1", "CompleteTask", json.RawMessage(`{"task_id":"task-1"}`))
		report := session.NewToolCall("w2", "RecordFinding", json.RawMessage(`{"finding":"done"}`))
		workerProv := mockllm.New(
			mockllm.ChunksTurn(
				mockllm.ToolCallChunk(complete),
				mockllm.ToolCallChunk(report),
				mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}),
				mockllm.DoneChunk(session.StopEndTurn),
			),
			usageTurn("investigation complete", 10, 2),
		)
		providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}
		opts := []agent.SupervisorOption{agent.WithMaxRounds(10)}
		if withZeroOption {
			opts = append(opts, agent.WithTeamTokenBudget(0))
		}
		sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"), memberFactory(t, tm, providers), opts...)
		ctx := context.Background()
		if err := sup.AddMember(ctx, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "delegate then synthesise"}); err != nil {
			t.Fatalf("AddMember(lead): %v", err)
		}
		if err := sup.AddMember(ctx, agent.MemberSpec{Name: "worker", InitialPrompt: "claim and do"}); err != nil {
			t.Fatalf("AddMember(worker): %v", err)
		}
		return sup.Run(ctx, nil)
	}

	noOpt := run(false)
	zeroOpt := run(true)

	if noOpt.Rounds != zeroOpt.Rounds {
		t.Errorf("rounds differ: no-option %d vs WithTeamTokenBudget(0) %d", noOpt.Rounds, zeroOpt.Rounds)
	}
	if noOpt.Report != zeroOpt.Report {
		t.Errorf("reports differ:\n no-option: %q\n zero:      %q", noOpt.Report, zeroOpt.Report)
	}
	if noOpt.Quiescent != zeroOpt.Quiescent {
		t.Errorf("quiescence differs: %v vs %v", noOpt.Quiescent, zeroOpt.Quiescent)
	}
	if len(noOpt.Members) != len(zeroOpt.Members) {
		t.Fatalf("member count differs: %d vs %d", len(noOpt.Members), len(zeroOpt.Members))
	}
	for i := range noOpt.Members {
		if noOpt.Members[i].Disposition != zeroOpt.Members[i].Disposition ||
			noOpt.Members[i].Reason != zeroOpt.Members[i].Reason {
			t.Errorf("member %d disposition differs: %+v vs %+v", i, noOpt.Members[i], zeroOpt.Members[i])
		}
	}
	if noOpt.BudgetExhausted || zeroOpt.BudgetExhausted {
		t.Errorf("BudgetExhausted must be false when disabled: no-option %v, zero %v", noOpt.BudgetExhausted, zeroOpt.BudgetExhausted)
	}
}

// TestTeamTokenBudgetZeroUsageNeverTrips is item 4 (adversarial): members whose every
// EvResult carries ZERO usage NEVER cross even a budget of 1 — the team runs to genuine
// quiescence within maxRounds and produces a deliverable. It guards against a trip on a
// zero-usage team.
func TestTeamTokenBudgetZeroUsageNeverTrips(t *testing.T) {
	tm := team.New("zerousage")
	// Canned TextTurn/ToolCallTurn emit ZERO usage by construction.
	addTask := session.NewToolCall("l1", "AddTask", json.RawMessage(`{"description":"investigate"}`))
	leadProv := mockllm.New(
		mockllm.ToolCallTurn(addTask),
		mockllm.TextTurn("delegated; waiting"),
		mockllm.TextTurn("CONSOLIDATED report"),
	)
	complete := session.NewToolCall("w1", "CompleteTask", json.RawMessage(`{"task_id":"task-1"}`))
	report := session.NewToolCall("w2", "RecordFinding", json.RawMessage(`{"finding":"done"}`))
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(complete, report),
		mockllm.TextTurn("investigation complete"),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"), memberFactory(t, tm, providers),
		agent.WithMaxRounds(10),
		agent.WithTeamTokenBudget(1), // smallest possible positive budget
	)
	ctx := context.Background()
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "delegate then synthesise"}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "worker", InitialPrompt: "claim and do"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	out := sup.Run(ctx, nil)
	if out.BudgetExhausted {
		t.Errorf("a zero-usage team must NEVER trip the budget; BudgetExhausted = true (total=%d)", out.Usage.TotalTokens())
	}
	// It must finish well under the round cap (no runaway) and produce a deliverable —
	// the budget never interfered because nothing ever crossed it.
	if out.Rounds >= 10 {
		t.Errorf("a zero-usage team must finish under the round cap, got Rounds=%d", out.Rounds)
	}
	if out.Usage.TotalTokens() != 0 {
		t.Errorf("a zero-usage team's accumulated Usage must be 0, got %d", out.Usage.TotalTokens())
	}
	if strings.TrimSpace(out.Report) == "" {
		t.Errorf("a zero-usage team must still produce a deliverable; Report empty")
	}
}

// TestTeamTokenBudgetSmallerThanOneRound is item 5 (adversarial): a budget smaller than
// even ONE round's spend trips before round 1, the deliverable carries the budget header,
// and the team did not run away.
func TestTeamTokenBudgetSmallerThanOneRound(t *testing.T) {
	tm := team.New("tiny")
	leadProv := mockllm.New(
		usageTurn("round-0 work", 1000, 0), // round 0 spends 1000, far above the budget
		usageTurn("CONSOLIDATED report after budget stop", 50, 0),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv}

	const budget = 1 // smaller than one round's spend (1000)
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		memberFactory(t, tm, providers),
		agent.WithMaxRounds(10),
		agent.WithTeamTokenBudget(budget),
	)
	if err := sup.AddMember(context.Background(), agent.MemberSpec{
		Name: "lead", Lead: true, InitialPrompt: "do one round of work",
	}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}

	out := sup.Run(context.Background(), nil)
	if !out.BudgetExhausted {
		t.Fatalf("a budget smaller than one round's spend must trip; BudgetExhausted=false")
	}
	// Only the round-0 turn + synthesis should have run (round 1 never scheduled).
	if got := leadProv.Calls(); got != 2 {
		t.Errorf("lead consumed %d turns, want 2 (round-0 + synthesis); a larger count means the team ran away", got)
	}
}

// memberFactoryAllow is the small allow-all policy reused below.
var _ = permpolicy.NewPolicy

// teamBudgetToolFactory mirrors teamToolFactory (teamtool_test.go) for the e2e items:
// a per-member Engine looked up by member name with that member's coordination tools.
func teamBudgetToolFactory(t *testing.T, providers map[string]*mockllm.Provider) agent.TeamMemberEngineFactory {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("teamBudgetToolFactory: no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM:     prov,
			Catalog: cat,
			Policy:  allow,
			Hooks:   noopHooks{},
			Model:   "member-model",
		})}
	}
}

// TestTeamToolTokenBudgetReportsStop is item 6: the MODEL-facing path through the real
// loop + dispatcher. With a configured team budget that the round-0 spend crosses, the
// Team ToolResult contains the "Team token budget exhausted after" header AND still
// starts with the "Team id: " line, the EvTeamEnd Usage equals the sink sum, and the
// EvTeamEnd Stop is "budget" on the non-quiescent trip.
func TestTeamToolTokenBudgetReportsStop(t *testing.T) {
	// The lead creates a task that nothing ever completes (a lead-only team — the lead
	// does not auto-claim), so the team is genuinely NON-quiescent (a pending task
	// remains). Its round-0 spend crosses the budget, so scheduling stops at the
	// round-1 boundary and the lead synthesises after the loop. Each turn carries usage.
	addTask := session.NewToolCall("lt", "AddTask", json.RawMessage(`{"description":"never completed"}`))
	leadProv := mockllm.New(
		usageToolTurn(addTask, 600, 0),                          // round 0: create a pending task
		usageTurn("round-0 done", 600, 0),                       // round 0: end run (total 1200)
		usageTurn("CONSOLIDATED budget-stopped report", 100, 0), // synthesis
		usageTurn("EXTRA (should never run)", 100, 0),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv}

	const budget = 1000 // round-0 spends 1200 → trips at round-1 boundary, non-quiescent
	teamTool := agent.NewTeamTool(
		teamBudgetToolFactory(t, providers),
		agent.WithTeamToolTokenBudget(budget),
	)
	parentCat := catalogWith(t, teamTool)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"investigate forever","members":[{"name":"lead","role":"keep working"}]}`)),
		mockllm.TextTurn("parent received the team summary"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "investigate forever"})
	evs := drain(r)

	// --- the Team ToolResult: budget header AND the Team id line -----------
	var summary string
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			summary = ev.ToolResult.Content
		}
	}
	if summary == "" {
		t.Fatalf("no Team ToolResult in %v", typesOf(evs))
	}
	if !strings.HasPrefix(summary, "Team id: ") {
		t.Errorf("Team ToolResult must still start with the Team id line:\n%s", summary)
	}
	if !strings.Contains(summary, "Team token budget exhausted after") {
		t.Errorf("Team ToolResult must carry the budget-exhausted header:\n%s", summary)
	}

	// --- EvTeamEnd: Stop == budget, Usage == sink sum ----------------------
	var end *session.TeamPayload
	for _, ev := range evs {
		if ev.Type == session.EvTeamEnd {
			end = ev.Team
		}
	}
	if end == nil {
		t.Fatalf("no team.end event")
	}
	if end.Stop != session.StopBudget {
		t.Errorf("team.end Stop = %q, want %q (non-quiescent budget trip)", end.Stop, session.StopBudget)
	}
	// EvTeamEnd Usage is the sink's turn.end sum — reconstruct it from the forwarded
	// member turn.end events and assert equality (the sink remains authoritative).
	var wantUsage session.Usage
	for _, ev := range evs {
		if ev.Type == session.EvTeamMember && ev.Team.InnerKind == session.EvTurnEnd {
			wantUsage = wantUsage.Add(ev.Team.Usage)
		}
	}
	if wantUsage == (session.Usage{}) {
		t.Fatalf("test setup: no member turn.end usage forwarded to sum")
	}
	if end.Usage != wantUsage {
		t.Errorf("team.end Usage = %+v, want the summed member total %+v", end.Usage, wantUsage)
	}
}

// TestTeamToolTokenBudgetTightenOnly is item 7: the tighten-only matrix for the per-call
// max_team_tokens arg against a configured budget. The round-0 spend (500) deliberately
// STRADDLES the two candidate values in every divergent row, so trip-vs-no-trip is
// decided by WHICH value won — a mutation that lets the call RAISE the config (or
// ignores either side) flips a row's outcome and fails the matrix:
//
//   - config 1000 + call 100  → effective 100  → TRIPS;  call-ignored ⇒ 1000 ⇒ no trip (caught).
//   - config 100  + call 1000 → effective 100  → TRIPS;  call-raises ⇒ 1000 ⇒ no trip (caught).
//   - config 0    + call 100  → effective 100  → TRIPS;  call-ignored-vs-unlimited ⇒ disabled ⇒ no trip (caught).
//   - config 200  + call absent / call 0 → effective 200 → TRIPS; config-ignored ⇒ disabled ⇒ no trip (caught).
//   - config 1000 + call absent → effective 1000 → must NOT trip (guards spurious tripping).
//   - config 0    + call absent → disabled       → must NOT trip.
func TestTeamToolTokenBudgetTightenOnly(t *testing.T) {
	// The lead spends exactly `spend` in round 0 then quiesces; the lead synthesises
	// after the loop. The budget trips at the round-1 boundary iff spend >= effective
	// budget, observed via the budget header on the ToolResult (forced into tier 1
	// even on a quiescent team).
	const spend = 500
	cases := []struct {
		name         string
		configBudget int
		callArg      string // the "max_team_tokens": fragment, or "" to omit
		wantTrip     bool
	}{
		{"config-1000-call-100-tightens-trips", 1000, `,"max_team_tokens":100`, true},
		{"config-100-call-1000-config-wins-trips", 100, `,"max_team_tokens":1000`, true},
		{"config-0-call-100-call-applies-trips", 0, `,"max_team_tokens":100`, true},
		{"config-200-call-absent-config-applies-trips", 200, ``, true},
		{"config-200-call-0-config-applies-trips", 200, `,"max_team_tokens":0`, true},
		{"config-1000-call-absent-no-trip", 1000, ``, false},
		{"config-0-call-absent-disabled-no-trip", 0, ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			leadProv := mockllm.New(
				usageTurn("round-0 work done", spend, 0), // round 0: spend, then quiesce
				usageTurn("CONSOLIDATED report", 10, 0),  // synthesis
			)
			providers := map[string]*mockllm.Provider{"lead": leadProv}

			opts := []agent.TeamOption{}
			if c.configBudget > 0 {
				opts = append(opts, agent.WithTeamToolTokenBudget(c.configBudget))
			}
			teamTool := agent.NewTeamTool(teamBudgetToolFactory(t, providers), opts...)
			parentCat := catalogWith(t, teamTool)

			parentLLM := mockllm.New(
				mockllm.ToolCallTurn(toolCall("p1", "Team",
					`{"goal":"go","members":[{"name":"lead","role":"do one round"}]`+c.callArg+`}`)),
				mockllm.TextTurn("done"),
			)
			e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
			sess := newSession(t, session.Limits{})
			r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
			evs := drain(r)

			var summary string
			for _, ev := range evs {
				if ev.Type == session.EvToolResult && ev.ToolResult != nil {
					summary = ev.ToolResult.Content
				}
			}
			if summary == "" {
				t.Fatalf("no Team ToolResult in %v", typesOf(evs))
			}
			tripped := strings.Contains(summary, "Team token budget exhausted after")
			if tripped != c.wantTrip {
				t.Errorf("tripped = %v, want %v (config %d, call %q, spend %d); ToolResult:\n%s",
					tripped, c.wantTrip, c.configBudget, c.callArg, spend, summary)
			}
		})
	}
}

// TestTeamTokenBudgetExactBoundaryTrips pins the >= comparison (mirroring
// Engine.budgetExhausted): a team whose accumulated spend equals the budget EXACTLY
// must trip — a >= → > regression leaves BudgetExhausted false here. The sibling run
// one token UNDER must NOT trip, bracketing the boundary from both sides.
func TestTeamTokenBudgetExactBoundaryTrips(t *testing.T) {
	const budget = 500
	run := func(spend int) agent.TeamOutcome {
		tm := team.New("boundary")
		leadProv := mockllm.New(
			usageTurn("round-0 work", spend, 0),    // round 0: the whole spend
			usageTurn("CONSOLIDATED report", 0, 0), // synthesis (zero usage: keeps the gate input exact)
		)
		providers := map[string]*mockllm.Provider{"lead": leadProv}
		sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
			memberFactory(t, tm, providers),
			agent.WithMaxRounds(10),
			agent.WithTeamTokenBudget(budget),
		)
		if err := sup.AddMember(context.Background(), agent.MemberSpec{
			Name: "lead", Lead: true, InitialPrompt: "do one round",
		}); err != nil {
			t.Fatalf("AddMember(lead): %v", err)
		}
		return sup.Run(context.Background(), nil)
	}

	exact := run(budget)
	if !exact.BudgetExhausted {
		t.Errorf("spend == budget (%d) must trip (the gate is >=, mirroring Engine.budgetExhausted); BudgetExhausted=false", budget)
	}
	under := run(budget - 1)
	if under.BudgetExhausted {
		t.Errorf("spend == budget-1 (%d) must NOT trip; BudgetExhausted=true", budget-1)
	}
}

// TestTeamTokenBudgetTripBeforePlanRoundSideEffects pins the trip check's PLACEMENT:
// it must sit BEFORE planRound, whose Drain/ClaimNext side effects must not fire for a
// round that never runs. Round 0 leaves a message in the worker's inbox and a pending
// task on the list; the budget trips at the round-1 boundary, so round 1's planRound
// never runs — the message must still be drainable and the task still pending and
// unclaimed after the Run. A mutation that moves the check after planRound drains the
// message and claims the task for the cancelled round, failing both assertions.
// (synthesise drains only the LEAD's inbox, so the worker's inbox is untouched by it.)
func TestTeamTokenBudgetTripBeforePlanRoundSideEffects(t *testing.T) {
	tm := team.New("placement")

	// Round 0: the lead creates a task (claimable by the worker in round 1) and sends
	// the worker a message (drainable in round 1), spending past the budget.
	addTask := session.NewToolCall("l1", "AddTask", json.RawMessage(`{"description":"left for round 1"}`))
	msg := session.NewToolCall("l2", "SendMessage", json.RawMessage(`{"to":"worker","body":"for round 1"}`))
	leadProv := mockllm.New(
		usageToolTurn(addTask, 600, 0), // round 0: create the task (crosses the budget)
		usageToolTurn(msg, 0, 0),       // (same run) message the worker
		usageTurn("round-0 done", 0, 0),
		usageTurn("CONSOLIDATED report", 10, 0), // synthesis
	)
	// The worker runs its initial prompt in round 0 (no claim there — round 0 plans
	// initial prompts only) and would Drain+ClaimNext in round 1.
	workerProv := mockllm.New(
		usageTurn("worker round-0 done", 0, 0),
		usageTurn("ROUND 1 worker turn (should never run)", 0, 0),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	const budget = 500 // round-0 spends 600 → trips at the round-1 boundary
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		memberFactory(t, tm, providers),
		agent.WithMaxRounds(10),
		agent.WithTeamTokenBudget(budget),
	)
	ctx := context.Background()
	if err := sup.AddMember(ctx, agent.MemberSpec{
		Name: "lead", Lead: true, InitialPrompt: "create a task and message the worker",
	}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "worker", InitialPrompt: "stand by"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	out := sup.Run(ctx, nil)
	if !out.BudgetExhausted {
		t.Fatalf("precondition: the budget must trip at the round-1 boundary; BudgetExhausted=false")
	}

	// The cancelled round's planRound never ran: the worker's message is STILL in its
	// inbox (Drain never fired for round 1)...
	msgs, err := tm.Drain("worker")
	if err != nil {
		t.Fatalf("Drain(worker): %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "for round 1" {
		t.Errorf("worker inbox = %+v, want the one undrained round-1 message (planRound must not Drain for a round that never runs)", msgs)
	}
	// ...and the task is STILL pending and unclaimed (ClaimNext never fired).
	tasks := tm.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want exactly the one round-0 task", tasks)
	}
	if tasks[0].State != team.TaskPending || tasks[0].Assignee != "" {
		t.Errorf("task = state %q assignee %q, want pending/unclaimed (planRound must not ClaimNext for a round that never runs)",
			tasks[0].State, tasks[0].Assignee)
	}
	// The worker's round-1 turn was never driven.
	if got := workerProv.Calls(); got != 1 {
		t.Errorf("worker consumed %d turns, want 1 (round 0 only)", got)
	}
}

// TestTeamToolTokenBudgetTier1ForcedHeader is item 8: a QUIESCENT-but-budget-tripped run
// still carries the budget line in the (tier-1) deliverable. The lead converges cleanly
// (no self-ping) but its round-0 spend crosses the budget, so the trip fires at the next
// boundary even though the team is quiescent — the tier-1 synthesis still gets the header.
func TestTeamToolTokenBudgetTier1ForcedHeader(t *testing.T) {
	leadProv := mockllm.New(
		usageTurn("round-0 work; nothing pending", 800, 0), // round 0 crosses budget, then quiesces
		usageTurn("CONSOLIDATED clean report", 50, 0),      // synthesis (tier-1 usable)
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv}

	const budget = 500 // round-0 spends 800 → trips at the round-1 boundary
	teamTool := agent.NewTeamTool(
		teamBudgetToolFactory(t, providers),
		agent.WithTeamToolTokenBudget(budget),
	)
	parentCat := catalogWith(t, teamTool)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"do one round","members":[{"name":"lead","role":"do the work then stop"}]}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "do one round"})
	evs := drain(r)

	var summary string
	var end *session.TeamPayload
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			summary = ev.ToolResult.Content
		}
		if ev.Type == session.EvTeamEnd {
			end = ev.Team
		}
	}
	if !strings.Contains(summary, "Team token budget exhausted after") {
		t.Errorf("a quiescent-but-budget-tripped run must still carry the budget line in tier 1:\n%s", summary)
	}
	if !strings.Contains(summary, "CONSOLIDATED clean report") {
		t.Errorf("the tier-1 synthesis body must be preserved alongside the budget header:\n%s", summary)
	}
	// A quiescent team that tripped the budget: teamStop maps to StopEndTurn (only the
	// NON-quiescent trip maps to StopBudget), so the wire stop is end-turn here.
	if end == nil {
		t.Fatalf("no team.end event")
	}
	if end.Stop != session.StopEndTurn {
		t.Errorf("a QUIESCENT budget-tripped team's wire Stop = %q, want %q (StopBudget is non-quiescent only)",
			end.Stop, session.StopEndTurn)
	}
}
