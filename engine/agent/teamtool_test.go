package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// teamToolFactory builds the unified member-engine factory the Team tool needs: a
// per-member Engine whose provider is looked up by member name and whose catalog
// carries that member's coordination tools (bound to the per-call team). It mirrors
// the composition root's buildMemberEngine shape, scoped for an offline test.
func teamToolFactory(t *testing.T, providers map[string]*mockllm.Provider) agent.TeamMemberEngineFactory {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("teamToolFactory: no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		eng := agent.NewEngine(agent.Deps{
			LLM:     prov,
			Catalog: cat,
			Policy:  allow,
			Hooks:   noopHooks{},
			Model:   "member-model",
		})
		return agent.MemberBuild{Engine: eng}
	}
}

// TestTeamToolFormsTeamAndIsolatesContent is the team analogue of gauntlet #7
// (content isolation), adapted for the Team tool. The main model calls Team with a
// 2-member roster; the test asserts:
//   - team.start carries the roster the model formed (names/roles/lead flag).
//   - interleaved team.member events are tagged by member and carry CONTENT (the
//     member's message text and tool names) — the fuller-but-bounded projection.
//   - team.end carries the round count.
//   - CRITICALLY, the parent's terminal ToolResult and the parent Conversation
//     contain ONLY the joined summary — none of the per-member transcripts.
func TestTeamToolFormsTeamAndIsolatesContent(t *testing.T) {
	// Lead: creates a task, then (after the worker reports) synthesises.
	addTask := session.NewToolCall("l1", "AddTask",
		json.RawMessage(`{"description":"investigate the reported bug"}`))
	// Each member turn carries a known per-turn usage (via UsageChunk on turn.end)
	// so the test can assert team.end's Usage is the SUM of every member turn.
	leadProv := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(addTask),
			mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("LEAD_SECRET: delegated, waiting for worker"),
			mockllm.UsageChunk(session.Usage{InputTokens: 20, OutputTokens: 4}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("LEAD_SECRET: worker reported; team complete"),
			mockllm.UsageChunk(session.Usage{InputTokens: 30, OutputTokens: 6}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		// Synthesis turn: the lead consolidates into the team's deliverable.
		mockllm.ChunksTurn(
			mockllm.TextChunk("CONSOLIDATED REPORT for lead and worker: bug fixed."),
			mockllm.UsageChunk(session.Usage{InputTokens: 60, OutputTokens: 12}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)

	// Worker: completes the task and reports back.
	complete := session.NewToolCall("w1", "CompleteTask", json.RawMessage(`{"task_id":"task-1"}`))
	report := session.NewToolCall("w2", "SendMessage",
		json.RawMessage(`{"to":"lead","body":"WORKER_SECRET: root cause found"}`))
	workerProv := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(complete),
			mockllm.ToolCallChunk(report),
			mockllm.UsageChunk(session.Usage{InputTokens: 40, OutputTokens: 8}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("WORKER_SECRET: investigation complete"),
			mockllm.UsageChunk(session.Usage{InputTokens: 50, OutputTokens: 10}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"fix the bug","members":[{"name":"lead","role":"coordinate the fix"},{"name":"worker","role":"investigate and report"}]}`)),
		mockllm.TextTurn("parent received the team summary"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "fix the bug"})
	evs := drain(r)

	// --- team.start: roster the model formed -------------------------------
	var start *session.TeamPayload
	for _, ev := range evs {
		if ev.Type == session.EvTeamStart {
			start = ev.Team
		}
	}
	if start == nil {
		t.Fatalf("no team.start event in %v", typesOf(evs))
	}
	if len(start.Roster) != 2 {
		t.Fatalf("roster len = %d, want 2: %+v", len(start.Roster), start.Roster)
	}
	if start.Roster[0].Name != "lead" || !start.Roster[0].Lead {
		t.Errorf("first roster member should be the synthesized lead: %+v", start.Roster[0])
	}
	if start.Roster[1].Name != "worker" || start.Roster[1].Lead {
		t.Errorf("second roster member should be a non-lead worker: %+v", start.Roster[1])
	}

	// --- team.member: interleaved, tagged, WITH content --------------------
	var members []*session.TeamPayload
	sawLeadText, sawWorkerToolCall := false, false
	for _, ev := range evs {
		if ev.Type != session.EvTeamMember {
			continue
		}
		members = append(members, ev.Team)
		if ev.Team.Member == "lead" && strings.Contains(ev.Team.Text, "delegated") {
			sawLeadText = true
		}
		if ev.Team.Member == "worker" && ev.Team.InnerKind == session.EvToolCall &&
			ev.Team.ToolName == "CompleteTask" {
			sawWorkerToolCall = true
		}
	}
	if len(members) == 0 {
		t.Fatalf("no team.member events forwarded")
	}
	if !sawLeadText {
		t.Errorf("expected a lead team.member event carrying message text (fuller projection)")
	}
	if !sawWorkerToolCall {
		t.Errorf("expected a worker team.member event carrying the CompleteTask tool name")
	}

	// --- team.end: round count + summed team-total usage -------------------
	var end *session.TeamPayload
	for _, ev := range evs {
		if ev.Type == session.EvTeamEnd {
			end = ev.Team
		}
	}
	if end == nil {
		t.Fatalf("no team.end event")
	}
	if end.Rounds < 1 {
		t.Errorf("team.end rounds = %d, want >= 1", end.Rounds)
	}

	// team.end.Usage must be the SUM of every member's per-turn usage, reconstructed
	// here from the per-event usage carried on the forwarded team.member turn.end
	// events. With the scripted turns above this is in=150 out=30; deriving the
	// expected sum from the stream (rather than hardcoding) keeps the assertion
	// robust to the exact number of rounds.
	var wantUsage session.Usage
	for _, ev := range evs {
		if ev.Type == session.EvTeamMember && ev.Team.InnerKind == session.EvTurnEnd {
			wantUsage = wantUsage.Add(ev.Team.Usage)
		}
	}
	if wantUsage == (session.Usage{}) {
		t.Fatalf("test setup: no member turn.end usage was forwarded to sum")
	}
	if end.Usage != wantUsage {
		t.Errorf("team.end usage = %+v, want the summed member total %+v", end.Usage, wantUsage)
	}
	// end.Usage is the EvTeamEnd DISPLAY payload (summed from member turn.end events,
	// memberEventUsage) — the meter-class figure, NOT the budget figure (the supervisor's
	// budget total reads per-drive EvResult.Usage = provider truth; see
	// TestTeamTokenBudgetZeroUsageNeverTrips). The 6 scripted member turns report
	// in=210 out=42; additional usage-LESS turns (an exhausted mockllm script yields an
	// empty turn) pick up the issue-#82 DISPLAY-ONLY zero-usage estimate on their
	// turn.end, so the summed input is >= 210 (a small positive estimate, never the
	// budget). Output is never fabricated, so it stays exactly 42.
	if end.Usage.OutputTokens != 42 {
		t.Errorf("team.end output = %d, want 42 (sum of scripted turn output; the zero-usage fallback never fabricates output)", end.Usage.OutputTokens)
	}
	if end.Usage.InputTokens < 210 {
		t.Errorf("team.end input = %d, want >= 210 (scripted 210 + display-only zero-usage estimate on usage-less turns)", end.Usage.InputTokens)
	}

	// --- CONTENT ISOLATION: only the joined summary enters the parent ------
	var parentResults []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult {
			parentResults = append(parentResults, ev.ToolResult)
		}
	}
	if len(parentResults) != 1 {
		t.Fatalf("parent saw %d tool results, want exactly 1 (the Team summary)", len(parentResults))
	}
	summary := parentResults[0].Content
	// The returned deliverable is now the LEAD's consolidated synthesis (TeamOutcome
	// .Report), NOT a header-only concatenation of member LastText. The isolation
	// guarantee is about the parent CONVERSATION (the LLM context), asserted below: the
	// per-member transcript (intermediate tool calls/results, mid-run messages) must not
	// be in Conversation. The synthesis text is the lead's own authored summary, which
	// legitimately names members.
	if !strings.Contains(summary, "CONSOLIDATED REPORT") {
		t.Errorf("returned result should be the lead's consolidated synthesis, got: %q", summary)
	}
	// The deliverable chain returns the lead's synthesis (tier 1, a usable non-refusal
	// report), NOT the degraded structured fallback — the synthesis text is present and
	// the "Recorded findings:" fallback header is absent.
	if strings.Contains(summary, "Recorded findings:") {
		t.Errorf("a usable synthesis must NOT be degraded to the structured fallback: %q", summary)
	}
	// WHY the non-convergence banner is expected: this scripted roster never reaches
	// genuine quiescence — the worker ends after reporting but the lead keeps producing
	// LEAD_SECRET turns, so the team stops at the round cap (Quiescent=false) rather than
	// idling to convergence. deliverable therefore prepends the "did NOT converge" banner
	// to tier 1 (AC6). This assertion depends on that SCHEDULER behaviour, not on the
	// deliverable chain itself; a roster that idled cleanly would omit the banner.
	if !strings.Contains(summary, "did NOT converge") {
		t.Errorf("a non-quiescent team's deliverable must carry the non-convergence banner (AC6): %q", summary)
	}

	// The parent Conversation must contain ONLY: the user prompt, the parent's Team
	// tool call, the Team ToolResult (the joined summary), and the parent's final
	// text. It must NOT contain any member's intermediate tool calls (AddTask /
	// CompleteTask / SendMessage) nor any member message text other than via the
	// summary ToolResult.
	for _, msg := range sess.Conversation.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Name == "AddTask" || tc.Name == "CompleteTask" || tc.Name == "SendMessage" {
				t.Fatalf("member tool call %q leaked into parent Conversation", tc.Name)
			}
		}
		// A member's intermediate message text ("delegated, waiting") must not appear
		// in any parent message TEXT (it may legitimately appear only inside the Team
		// ToolResult content, which is the summary's member LastText — checked via the
		// ToolResult body, not message Text).
		if strings.Contains(msg.Text, "delegated, waiting") {
			t.Fatalf("member intermediate message text leaked into parent Conversation message text: %q", msg.Text)
		}
	}
}

// TestTeamToolStreamsTaskSnapshots drives a real task lifecycle through a full team
// run (lead creates a task, worker completes it) and asserts the task-snapshot stream
// contract: first-class team.tasks events carry the shared task list (no Member); the
// snapshot is DE-DUPED (an unchanged list is not re-emitted, so distinct snapshots are
// bounded by the number of real transitions, not by member-event volume); and the
// terminal team.end carries the final, completed task list.
//
// The stream is CHANGE-DRIVEN and EVENTUALLY-CONSISTENT, NOT per-transition-guaranteed:
// the sink samples live team state (tm.Tasks()) when the single forwarder goroutine
// drains each buffered event, decoupled in time from the member/supervisor goroutines
// that mutate the task list. Under scheduler starvation the forwarder can lag until the
// task is already completed, so every drained event reads the same terminal state and the
// intermediate pending/in_progress snapshots legitimately coalesce away. Asserting that a
// pre-completed state appears in the stream is therefore inherently scheduling-dependent
// (it flaked in CI under load) — so this test asserts only the deterministic properties:
// de-dup, no-Member, and the authoritative TERMINAL state (the last live snapshot and the
// settled team.end both show completed). Per-transition visibility is best-effort; making
// it a guarantee would require capturing the snapshot at mutation time and is a separate,
// deliberate emit-path change (see the "team-snapshot fidelity note" in
// docs/design/IMPLEMENTATION-NOTES.md), not a test fix.
func TestTeamToolStreamsTaskSnapshots(t *testing.T) {
	// The lead creates the task on its first turn, then idles. The worker's first
	// turn is a no-op (the task does not exist yet in round 1); the supervisor then
	// auto-claims task-1 for the idle worker, and the worker COMPLETES it on a later
	// turn — driving a pending → in_progress → completed lifecycle in the shared state
	// (whether each step surfaces as a distinct streamed snapshot is best-effort; see
	// the doc comment).
	addTask := session.NewToolCall("l1", "AddTask",
		json.RawMessage(`{"description":"investigate the reported bug"}`))
	leadProv := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(addTask),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("delegated, waiting for worker"),
		mockllm.TextTurn("worker reported; team complete"),
		mockllm.TextTurn("team complete"),
	)
	complete := session.NewToolCall("w1", "CompleteTask", json.RawMessage(`{"task_id":"task-1"}`))
	workerProv := mockllm.New(
		mockllm.TextTurn("standing by"),
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(complete),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("investigation complete"),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"fix the bug","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent received the team summary"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "fix the bug"})
	evs := drain(r)

	// Collect the task-snapshot projections (the first-class team.tasks event).
	var snapshots [][]session.TeamTaskSnapshot
	for _, ev := range evs {
		if ev.Type == session.EvTeamTasks {
			if ev.Team.Member != "" {
				t.Errorf("a task snapshot must carry no Member, got %q", ev.Team.Member)
			}
			snapshots = append(snapshots, ev.Team.Tasks)
		}
	}
	if len(snapshots) == 0 {
		t.Fatalf("no task-snapshot (team.tasks) events streamed")
	}
	// De-dup: every consecutive snapshot must differ from its predecessor (the sink
	// only emits on change). Equal adjacent snapshots would prove the guard is dead.
	for i := 1; i < len(snapshots); i++ {
		if tasksSnapshotEqual(snapshots[i-1], snapshots[i]) {
			t.Errorf("snapshot %d duplicates snapshot %d (de-dup guard failed): %+v", i, i-1, snapshots[i])
		}
	}
	// The TERMINAL streamed snapshot is authoritative: the last live snapshot shows
	// task-1 completed. (The first/intermediate snapshots are NOT asserted — see the
	// doc comment: under forwarder lag they legitimately coalesce to the terminal
	// state, so a "pre-completed appears" assertion is non-deterministic.)
	last := snapshots[len(snapshots)-1]
	if len(last) != 1 || last[0].ID != "task-1" || last[0].State != string(team.TaskCompleted) {
		t.Errorf("last snapshot should show task-1 completed, got %+v", last)
	}

	// team.end carries the terminal task snapshot (the final, completed list).
	var end *session.TeamPayload
	for _, ev := range evs {
		if ev.Type == session.EvTeamEnd {
			end = ev.Team
		}
	}
	if end == nil {
		t.Fatal("no team.end event")
	}
	if len(end.Tasks) != 1 || end.Tasks[0].State != string(team.TaskCompleted) {
		t.Errorf("team.end should carry the final completed task list, got %+v", end.Tasks)
	}
}

// TestTeamFindingsProjectedOnChangeAndOnEnd drives a scripted team whose worker
// records findings and asserts the findings-snapshot stream contract (mirroring the
// task-snapshot one): first-class team.findings events carry the ledger (no Member);
// the snapshot is DE-DUPED (an unchanged ledger is not re-emitted); bodies are
// clamped; and the terminal team.end carries the final findings snapshot.
func TestTeamFindingsProjectedOnChangeAndOnEnd(t *testing.T) {
	// The worker records two findings (one per turn) so the ledger changes twice.
	f1 := session.NewToolCall("w1", "RecordFinding", json.RawMessage(`{"finding":"first finding"}`))
	f2 := session.NewToolCall("w2", "RecordFinding", json.RawMessage(`{"finding":"second finding"}`))
	leadProv := mockllm.New(
		mockllm.TextTurn("delegating"), // round 0
		mockllm.TextTurn("the report"), // synthesis
	)
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(f1), // round 0 turn 1
		mockllm.ToolCallTurn(f2), // round 0 turn 2
		mockllm.TextTurn("done"), // round 0 turn 3 (ends run)
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"investigate","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent received the report"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate"})
	evs := drain(r)

	var snapshots [][]session.TeamFindingSnapshot
	var memberEvents int
	for _, ev := range evs {
		if ev.Type == session.EvTeamMember {
			memberEvents++
		}
		if ev.Type == session.EvTeamFindings {
			if ev.Team.Member != "" {
				t.Errorf("a findings snapshot must carry no Member, got %q", ev.Team.Member)
			}
			snapshots = append(snapshots, ev.Team.Findings)
		}
	}
	if len(snapshots) == 0 {
		t.Fatalf("no findings-snapshot (team.findings) events streamed")
	}
	// De-dup: every consecutive snapshot must differ from its predecessor.
	for i := 1; i < len(snapshots); i++ {
		if findingsSnapshotEqual(snapshots[i-1], snapshots[i]) {
			t.Errorf("snapshot %d duplicates snapshot %d (de-dup guard failed): %+v", i, i-1, snapshots[i])
		}
	}
	// TRUE de-dup proof: the ledger changed exactly TWICE (two RecordFinding calls), so
	// at most 2 team.findings events were emitted — even though the sink fires for EVERY
	// member event. If the de-dup branch were a no-op, every member event would re-emit
	// the unchanged ledger and this count would balloon to ~memberEvents.
	if len(snapshots) > 2 {
		t.Errorf("emitted %d findings snapshots for 2 ledger changes — de-dup not bounding emits", len(snapshots))
	}
	if memberEvents <= len(snapshots) {
		t.Fatalf("test setup too weak to prove de-dup: %d member events vs %d snapshots (need many more member events than emits)",
			memberEvents, len(snapshots))
	}
	// The ledger grew across snapshots: the last snapshot holds both findings.
	last := snapshots[len(snapshots)-1]
	if len(last) != 2 || last[0].Body != "first finding" || last[1].Body != "second finding" {
		t.Errorf("last findings snapshot = %+v, want both findings in append order", last)
	}
	if last[0].Member != "worker" {
		t.Errorf("finding member = %q, want worker", last[0].Member)
	}

	// team.end carries the terminal findings snapshot.
	var end *session.TeamPayload
	for _, ev := range evs {
		if ev.Type == session.EvTeamEnd {
			end = ev.Team
		}
	}
	if end == nil {
		t.Fatal("no team.end event")
	}
	if len(end.Findings) != 2 {
		t.Errorf("team.end should carry the final findings ledger, got %+v", end.Findings)
	}
}

// teamResultContent drives a 2-member team (lead coordinates, worker records ONE finding
// then ends) through the real Team tool path with a scripted LEAD synthesis text, and
// returns the parent's single Team ToolResult content. It is the shared harness for the
// deliverable-resilience e2e tests: the only variable across them is the lead's synthesis
// text, so each test passes its own and asserts on the resulting deliverable.
func teamResultContent(t *testing.T, leadSynthesis string) string {
	t.Helper()
	finding := session.NewToolCall("w1", "RecordFinding",
		json.RawMessage(`{"finding":"auth path validates tokens in middleware.go"}`))
	leadProv := mockllm.New(
		mockllm.TextTurn("delegating to the worker"), // round 0
		mockllm.TextTurn(leadSynthesis),              // synthesis (last lead turn)
	)
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(finding), // round 0 turn 1: record a finding
		mockllm.TextTurn("done"),      // round 0 turn 2: end the run cleanly
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"investigate the auth path","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate and report"}]}`)),
		mockllm.TextTurn("parent received the team result"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate the auth path"})
	evs := drain(r)

	var results []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult {
			results = append(results, ev.ToolResult)
		}
	}
	if len(results) != 1 {
		t.Fatalf("parent saw %d tool results, want exactly 1 (the Team deliverable)", len(results))
	}
	// AC9 (gauntlet #7): the member's intermediate activity must NOT leak into the parent
	// Conversation — only the single Team ToolResult folds back. Assert the isolation
	// property holds regardless of the deliverable's content.
	for _, msg := range sess.Conversation.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Name == "RecordFinding" || tc.Name == "CompleteTask" || tc.Name == "SendMessage" {
				t.Fatalf("member tool call %q leaked into parent Conversation", tc.Name)
			}
		}
	}
	return results[0].Content
}

