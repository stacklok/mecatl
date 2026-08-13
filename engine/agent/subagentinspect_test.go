package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// runOneSubagent drives a single Subagent.Execute against the given task tool with the
// supplied prompt JSON and call id, returning the result. It is the direct-Execute
// driver the persistence tests use (so they can inspect the store afterward), as
// distinct from subagentParentResults which runs through a parent loop.
func runOneSubagent(t *testing.T, task tool.Tool, callID, argsJSON string) session.ToolResult {
	t.Helper()
	res, err := task.Execute(context.Background(),
		session.NewToolCall(session.ToolCallID(callID), "Subagent", json.RawMessage(argsJSON)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("Subagent.Execute returned a transport error: %v", err)
	}
	return res
}

// TestSubagentPersistsChildAfterRun proves WithSubagentStore best-effort persists the
// child session after its run: the store holds "subagent-p1", state completed, with the
// child's prompt + answer in its history.
func TestSubagentPersistsChildAfterRun(t *testing.T) {
	store := memstore.New()
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("CHILD ANSWER")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	runOneSubagent(t, task, "p1", `{"prompt":"investigate the bug"}`)

	sess, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("child session was not persisted under subagent-p1: %v", err)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("persisted child state = %q, want completed", sess.State)
	}
	var sawPrompt, sawAnswer bool
	for _, m := range sess.Conversation.Messages {
		if strings.Contains(m.Text, "investigate the bug") {
			sawPrompt = true
		}
		if strings.Contains(m.Text, "CHILD ANSWER") {
			sawAnswer = true
		}
	}
	if !sawPrompt || !sawAnswer {
		t.Fatalf("persisted history missing prompt(%v)/answer(%v)", sawPrompt, sawAnswer)
	}
}

// TestSubagentPersistsFinalStateAfterStructuredRetry proves the persisted session is the
// FINAL state after a structured-output correction re-drive: the invalid-then-valid run
// re-injects a correction user message, and that correction must be present in the saved
// transcript (the save happens after all re-drives).
func TestSubagentPersistsFinalStateAfterStructuredRetry(t *testing.T) {
	store := memstore.New()
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "SubmitResult", `{"name":"Ada"}`)),
		mockllm.TextTurn("oops"),
		mockllm.ToolCallTurn(toolCall("k2", "SubmitResult", `{"name":"Ada","age":36}`)),
		mockllm.TextTurn("done"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	runOneSubagent(t, task, "p1", `{"prompt":"profile Ada","output_schema":`+personSchema+`}`)

	sess, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("child not persisted: %v", err)
	}
	// The correction re-injects a model-visible user message naming the schema mismatch;
	// it must be present in the FINAL saved transcript (post-retry state).
	var sawCorrection bool
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(strings.ToLower(m.Text), "schema") {
			sawCorrection = true
		}
	}
	if !sawCorrection {
		t.Fatalf("persisted transcript does not contain the structured-output correction message; not the final state")
	}
}

// TestSubagentPersistsAfterErrorTerminal proves a child that ends in StopError is still
// persisted (state failed), and the error ToolResult carries the agentId trailer.
func TestSubagentPersistsAfterErrorTerminal(t *testing.T) {
	store := memstore.New()
	childEngine := childEngineWith(mockllm.New(mockllm.EmptyTurnWithStop(session.StopError)), catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p1", `{"prompt":"crash please"}`)
	if !res.IsError {
		t.Fatalf("StopError child must be an error result, got %+v", res)
	}
	if !strings.Contains(res.Content, "agentId: subagent-p1") {
		t.Fatalf("error result must carry the agentId trailer, got %q", res.Content)
	}
	sess, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("errored child not persisted: %v", err)
	}
	if sess.State != session.StateFailed {
		t.Fatalf("persisted errored child state = %q, want failed", sess.State)
	}
}

// TestSubagentPersistsAfterTimeoutTerminal is the companion to the error-terminal test:
// a child that blows its timeout_ms is cancelled and persisted (state cancelled), and the
// timeout error result carries the agentId trailer.
func TestSubagentPersistsAfterTimeoutTerminal(t *testing.T) {
	store := memstore.New()
	slow := &sleepThenLoopTool{sleep: 50 * time.Millisecond}
	var script []mockllm.Turn
	for i := 0; i < 100; i++ {
		script = append(script, mockllm.ToolCallTurn(toolCall("k", "Slow", `{}`)))
	}
	childEngine := childEngineWith(mockllm.New(script...), catalogWith(t, slow))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p1", `{"prompt":"loop forever","timeout_ms":120}`)
	if !res.IsError {
		t.Fatalf("timed-out child must be an error result, got %+v", res)
	}
	if !strings.Contains(res.Content, "time budget") || !strings.Contains(res.Content, "agentId: subagent-p1") {
		t.Fatalf("timeout error must mention the time budget AND carry the agentId trailer, got %q", res.Content)
	}
	sess, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("timed-out child not persisted: %v", err)
	}
	if sess.State != session.StateCancelled {
		t.Fatalf("persisted timed-out child state = %q, want cancelled", sess.State)
	}
}

