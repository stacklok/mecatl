package agent_test

// Review finding 2 (issue #368, ADR 0102): a durable delegation id previously
// derived ONLY from the provider tool-call id — a value the LLM API supplies
// and does not guarantee unique across independent conversations, let alone
// across owners (two different top-level sessions). These tests pin the fix
// (childSessionID/MemberSessionID now namespaced under the PARENT session's
// own id): two DIFFERENT parent sessions issuing the IDENTICAL (adversarially
// equal) tool-call id must derive DISTINCT durable ids for all three
// delegation families, and both children must persist independently — never
// one silently overwriting the other before any inspect/resume authorization
// runs.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestSubagentChildSessionIDsDoNotCollideAcrossOwnersWithEqualCallID: two
// top-level sessions ("owner-a-session", "owner-b-session") both issue a
// Subagent call with the SAME call id "p1". The derived child ids must differ
// (namespaced by the parent session id), and both children must persist their
// OWN distinct content — neither run's child silently overwrote the other's.
func TestSubagentChildSessionIDsDoNotCollideAcrossOwnersWithEqualCallID(t *testing.T) {
	store := memstore.New()

	run := func(parentSessionID session.SessionID, childAnswer string) session.ToolResult {
		childEngine := childEngineWith(mockllm.New(mockllm.TextTurn(childAnswer)), catalogWith(t))
		task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))
		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
			mockllm.TextTurn("parent done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
		sess := session.New(parentSessionID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
		r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		evs := drain(r)
		res := resultByCallID(evs)["p1"]
		if res == nil || res.IsError {
			t.Fatalf("Subagent call for parent %q failed: %+v", parentSessionID, res)
		}
		return *res
	}

	resA := run("owner-a-session", "OWNER_A_ANSWER")
	resB := run("owner-b-session", "OWNER_B_ANSWER")

	idA := extractAgentID(t, resA.Content)
	idB := extractAgentID(t, resB.Content)
	if idA == "" || idB == "" {
		t.Fatalf("missing agentId trailer: A=%q B=%q", resA.Content, resB.Content)
	}
	if idA == idB {
		t.Fatalf("equal call id across two different parent sessions derived the SAME child id %q — collision", idA)
	}

	childA, err := store.Load(context.Background(), session.SessionID(idA))
	if err != nil {
		t.Fatalf("load child A %q: %v", idA, err)
	}
	childB, err := store.Load(context.Background(), session.SessionID(idB))
	if err != nil {
		t.Fatalf("load child B %q: %v", idB, err)
	}
	if !conversationContainsIn(childA, "OWNER_A_ANSWER") {
		t.Fatalf("child A does not carry owner A's own answer: %+v", childA.Conversation.Messages)
	}
	if !conversationContainsIn(childB, "OWNER_B_ANSWER") {
		t.Fatalf("child B does not carry owner B's own answer: %+v", childB.Conversation.Messages)
	}
	if conversationContainsIn(childA, "OWNER_B_ANSWER") || conversationContainsIn(childB, "OWNER_A_ANSWER") {
		t.Fatalf("one owner's child was overwritten by the other's: A=%+v B=%+v", childA.Conversation.Messages, childB.Conversation.Messages)
	}
}

