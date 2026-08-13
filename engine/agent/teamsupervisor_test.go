package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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

// memberFactory builds a per-member Engine whose catalog carries that member's
// coordination tools (bound to tm and self) and whose provider is looked up by
// member name — the per-member scripting the supervisor relies on.
func memberFactory(t *testing.T, tm *team.Team, providers map[string]*mockllm.Provider) agent.MemberEngine {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("memberFactory: no provider scripted for member %q", spec.Name)
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
			Model:   "mock",
		})}
	}
}

// hasToolCall reports whether the tagged event stream contains a tool.call for the
// named tool produced by the named member.
func hasToolCall(events []agent.TeamEvent, member, toolName string) bool {
	for _, ev := range events {
		if ev.Member == member && ev.Event.Type == session.EvToolCall &&
			ev.Event.ToolCall != nil && ev.Event.ToolCall.Name == toolName {
			return true
		}
	}
	return false
}

// TestSupervisorTwoMemberFlow exercises a full lead→worker collaboration entirely
// offline: the lead adds a task, a worker auto-claims it, does the work, completes
// the task and reports back via the mailbox, and the lead synthesises — then the
// team reaches genuine quiescence. It asserts round count, task completion, the
// tagged event stream, and that each member's script was fully consumed.
func TestSupervisorTwoMemberFlow(t *testing.T) {
	tm := team.New("demo")

	addTask := session.NewToolCall("l1", "AddTask",
		json.RawMessage(`{"description":"investigate the reported bug"}`))
	leadProv := mockllm.New(
		mockllm.ToolCallTurn(addTask),                                                   // round 0, turn 1
		mockllm.TextTurn("Task created; waiting for worker."),                           // round 0, turn 2 (ends run)
		mockllm.TextTurn("CONSOLIDATED: root cause found; fix applied. Team complete."), // synthesis turn
	)

	// The worker completes the task and RECORDS A FINDING (the primary synthesis
	// channel) rather than messaging the lead — so the lead is not re-scheduled and
	// the synthesis phase produces the deliverable. The lead reads the ledger.
	complete := session.NewToolCall("w1", "CompleteTask", json.RawMessage(`{"task_id":"task-1"}`))
	report := session.NewToolCall("w2", "RecordFinding",
		json.RawMessage(`{"finding":"done investigating; root cause found"}`))
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(complete, report),      // round 1, turn 1
		mockllm.TextTurn("Investigation complete."), // round 1, turn 2 (ends run)
	)

	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}
	base := memfs.NewWorkspace("/ws")
	sup := agent.NewSupervisor(tm, agent.EnvForWS(base, nil), memberFactory(t, tm, providers), agent.WithMaxRounds(10))

	ctx := context.Background()
	if err := sup.AddMember(ctx, agent.MemberSpec{
		Name: "lead", Lead: true,
		InitialPrompt: "Investigate the reported bug. Create a task and let a teammate handle it.",
	}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "worker"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	var events []agent.TeamEvent
	out := sup.Run(ctx, func(ev agent.TeamEvent) { events = append(events, ev) })

	if !out.Quiescent {
		t.Errorf("team did not reach quiescence: %+v", out)
	}
	if out.Rounds != 3 {
		t.Errorf("rounds = %d, want 3 (lead delegates / worker works / lead synthesises)", out.Rounds)
	}
	if got := leadProv.Calls(); got != 3 {
		t.Errorf("lead consumed %d turns, want 3", got)
	}
	if got := workerProv.Calls(); got != 2 {
		t.Errorf("worker consumed %d turns, want 2", got)
	}

	tasks := tm.Tasks()
	if len(tasks) != 1 || tasks[0].State != team.TaskCompleted {
		t.Fatalf("task state = %+v, want exactly one completed task", tasks)
	}

	if !hasToolCall(events, "lead", "AddTask") {
		t.Error("event stream missing lead's AddTask tool.call")
	}
	if !hasToolCall(events, "worker", "CompleteTask") {
		t.Error("event stream missing worker's CompleteTask tool.call")
	}
	if !hasToolCall(events, "worker", "RecordFinding") {
		t.Error("event stream missing worker's RecordFinding tool.call")
	}

	// The lead's synthesis is the team's deliverable.
	if !strings.Contains(out.Report, "CONSOLIDATED") {
		t.Errorf("outcome.Report = %q, want the lead's synthesis text", out.Report)
	}

	last := map[string]string{}
	for _, m := range out.Members {
		last[m.Name] = m.LastText
		if m.Stopped {
			t.Errorf("member %q ended stopped, want a clean finish", m.Name)
		}
	}
	if !strings.Contains(last["lead"], "CONSOLIDATED") {
		t.Errorf("lead last text = %q, want the synthesis output", last["lead"])
	}
	if !strings.Contains(last["worker"], "Investigation complete") {
		t.Errorf("worker last text = %q", last["worker"])
	}
}

