package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// runParallel direct-Executes a Parallel tool with the supplied args JSON + call id and
// returns the joined ToolResult, failing on a transport error. It is the direct-Execute
// driver the branch-id and persistence tests use (so they can inspect the store and the
// result text without a parent loop).
func runParallel(t *testing.T, par tool.Tool, callID, argsJSON string) session.ToolResult {
	t.Helper()
	res, err := par.Execute(context.Background(),
		session.NewToolCall(session.ToolCallID(callID), "Parallel", json.RawMessage(argsJSON)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("Parallel.Execute returned a transport error: %v", err)
	}
	return res
}

// TestParallelRendersBranchIDPerJoinMode is the table-driven renderer test (issue #30):
// EVERY join mode emits a "branch id: parallel-<callID>-<i>" line for each branch (and a
// prominent winner line for first/judge). It direct-executes the Parallel tool with a
// stateless child so the result text is deterministic regardless of branch interleaving.
func TestParallelRendersBranchIDPerJoinMode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		join           string
		wantWinnerLine bool   // first/judge surface a prominent "branch id:" winner line
		wantWinnerID   string // the deterministic winner id (judge only; "" = non-deterministic)
		wantAllIDs     bool   // every branch id must be present somewhere in the text
		needJudge      bool
		branchCount    int
	}{
		{name: "all", join: "all", wantAllIDs: true, branchCount: 3},
		// join=first's winner is decided by completion ORDER (non-deterministic), so we
		// only assert a prominent "branch id:" line exists + all ids are discoverable.
		{name: "first", join: "first", wantWinnerLine: true, wantAllIDs: true, branchCount: 3},
		// join=judge's winner is pinned by the fakeJudge (branch-1 / parallel-c1-0).
		{name: "judge", join: "judge", wantWinnerLine: true, wantWinnerID: "parallel-c1-0", wantAllIDs: true, needJudge: true, branchCount: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			childRead := &fakeTool{name: "Read", readOnly: true,
				exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
					return session.NewToolResult(in.ID, "child read"), nil
				}}
			childEngine := childEngineWith(&branchProvider{summary: "branch summary"}, catalogWith(t, childRead))
			opts := []agent.ParallelOption{}
			if tc.needJudge {
				// A judge that always picks the first candidate keeps the winner deterministic.
				// fakeJudge picks the candidate whose Label contains "branch-1" (the first
				// branch), keeping the winner deterministic (parallel-c1-0).
				opts = append(opts, agent.WithParallelJudge(&fakeJudge{pick: "branch-1", rationale: "picked first"}))
			}
			par := agent.NewParallelTool(childEngine, &memForker{}, opts...)

			tasks := make([]string, tc.branchCount)
			for i := range tasks {
				tasks[i] = fmt.Sprintf("explore %d", i)
			}
			tasksJSON, _ := json.Marshal(tasks)
			argsJSON := fmt.Sprintf(`{"tasks":%s,"join":%q}`, tasksJSON, tc.join)
			res := runParallel(t, par, "c1", argsJSON)
			if res.IsError {
				t.Fatalf("join=%s result is an error: %q", tc.join, res.Content)
			}

			// Every branch id ("parallel-c1-<i>") must be discoverable in the text.
			if tc.wantAllIDs {
				for i := 0; i < tc.branchCount; i++ {
					wantID := fmt.Sprintf("parallel-c1-%d", i)
					if !strings.Contains(res.Content, wantID) {
						t.Fatalf("join=%s: result text missing branch id %q:\n%s", tc.join, wantID, res.Content)
					}
				}
			}
			// A prominent "branch id:" winner line must appear for first/judge.
			if tc.wantWinnerLine && !strings.Contains(res.Content, "branch id: parallel-c1-") {
				t.Fatalf("join=%s: missing prominent winner 'branch id:' line:\n%s", tc.join, res.Content)
			}
			// When the winner is deterministic (judge), pin its exact id.
			if tc.wantWinnerID != "" && !strings.Contains(res.Content, "branch id: "+tc.wantWinnerID) {
				t.Fatalf("join=%s: missing the pinned winner id %q:\n%s", tc.join, tc.wantWinnerID, res.Content)
			}
			// join=all uses a per-branch "branch id:" line for every branch.
			if tc.join == "all" {
				if got := strings.Count(res.Content, "branch id: parallel-c1-"); got != tc.branchCount {
					t.Fatalf("join=all: want %d 'branch id:' lines, got %d:\n%s", tc.branchCount, got, res.Content)
				}
			}
		})
	}
}