// TestParallelBranchSessionIDsDoNotCollideAcrossOwnersWithEqualCallID is the
// Parallel analogue: two parent sessions both issue a single-branch Parallel
// call under the SAME call id "p1". The derived branch ids must differ and
// both branches persist independently.
func TestParallelBranchSessionIDsDoNotCollideAcrossOwnersWithEqualCallID(t *testing.T) {
	store := memstore.New()

	run := func(parentSessionID session.SessionID, branchAnswer string) session.ToolResult {
		childEngine := childEngineWith(&branchProvider{summary: branchAnswer}, catalogWith(t))
		par := agent.NewParallelTool(childEngine, &memForker{}, agent.WithParallelStore(store))
		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["explore"]}`)),
			mockllm.TextTurn("parent done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, par)})
		sess := session.New(parentSessionID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
		r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
		evs := drain(r)
		res := resultByCallID(evs)["p1"]
		if res == nil || res.IsError {
			t.Fatalf("Parallel call for parent %q failed: %+v", parentSessionID, res)
		}
		return *res
	}

	resA := run("owner-a-session", "BRANCH_A_ANSWER")
	resB := run("owner-b-session", "BRANCH_B_ANSWER")

	idA := extractBranchID(t, resA.Content)
	idB := extractBranchID(t, resB.Content)
	if idA == idB {
		t.Fatalf("equal call id across two different parent sessions derived the SAME branch id %q — collision", idA)
	}

	branchA, err := store.Load(context.Background(), session.SessionID(idA))
	if err != nil {
		t.Fatalf("load branch A %q: %v", idA, err)
	}
	branchB, err := store.Load(context.Background(), session.SessionID(idB))
	if err != nil {
		t.Fatalf("load branch B %q: %v", idB, err)
	}
	if !conversationContainsIn(branchA, "BRANCH_A_ANSWER") {
		t.Fatalf("branch A does not carry owner A's own answer: %+v", branchA.Conversation.Messages)
	}
	if !conversationContainsIn(branchB, "BRANCH_B_ANSWER") {
		t.Fatalf("branch B does not carry owner B's own answer: %+v", branchB.Conversation.Messages)
	}
	if conversationContainsIn(branchA, "BRANCH_B_ANSWER") || conversationContainsIn(branchB, "BRANCH_A_ANSWER") {
		t.Fatalf("one owner's branch was overwritten by the other's: A=%+v B=%+v", branchA.Conversation.Messages, branchB.Conversation.Messages)
	}
}

// TestTeamMemberSessionIDsDoNotCollideAcrossOwnersWithEqualCallID is the Team
// analogue: two parent sessions both issue a Team call under the SAME call id
// "p1". The published team id (and so every MemberSessionID it derives) must
// differ across the two runs, and both members' persisted sessions must carry
// their own owner's content.
func TestTeamMemberSessionIDsDoNotCollideAcrossOwnersWithEqualCallID(t *testing.T) {
	store := memstore.New()

	run := func(parentSessionID session.SessionID, workerFinding string) session.ToolResult {
		workerProv := mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("w1", "RecordFinding", json.RawMessage(`{"finding":"`+workerFinding+`"}`))),
			mockllm.TextTurn("worker done"),
		)
		leadProv := mockllm.New(mockllm.TextTurn("REPORT: done"))
		providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

		teamTool := agent.NewTeamTool(teamToolFactory(t, providers), agent.WithTeamToolStore(store))
		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("p1", "Team",
				json.RawMessage(`{"goal":"fix it","members":[{"name":"lead","role":"lead"},{"name":"worker","role":"work"}]}`))),
			mockllm.TextTurn("parent done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, teamTool)})
		sess := session.New(parentSessionID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
		r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "fix it"})
		evs := drain(r)
		res := resultByCallID(evs)["p1"]
		if res == nil || res.IsError {
			t.Fatalf("Team call for parent %q failed: %+v", parentSessionID, res)
		}
		return *res
	}

	resA := run("owner-a-session", "OWNER_A_FINDING")
	resB := run("owner-b-session", "OWNER_B_FINDING")

	teamIDA := extractTeamID(t, resA.Content)
	teamIDB := extractTeamID(t, resB.Content)
	if teamIDA == teamIDB {
		t.Fatalf("equal call id across two different parent sessions derived the SAME team id %q — collision", teamIDA)
	}

	workerA, err := store.Load(context.Background(), agent.MemberSessionID(teamIDA, "worker"))
	if err != nil {
		t.Fatalf("load worker A (team %q): %v", teamIDA, err)
	}
	workerB, err := store.Load(context.Background(), agent.MemberSessionID(teamIDB, "worker"))
	if err != nil {
		t.Fatalf("load worker B (team %q): %v", teamIDB, err)
	}
	if !conversationContainsIn(workerA, "OWNER_A_FINDING") {
		t.Fatalf("worker A does not carry owner A's own finding: %+v", workerA.Conversation.Messages)
	}
	if !conversationContainsIn(workerB, "OWNER_B_FINDING") {
		t.Fatalf("worker B does not carry owner B's own finding: %+v", workerB.Conversation.Messages)
	}
	if conversationContainsIn(workerA, "OWNER_B_FINDING") || conversationContainsIn(workerB, "OWNER_A_FINDING") {
		t.Fatalf("one owner's team member was overwritten by the other's: A=%+v B=%+v", workerA.Conversation.Messages, workerB.Conversation.Messages)
	}
}

// conversationContains reports whether any message in sess's conversation
// contains the given substring (in its Text or, for a tool call, its raw
// args), tolerating a nil session.
func conversationContainsIn(sess *session.Session, want string) bool {
	if sess == nil || sess.Conversation == nil {
		return false
	}
	for _, m := range sess.Conversation.Messages {
		if strings.Contains(m.Text, want) {
			return true
		}
		for _, p := range m.ToolCalls {
			if strings.Contains(string(p.Args), want) {
				return true
			}
		}
	}
	return false
}