// TestSupervisorStuckTaskQuiescesNoSpin asserts Fix E's stuck-task handling: a
// worker claims a task but its mock NEVER calls CompleteTask and the run then ends
// (the worker stops being scheduled). The team must NOT dead-spin to maxRounds; the
// stopped worker's task is released back to pending and, with no member left to
// claim it, the next round plans no work and the team stops well under the cap.
func TestSupervisorStuckTaskQuiescesNoSpin(t *testing.T) {
	tm := team.New("stuck")

	// The lead adds one task in round 0, then only ever emits text.
	addTask := session.NewToolCall("l1", "AddTask",
		json.RawMessage(`{"description":"do the thing"}`))
	leadProv := mockllm.New(
		mockllm.ToolCallTurn(addTask),
		mockllm.TextTurn("delegated"),
		mockllm.TextTurn("idle"),
		mockllm.TextTurn("idle"),
		mockllm.TextTurn("idle"),
	)
	// The worker claims (auto-claimed by the supervisor) and just reports text —
	// it NEVER calls CompleteTask, leaving its claimed task in_progress.
	workerProv := mockllm.New(
		mockllm.TextTurn("working but never completing"),
		mockllm.TextTurn("still not done"),
		mockllm.TextTurn("nope"),
		mockllm.TextTurn("nope"),
		mockllm.TextTurn("nope"),
	)

	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}
	maxRounds := 8
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, providers), agent.WithMaxRounds(maxRounds))

	ctx := context.Background()
	if err := sup.AddMember(ctx, agent.MemberSpec{
		Name: "lead", Lead: true, InitialPrompt: "delegate work",
	}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "worker"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	out := sup.Run(ctx, nil)

	// The team must not run the full cap: once the worker has claimed once and gone
	// idle (no message, no new claimable task since it still holds one), rounds plan
	// no work and the run stops.
	if out.Rounds >= maxRounds {
		t.Errorf("team spun to the round cap (rounds=%d, cap=%d); a stuck task should not dead-spin",
			out.Rounds, maxRounds)
	}
	// A task left in_progress (never completed) means the team is NOT genuinely
	// quiescent — Quiescent distinguishes completion from a stuck dependency.
	if out.Quiescent {
		t.Errorf("team reported quiescent, but a task was never completed: %+v", tm.Tasks())
	}
	tasks := tm.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want exactly one", tasks)
	}
	// The one task ends pending or in_progress — never completed (nobody completed
	// it). It must not be stuck wedging the loop forever, which the round-cap check
	// above guarantees.
	if tasks[0].State == team.TaskCompleted {
		t.Errorf("task state = %s, want it never completed", tasks[0].State)
	}
}

// TestSupervisorMemberHoldsAtMostOneTask asserts Fix E's single-claim bound: a
// worker that claims a task but does not complete it is NOT auto-claimed a second
// task in a later round. The team has two tasks and one worker; with the worker
// never completing, it must hold at most one in_progress task at a time, leaving
// the second pending.
func TestSupervisorMemberHoldsAtMostOneTask(t *testing.T) {
	tm := team.New("oneclaim")
	// Two independent tasks seeded directly on the team so the worker is the only
	// claimant and there is no lead to interfere.
	if _, err := tm.CreateTask("task A"); err != nil {
		t.Fatalf("CreateTask A: %v", err)
	}
	if _, err := tm.CreateTask("task B"); err != nil {
		t.Fatalf("CreateTask B: %v", err)
	}

	// The worker just emits text every round; it never completes its claim.
	workerProv := mockllm.New(
		mockllm.TextTurn("r1"),
		mockllm.TextTurn("r2"),
		mockllm.TextTurn("r3"),
		mockllm.TextTurn("r4"),
	)
	providers := map[string]*mockllm.Provider{"worker": workerProv}

	// A sink that, after each round's events, checks the team never has two
	// in_progress tasks assigned to the worker. Run serialises sink calls.
	var maxInProgress int
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, providers), agent.WithMaxRounds(6))

	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	sup.Run(context.Background(), func(agent.TeamEvent) {
		n := 0
		for _, tk := range tm.Tasks() {
			if tk.State == team.TaskInProgress && tk.Assignee == "worker" {
				n++
			}
		}
		if n > maxInProgress {
			maxInProgress = n
		}
	})

	if maxInProgress > 1 {
		t.Errorf("worker held %d in-progress tasks at once, want at most 1", maxInProgress)
	}
	// Exactly one task should ever have been claimed (the worker never frees it), so
	// the other stays pending.
	var pending, inProgress int
	for _, tk := range tm.Tasks() {
		switch tk.State {
		case team.TaskPending:
			pending++
		case team.TaskInProgress:
			inProgress++
		}
	}
	if inProgress != 1 || pending != 1 {
		t.Errorf("task split = %d in_progress / %d pending, want 1/1 (single-claim bound)", inProgress, pending)
	}
}