// TestParallelFirstSurfacesLoserBranchIDs proves join=first surfaces an "other branch
// ids:" line listing EVERY non-winner (loser) branch id (issue #30), so a parent can pull
// a rejected/cancelled branch's persisted transcript via InspectSubagent. join=first's
// WINNER is decided by completion order (non-deterministic), so this asserts on the
// presence of the line plus that ALL non-winner ids are discoverable — never a specific
// winner. This is the guard for the writeOtherBranchIDs(&b, ...) call in joinFirstResult:
// remove that call and this test fails (mutation-verified).
func TestParallelFirstSurfacesLoserBranchIDs(t *testing.T) {
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "branch summary"}, catalogWith(t, childRead))
	par := agent.NewParallelTool(childEngine, &memForker{})

	const branchCount = 3
	tasks := make([]string, branchCount)
	for i := range tasks {
		tasks[i] = fmt.Sprintf("explore %d", i)
	}
	tasksJSON, _ := json.Marshal(tasks)
	res := runParallel(t, par, "c1", fmt.Sprintf(`{"tasks":%s,"join":"first"}`, tasksJSON))
	if res.IsError {
		t.Fatalf("join=first result is an error: %q", res.Content)
	}

	// The winner is non-deterministic, so derive it from the prominent winner "branch id:"
	// line, then assert every OTHER id is discoverable on the "other branch ids:" line.
	winnerID := extractBranchID(t, res.Content)
	if !strings.Contains(res.Content, "other branch ids: ") {
		t.Fatalf("join=first must surface an 'other branch ids:' line listing the losers:\n%s", res.Content)
	}
	otherIDs := extractOtherBranchIDs(t, res.Content)
	for i := 0; i < branchCount; i++ {
		id := fmt.Sprintf("parallel-c1-%d", i)
		if id == winnerID {
			continue // the winner rides the prominent "branch id:" line, not "other branch ids:"
		}
		if !contains(otherIDs, id) {
			t.Fatalf("loser branch id %q missing from the 'other branch ids:' line %v (winner=%q):\n%s",
				id, otherIDs, winnerID, res.Content)
		}
	}
	// Every non-winner branch must be accounted for on the line.
	if len(otherIDs) != branchCount-1 {
		t.Fatalf("want %d loser ids on 'other branch ids:', got %d (%v):\n%s",
			branchCount-1, len(otherIDs), otherIDs, res.Content)
	}
}

// extractOtherBranchIDs parses the comma-separated ids off the "other branch ids: …" line
// joinFirstResult renders for the losers.
func extractOtherBranchIDs(t *testing.T, body string) []string {
	t.Helper()
	const marker = "other branch ids: "
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, marker) {
			var ids []string
			for _, id := range strings.Split(strings.TrimPrefix(trimmed, marker), ",") {
				ids = append(ids, strings.TrimSpace(id))
			}
			return ids
		}
	}
	t.Fatalf("no 'other branch ids:' line in:\n%s", body)
	return nil
}

// contains reports whether s appears in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestParallelPersistsBranchAfterRun proves WithParallelStore best-effort persists each
// branch's child session after its run: the store holds "parallel-c1-0", state completed.
func TestParallelPersistsBranchAfterRun(t *testing.T) {
	store := memstore.New()
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "BRANCH_MARKER answer"}, catalogWith(t, childRead))
	par := agent.NewParallelTool(childEngine, &memForker{}, agent.WithParallelStore(store))

	runParallel(t, par, "c1", `{"tasks":["explore A"]}`)

	sess, err := store.Load(context.Background(), session.SessionID("parallel-c1-0"))
	if err != nil {
		t.Fatalf("branch session was not persisted under parallel-c1-0: %v", err)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("persisted branch state = %q, want completed", sess.State)
	}
	var sawAnswer bool
	for _, m := range sess.Conversation.Messages {
		if strings.Contains(m.Text, "BRANCH_MARKER") {
			sawAnswer = true
		}
	}
	if !sawAnswer {
		t.Fatalf("persisted branch history missing the child's answer")
	}
}

