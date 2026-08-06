package agent

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
)

// TestProjectTeamEventTurnEndContextMeter asserts the turn.end projection carries
// the per-member context-meter fields: ContextUsed is THIS turn's input-token
// count (current occupancy, the meter numerator — an assignment, not a sum) and
// ContextWindow is the producing member engine's window (forwarded off the
// TeamEvent, the meter denominator). It also confirms the existing per-turn Usage
// is still forwarded unchanged.
func TestProjectTeamEventTurnEndContextMeter(t *testing.T) {
	const window = 200000
	te := TeamEvent{
		Member:        "scout",
		ContextWindow: window,
		Event: session.Event{
			Type: session.EvTurnEnd,
			TurnEnd: &session.TurnEndPayload{
				Usage: session.Usage{InputTokens: 40000, OutputTokens: 80},
			},
		},
	}

	ev, ok := projectTeamEvent("p1", "team-p1", te)
	if !ok {
		t.Fatal("turn.end must project to a team.member event")
	}
	if ev.Type != session.EvTeamMember || ev.Team == nil {
		t.Fatalf("projected event mis-shaped: %+v", ev)
	}
	p := ev.Team
	if p.ContextUsed != 40000 {
		t.Errorf("ContextUsed = %d, want 40000 (this turn's input tokens)", p.ContextUsed)
	}
	if p.ContextWindow != window {
		t.Errorf("ContextWindow = %d, want %d (the member engine's window)", p.ContextWindow, window)
	}
	// The pre-existing per-turn usage projection must be unchanged.
	if p.Usage.InputTokens != 40000 || p.Usage.OutputTokens != 80 {
		t.Errorf("Usage = %+v, want the forwarded per-turn usage", p.Usage)
	}
}

// TestProjectTeamEventUnknownWindow asserts a turn.end whose TeamEvent carries no
// window (a member engine with ContextWindowTokens == 0) still projects ContextUsed
// but a zero ContextWindow — the client then suppresses the meter (no denominator).
func TestProjectTeamEventUnknownWindow(t *testing.T) {
	te := TeamEvent{
		Member: "scout",
		Event: session.Event{
			Type:    session.EvTurnEnd,
			TurnEnd: &session.TurnEndPayload{Usage: session.Usage{InputTokens: 1200}},
		},
	}
	ev, ok := projectTeamEvent("p1", "team-p1", te)
	if !ok || ev.Team == nil {
		t.Fatal("turn.end must still project")
	}
	if ev.Team.ContextUsed != 1200 {
		t.Errorf("ContextUsed = %d, want 1200", ev.Team.ContextUsed)
	}
	if ev.Team.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (unknown window)", ev.Team.ContextWindow)
	}
}

// TestProjectTeamTasksSnapshot asserts the team.Task → session.TeamTaskSnapshot
// projection maps every field (id/state/assignee/deps) and clamps the description
// like every other member-derived preview, with Deps copied as []string.
func TestProjectTeamTasksSnapshot(t *testing.T) {
	tm := team.New("team-p1")
	if err := tm.AddMember("scout", ""); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	id1, err := tm.CreateTask("investigate the failing path")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	id2, err := tm.CreateTask("fix it", id1)
	if err != nil {
		t.Fatalf("CreateTask dep: %v", err)
	}
	if err := tm.ClaimTask(id1, "scout"); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	snap := projectTeamTasksSnapshot(tm.Tasks())
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	if snap[0].ID != string(id1) || snap[0].State != string(team.TaskInProgress) || snap[0].Assignee != "scout" {
		t.Errorf("task 1 snapshot mis-mapped: %+v", snap[0])
	}
	if snap[1].ID != string(id2) || snap[1].State != string(team.TaskPending) {
		t.Errorf("task 2 snapshot mis-mapped: %+v", snap[1])
	}
	if len(snap[1].Deps) != 1 || snap[1].Deps[0] != string(id1) {
		t.Errorf("task 2 deps mis-mapped: %+v", snap[1].Deps)
	}
}