// TestSupervisorMemberTurnBudgetStops asserts Fix F's lifetime turn budget: a
// member that would otherwise loop forever — it re-queues a message to itself every
// round, so it is always re-scheduled and never finishes — is stopped by the
// cumulative per-member turn budget rather than running to the round cap. The
// per-round Limits cannot do this because session.Reopen resets their counters each
// round; only the lifetime budget, which accumulates across rounds, can.
func TestSupervisorMemberTurnBudgetStops(t *testing.T) {
	tm := team.New("loop")

	// Each round the worker emits a tool call sending a message to ITSELF (so it is
	// planned again next round) and then a line of text (ending the round's run
	// cleanly via StopEndTurn — never an error). The script is long enough that,
	// without a lifetime budget, the worker would be re-scheduled every round up to
	// the (large) round cap. Two turns per round.
	selfPing := session.NewToolCall("p", "SendMessage",
		json.RawMessage(`{"to":"worker","body":"keep going"}`))
	var turns []mockllm.Turn
	for i := 0; i < 60; i++ {
		turns = append(turns, mockllm.ToolCallTurn(selfPing), mockllm.TextTurn("still working"))
	}
	workerProv := mockllm.New(turns...)
	providers := map[string]*mockllm.Provider{"worker": workerProv}

	const maxRounds = 30
	const budget = 5 // lifetime turns; ~2 turns/round → stops after ~3 rounds
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, providers),
		agent.WithMaxRounds(maxRounds),
		agent.WithMemberTurnBudget(budget),
	)

	if err := sup.AddMember(context.Background(), agent.MemberSpec{
		Name: "worker", InitialPrompt: "begin the loop and ping yourself each round",
	}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	out := sup.Run(context.Background(), nil)

	// The worker must have been stopped by the budget, well short of the round cap.
	if out.Rounds >= maxRounds {
		t.Errorf("team ran to the round cap (rounds=%d, cap=%d); the lifetime turn budget should have stopped the looping member",
			out.Rounds, maxRounds)
	}
	if len(out.Members) != 1 || !out.Members[0].Stopped {
		t.Fatalf("worker outcome = %+v, want it stopped by the budget", out.Members)
	}
	// It must not have consumed anywhere near maxRounds worth of turns: the budget is
	// a hard ceiling the per-round-reset counters could not provide. Allow one extra
	// round's worth of turns (the round in which the budget is crossed runs to
	// completion before the member is marked stopped).
	if got := workerProv.Calls(); got > budget+4 {
		t.Errorf("worker consumed %d turns, want bounded near the budget %d", got, budget)
	}
}

// limitedMemberFactory builds a member factory that returns the given per-member
// Limits on every MemberBuild (the composition-layer analogue: a def's
// maxTurns/maxToolCalls mapped onto MemberBuild.Limits). The engine carries only
// the member's coordination tools.
func limitedMemberFactory(t *testing.T, tm *team.Team, providers map[string]*mockllm.Provider, limits session.Limits) agent.MemberEngine {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("limitedMemberFactory: no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{
			Engine: agent.NewEngine(agent.Deps{LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock"}),
			Limits: limits,
		}
	}
}

// barrierMemberTool is a read-only member tool that signals each Execute entry on
// `entered` and blocks until `release` is closed, tracking peak concurrency. It is the
// instrument for the team-concurrency guard test.
type barrierMemberTool struct {
	mu      sync.Mutex
	live    int
	peak    int
	entered chan struct{}
	release chan struct{}
}

func (*barrierMemberTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Barrier", Description: "barrier", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*barrierMemberTool) ReadOnly() bool { return true }
func (b *barrierMemberTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	b.mu.Lock()
	b.live++
	if b.live > b.peak {
		b.peak = b.live
	}
	b.mu.Unlock()
	b.entered <- struct{}{}
	<-b.release
	b.mu.Lock()
	b.live--
	b.mu.Unlock()
	return session.NewToolResult(call.ID, "ok"), nil
}
func (b *barrierMemberTool) peakLive() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peak
}

// TestSupervisorRoundConcurrencyBounded is the guard test the plan asks for: the
// Supervisor bounds a scheduling round via errgroup.SetLimit(concurrency), so a round
// with MORE planned members than the concurrency cap NEVER runs more than `concurrency`
// member turns at once. It is the tripwire against a future refactor silently dropping
// SetLimit (the F1 "unbounded errgroup" claim was stale — this keeps it stale). Each
// member blocks inside a barrier tool so the test can hold the in-flight set and read
// the peak.
func TestSupervisorRoundConcurrencyBounded(t *testing.T) {
	const concurrency = 2
	const members = 6

	tm := team.New("conc")
	barrier := &barrierMemberTool{entered: make(chan struct{}, members), release: make(chan struct{})}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)

	providers := map[string]*mockllm.Provider{}
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov := providers[spec.Name]
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(barrier)
		return agent.MemberBuild{
			Engine: agent.NewEngine(agent.Deps{LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock"}),
		}
	}

	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory,
		agent.WithMaxRounds(1),
		agent.WithTeamConcurrency(concurrency),
	)
	for i := 0; i < members; i++ {
		name := "m" + string(rune('0'+i))
		// Each member calls Barrier (blocks) then ends its turn — all scheduled in round 0.
		providers[name] = mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall(session.ToolCallID(name+"b"), "Barrier", json.RawMessage(`{}`))),
			mockllm.TextTurn("done"),
		)
		if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: name, InitialPrompt: "go"}); err != nil {
			t.Fatalf("AddMember(%s): %v", name, err)
		}
	}

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		sup.Run(context.Background(), nil)
	}()

	// Hold until exactly `concurrency` members are inside the barrier; the rest must be
	// blocked on the errgroup limit, not the barrier.
	for i := 0; i < concurrency; i++ {
		<-barrier.entered
	}
	if got := barrier.peakLive(); got > concurrency {
		close(barrier.release)
		<-runDone
		t.Fatalf("%d member turns live at once, want <= concurrency=%d (the round errgroup must bound concurrency)", got, concurrency)
	}

	// Release everything; remaining members pass the now-open barrier as the group admits
	// them. `entered` is buffered to `members`, so no Execute blocks on it.
	close(barrier.release)
	<-runDone

	if peak := barrier.peakLive(); peak > concurrency {
		t.Fatalf("peak concurrent member turns = %d, want <= concurrency=%d", peak, concurrency)
	}
}