// TestTeamToolRefusalSynthesisFallsBackToLedger is the HEADLINE incident-regression
// guard: when the lead's synthesis is a REFUSAL ("I'm sorry, but I cannot assist…") yet
// the team recorded a finding, the deliverable that folds back to the parent must be the
// ledger-rich structured fallback — the recorded finding present, the refusal text GONE.
// Reverting the isNonDeliverable refusal check makes this test fail (the refusal would
// pass through tier 1).
func TestTeamToolRefusalSynthesisFallsBackToLedger(t *testing.T) {
	content := teamResultContent(t, "I'm sorry, but I cannot assist with that request.")

	if strings.Contains(content, "I cannot assist") || strings.Contains(content, "I'm sorry") {
		t.Errorf("the refusal text must be DISCARDED, not returned to the parent:\n%s", content)
	}
	if !strings.Contains(content, "auth path validates tokens in middleware.go") {
		t.Errorf("the recorded finding must reach the fallback deliverable (ledger fallback):\n%s", content)
	}
	if !strings.Contains(content, "Recorded findings:") {
		t.Errorf("the structured fallback must lead with the findings ledger:\n%s", content)
	}
	if !strings.Contains(content, "From worker:") {
		t.Errorf("the fallback must group findings by member:\n%s", content)
	}
}