// TestProjectTeamTasksEvent asserts the task snapshot is wrapped in a first-class
// EvTeamTasks event carrying no Member and no InnerKind (the task list is team-wide,
// not a per-member projection), so the client routes it to the task sub-view.
func TestProjectTeamTasksEvent(t *testing.T) {
	snap := []session.TeamTaskSnapshot{{ID: "task-1", State: "pending"}}
	ev := projectTeamTasks("p1", "team-p1", snap)
	if ev.Type != session.EvTeamTasks {
		t.Fatalf("event type = %q, want team.tasks", ev.Type)
	}
	if ev.Team == nil {
		t.Fatal("EvTeamTasks must carry a TeamPayload")
	}
	if ev.Team.Member != "" {
		t.Errorf("a task snapshot must carry no Member, got %q", ev.Team.Member)
	}
	if ev.Team.InnerKind != "" {
		t.Errorf("a task snapshot must carry no InnerKind, got %q", ev.Team.InnerKind)
	}
	if len(ev.Team.Tasks) != 1 || ev.Team.Tasks[0].ID != "task-1" {
		t.Errorf("tasks not carried: %+v", ev.Team.Tasks)
	}
}

// TestProjectTeamEventAlwaysHasMember pins the EvTeamMember contract invariant: a
// per-member projection ALWAYS sets Member (the task-wide snapshot now rides its own
// EvTeamTasks event, so it can no longer produce a memberless EvTeamMember). Drive
// every projectTeamEvent-producing inner kind and assert Member is populated.
func TestProjectTeamEventAlwaysHasMember(t *testing.T) {
	cases := []session.Event{
		{Type: session.EvMessageDelta, Text: "hello"},
		{Type: session.EvToolCall, ToolCall: &session.ToolCall{Name: "Read"}},
		{Type: session.EvToolResult, ToolResult: &session.ToolResult{Content: "body"}},
		{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{}},
		{Type: session.EvResult, Result: &session.ResultPayload{Text: "done"}},
	}
	for _, inner := range cases {
		ev, ok := projectTeamEvent("p1", "team-p1", TeamEvent{Member: "scout", Event: inner})
		if !ok {
			t.Fatalf("%s did not project", inner.Type)
		}
		if ev.Type != session.EvTeamMember {
			t.Errorf("%s projected to %q, want team.member", inner.Type, ev.Type)
		}
		if ev.Team == nil || ev.Team.Member == "" {
			t.Errorf("%s produced a memberless team.member event: %+v", inner.Type, ev.Team)
		}
	}
}

// TestTasksEqualDedup asserts the de-dup guard: snapshots equal on id/state/
// assignee/deps compare equal (no re-emit), and a state OR assignee OR deps change
// compares unequal (re-emit). Description is deliberately excluded.
func TestTasksEqualDedup(t *testing.T) {
	base := []session.TeamTaskSnapshot{
		{ID: "task-1", Description: "a", State: "in_progress", Assignee: "scout", Deps: []string{"task-0"}},
	}
	// TRIPWIRE: Description is excluded from the de-dup key because it is create-only
	// today (team.CreateTask sets it; no setter mutates it), so it can never change
	// for a given task id and excluding it cannot drop a real update. If a future
	// RedescribeTask makes Description mutable, this assertion will catch that the
	// de-dup now silently swallows the change — flip the exclusion (and update
	// tasksEqual) at that point.
	descOnlyChange := []session.TeamTaskSnapshot{
		{ID: "task-1", Description: "DIFFERENT desc", State: "in_progress", Assignee: "scout", Deps: []string{"task-0"}},
	}
	if !tasksEqual(base, descOnlyChange) {
		t.Error("a description-only change must compare EQUAL today (description is create-only; excluded from the de-dup key) — if this trips, Description became mutable and the exclusion is now unsafe")
	}
	stateChanged := []session.TeamTaskSnapshot{
		{ID: "task-1", State: "completed", Assignee: "scout", Deps: []string{"task-0"}},
	}
	if tasksEqual(base, stateChanged) {
		t.Error("a state change must compare unequal")
	}
	assigneeChanged := []session.TeamTaskSnapshot{
		{ID: "task-1", State: "in_progress", Assignee: "other", Deps: []string{"task-0"}},
	}
	if tasksEqual(base, assigneeChanged) {
		t.Error("an assignee change must compare unequal")
	}
	depsChanged := []session.TeamTaskSnapshot{
		{ID: "task-1", State: "in_progress", Assignee: "scout", Deps: []string{"task-9"}},
	}
	if tasksEqual(base, depsChanged) {
		t.Error("a deps change must compare unequal")
	}
	if tasksEqual(base, nil) {
		t.Error("differing lengths must compare unequal")
	}
}