// TestSupervisorMemberLimitsBindSession proves a member's per-def Limits
// (MemberBuild.Limits) bound its per-round session: with MaxTurns=2 the member makes
// exactly 2 model calls in round 0, even though its script and the team-wide default
// (WithTeamLimits) would allow many more. WithMaxRounds(1) isolates a single round.
func TestSupervisorMemberLimitsBindSession(t *testing.T) {
	tm := team.New("limits")

	// A self-pinging worker that would loop indefinitely; two turns per round.
	selfPing := session.NewToolCall("p", "SendMessage", json.RawMessage(`{"to":"worker","body":"again"}`))
	var turns []mockllm.Turn
	for i := 0; i < 12; i++ {
		turns = append(turns, mockllm.ToolCallTurn(selfPing))
	}
	workerProv := mockllm.New(turns...)
	providers := map[string]*mockllm.Provider{"worker": workerProv}

	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		limitedMemberFactory(t, tm, providers, session.Limits{MaxTurns: 2, MaxToolCalls: 40, MaxConsecutiveFailures: 3}),
		agent.WithMaxRounds(1),
		// A deliberately LOOSE team default, so the per-member override (2) is what bites.
		agent.WithTeamLimits(session.Limits{MaxTurns: 20, MaxToolCalls: 200, MaxConsecutiveFailures: 5}),
	)
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker", InitialPrompt: "loop"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	sup.Run(context.Background(), nil)

	if got := workerProv.Calls(); got != 2 {
		t.Fatalf("member made %d model calls in one round, want 2 (MemberBuild.Limits MaxTurns=2 should bind the session)", got)
	}
}

// TestSupervisorMemberZeroLimitsUsesTeamDefault proves a member with a ZERO
// MemberBuild.Limits runs under the team-wide default (WithTeamLimits), per-field:
// with the team default MaxTurns=3 the member makes 3 model calls in round 0.
func TestSupervisorMemberZeroLimitsUsesTeamDefault(t *testing.T) {
	tm := team.New("default")

	selfPing := session.NewToolCall("p", "SendMessage", json.RawMessage(`{"to":"worker","body":"again"}`))
	var turns []mockllm.Turn
	for i := 0; i < 12; i++ {
		turns = append(turns, mockllm.ToolCallTurn(selfPing))
	}
	workerProv := mockllm.New(turns...)
	providers := map[string]*mockllm.Provider{"worker": workerProv}

	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		limitedMemberFactory(t, tm, providers, session.Limits{}), // zero => team default
		agent.WithMaxRounds(1),
		agent.WithTeamLimits(session.Limits{MaxTurns: 3, MaxToolCalls: 200, MaxConsecutiveFailures: 5}),
	)
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker", InitialPrompt: "loop"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	sup.Run(context.Background(), nil)

	if got := workerProv.Calls(); got != 3 {
		t.Fatalf("member made %d model calls in one round, want 3 (zero MemberBuild.Limits falls back to the team default MaxTurns=3)", got)
	}
}

// TestSupervisorMemberPartialLimitsMergePerField proves the per-field merge: a
// member that pins ONLY MaxTurns keeps the team default for the other fields. With a
// per-member MaxTurns=2 over a team default MaxTurns=9, the member is bounded to 2.
func TestSupervisorMemberPartialLimitsMergePerField(t *testing.T) {
	tm := team.New("merge")

	selfPing := session.NewToolCall("p", "SendMessage", json.RawMessage(`{"to":"worker","body":"again"}`))
	var turns []mockllm.Turn
	for i := 0; i < 12; i++ {
		turns = append(turns, mockllm.ToolCallTurn(selfPing))
	}
	workerProv := mockllm.New(turns...)
	providers := map[string]*mockllm.Provider{"worker": workerProv}

	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		limitedMemberFactory(t, tm, providers, session.Limits{MaxTurns: 2}), // only MaxTurns pinned
		agent.WithMaxRounds(1),
		agent.WithTeamLimits(session.Limits{MaxTurns: 9, MaxToolCalls: 200, MaxConsecutiveFailures: 5}),
	)
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker", InitialPrompt: "loop"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	sup.Run(context.Background(), nil)

	if got := workerProv.Calls(); got != 2 {
		t.Fatalf("member made %d model calls, want 2 (per-member MaxTurns=2 overrides the team default 9)", got)
	}
}