// TestTeamToolShortValidSynthesisKept is the no-false-positive guard (AC2): a SHORT but
// legitimate, non-refusal-shaped report is KEPT verbatim (tier 1 passthrough), NOT
// degraded to the structured fallback — even though the ledger is populated.
func TestTeamToolShortValidSynthesisKept(t *testing.T) {
	const report = "All modules reviewed; no security issues found."
	content := teamResultContent(t, report)

	if !strings.Contains(content, report) {
		t.Errorf("a short valid synthesis must pass through verbatim:\n%s", content)
	}
	if strings.Contains(content, "Recorded findings:") {
		t.Errorf("a valid synthesis must NOT be degraded to the structured fallback:\n%s", content)
	}
}

// TestTeamToolEmptySynthesisFallsBack asserts AC3/AC5: an EMPTY lead synthesis still
// falls back to the ledger-rich structured deliverable containing the recorded finding
// (the pre-existing empty-fallback behaviour, now enriched with the grouped ledger).
func TestTeamToolEmptySynthesisFallsBack(t *testing.T) {
	content := teamResultContent(t, "")

	if !strings.Contains(content, "auth path validates tokens in middleware.go") {
		t.Errorf("an empty synthesis must fall back to the ledger-rich deliverable:\n%s", content)
	}
	if !strings.Contains(content, "Recorded findings:") {
		t.Errorf("the empty-synthesis fallback must lead with the findings ledger:\n%s", content)
	}
}