// TestIsNonDeliverable pins the conservative predicate: it fires on empty/whitespace and
// on a SHORT refusal-PREFIXED text over a populated ledger, but NEVER on a short valid
// terse report, a long refusal-prefixed text (over the floor), a refusal phrase appearing
// mid-body (not prefix-anchored), or a refusal-shaped text with an empty ledger. Reverting
// the refusal-prefix half of the trigger flips the headline "short refusal + ledger" row.
func TestIsNonDeliverable(t *testing.T) {
	longRefusal := "I'm sorry, but I cannot assist " + strings.Repeat("with the detailed analysis here ", 20)
	cases := []struct {
		name      string
		text      string
		ledgerLen int
		want      bool
	}{
		{"empty", "", 3, true},
		{"whitespace", "   \n\t", 3, true},
		{"short refusal + ledger", "I'm sorry, but I cannot assist with that request.", 3, true},
		{"short refusal, no ledger", "I'm sorry, but I cannot assist with that request.", 0, false},
		{"long refusal-prefixed report", longRefusal, 3, false},
		{"short valid terse report", "All three modules pass; no issues found.", 3, false},
		{"refusal phrase mid-body", "The team reviewed auth. One member noted: I cannot assist further here.", 3, false},
		{"good substantive report", "The team investigated the authentication path across three modules.\n\nFindings: tokens are validated in middleware.go before any handler runs; the refresh flow rotates secrets correctly; no missing checks were found in the reviewed code.", 3, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isNonDeliverable(c.text, c.ledgerLen); got != c.want {
				t.Errorf("isNonDeliverable(%q, %d) = %v, want %v", c.text, c.ledgerLen, got, c.want)
			}
		})
	}
}

// TestDeliverableHonestFloorWhenLedgerEmpty asserts tier 3: a TeamOutcome with no report,
// no findings, and members all stopped yields the honest floor — non-empty, naming
// non-convergence, the round count, and the stopped-member reasons.
func TestDeliverableHonestFloorWhenLedgerEmpty(t *testing.T) {
	o := TeamOutcome{
		Rounds:    5,
		Quiescent: false,
		Members: []MemberOutcome{
			{Name: "lead", Stopped: true, Disposition: DispositionStopped, Reason: StopReasonBudget},
			{Name: "scout", Stopped: true, Disposition: DispositionStopped, Reason: StopReasonError},
		},
	}
	got := deliverable(o)
	if strings.TrimSpace(got) == "" {
		t.Fatal("deliverable must NEVER be empty (structural invariant)")
	}
	for _, want := range []string{"did NOT converge", "5 round", "lead: budget", "scout: error", "recoverable result"} {
		if !strings.Contains(got, want) {
			t.Errorf("honest floor missing %q:\n%s", want, got)
		}
	}
}

// TestDeliverableNonConvergenceHeaderOnPlausibleSynthesis asserts AC6: a substantive,
// non-refusal synthesis on a NON-quiescent team still carries the non-convergence banner
// (tier 1 + header), while the SAME report on a quiescent team does NOT.
func TestDeliverableNonConvergenceHeaderOnPlausibleSynthesis(t *testing.T) {
	report := "The team reviewed the auth path and confirmed tokens are validated in middleware before any handler runs. No gaps were found in the reviewed code."
	nonConv := TeamOutcome{
		Rounds:    8,
		Quiescent: false,
		Report:    report,
		Findings:  []session.TeamFindingSnapshot{{Member: "scout", Body: "tokens validated"}},
		Members:   []MemberOutcome{{Name: "lead", Disposition: DispositionDone}},
	}
	got := deliverable(nonConv)
	if !strings.Contains(got, "did NOT converge (stop: max-turns)") {
		t.Errorf("non-quiescent tier-1 deliverable must carry the non-convergence header:\n%s", got)
	}
	if !strings.Contains(got, report) {
		t.Errorf("tier-1 deliverable must still contain the report body:\n%s", got)
	}

	conv := nonConv
	conv.Quiescent = true
	gotConv := deliverable(conv)
	if strings.Contains(gotConv, "did NOT converge") {
		t.Errorf("a converged tier-1 deliverable must NOT carry a non-convergence banner:\n%s", gotConv)
	}
	if !strings.Contains(gotConv, report) {
		t.Errorf("converged tier-1 deliverable must contain the report verbatim:\n%s", gotConv)
	}
}