// recordingForker is a fake tool.EnvironmentForker that hands out in-memory
// workspaces and records the fork labels and cleanup calls.
type recordingForker struct {
	mu       sync.Mutex
	labels   []string
	cleanups int
}

func (f *recordingForker) Fork(_ context.Context, _ tool.Environment, label string) (tool.Environment, func() error, string, error) {
	f.mu.Lock()
	f.labels = append(f.labels, label)
	f.mu.Unlock()
	ws := memfs.NewWorkspace("/fork/" + label)
	cleanup := func() error {
		f.mu.Lock()
		f.cleanups++
		f.mu.Unlock()
		return nil
	}
	return agent.ForkEnv(ws), cleanup, "", nil
}

// TestSupervisorMutatingMemberForksWorkspace asserts a Mutating member runs in an
// isolated forked workspace (not the shared base) and that the fork is cleaned up.
func TestSupervisorMutatingMemberForksWorkspace(t *testing.T) {
	tm := team.New("fork-demo")
	providers := map[string]*mockllm.Provider{"impl": mockllm.New(mockllm.TextTurn("done"))}
	base := memfs.NewWorkspace("/ws")
	ff := &recordingForker{}

	sup := agent.NewSupervisor(tm, agent.EnvForWS(base, nil), memberFactory(t, tm, providers),
		agent.WithForker(ff), agent.WithMaxRounds(5))

	ctx := context.Background()
	if err := sup.AddMember(ctx, agent.MemberSpec{
		Name: "impl", Mutating: true, InitialPrompt: "make the change",
	}); err != nil {
		t.Fatalf("AddMember(impl): %v", err)
	}

	out := sup.Run(ctx, nil)

	ff.mu.Lock()
	defer ff.mu.Unlock()
	if len(ff.labels) != 1 || ff.labels[0] != "impl" {
		t.Fatalf("forker labels = %v, want [impl] (mutating member forks its workspace)", ff.labels)
	}
	if ff.cleanups != 1 {
		t.Errorf("fork cleanups = %d, want 1", ff.cleanups)
	}
	if !out.Quiescent {
		t.Errorf("single-member team not quiescent: %+v", out)
	}
}

// TestSupervisorRequiresForkerForMutatingMember asserts enrolling a Mutating member
// without a forker is rejected.
func TestSupervisorRequiresForkerForMutatingMember(t *testing.T) {
	tm := team.New("t")
	providers := map[string]*mockllm.Provider{"impl": mockllm.New(mockllm.TextTurn("x"))}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), memberFactory(t, tm, providers))
	err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "impl", Mutating: true})
	if err == nil {
		t.Fatal("AddMember of a Mutating member without a forker should fail")
	}
}

// fakeMutatingTool is a stand-in workspace-mutating tool: it reports
// ReadOnly()==false and carries an arbitrary name (e.g. "Edit"). It lets the team
// tests assert the supervisor's catalog inspection without importing the
// adapter-layer concrete tool types (which the layering rule forbids).
type fakeMutatingTool struct{ name string }

func (f fakeMutatingTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: f.name, Description: f.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (fakeMutatingTool) ReadOnly() bool { return false }
func (fakeMutatingTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

// catalogFactory builds a member Engine whose catalog is exactly the supplied
// tools plus the member's coordination tools — the seam Fix A's tests drive.
func catalogFactory(t *testing.T, tm *team.Team, extra ...tool.Tool) agent.MemberEngine {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		for _, tl := range extra {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("x")),
			Catalog: cat,
			Policy:  allow,
			Model:   "mock",
		})}
	}
}

// TestSupervisorRejectsReadOnlyMemberWithMutatingTool asserts Fix A: a read-only
// (base-sharing) member whose factory hands back a WORKSPACE-mutating tool is
// rejected by AddMember — the supervisor is authoritative and will not let a
// base-sharing member corrupt the shared workspace.
func TestSupervisorRejectsReadOnlyMemberWithMutatingTool(t *testing.T) {
	tm := team.New("t")
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		catalogFactory(t, tm, fakeMutatingTool{name: "Edit"}))
	err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ro"})
	if err == nil {
		t.Fatal("AddMember of a read-only member with a workspace-mutating tool should be rejected")
	}
	if !strings.Contains(err.Error(), "Edit") {
		t.Errorf("error %q should name the offending tool", err)
	}
	// The rejected member must not linger on the roster.
	if got := tm.Members(); len(got) != 0 {
		t.Errorf("roster = %v, want empty after rejection", got)
	}
}

// TestSupervisorAcceptsReadOnlyMemberWithCoordinationTools asserts the CRUCIAL
// nuance: the coordination tools report ReadOnly()==false but only mutate TEAM
// state, so a read-only member carrying just those is ACCEPTED.
func TestSupervisorAcceptsReadOnlyMemberWithCoordinationTools(t *testing.T) {
	tm := team.New("t")
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), catalogFactory(t, tm))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ro"}); err != nil {
		t.Fatalf("read-only member with only coordination tools should be accepted: %v", err)
	}
}