// TestTeamEndCarriesMemberDispositions drives a real team run through the Team tool →
// supervisor → EvTeamEnd path and asserts the per-member terminal disposition snapshot
// is bridged onto the EMITTED team.end payload (the contractual stitch the supervisor /
// mapper tests bracket but neither exercises): the supervisor's verdict for a STOPPED
// member must actually reach EvTeamEnd.Dispositions with the right {name, disposition,
// reason}, and a clean member must reach it as done/no-reason. It also pins the
// ErrorRounds count (issue #318) across the same stitch: a run-level failure must be
// countable on the wire, not only inferable from the Stopped flag.
//
// The Team tool builds the supervisor itself, so this runs on the DEFAULT retry cap: the
// worker therefore needs TWO errored rounds to be benched (the first is retried), which
// is exactly the end-to-end default-tier coverage the supervisor-level tests pin in
// isolation. The lead finishes cleanly and synthesises, so it is done with 0 error rounds.
func TestTeamEndCarriesMemberDispositions(t *testing.T) {
	leadProv := mockllm.New(
		mockllm.TextTurn("delegating to the worker"), // round 0
		mockllm.TextTurn("CONSOLIDATED REPORT"),      // synthesis
	)
	// Both of the worker's turns end StopError (a refused/truncated/failed response
	// shape). The first is RECOVERED and retried; the second exceeds the default retry
	// cap → the supervisor benches it stopped with reason=error.
	workerProv := mockllm.New(
		mockllm.EmptyTurnWithStop(session.StopError),
		mockllm.EmptyTurnWithStop(session.StopError),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"investigate","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent received the report"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate"})
	evs := drain(r)

	var end *session.TeamPayload
	for _, ev := range evs {
		if ev.Type == session.EvTeamEnd {
			end = ev.Team
		}
	}
	if end == nil {
		t.Fatal("no team.end event")
	}
	if len(end.Dispositions) == 0 {
		t.Fatalf("team.end carried no member dispositions; the supervisor verdict did not reach the wire")
	}
	byName := map[string]session.TeamMemberDisposition{}
	for _, d := range end.Dispositions {
		byName[d.Name] = d
	}
	worker, ok := byName["worker"]
	if !ok {
		t.Fatalf("team.end dispositions missing the worker: %+v", end.Dispositions)
	}
	if worker.Disposition != "stopped" || worker.Reason != "error" {
		t.Errorf("worker disposition = %q/%q, want stopped/error", worker.Disposition, worker.Reason)
	}
	// Both errored rounds are counted — the retried one AND the one that benched it.
	if worker.ErrorRounds != 2 {
		t.Errorf("worker ErrorRounds = %d, want 2 (one retried round + the round that hit the cap)", worker.ErrorRounds)
	}
	lead, ok := byName["lead"]
	if !ok {
		t.Fatalf("team.end dispositions missing the lead: %+v", end.Dispositions)
	}
	if lead.Disposition != "done" || lead.Reason != "" {
		t.Errorf("lead disposition = %q/%q, want done/\"\"", lead.Disposition, lead.Reason)
	}
	if lead.ErrorRounds != 0 {
		t.Errorf("a clean lead must report ErrorRounds = 0, got %d", lead.ErrorRounds)
	}
}

// TestTeamFindingsBodyClamped asserts a long finding body is CLAMPED on the
// projected snapshot (the same cap every member-derived preview uses), never copied
// verbatim onto the stream.
func TestTeamFindingsBodyClamped(t *testing.T) {
	longBody := strings.Repeat("x", 500)
	f := session.NewToolCall("w1", "RecordFinding",
		json.RawMessage(`{"finding":"`+longBody+`"}`))
	leadProv := mockllm.New(mockllm.TextTurn("delegating"), mockllm.TextTurn("report"))
	workerProv := mockllm.New(mockllm.ToolCallTurn(f), mockllm.TextTurn("done"))
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"g","members":[{"name":"lead","role":"c"},{"name":"worker","role":"w"}]}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "g"}))

	for _, ev := range evs {
		if ev.Type == session.EvTeamFindings && len(ev.Team.Findings) > 0 {
			body := ev.Team.Findings[0].Body
			if len([]rune(body)) >= len(longBody) {
				t.Fatalf("finding body was not clamped: len=%d", len([]rune(body)))
			}
			return
		}
	}
	t.Fatal("no team.findings event carried the recorded finding")
}