// TestSubagentNilStoreSkipsPersist proves the no-store default is unchanged: a Subagent
// run with no WithSubagentStore option succeeds and persists nothing. It also proves
// NewInspectSubagentTool(nil) panics (a composition-root programming error).
func TestSubagentNilStoreSkipsPersist(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("CHILD ANSWER")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine) // no store

	res := runOneSubagent(t, task, "p1", `{"prompt":"investigate"}`)
	if res.IsError || !strings.Contains(res.Content, "CHILD ANSWER") {
		t.Fatalf("nil-store run must be unchanged; got %+v", res)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewInspectSubagentTool(nil) must panic")
		}
	}()
	agent.NewInspectSubagentTool(nil)
}

// failingStore is a port.SessionStore whose Load always returns a non-not-found infra
// error, so the inspect tool's DISTINCT failed-to-load branch can be exercised.
type failingStore struct{}

func (failingStore) Save(context.Context, *session.Session) error { return nil }
func (failingStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, fmt.Errorf("disk on fire")
}

// TestInspectSubagentHappyPath seeds the store via a real subagent run, then proves
// InspectSubagent renders a bounded transcript under the neutral "Transcript of agent" header.
func TestInspectSubagentHappyPath(t *testing.T) {
	store := memstore.New()
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("CHILD_MARKER answer")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))
	runOneSubagent(t, task, "p1", `{"prompt":"do the thing"}`)

	inspect := agent.NewInspectSubagentTool(store)
	res, err := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"subagent-p1"}`)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("InspectSubagent.Execute transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("InspectSubagent should succeed for a persisted id, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, `Transcript of agent "subagent-p1":`) {
		t.Fatalf("transcript missing the header, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "CHILD_MARKER") {
		t.Fatalf("transcript missing the child's real content, got %q", res.Content)
	}
}

// TestInspectSubagentUnknownIDErrors proves an unknown id is a model-addressable
// not-found error with the exact copy.
func TestInspectSubagentUnknownIDErrors(t *testing.T) {
	inspect := agent.NewInspectSubagentTool(memstore.New())
	res, _ := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"subagent-nope"}`)),
		agent.MemEnv("/ws"))
	if !res.IsError {
		t.Fatalf("unknown id must be an error result, got %+v", res)
	}
	if !strings.Contains(res.Content, `no transcript for agent id "subagent-nope"`) {
		t.Fatalf("not-found copy mismatch, got %q", res.Content)
	}
}