// TestSupervisorAcceptsMutatingMemberWithMutatingTool asserts a Mutating member —
// which runs in its own isolated fork — MAY carry the same workspace-mutating tool.
func TestSupervisorAcceptsMutatingMemberWithMutatingTool(t *testing.T) {
	tm := team.New("t")
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		catalogFactory(t, tm, fakeMutatingTool{name: "Edit"}),
		agent.WithForker(&recordingForker{}))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "impl", Mutating: true}); err != nil {
		t.Fatalf("mutating member with a workspace-mutating tool should be accepted: %v", err)
	}
}

// TestSupervisorAcceptsReadOnlyMemberWithMCPTool asserts the per-agent-MCP backstop
// exemption: a read-only member whose catalog holds a tool that reports
// ReadOnly()==false but is named in MemberBuild.MCPToolNames (an MCP tool — never
// touches the workspace) is ACCEPTED, exactly like a coordination tool.
func TestSupervisorAcceptsReadOnlyMemberWithMCPTool(t *testing.T) {
	tm := team.New("t")
	mcpTool := fakeMutatingTool{name: "mcp__remote__do"}
	factory := func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
		b := catalogFactory(t, tm, mcpTool)(spec, routedModel)
		b.MCPToolNames = []string{"mcp__remote__do"}
		return b
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory)
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ro"}); err != nil {
		t.Fatalf("read-only member holding an exempt MCP tool should be accepted: %v", err)
	}
}

// TestSupervisorStillRejectsRealMutatingDespiteMCPExempt asserts the exemption is
// SCOPED to the named MCP tools: a read-only member that ALSO holds a genuine
// workspace-mutating tool (not in MCPToolNames) is still rejected.
func TestSupervisorStillRejectsRealMutatingDespiteMCPExempt(t *testing.T) {
	tm := team.New("t")
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		b := catalogFactory(t, tm, fakeMutatingTool{name: "mcp__remote__do"}, fakeMutatingTool{name: "Edit"})(spec, "")
		b.MCPToolNames = []string{"mcp__remote__do"} // exempt the MCP tool only
		return b
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory)
	err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ro"})
	if err == nil || !strings.Contains(err.Error(), "Edit") {
		t.Fatalf("read-only member with a real mutating tool must still be rejected naming Edit, got %v", err)
	}
}

// roShellFactory builds a read-only member whose catalog holds a workspace-mutating
// Bash stand-in AND sets MemberBuild.IsolateReadOnly=true — the composition-layer
// signal that the member was granted a shell and must run in an isolated worktree.
func roShellFactory(t *testing.T, tm *team.Team) agent.MemberEngine {
	t.Helper()
	return func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		b := catalogFactory(t, tm, fakeMutatingTool{name: "Bash"})(spec, "")
		b.IsolateReadOnly = true
		return b
	}
}

// TestSupervisorReadOnlyIsolatedMemberForksViaReadOnlyForker asserts the new
// three-tier behaviour: a read-only member the factory marked IsolateReadOnly is
// ACCEPTED (its mutating Bash is exempt because it is not base-sharing) and is forked
// via the READ-ONLY forker (the worktree seam), not the force-copy one.
func TestSupervisorReadOnlyIsolatedMemberForksViaReadOnlyForker(t *testing.T) {
	tm := team.New("t")
	mutatingFk := &recordingForker{}
	roFk := &recordingForker{}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), roShellFactory(t, tm),
		agent.WithForker(mutatingFk), agent.WithReadOnlyForker(roFk))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ro"}); err != nil {
		t.Fatalf("read-only-isolated member with a read-only forker should be accepted: %v", err)
	}
	roFk.mu.Lock()
	defer roFk.mu.Unlock()
	mutatingFk.mu.Lock()
	defer mutatingFk.mu.Unlock()
	if len(roFk.labels) != 1 || roFk.labels[0] != "ro" {
		t.Fatalf("read-only forker labels = %v, want [ro] (an isolated read-only member forks via the worktree forker)", roFk.labels)
	}
	if len(mutatingFk.labels) != 0 {
		t.Fatalf("force-copy forker labels = %v, want none (a read-only member must not use the mutating forker)", mutatingFk.labels)
	}
}

// TestSupervisorReadOnlyIsolatedMemberNeedsReadOnlyForker asserts a member marked
// IsolateReadOnly with NO read-only forker wired is rejected with the dedicated
// mis-wire sentinel.
func TestSupervisorReadOnlyIsolatedMemberNeedsReadOnlyForker(t *testing.T) {
	tm := team.New("t")
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), roShellFactory(t, tm))
	err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ro"})
	if !errors.Is(err, agent.ErrReadOnlyShellNoForker) {
		t.Fatalf("AddMember of an IsolateReadOnly member without a read-only forker = %v, want ErrReadOnlyShellNoForker", err)
	}
	if got := tm.Members(); len(got) != 0 {
		t.Errorf("roster = %v, want empty after rejection", got)
	}
}