// TestParallelPersistsAllBranchesAllJoinModes proves the persistence guarantee each
// join mode actually makes. Under all/judge no branch is cancelled, so EVERY branch
// (winner AND losers) persists deterministically. Under first the losing branches are
// cancelled the instant a winner completes; a loser cancelled BEFORE it acquired a
// worker slot never created a child session, so its persistence is best-effort, NOT a
// guarantee — the HARD guarantee is that the WINNER (which always ran to completion)
// persists and stays inspectable. Asserting losers persist under first was the
// load-dependent flake in issue #142 (the loser's persistence raced its cancellation).
func TestParallelPersistsAllBranchesAllJoinModes(t *testing.T) {
	for _, join := range []string{"all", "first", "judge"} {
		t.Run(join, func(t *testing.T) {
			store := memstore.New()
			childRead := &fakeTool{name: "Read", readOnly: true,
				exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
					return session.NewToolResult(in.ID, "child read"), nil
				}}
			childEngine := childEngineWith(&branchProvider{summary: "branch summary"}, catalogWith(t, childRead))
			opts := []agent.ParallelOption{agent.WithParallelStore(store)}
			if join == "judge" {
				// fakeJudge picks the candidate whose Label contains "branch-1" (the first
				// branch), keeping the winner deterministic (parallel-c1-0).
				opts = append(opts, agent.WithParallelJudge(&fakeJudge{pick: "branch-1", rationale: "picked first"}))
			}
			par := agent.NewParallelTool(childEngine, &memForker{}, opts...)

			res := runParallel(t, par, "c1", fmt.Sprintf(`{"tasks":["a","b"],"join":%q}`, join))

			if join == "first" {
				// join=first cancels the losers, so only the WINNER is guaranteed to
				// persist. The winner is non-deterministic (decided by completion order),
				// so derive it from the prominent "branch id:" line and assert it loads.
				winnerID := extractBranchID(t, res.Content)
				if _, err := store.Load(context.Background(), session.SessionID(winnerID)); err != nil {
					t.Fatalf("join=first: winner branch %s not persisted (winner must remain inspectable): %v", winnerID, err)
				}
				return
			}

			// all/judge: every branch ran to completion, so winner AND losers persist.
			for i := 0; i < 2; i++ {
				id := session.SessionID(fmt.Sprintf("parallel-c1-%d", i))
				if _, err := store.Load(context.Background(), id); err != nil {
					t.Fatalf("join=%s: branch %s not persisted (every branch must remain inspectable): %v", join, id, err)
				}
			}
		})
	}
}

// TestParallelNilStoreSkipsPersist proves the no-store default is unchanged: a Parallel
// run with no WithParallelStore option succeeds and persists nothing (mirrors
// TestSubagentNilStoreSkipsPersist).
func TestParallelNilStoreSkipsPersist(t *testing.T) {
	store := memstore.New()
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "branch done"}, catalogWith(t, childRead))
	par := agent.NewParallelTool(childEngine, &memForker{}) // NO store

	res := runParallel(t, par, "c1", `{"tasks":["explore"]}`)
	if res.IsError {
		t.Fatalf("nil-store run must succeed, got %+v", res)
	}
	// Even though it surfaces a branch id, nothing was persisted.
	if _, err := store.Load(context.Background(), session.SessionID("parallel-c1-0")); err == nil {
		t.Fatalf("nil-store Parallel must persist nothing, but a branch session was found")
	}
}

// TestInspectSubagentAcceptsParallelPrefix proves the gate now admits a Parallel branch
// id (issue #30): a branch persisted via WithParallelStore loads through InspectSubagent
// under the neutral "Transcript of agent" header.
func TestInspectSubagentAcceptsParallelPrefix(t *testing.T) {
	store := memstore.New()
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "BRANCH_TRANSCRIPT_MARKER"}, catalogWith(t, childRead))
	par := agent.NewParallelTool(childEngine, &memForker{}, agent.WithParallelStore(store))
	runParallel(t, par, "c1", `{"tasks":["explore"]}`)

	inspect := agent.NewInspectSubagentTool(store)
	res, err := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"parallel-c1-0"}`)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("InspectSubagent.Execute transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("InspectSubagent must accept a parallel- branch id, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, `Transcript of agent "parallel-c1-0":`) {
		t.Fatalf("transcript missing the neutral agent header, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "BRANCH_TRANSCRIPT_MARKER") {
		t.Fatalf("transcript missing the branch's real content, got %q", res.Content)
	}
}

// TestInspectSubagentRejectsTeamPrefix proves team- ids stay EXCLUDED from the widened
// allow-list (InspectMember owns them): even a persisted team-member session is rejected
// before the store is touched.
func TestInspectSubagentRejectsTeamPrefix(t *testing.T) {
	store := memstore.New()
	inspect := agent.NewInspectSubagentTool(store)
	res, err := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"team-p1-worker"}`)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("team id produced a transport error (must be a clean tool error): %v", err)
	}
	if !res.IsError {
		t.Fatalf("team- id must be rejected by the gate, got %+v", res)
	}
	if !strings.Contains(res.Content, "is not an inspectable child session") ||
		!strings.Contains(res.Content, "InspectMember") {
		t.Fatalf("team- rejection copy mismatch, got %q", res.Content)
	}
}

