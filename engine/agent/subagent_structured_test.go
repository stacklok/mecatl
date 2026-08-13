package agent_test

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// personSchema is a small structured-output schema reused across the tests: an object
// with a required string `name` and an integer `age`.
const personSchema = `{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"]}`

// TestSubagentStructuredOutputHappyPath is the MODEL-FACING e2e: a Subagent call with an
// output_schema where the child calls SubmitResult with a VALID payload. The Subagent
// RESULT is the validated JSON (carried after the agentId trailer), and the child only
// drove once (no correction needed).
func TestSubagentStructuredOutputHappyPath(t *testing.T) {
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "SubmitResult", `{"name":"Ada","age":36}`)),
		mockllm.TextTurn("done"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			`{"prompt":"profile Ada","output_schema":`+personSchema+`}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("valid structured output must be a success result, got error: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, `"name":"Ada"`) || !strings.Contains(results[0].Content, `"age":36`) {
		t.Fatalf("result must carry the validated payload, got %q", results[0].Content)
	}
	// The agentId trailer is UNIVERSAL — it must ride the structured-output success path
	// too, not only the free-text path (QA SHOULD #6).
	if !strings.Contains(results[0].Content, "agentId: subagent-s1-p1") {
		t.Fatalf("structured-output success must carry the agentId trailer, got %q", results[0].Content)
	}
}

// TestSubagentStructuredOutputRetryCorrects is the ADVERSARIAL/uncooperative-mock test: the
// child submits a SCHEMA-VIOLATING payload twice (missing the required `age`, then a
// wrong type) before a valid one. The bounded retry must correct it and the run must
// succeed with the eventually-valid payload.
func TestSubagentStructuredOutputRetryCorrects(t *testing.T) {
	childLLM := mockllm.New(
		// Attempt 0: missing required `age`.
		mockllm.ToolCallTurn(toolCall("k1", "SubmitResult", `{"name":"Ada"}`)),
		mockllm.TextTurn("oops"),
		// Attempt 1 (after correction): wrong type for `age`.
		mockllm.ToolCallTurn(toolCall("k2", "SubmitResult", `{"name":"Ada","age":"old"}`)),
		mockllm.TextTurn("oops again"),
		// Attempt 2 (after correction): valid.
		mockllm.ToolCallTurn(toolCall("k3", "SubmitResult", `{"name":"Ada","age":36}`)),
		mockllm.TextTurn("done"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			`{"prompt":"profile Ada","output_schema":`+personSchema+`}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("bounded retry must correct the payload and succeed, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, `"age":36`) {
		t.Fatalf("result must carry the eventually-valid payload, got %q", results[0].Content)
	}
}