// TestSupervisorBaseSharingMemberWithBashStillRejected asserts the backstop still
// trips for a GENUINELY base-sharing member: one that holds Bash but did NOT set
// IsolateReadOnly (so the supervisor would run it on the shared base). Its
// mutating-classified tool would corrupt the shared workspace, so it is rejected.
func TestSupervisorBaseSharingMemberWithBashStillRejected(t *testing.T) {
	tm := team.New("t")
	// A read-only forker IS wired, but the factory did NOT mark IsolateReadOnly, so
	// this member would base-share — the backstop must still catch its Bash.
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		catalogFactory(t, tm, fakeMutatingTool{name: "Bash"}),
		agent.WithReadOnlyForker(&recordingForker{}))
	err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ro"})
	if !errors.Is(err, agent.ErrReadOnlyMemberMutating) {
		t.Fatalf("base-sharing member holding Bash = %v, want ErrReadOnlyMemberMutating", err)
	}
	if !strings.Contains(err.Error(), "Bash") {
		t.Errorf("error %q should name the offending tool", err)
	}
}

// TestSupervisorRunsMemberCloseOnCleanup asserts the MemberBuild.Close teardown seam:
// the supervisor composes build.Close with the fork cleanup and runs it on Run's
// cleanupAll, so a per-member inline MCP manager is torn down (no leak).
// TestSupervisorMemberDispositionError asserts a member whose run ends StopError is
// reported with Disposition==stopped and Reason==error, and that the errored round is
// COUNTED on ErrorRounds. It pins the retry-DISABLED tier of issue #318's bounded retry:
// WithMemberErrorRetries(0) is the escape hatch an operator uses to get exactly the
// unchanged benched disposition, so the classification path must still be reachable and
// unchanged. The default tier (one retry) is covered by
// TestSupervisorRetriedMemberContributesInLaterRound and its cap sibling.
//
// The team has no lead, so there is no synthesis to mask the member's terminal state.
func TestSupervisorMemberDispositionError(t *testing.T) {
	tm := team.New("t")
	// One uncooperative turn that ends StopError (a refused/truncated/failed response
	// shape — see mockllm.EmptyTurnWithStop). The run terminates in error, so with retry
	// disabled the member is DESCHEDULED (stopped) and never scheduled again — its
	// session is nonetheless recovered and remains drivable (issue #318).
	prov := mockllm.New(mockllm.EmptyTurnWithStop(session.StopError))
	providers := map[string]*mockllm.Provider{"worker": prov}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, providers), agent.WithMaxRounds(5),
		agent.WithMemberErrorRetries(0))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	out := sup.Run(context.Background(), nil)
	m := singleMember(t, out)
	if m.Disposition != agent.DispositionStopped || m.Reason != agent.StopReasonError {
		t.Fatalf("disposition = %q/%q, want stopped/error", m.Disposition, m.Reason)
	}
	if m.ErrorRounds != 1 {
		t.Errorf("ErrorRounds = %d, want 1 (the errored round is counted even when retry is off)", m.ErrorRounds)
	}
}

// TestSupervisorMemberDispositionBudget asserts a member stopped purely by its
// lifetime turn budget is reported with Disposition==stopped and Reason==budget — NOT
// error. A budget-exhausted member's session re-opens cleanly (it is non-schedulable,
// not non-resumable), so the error/cancelled tests must both be false for the budget
// branch to win.
func TestSupervisorMemberDispositionBudget(t *testing.T) {
	tm := team.New("loop")
	// Each round: ping self (so it is re-planned) then a clean text turn (StopEndTurn,
	// never an error). Long enough to outlast the budget without exhausting the script.
	selfPing := session.NewToolCall("p", "SendMessage",
		json.RawMessage(`{"to":"worker","body":"keep going"}`))
	var turns []mockllm.Turn
	for i := 0; i < 60; i++ {
		turns = append(turns, mockllm.ToolCallTurn(selfPing), mockllm.TextTurn("still working"))
	}
	prov := mockllm.New(turns...)
	providers := map[string]*mockllm.Provider{"worker": prov}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, providers), agent.WithMaxRounds(30), agent.WithMemberTurnBudget(5))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker", InitialPrompt: "loop"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	out := sup.Run(context.Background(), nil)
	m := singleMember(t, out)
	if m.Disposition != agent.DispositionStopped || m.Reason != agent.StopReasonBudget {
		t.Fatalf("disposition = %q/%q, want stopped/budget", m.Disposition, m.Reason)
	}
}

// TestSupervisorMemberDispositionCancelled asserts a member ended by ctx cancellation
// is reported with Disposition==stopped and Reason==cancelled — NOT error. This proves
// the runTurn precedence: a cancelled member's Reopen ALSO fails (Reopen is
// completed-only), so the classification must test stop==StopCancelled BEFORE reopenErr
// would fold it into the generic error class.
func TestSupervisorMemberDispositionCancelled(t *testing.T) {
	tm := team.New("t")
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel the run as soon as the worker's turn reaches the provider, so the member's
	// in-flight run observes the cancellation and ends StopCancelled.
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		prov := mockllm.NewWith(
			[]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { cancel() })},
			mockllm.TextTurn("never reached cleanly"),
		)
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock",
		})}
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory, agent.WithMaxRounds(5))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	out := sup.Run(ctx, nil)
	m := singleMember(t, out)
	if m.Disposition != agent.DispositionStopped || m.Reason != agent.StopReasonCancelled {
		t.Fatalf("disposition = %q/%q, want stopped/cancelled (precedence: StopCancelled before reopenErr)", m.Disposition, m.Reason)
	}
}

