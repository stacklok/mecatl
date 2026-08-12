package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// extractTeamID parses the "Team id: <id>" line the Team ToolResult now carries, the
// SAME way a parent model would have to. A miss means the id is undiscoverable (the
// pre-fix defect this whole test exists to catch).
func extractTeamID(t *testing.T, body string) string {
	t.Helper()
	const marker = "Team id: "
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, marker) {
			return strings.TrimSpace(strings.TrimPrefix(line, marker))
		}
	}
	t.Fatalf("Team ToolResult does not surface a 'Team id:' line; the model cannot discover the id:\n%s", body)
	return ""
}

// firstToolResult returns the content of the first tool result for the named tool in
// the parent conversation (matched by the EvToolCall name → call id mapping).
func toolResultForName(evs []session.Event, name string) (content string, isError, found bool) {
	var id session.ToolCallID
	for _, ev := range evs {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.Name == name {
			id = ev.ToolCall.ID
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == id && id != "" {
			return ev.ToolResult.Content, ev.ToolResult.IsError, true
		}
	}
	return "", false, false
}

// TestParentDiscoversTeamIDFromResultAndInspects is the MODEL-FACING e2e test that
// would have caught defect #2 (team_id unreachable). The parent model: (turn 1) calls
// Team; (turn 2) reads the Team ToolResult, extracts the surfaced team id, and calls
// InspectMember with it; (turn 3) finishes. The member persists to a real store, so
// InspectMember's success PROVES the extracted id matched the saved MemberSessionID.
//
// FAILS-ON-REGRESSION: without renderTeamResult the ToolResult carries no team id,
// extractTeamID fails, and even a hard-coded id would have to be guessed — the model
// has no runtime path to it.
func TestParentDiscoversTeamIDFromResultAndInspects(t *testing.T) {
	store := memstore.New()

	// Worker records a finding and produces text (so its transcript is non-empty).
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("w1", "RecordFinding",
			json.RawMessage(`{"finding":"WORKER_TRANSCRIPT_MARKER root cause found"}`))),
		mockllm.TextTurn("worker done"),
	)
	// Lead coordinates then synthesises a report.
	leadProv := mockllm.New(
		mockllm.TextTurn("coordinating"),
		mockllm.TextTurn("CONSOLIDATED REPORT: fixed."),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers), agent.WithTeamToolStore(store))
	inspectTool := agent.NewInspectMemberTool(store)
	parentCat := catalogWith(t, teamTool, inspectTool)

	// The parent's Team call id is the published team id. The parent's turn-2
	// InspectMember must pass THAT id; we script it to use the Team call id "p1"
	// VERBATIM (which is exactly what extractTeamID will recover from the result, and
	// what the test asserts below — so the script and the surfaced id agree).
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Team",
			json.RawMessage(`{"goal":"fix it","members":[{"name":"lead","role":"lead"},{"name":"worker","role":"work"}]}`))),
		mockllm.ToolCallTurn(session.NewToolCall("p2", "InspectMember",
			json.RawMessage(`{"team_id":"p1","member":"worker"}`))),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "fix it"}))

	// 1. The Team ToolResult must SURFACE the team id (the discovery contract).
	teamBody, teamErr, ok := toolResultForName(evs, "Team")
	if !ok {
		t.Fatalf("no Team tool result in %v", typesOf(evs))
	}
	if teamErr {
		t.Fatalf("Team tool errored: %s", teamBody)
	}
	gotID := extractTeamID(t, teamBody)
	// The surfaced id must be the published id (the Team call id), byte-identical, so
	// MemberSessionID reconstructs the saved id.
	if gotID != "p1" {
		t.Fatalf("surfaced team id = %q, want the published Team call id %q (verbatim, per the MemberSessionID contract)", gotID, "p1")
	}

	// 2. The parent's InspectMember call (using the surfaced id) must succeed with a
	// REAL transcript — proving the id resolved to the persisted member session.
	inspBody, inspErr, ok := toolResultForName(evs, "InspectMember")
	if !ok {
		t.Fatalf("no InspectMember tool result in %v", typesOf(evs))
	}
	if inspErr {
		t.Fatalf("InspectMember failed (the surfaced id did not resolve to a persisted member): %s", inspBody)
	}
	// The rendered transcript is the worker's real session (its message text + its
	// recorded tool call), not the "no transcript" miss.
	if !strings.Contains(inspBody, "worker done") || !strings.Contains(inspBody, "RecordFinding") {
		t.Fatalf("InspectMember result is not the worker's real transcript: %q", inspBody)
	}

	// 3. Sanity: the surfaced id maps to the persisted worker session.
	if _, lerr := store.Load(context.Background(), agent.MemberSessionID(gotID, "worker")); lerr != nil {
		t.Fatalf("surfaced id %q does not map to a persisted worker session: %v", gotID, lerr)
	}
}

// TestLeadEmptySynthesisThenNudgedProducesReport (AC-7) proves the no-progress fix
// makes the team's LEAD synthesis substantive: the lead's synthesis turn first comes
// back EMPTY, gets nudged inside its own drive, and then produces the report. The
// returned deliverable must be the REPORT, not joinTeamFallback and not a skeleton.
//
// FAILS-ON-REGRESSION: without the A-fix the empty synthesis turn terminates the lead
// at once → synthesise returns "" → joinTeamFallback → a "=== lead [DONE] ===" skeleton.
func TestLeadEmptySynthesisThenNudgedProducesReport(t *testing.T) {
	// Lead: round-0 coordination text, then the SYNTHESIS turn is empty, then (after
	// the nudge) the real report.
	leadProv := mockllm.New(
		mockllm.TextTurn("coordinating"),
		mockllm.EmptyTurn(),                         // synthesis attempt 1: no progress
		mockllm.TextTurn("THE REPORT: leak fixed."), // synthesis attempt 2 (post-nudge)
	)
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("w1", "RecordFinding",
			json.RawMessage(`{"finding":"the leak is in the cache"}`))),
		mockllm.TextTurn("worker done"),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	teamTool := agent.NewTeamTool(teamToolFactory(t, providers))
	parentCat := catalogWith(t, teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Team",
			json.RawMessage(`{"goal":"fix it","members":[{"name":"lead","role":"lead"},{"name":"worker","role":"work"}]}`))),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "fix it"}))

	body, isErr, ok := toolResultForName(evs, "Team")
	if !ok || isErr {
		t.Fatalf("Team tool result missing/errored: ok=%v err=%v body=%q", ok, isErr, body)
	}
	if !strings.Contains(body, "THE REPORT") {
		t.Fatalf("team deliverable is not the post-nudge report: %q", body)
	}
	if strings.Contains(body, "=== ") {
		t.Fatalf("team deliverable fell back to a header-only skeleton (joinTeamFallback) instead of the nudged report: %q", body)
	}
}