// TestSubagentStructuredOutputExhaustionFails is the ADVERSARIAL exhaustion test: the child
// NEVER produces a valid payload. The bounded retry must give up and the Subagent RESULT
// must be a MODEL-VISIBLE structured-output validation-failure tool error (a recoverable
// terminal — StopStructuredOutput — never `failed`/StopError), carrying the last
// validation message.
func TestSubagentStructuredOutputExhaustionFails(t *testing.T) {
	// Every SubmitResult is missing the required `age` — never valid. Script enough
	// turns to outlast the bounded retry budget.
	var script []mockllm.Turn
	for i := 0; i < 8; i++ {
		script = append(script,
			mockllm.ToolCallTurn(toolCall("k", "SubmitResult", `{"name":"Ada"}`)),
			mockllm.TextTurn("still wrong"),
		)
	}
	childLLM := mockllm.New(script...)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			`{"prompt":"profile Ada","output_schema":`+personSchema+`}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("retry exhaustion must be a MODEL-VISIBLE tool error, got success: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "schema") {
		t.Fatalf("structured-output failure must name the schema mismatch, got %q", results[0].Content)
	}
	// The last validation error (missing required age) must reach the model.
	if !strings.Contains(results[0].Content, "required") {
		t.Fatalf("failure should carry the last validation error, got %q", results[0].Content)
	}
	// The agentId trailer is UNIVERSAL — it must ride the StopStructuredOutput error
	// path too, so the model can InspectSubagent the failed child.
	if !strings.Contains(results[0].Content, "agentId: subagent-s1-p1") {
		t.Fatalf("structured-output failure must carry the agentId trailer, got %q", results[0].Content)
	}
}

// TestSubagentFreeTextUnchangedByStructuredPath is the regression guard: a Subagent call with NO
// output_schema is byte-identical to today — the child's free-text summary is returned
// (after the agentId trailer), no SubmitResult involved.
func TestSubagentFreeTextUnchangedByStructuredPath(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("FREE TEXT SUMMARY")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("free-text path must be unchanged, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "FREE TEXT SUMMARY") {
		t.Fatalf("result must carry the free-text summary, got %q", results[0].Content)
	}
}

// TestSubagentAgentIdTrailerInResultText is the RUNTIME-DISCOVERABILITY guard (R2/D5): the
// child session id must appear IN THE RESULT TEXT (where the model reads it), not only on
// the client-only subagent.* events. The id is the deterministic "subagent-<callID>".
func TestSubagentAgentIdTrailerInResultText(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("summary")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("want 1 success result, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "agentId: subagent-s1-p1") {
		t.Fatalf("result text must carry the agentId trailer (discoverability), got %q", results[0].Content)
	}
}

// childEngineWithBudget builds a child engine carrying a MaxRunTokens ceiling, for the
// cross-drive budget test. The empty catalog is enough — SubmitResult is injected
// run-scoped by the Subagent tool.
func childEngineWithBudget(t *testing.T, llm *mockllm.Provider, budget int) *agent.Engine {
	t.Helper()
	return agent.NewEngine(agent.Deps{
		LLM:          llm,
		Catalog:      catalogWith(t),
		Policy:       permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:        "child-model",
		MaxRunTokens: budget,
	})
}

// TestStructuredOutputBudgetTripsAcrossDrives is the cross-attempt-brake guard
// (cloud-native Phase 1, QA SHOULD-ADD): a structured-output child whose per-attempt
// usage is BELOW the MaxRunTokens ceiling but ACCUMULATES across the in-call Reopens
// must trip StopBudget — proving the cumulative sess.Usage carries across the
// driveChild Reopens (resetToIdle preserves Usage) rather than each attempt re-granting
// a fresh budget. The budget is sized to trip at the START of the 3rd attempt (after two
// invalid SubmitResults), so it is a clean cross-drive StopBudget, never the
// retry-exhaustion StopStructuredOutput. Mutation: making resetToIdle zero Usage clears
// the child's accumulator on every Reopen, so the budget never accumulates, all three
// attempts run invalid, and the result becomes StopStructuredOutput (a tool error)
// instead of the StopBudget success-with-note.
func TestStructuredOutputBudgetTripsAcrossDrives(t *testing.T) {
	// Budget 250; each attempt's SubmitResult turn spends 150 (90 in + 60 out). Attempt 0
	// boundary sees 0 (<250) → runs → cumulative 150. Attempt 1 Reopen (Usage preserved
	// = 150), boundary 150 (<250) → runs → cumulative 300. Attempt 2 Reopen (Usage = 300),
	// boundary 300 (>=250) → trips StopBudget before any turn. Two invalid SubmitResults
	// happened; the 3rd attempt never drove a model turn.
	const budget = 250
	invalid := `{"name":"Ada"}` // missing the required `age` — never valid
	mkAttempt := func(id string) []mockllm.Turn {
		return []mockllm.Turn{
			mockllm.ChunksTurn(
				mockllm.ToolCallChunk(toolCall(id, "SubmitResult", invalid)),
				mockllm.UsageChunk(session.Usage{InputTokens: 90, OutputTokens: 60}),
				mockllm.DoneChunk(session.StopEndTurn),
			),
			mockllm.TextTurn("submitted (still missing age)"),
		}
	}
	var script []mockllm.Turn
	for _, id := range []string{"k0", "k1", "k2", "k3"} { // extra turns are harmless slack
		script = append(script, mkAttempt(id)...)
	}
	childLLM := mockllm.New(script...)
	childEngine := childEngineWithBudget(t, childLLM, budget)
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			`{"prompt":"profile Ada","output_schema":`+personSchema+`}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	// The cross-drive budget tripped BEFORE retry exhaustion: StopBudget is a CLEAN
	// terminal rendered as a success-with-note, NOT the StopStructuredOutput tool error.
	// If resetToIdle zeroed Usage on each Reopen, the budget would never accumulate and
	// the run would instead exhaust retries → a structured-output validation tool error.
	if results[0].IsError {
		t.Fatalf("cross-drive StopBudget must be a clean result, not a structured-output error: %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "schema") || strings.Contains(results[0].Content, "required") {
		t.Fatalf("result reads as a structured-output validation failure (retries exhausted) — the cross-drive budget did NOT trip: %q", results[0].Content)
	}
	// The agentId trailer rides every terminal.
	if !strings.Contains(results[0].Content, "agentId: subagent-s1-p1") {
		t.Fatalf("result must carry the agentId trailer, got %q", results[0].Content)
	}
}