// TestSupervisorMemberDispositionDone asserts a member that finishes cleanly is
// reported with Disposition==done and an empty Reason.
func TestSupervisorMemberDispositionDone(t *testing.T) {
	tm := team.New("t")
	prov := mockllm.New(mockllm.TextTurn("all done"))
	providers := map[string]*mockllm.Provider{"worker": prov}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"),
		memberFactory(t, tm, providers), agent.WithMaxRounds(5))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	out := sup.Run(context.Background(), nil)
	m := singleMember(t, out)
	if m.Disposition != agent.DispositionDone || m.Reason != "" {
		t.Fatalf("disposition = %q/%q, want done/\"\"", m.Disposition, m.Reason)
	}
}

// TestSupervisorMemberDispositionNoProgressIsDone asserts a member that ends
// StopNoProgress (the clean reasoning-model terminal introduced by the no-progress
// work) is reported as done — NOT stopped. StopNoProgress is neither StopError nor
// cancellation and its session re-opens, so runTurn never marks it stopped; this guards
// against a future regression that mislabels the clean no-progress terminal.
func TestSupervisorMemberDispositionNoProgressIsDone(t *testing.T) {
	tm := team.New("t")
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		// One empty (no text, no tool call) turn with nudging DISABLED, so the run
		// terminates immediately with StopNoProgress — the clean reasoning-model
		// no-deliverable terminal.
		prov := mockllm.New(mockllm.EmptyTurn())
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock",
			MaxNoProgressNudges: -1,
		})}
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory, agent.WithMaxRounds(5))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "worker", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	out := sup.Run(context.Background(), nil)
	m := singleMember(t, out)
	if m.Disposition != agent.DispositionDone || m.Reason != "" {
		t.Fatalf("disposition = %q/%q, want done/\"\" (StopNoProgress is a clean terminal)", m.Disposition, m.Reason)
	}
}

// singleMember asserts the outcome has exactly one member and returns it.
func singleMember(t *testing.T, out agent.TeamOutcome) agent.MemberOutcome {
	t.Helper()
	if len(out.Members) != 1 {
		t.Fatalf("expected exactly one member outcome, got %d: %+v", len(out.Members), out.Members)
	}
	return out.Members[0]
}

func TestSupervisorRunsMemberCloseOnCleanup(t *testing.T) {
	tm := team.New("t")
	var closed int
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		b := catalogFactory(t, tm)(spec, "")
		b.Close = func() error { closed++; return nil }
		return b
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory)
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ro", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	sup.Run(context.Background(), nil)
	if closed != 1 {
		t.Fatalf("member Close should run exactly once on cleanup, ran %d times", closed)
	}
}

// TestSupervisorRelaysMemberContextWindow is the relay half of the issue #63/#64
// wiring (req 6): the supervisor must tag EVERY member event with the producing
// member engine's ContextWindow() — the denominator the ctrl+a context meter and
// the proto TeamEvent.context_window carry. It builds a member engine on a
// catalog-realistic window (1,050,000 — NOT the 128k floor) and asserts every
// captured TeamEvent for that member reports that window verbatim. The composition
// half (childWindowFor actually resolving that catalog window into the member
// engine) is proven in internal/app's TestMemberEngineRelaysResolvedContextWindow.
func TestSupervisorRelaysMemberContextWindow(t *testing.T) {
	const window = 1_050_000 // a catalogued large-context model window, NOT the 128k floor
	tm := team.New("ctxwin")
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM:           mockllm.New(mockllm.TextTurn("done")),
			Catalog:       cat,
			Policy:        allow,
			Model:         "mock",
			ContextWindow: func() int { return window },
		})}
	}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), factory, agent.WithMaxRounds(2))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	var seen int
	sup.Run(context.Background(), func(ev agent.TeamEvent) {
		if ev.Member != "lead" || ev.Event.Type == "" {
			return // skip the terminal/outcome frame and any non-member frame
		}
		seen++
		// TeamEvent.ContextWindow is the relay seam: the supervisor tags every member
		// event with m.engine.ContextWindow(). projectTeamEvent then copies this onto
		// the wire-bound session.Event (TeamPayload.ContextWindow / proto field 14) on
		// turn.end — proven by TestProjectTeamEventTurnEndContextMeter.
		if ev.ContextWindow != window {
			t.Fatalf("TeamEvent[%s].ContextWindow = %d, want the member engine's resolved window %d (NOT the 128k floor)",
				ev.Event.Type, ev.ContextWindow, window)
		}
	})
	if seen == 0 {
		t.Fatal("no member events captured; the relay assertion was vacuous")
	}
}