// TestInspectSubagentStoreFailureDistinct proves an infra failure (not not-found) is
// surfaced with the DISTINCT failed-to-load copy, never as "no transcript".
func TestInspectSubagentStoreFailureDistinct(t *testing.T) {
	inspect := agent.NewInspectSubagentTool(failingStore{})
	res, _ := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"subagent-x"}`)),
		agent.MemEnv("/ws"))
	if !res.IsError {
		t.Fatalf("store failure must be an error result, got %+v", res)
	}
	if !strings.Contains(res.Content, `failed to load agent "subagent-x"`) {
		t.Fatalf("failed-to-load copy mismatch, got %q", res.Content)
	}
	if strings.Contains(res.Content, "no transcript") {
		t.Fatalf("infra failure must NOT be misreported as not-found: %q", res.Content)
	}
}

// extractAgentID parses the "agentId: <id>" line a Subagent ToolResult carries, the SAME
// way a parent model must to discover the child id. A miss means the id is
// undiscoverable (the defect this test exists to catch).
func extractAgentID(t *testing.T, body string) string {
	t.Helper()
	const marker = "agentId: "
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, marker) {
			return strings.TrimSpace(strings.TrimPrefix(line, marker))
		}
	}
	t.Fatalf("Subagent ToolResult does not surface an 'agentId:' line; the model cannot discover the id:\n%s", body)
	return ""
}

// TestParentDiscoversAgentIDFromResultAndInspects is the MODEL-FACING e2e (mirrors
// TestParentDiscoversTeamIDFromResultAndInspects): the parent model (turn 1) calls
// Subagent; (turn 2) reads the result, extracts the surfaced agentId, and calls
// InspectSubagent with it; (turn 3) finishes. The child persists to a real store, so
// InspectSubagent's success PROVES the extracted id matched the saved session id.
func TestParentDiscoversAgentIDFromResultAndInspects(t *testing.T) {
	store := memstore.New()

	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("CHILD_TRANSCRIPT_MARKER conclusion reached")), catalogWith(t))
	subTool := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))
	inspectTool := agent.NewInspectSubagentTool(store)
	parentCat := catalogWith(t, subTool, inspectTool)

	// The parent's Subagent call id is "p1", so the child id is "subagent-p1" — exactly
	// what extractAgentID recovers and what the scripted turn-2 InspectSubagent uses.
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent",
			json.RawMessage(`{"prompt":"trace the code path"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("p2", "InspectSubagent",
			json.RawMessage(`{"agent_id":"subagent-p1"}`))),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	// 1. The Subagent result must surface the agentId (the discovery contract).
	subBody, subErr, ok := toolResultForName(evs, "Subagent")
	if !ok {
		t.Fatalf("no Subagent tool result in %v", typesOf(evs))
	}
	if subErr {
		t.Fatalf("Subagent tool errored: %s", subBody)
	}
	gotID := extractAgentID(t, subBody)
	if gotID != "subagent-p1" {
		t.Fatalf("surfaced agentId = %q, want the deterministic child id %q (verbatim)", gotID, "subagent-p1")
	}

	// 2. The parent's InspectSubagent call (using the surfaced id) must succeed with the
	// child's real transcript — proving the id resolved to the persisted child session.
	inspBody, inspErr, ok := toolResultForName(evs, "InspectSubagent")
	if !ok {
		t.Fatalf("no InspectSubagent tool result in %v", typesOf(evs))
	}
	if inspErr {
		t.Fatalf("InspectSubagent failed (the surfaced id did not resolve to a persisted child): %s", inspBody)
	}
	if !strings.Contains(inspBody, "CHILD_TRANSCRIPT_MARKER") {
		t.Fatalf("InspectSubagent result is not the child's real transcript: %q", inspBody)
	}

	// 3. No-drift guard: the surfaced id maps to the persisted child session.
	if _, lerr := store.Load(context.Background(), session.SessionID(gotID)); lerr != nil {
		t.Fatalf("surfaced id %q does not map to a persisted child session: %v", gotID, lerr)
	}
}

// TestInspectSubagentForgedIDCleanError is the ADVERSARIAL test: the model calls
// InspectSubagent with garbage/forged ids. A non-subagent id (a team member id, a
// path-traversal string) is REJECTED by the prefix gate BEFORE the store is touched —
// the model cannot read team-member or service-session transcripts through this tool
// (those go through InspectMember's team_id+member framing). A well-formed-but-unknown
// subagent id passes the gate and is a clean not-found. Every case is a model-visible
// tool error, the run continues, and no panic occurs.
func TestInspectSubagentForgedIDCleanError(t *testing.T) {
	store := memstore.New()
	// Seed a TEAM MEMBER session under its real id: the gate must keep InspectSubagent
	// from reading it even though it EXISTS in the shared store.
	member := session.New("team-p1-worker", session.ModeDefault, "/ws", session.Limits{}, time.Now())
	if err := store.Save(context.Background(), member); err != nil {
		t.Fatalf("seeding member session: %v", err)
	}
	inspect := agent.NewInspectSubagentTool(store)

	// Non-subagent ids: rejected by the prefix gate with the exact framing copy.
	for _, forged := range []string{"team-p1-worker", "../../etc/passwd"} {
		argsJSON := fmt.Sprintf(`{"agent_id":%q}`, forged)
		res, err := inspect.Execute(context.Background(),
			session.NewToolCall("i1", "InspectSubagent", json.RawMessage(argsJSON)),
			agent.MemEnv("/ws"))
		if err != nil {
			t.Fatalf("forged id %q produced a transport error (must be a clean tool error): %v", forged, err)
		}
		if !res.IsError {
			t.Fatalf("forged id %q must be a model-visible error, got %+v", forged, res)
		}
		want := fmt.Sprintf("InspectSubagent: agent id %q is not an inspectable child session; only a Subagent result's 'agentId:' line or a Parallel result's 'branch id:' line can be inspected (team member transcripts are read via InspectMember)", forged)
		if !strings.Contains(res.Content, want) {
			t.Fatalf("forged id %q: prefix-rejection copy mismatch, got %q", forged, res.Content)
		}
	}

	// A well-formed but UNKNOWN subagent id passes the gate and is a clean not-found.
	res, err := inspect.Execute(context.Background(),
		session.NewToolCall("i2", "InspectSubagent", json.RawMessage(`{"agent_id":"subagent-zzz"}`)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("unknown subagent id produced a transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, `no transcript for agent id "subagent-zzz"`) {
		t.Fatalf("unknown subagent id must be a clean not-found, got %+v", res)
	}
}

// compile-time: the failing store satisfies the port.
var _ port.SessionStore = failingStore{}