// TestDeliverableRefusalFallsBackToLedger is the pure-layer guard for the headline: a
// refusal-shaped report over a populated ledger is REPLACED by the structured fallback
// (the ledger reaches the deliverable; the refusal text does not). It is the unit-level
// companion to the e2e TestTeamToolRefusalSynthesisFallsBackToLedger.
func TestDeliverableRefusalFallsBackToLedger(t *testing.T) {
	o := TeamOutcome{
		Rounds:    4,
		Quiescent: false,
		Report:    "I'm sorry, but I cannot assist with that request.",
		Findings: []session.TeamFindingSnapshot{
			{Member: "scout", Body: "auth path validates tokens in middleware.go"},
		},
		Members: []MemberOutcome{
			{Name: "scout", Disposition: DispositionDone, Completed: []string{"review auth"}},
		},
	}
	got := deliverable(o)
	if strings.Contains(got, "I cannot assist") || strings.Contains(got, "I'm sorry") {
		t.Errorf("the refusal text must be discarded, not surfaced:\n%s", got)
	}
	if !strings.Contains(got, "auth path validates tokens in middleware.go") {
		t.Errorf("the recorded finding must reach the fallback deliverable:\n%s", got)
	}
	if !strings.Contains(got, "Recorded findings:") {
		t.Errorf("the structured fallback must lead with the findings ledger:\n%s", got)
	}
	if !strings.Contains(got, "completed: review auth") {
		t.Errorf("the per-member completed tasks must appear:\n%s", got)
	}
}

// TestDeliverableSingleMemberLeadRefusalDropsLeadLastText pins the lead-LastText skip
// (the MemberOutcome.Lead deviation). A lead-only team whose synthesis is a refusal —
// which is ALSO the lead's terminal LastText — must still surface the lead-authored
// finding from the ledger while DROPPING the refusal text. If a regression started
// counting the lead's LastText in joinTeamFallback/hasMemberContent, the refusal would
// re-leak into the deliverable and this test would fail.
func TestDeliverableSingleMemberLeadRefusalDropsLeadLastText(t *testing.T) {
	const refusal = "I'm sorry, but I cannot assist with that request."
	o := TeamOutcome{
		Rounds:    3,
		Quiescent: false,
		Report:    refusal, // the rejected synthesis
		Findings: []session.TeamFindingSnapshot{
			{Member: "lead", Body: "config loads from /etc/app before $HOME override"},
		},
		Members: []MemberOutcome{
			{Name: "lead", Lead: true, LastText: refusal, Disposition: DispositionDone},
		},
	}
	got := deliverable(o)
	if strings.Contains(got, "I cannot assist") || strings.Contains(got, "I'm sorry") {
		t.Errorf("the lead's refusal LastText must be dropped (the !m.Lead skip), not surfaced:\n%s", got)
	}
	if !strings.Contains(got, "config loads from /etc/app before $HOME override") {
		t.Errorf("the lead-authored finding must survive into the deliverable:\n%s", got)
	}
	if !strings.Contains(got, "Recorded findings:") {
		t.Errorf("the structured fallback must render the ledger:\n%s", got)
	}
}

// TestDeliverableSingleMemberLeadRefusalNoLedgerStaysNonEmpty is the sibling: a lead-only
// team that refuses AND recorded NO findings. By the LOCKED isNonDeliverable design the
// ledgerLen>0 guard means a refusal over an EMPTY ledger is NOT rejected — there is
// nothing better to show, so the lead's words pass through (no worse than the floor, and
// may carry context), with the non-convergence banner prepended. The invariant the
// reviewer cares about holds: the deliverable is NON-EMPTY (never an empty result), and it
// carries the "did NOT converge" banner so the parent still learns the team did not finish.
func TestDeliverableSingleMemberLeadRefusalNoLedgerStaysNonEmpty(t *testing.T) {
	const refusal = "I'm sorry, but I cannot assist with that request."
	o := TeamOutcome{
		Rounds:    3,
		Quiescent: false,
		Report:    refusal,
		Findings:  nil,
		Members: []MemberOutcome{
			{Name: "lead", Lead: true, LastText: refusal, Disposition: DispositionDone},
		},
	}
	got := deliverable(o)
	if strings.TrimSpace(got) == "" {
		t.Fatal("deliverable must NEVER be empty (structural invariant)")
	}
	if !strings.Contains(got, "did NOT converge") {
		t.Errorf("a non-quiescent deliverable must carry the non-convergence banner:\n%s", got)
	}
}