// TestMemberSessionIDRoundTripsTeamToolPath pins the id-scheme contract on the Team
// TOOL path: the member session the supervisor persists must load under
// MemberSessionID(publishedTeamID, member), where publishedTeamID is the EXACT
// EvTeamStart.TeamID the parent observed. This is the zero-wire-risk guard that the
// producer (save) and consumer (InspectMember) cannot drift.
func TestMemberSessionIDRoundTripsTeamToolPath(t *testing.T) {
	leadProv := mockllm.New(mockllm.TextTurn("delegating"), mockllm.TextTurn("report"))
	workerProv := mockllm.New(mockllm.TextTurn("done"))
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	store := memstore.New()
	teamTool := agent.NewTeamTool(teamToolFactory(t, providers), agent.WithTeamToolStore(store))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"investigate","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent received the report"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate"}))

	// Capture the EXACT published team id off EvTeamStart — the value a parent (or a
	// human) would feed back into InspectMember.
	var publishedTeamID string
	for _, ev := range evs {
		if ev.Type == session.EvTeamStart && ev.Team != nil {
			publishedTeamID = ev.Team.TeamID
		}
	}
	if publishedTeamID == "" {
		t.Fatal("no EvTeamStart team id observed")
	}

	// Each member's persisted session must load under MemberSessionID(publishedTeamID, name).
	for _, name := range []string{"lead", "worker"} {
		id := agent.MemberSessionID(publishedTeamID, name)
		got, err := store.Load(context.Background(), id)
		if err != nil {
			t.Fatalf("member %q not persisted under MemberSessionID(%q,%q)=%q: %v", name, publishedTeamID, name, id, err)
		}
		if got.ID != id {
			t.Errorf("loaded session id = %q, want %q", got.ID, id)
		}
		wantRelationship := session.SessionRelationship{TeamID: publishedTeamID, MemberName: name, ParentSessionID: sess.ID}
		if got.Kind != session.SessionKindTeamMember || got.Relationship != wantRelationship {
			t.Errorf("member metadata = (%q, %+v), want (%q, %+v)", got.Kind, got.Relationship, session.SessionKindTeamMember, wantRelationship)
		}
	}
}