// TestInspectParallelBranchUnknownIDErrors is the ADVERSARIAL not-found case (R6): a
// well-formed parallel- id that was never persisted is a clean, model-addressable
// not-found, NOT a harness error.
func TestInspectParallelBranchUnknownIDErrors(t *testing.T) {
	inspect := agent.NewInspectSubagentTool(memstore.New())
	res, err := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"parallel-nope-0"}`)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("unknown parallel id produced a transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("unknown parallel id must be an error result, got %+v", res)
	}
	if !strings.Contains(res.Content, `no transcript for agent id "parallel-nope-0"`) {
		t.Fatalf("not-found copy mismatch, got %q", res.Content)
	}
}

// TestInspectParallelBranchStoreFailureDistinct proves an infra failure on a parallel- id
// is surfaced with the DISTINCT failed-to-load copy, never as "no transcript" (mirrors
// the subagent store-failure-distinct test).
func TestInspectParallelBranchStoreFailureDistinct(t *testing.T) {
	inspect := agent.NewInspectSubagentTool(failingStore{})
	res, _ := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"parallel-x-0"}`)),
		agent.MemEnv("/ws"))
	if !res.IsError {
		t.Fatalf("store failure must be an error result, got %+v", res)
	}
	if !strings.Contains(res.Content, `failed to load agent "parallel-x-0"`) {
		t.Fatalf("failed-to-load copy mismatch, got %q", res.Content)
	}
	if strings.Contains(res.Content, "no transcript") {
		t.Fatalf("infra failure must NOT be misreported as not-found: %q", res.Content)
	}
}

// extractBranchID parses the FIRST "branch id: <id>" line a Parallel ToolResult carries,
// the same way a parent model must to discover a branch id. A miss means the id is
// undiscoverable (the defect this test exists to catch).
func extractBranchID(t *testing.T, body string) string {
	t.Helper()
	const marker = "branch id: "
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), marker) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), marker))
		}
	}
	t.Fatalf("Parallel ToolResult does not surface a 'branch id:' line; the model cannot discover a branch id:\n%s", body)
	return ""
}

// TestParentDiscoversBranchIDFromResultAndInspects is the MODEL-FACING e2e (mirrors
// TestParentDiscoversAgentIDFromResultAndInspects): the parent model (turn 1) calls
// Parallel; (turn 2) reads the joined result, extracts a surfaced branch id, and calls
// InspectSubagent with it; (turn 3) finishes. The branches persist to a real store, so
// InspectSubagent's success PROVES the extracted id matched the saved branch session.
// Full PULL round-trip, offline.
func TestParentDiscoversBranchIDFromResultAndInspects(t *testing.T) {
	store := memstore.New()
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "BRANCH_TRANSCRIPT_MARKER reached"}, catalogWith(t, childRead))
	parTool := agent.NewParallelTool(childEngine, &memForker{}, agent.WithParallelStore(store))
	inspectTool := agent.NewInspectSubagentTool(store)
	parentCat := catalogWith(t, parTool, inspectTool)

	// The parent's Parallel call id is "p1" with one branch, so the branch id is
	// "parallel-p1-0" — exactly what the scripted turn-2 InspectSubagent uses.
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Parallel",
			json.RawMessage(`{"tasks":["trace the code path"]}`))),
		mockllm.ToolCallTurn(session.NewToolCall("p2", "InspectSubagent",
			json.RawMessage(`{"agent_id":"parallel-p1-0"}`))),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	// 1. The Parallel result must surface a discoverable branch id.
	parBody, parErr, ok := toolResultForName(evs, "Parallel")
	if !ok {
		t.Fatalf("no Parallel tool result in %v", typesOf(evs))
	}
	if parErr {
		t.Fatalf("Parallel tool errored: %s", parBody)
	}
	gotID := extractBranchID(t, parBody)
	if gotID != "parallel-p1-0" {
		t.Fatalf("surfaced branch id = %q, want the deterministic branch id %q (verbatim)", gotID, "parallel-p1-0")
	}

	// 2. The parent's InspectSubagent call (using the surfaced id) must succeed with the
	// branch's real transcript — proving the id resolved to the persisted branch session.
	inspBody, inspErr, ok := toolResultForName(evs, "InspectSubagent")
	if !ok {
		t.Fatalf("no InspectSubagent tool result in %v", typesOf(evs))
	}
	if inspErr {
		t.Fatalf("InspectSubagent failed (the surfaced branch id did not resolve to a persisted branch): %s", inspBody)
	}
	if !strings.Contains(inspBody, "BRANCH_TRANSCRIPT_MARKER") {
		t.Fatalf("InspectSubagent result is not the branch's real transcript: %q", inspBody)
	}

	// 3. No-drift guard: the surfaced id maps to the persisted branch session.
	if _, lerr := store.Load(context.Background(), session.SessionID(gotID)); lerr != nil {
		t.Fatalf("surfaced branch id %q does not map to a persisted branch session: %v", gotID, lerr)
	}
}