// TestJoinTeamFallbackGroupsFindingsInFirstSeenOrder pins the findings grouping order: a
// two-member ledger (scout then fixer) must render "From scout:" before "From fixer:",
// driven by the first-seen order slice. A switch to map-iteration order would fail this.
func TestJoinTeamFallbackGroupsFindingsInFirstSeenOrder(t *testing.T) {
	o := TeamOutcome{
		Rounds:    2,
		Quiescent: true,
		Findings: []session.TeamFindingSnapshot{
			{Member: "scout", Body: "scout finding A"},
			{Member: "fixer", Body: "fixer finding B"},
			{Member: "scout", Body: "scout finding C"},
		},
		Members: []MemberOutcome{
			{Name: "scout", Disposition: DispositionDone},
			{Name: "fixer", Disposition: DispositionDone},
		},
	}
	got := joinTeamFallback(o)
	scoutIdx := strings.Index(got, "From scout:")
	fixerIdx := strings.Index(got, "From fixer:")
	if scoutIdx < 0 || fixerIdx < 0 {
		t.Fatalf("both member finding groups must render:\n%s", got)
	}
	if scoutIdx > fixerIdx {
		t.Errorf("findings must group in first-seen member order (scout before fixer):\n%s", got)
	}
	// Both of scout's findings must appear under its single group (append order preserved).
	if !strings.Contains(got, "scout finding A") || !strings.Contains(got, "scout finding C") {
		t.Errorf("all of a member's findings must render under its group:\n%s", got)
	}
}

// TestConvergenceHeaderBudgetMatrix pins the budget×quiescence matrix on the
// deliverable's leading status line: the stop label swaps to "budget" on a non-quiescent
// budget trip, and the budget-exhausted line is appended whenever BudgetExhausted is set
// (quiescent OR not), carrying the round count and the team token total. A run that did
// NOT exhaust the budget carries neither the budget line nor the "stop: budget" label.
func TestConvergenceHeaderBudgetMatrix(t *testing.T) {
	const total = 4242
	rows := []struct {
		name        string
		quiescent   bool
		budgetTrip  bool
		wantBudget  bool   // the "Team token budget exhausted after" line present
		wantStopLbl string // the stop label fragment expected in the non-converged line
	}{
		{"non-quiescent + budget", false, true, true, "stop: budget"},
		{"non-quiescent + no budget", false, false, false, "stop: max-turns"},
		{"quiescent + budget", true, true, true, ""}, // converged line, no stop label, but budget line appended
		{"quiescent + no budget", true, false, false, ""},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			o := TeamOutcome{
				Rounds:          6,
				Quiescent:       c.quiescent,
				BudgetExhausted: c.budgetTrip,
				Usage:           session.Usage{InputTokens: total},
				Members:         []MemberOutcome{{Name: "lead", Disposition: DispositionDone}},
			}
			got := convergenceHeader(o)
			hasBudgetLine := strings.Contains(got, "Team token budget exhausted after")
			if hasBudgetLine != c.wantBudget {
				t.Errorf("budget line present = %v, want %v:\n%s", hasBudgetLine, c.wantBudget, got)
			}
			if c.wantBudget && !strings.Contains(got, "~4242 tokens used") {
				t.Errorf("budget line must carry the team token total ~%d:\n%s", total, got)
			}
			if c.wantStopLbl != "" && !strings.Contains(got, c.wantStopLbl) {
				t.Errorf("non-converged line must carry %q:\n%s", c.wantStopLbl, got)
			}
			// The non-quiescent + no-budget row must NOT carry "stop: budget".
			if !c.budgetTrip && strings.Contains(got, "stop: budget") {
				t.Errorf("a non-budget run must not carry the budget stop label:\n%s", got)
			}
		})
	}
}