// findingsSnapshotEqual is the test-side mirror of teamtool.go's findingsEqual.
func findingsSnapshotEqual(a, b []session.TeamFindingSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Member != b[i].Member || a[i].Body != b[i].Body {
			return false
		}
	}
	return true
}

// tasksSnapshotEqual is the test-side mirror of teamtool.go's tasksEqual, comparing
// two task snapshots on the fields the de-dup guard keys off (id/state/assignee/deps).
func tasksSnapshotEqual(a, b []session.TeamTaskSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].State != b[i].State || a[i].Assignee != b[i].Assignee {
			return false
		}
		if len(a[i].Deps) != len(b[i].Deps) {
			return false
		}
		for j := range a[i].Deps {
			if a[i].Deps[j] != b[i].Deps[j] {
				return false
			}
		}
	}
	return true
}

// TestTeamToolReadOnlyIsFalse pins the mutate-serial contract: unlike the
// read-parallel Subagent/Fork tools, the Team tool reports ReadOnly() == false so the
// dispatcher serialises it.
func TestTeamToolReadOnlyIsFalse(t *testing.T) {
	tt := agent.NewTeamTool(func(*team.Team, agent.MemberSpec, string) agent.MemberBuild { return agent.MemberBuild{} })
	ro, ok := tt.(interface{ ReadOnly() bool })
	if !ok {
		t.Fatalf("Team tool does not expose ReadOnly()")
	}
	if ro.ReadOnly() {
		t.Errorf("Team.ReadOnly() = true, want false (teams are mutate-serial)")
	}
}