// TestDeliverableTier1ForcedByBudget pins the tier-1 header forcing: a QUIESCENT team
// that nonetheless tripped the team-wide budget still gets the convergence header (with
// the appended budget line) prepended to its usable synthesis — without the budget trip
// a quiescent tier-1 synthesis carries no banner (TestDeliverableNonConvergenceHeaderOn
// PlausibleSynthesis covers the converged-no-budget case).
func TestDeliverableTier1ForcedByBudget(t *testing.T) {
	report := "All modules reviewed; the consolidated answer is X."
	o := TeamOutcome{
		Rounds:          3,
		Quiescent:       true,
		BudgetExhausted: true,
		Usage:           session.Usage{InputTokens: 999},
		Report:          report,
		Findings:        []session.TeamFindingSnapshot{{Member: "scout", Body: "x"}},
		Members:         []MemberOutcome{{Name: "lead", Disposition: DispositionDone}},
	}
	got := deliverable(o)
	if !strings.Contains(got, "Team token budget exhausted after") {
		t.Errorf("a quiescent-but-budget-tripped tier-1 deliverable must carry the budget line:\n%s", got)
	}
	if !strings.Contains(got, report) {
		t.Errorf("tier-1 deliverable must still contain the report body:\n%s", got)
	}
	// Sanity: drop the budget trip and the SAME quiescent tier-1 report carries no banner.
	clean := o
	clean.BudgetExhausted = false
	gotClean := deliverable(clean)
	if strings.Contains(gotClean, "Team token budget exhausted") || strings.Contains(gotClean, "did NOT converge") {
		t.Errorf("a converged no-budget tier-1 deliverable must carry no banner:\n%s", gotClean)
	}
}

// TestDeliverableShortValidReportKept asserts AC2 at the pure layer: a SHORT but
// non-refusal-shaped real report over a populated ledger passes through verbatim (tier 1),
// NOT degraded to the structured fallback.
func TestDeliverableShortValidReportKept(t *testing.T) {
	report := "All modules reviewed; no security issues found."
	o := TeamOutcome{
		Rounds:    2,
		Quiescent: true,
		Report:    report,
		Findings:  []session.TeamFindingSnapshot{{Member: "scout", Body: "x"}},
		Members:   []MemberOutcome{{Name: "scout", Disposition: DispositionDone}},
	}
	got := deliverable(o)
	if got != report {
		t.Errorf("a short valid report on a quiescent team must pass through verbatim, got:\n%s", got)
	}
}

// TestProjectTeamEventResultCarriesCauseOnStopError asserts the per-round result
// projection sets Cause (normalised through subagentCausePayload) when the round ended
// StopError — the unit-level mirror of the loop-driven
// TestTeamMemberResultCarriesCauseOnStopError.
func TestProjectTeamEventResultCarriesCauseOnStopError(t *testing.T) {
	te := TeamEvent{
		Member: "scout",
		Event: session.Event{
			Type: session.EvResult,
			Result: &session.ResultPayload{
				Stop:  session.StopError,
				Text:  "partial",
				Error: "boom",
			},
		},
	}
	ev, ok := projectTeamEvent("p1", "team-p1", te)
	if !ok || ev.Team == nil {
		t.Fatal("result must project")
	}
	if ev.Team.Cause != subagentCausePayload("boom") {
		t.Errorf("Cause = %q, want %q (subagentCausePayload(\"boom\"))", ev.Team.Cause, subagentCausePayload("boom"))
	}
}

// TestProjectTeamEventResultNoCauseOnCleanStop asserts a clean per-round result leaves
// Cause empty.
func TestProjectTeamEventResultNoCauseOnCleanStop(t *testing.T) {
	te := TeamEvent{
		Member: "scout",
		Event: session.Event{
			Type: session.EvResult,
			Result: &session.ResultPayload{
				Stop: session.StopEndTurn,
				Text: "done",
			},
		},
	}
	ev, ok := projectTeamEvent("p1", "team-p1", te)
	if !ok || ev.Team == nil {
		t.Fatal("result must still project")
	}
	if ev.Team.Cause != "" {
		t.Errorf("a clean result must carry no cause, got %q", ev.Team.Cause)
	}
}