// TestTeamToolBadRoster asserts a malformed roster yields a recoverable tool ERROR
// result (not a harness error), so the model can retry. Cases: empty goal, empty
// members, empty member name, duplicate names, empty role.
func TestTeamToolBadRoster(t *testing.T) {
	tt := agent.NewTeamTool(func(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
		t.Fatalf("factory must not be called for an invalid roster")
		return agent.MemberBuild{}
	})
	cases := []struct {
		name string
		args string
	}{
		{"empty goal", `{"goal":"  ","members":[{"name":"a","role":"r"}]}`},
		{"no members", `{"goal":"g","members":[]}`},
		{"empty name", `{"goal":"g","members":[{"name":"","role":"r"}]}`},
		{"duplicate names", `{"goal":"g","members":[{"name":"a","role":"r"},{"name":"a","role":"r"}]}`},
		{"empty role", `{"goal":"g","members":[{"name":"a","role":" "}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tt.Execute(context.Background(),
				toolCall("c1", "Team", tc.args), agent.MemEnv("/ws"))
			if err != nil {
				t.Fatalf("Execute returned a harness error, want a tool error result: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected an error tool result for %s, got: %q", tc.name, res.Content)
			}
		})
	}
}

// TestTeamToolMutatingMemberNoForker asserts that a Mutating member with no Forker
// wired yields a recoverable tool error (the model can retry with a read-only
// roster) rather than panicking or returning a harness error.
func TestTeamToolMutatingMemberNoForker(t *testing.T) {
	providers := map[string]*mockllm.Provider{
		"lead": mockllm.New(mockllm.TextTurn("ok")),
	}
	// No WithTeamToolForker → a Mutating member cannot be enrolled.
	tt := agent.NewTeamTool(teamToolFactory(t, providers))
	res, err := tt.Execute(context.Background(),
		toolCall("c1", "Team",
			`{"goal":"g","members":[{"name":"lead","role":"do it","mutating":true}]}`),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("Execute returned a harness error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected a tool error for a Mutating member with no forker, got: %q", res.Content)
	}
}

// TestTeamToolReadOnlyMemberForksViaReadOnlyForker locks the IN-CATALOG Team-tool
// entry point in step with the gRPC path (the drift-twin): a factory that marks a
// read-only member IsolateReadOnly must, when driven through TeamTool.Execute, fork
// that member via the injected READ-ONLY forker (the worktree seam) — not the
// mutating force-copy forker. This is the same three-tier behaviour
// TestSupervisorReadOnlyIsolatedMemberForksViaReadOnlyForker asserts on the
// supervisor directly; here it is proven through the TeamTool wiring.
func TestTeamToolReadOnlyMemberForksViaReadOnlyForker(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	// A factory that grants the member a (mutating) Bash stand-in and marks it
	// IsolateReadOnly — the composition-layer signal that it was granted a shell and
	// must run in an isolated worktree via the read-only forker.
	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(fakeMutatingTool{name: "Bash"})
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("inspection done")),
			Catalog: cat,
			Policy:  allow,
			Model:   "member-model",
		})
		return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
	}

	mutatingFk := &recordingForker{}
	roFk := &recordingForker{}
	tt := agent.NewTeamTool(factory,
		agent.WithTeamToolForker(mutatingFk),
		agent.WithTeamToolReadOnlyForker(roFk))

	res, err := tt.Execute(context.Background(),
		toolCall("c1", "Team", `{"goal":"inspect","members":[{"name":"lead","role":"inspect the history"}]}`),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("Execute returned a harness error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Team execute errored: %q", res.Content)
	}

	roFk.mu.Lock()
	defer roFk.mu.Unlock()
	mutatingFk.mu.Lock()
	defer mutatingFk.mu.Unlock()
	if len(roFk.labels) != 1 || roFk.labels[0] != "lead" {
		t.Fatalf("read-only forker labels = %v, want [lead] (the IsolateReadOnly member forks via the worktree forker through TeamTool)", roFk.labels)
	}
	if len(mutatingFk.labels) != 0 {
		t.Fatalf("force-copy forker labels = %v, want none (a read-only member must not use the mutating forker)", mutatingFk.labels)
	}
}

// TestTeamToolNilFactoryPanics pins the composition-root contract: a Team tool with
// no member-engine factory is a programming error.
func TestTeamToolNilFactoryPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("NewTeamTool(nil) did not panic")
		}
	}()
	_ = agent.NewTeamTool(nil)
}

// TestTeamToolParentCancelStops asserts the parent ctx flows into the supervisor: a
// cancelled parent context stops the team promptly and the tool still returns a
// (joined-summary) result. The supervisor's deferred cleanupAll tears down member
// workspaces on exit; with read-only members there are no forks to leak, and the
// run must not hang.
func TestTeamToolParentCancelStops(t *testing.T) {
	providers := map[string]*mockllm.Provider{
		"lead": mockllm.New(
			mockllm.TextTurn("r1"), mockllm.TextTurn("r2"), mockllm.TextTurn("r3"),
		),
	}
	tt := agent.NewTeamTool(teamToolFactory(t, providers))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before running

	res, err := tt.Execute(ctx,
		toolCall("c1", "Team", `{"goal":"g","members":[{"name":"lead","role":"loop"}]}`),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("Execute returned a harness error on cancel: %v", err)
	}
	// A cancelled run returns a result (the joined summary); it must not hang or error
	// at the harness level. The content is the summary of however little ran.
	if strings.TrimSpace(res.Content) == "" {
		t.Fatalf("expected a non-empty joined summary even on cancel")
	}
}

// TestTeamMemberResultCarriesCauseOnStopError is the Team mirror of
// TestSubagentEndEventCarriesClampedCause: a worker whose round ends StopError with a
// multi-line, over-long provider error must forward that failure detail as Cause on the
// per-round EvTeamMember (InnerKind=EvResult) — clamped and single-line — and ONLY on
// that result event (zero cause on the message.delta/tool.call/tool.result/turn.end team
// member events). The default retry cap means the worker needs TWO errored rounds to be
// benched; both carry the cause.
func TestTeamMemberResultCarriesCauseOnStopError(t *testing.T) {
	// Multi-line AND over-long: the two halves of the field's normalisation contract in
	// one adversarial input, driven through the real supervisor loop.
	huge := "BOOM-\n  upstream detail\n\t" + strings.Repeat("z", 5000)
	leadProv := mockllm.New(
		mockllm.TextTurn("delegating"),          // round 0
		mockllm.TextTurn("CONSOLIDATED REPORT"), // synthesis
	)
	// Both worker rounds fail with the same provider error; the first is recovered+retried,
	// the second benches it stopped/error.
	workerProv := mockllm.New(
		mockllm.ErrorTurn(errors.New(huge)),
		mockllm.ErrorTurn(errors.New(huge)),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"investigate","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent received the report"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate"})
	evs := drain(r)

	var resultCauses []string
	var causesOnOtherKinds int
	for _, ev := range evs {
		if ev.Type != session.EvTeamMember || ev.Team == nil {
			continue
		}
		if ev.Team.Member != "worker" {
			continue
		}
		if ev.Team.InnerKind == session.EvResult {
			resultCauses = append(resultCauses, ev.Team.Cause)
			continue
		}
		if ev.Team.Cause != "" {
			causesOnOtherKinds++
		}
	}
	if len(resultCauses) == 0 {
		t.Fatalf("no worker result events carried a cause; the round failure did not project")
	}
	if causesOnOtherKinds != 0 {
		t.Fatalf("Cause is a result-only field, but %d other team.member events carried one", causesOnOtherKinds)
	}
	for _, c := range resultCauses {
		if !strings.Contains(c, "BOOM-") {
			t.Fatalf("worker result cause must carry the failure marker, got %q", c)
		}
		if n := len([]rune(c)); n > 401 { // maxSubagentCausePreview (400) + the ellipsis
			t.Fatalf("worker result cause was not clamped: %d runes", n)
		}
		// The line-oriented half of the contract: no newline, CR or tab reaches a consumer.
		if strings.ContainsAny(c, "\n\r\t") {
			t.Fatalf("worker result cause must be collapsed to ONE line, got %q", c)
		}
		if !strings.Contains(c, "BOOM- upstream detail ") {
			t.Fatalf("collapsing must join the source lines with single spaces, got %q", c)
		}
	}
}

// TestTeamMemberResultNoCauseOnCleanStop is the negative guard: a worker whose round ends
// cleanly (StopEndTurn) must leave Cause empty on its result event, so a consumer can
// treat a non-empty Cause as "this round failed".
func TestTeamMemberResultNoCauseOnCleanStop(t *testing.T) {
	leadProv := mockllm.New(
		mockllm.TextTurn("delegating"),
		mockllm.TextTurn("CONSOLIDATED REPORT"),
	)
	workerProv := mockllm.New(
		mockllm.TextTurn("all good here"),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"investigate","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent received the report"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate"})
	evs := drain(r)

	for _, ev := range evs {
		if ev.Type == session.EvTeamMember && ev.Team != nil && ev.Team.Member == "worker" &&
			ev.Team.InnerKind == session.EvResult && ev.Team.Cause != "" {
			t.Fatalf("a clean worker result must carry no cause, got %q", ev.Team.Cause)
		}
	}
}

// TestTeamRetriedMemberSurfacesCauseEachFailedRound asserts a retried member's failed
// rounds EACH surface their own cause on their per-round result event: round 1 carries
// C1, the retried round 2 carries C2. (The default retry cap recovers round 1 and
// benches on round 2.)
func TestTeamRetriedMemberSurfacesCauseEachFailedRound(t *testing.T) {
	leadProv := mockllm.New(
		mockllm.TextTurn("delegating"),
		mockllm.TextTurn("CONSOLIDATED REPORT"),
	)
	workerProv := mockllm.New(
		mockllm.ErrorTurn(errors.New("C1 first failure")),
		mockllm.ErrorTurn(errors.New("C2 second failure")),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"investigate","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent received the report"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate"})
	evs := drain(r)

	var resultCauses []string
	for _, ev := range evs {
		if ev.Type == session.EvTeamMember && ev.Team != nil && ev.Team.Member == "worker" &&
			ev.Team.InnerKind == session.EvResult {
			resultCauses = append(resultCauses, ev.Team.Cause)
		}
	}
	if len(resultCauses) < 2 {
		t.Fatalf("want at least 2 worker result events (one per failed round), got %d: %q", len(resultCauses), resultCauses)
	}
	// The first two result events must carry C1 then C2 (in order).
	if !strings.Contains(resultCauses[0], "C1 first failure") {
		t.Errorf("round 1 result cause = %q, want C1", resultCauses[0])
	}
	if !strings.Contains(resultCauses[1], "C2 second failure") {
		t.Errorf("round 2 result cause = %q, want C2", resultCauses[1])
	}
}
