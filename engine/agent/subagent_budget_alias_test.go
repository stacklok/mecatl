package agent_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// budgetTrippingChild builds a child engine with an operator-level token budget
// (MaxRunTokens = operatorBudget on the engine) running a script whose every turn keeps
// calling a read-only loop tool and spends `perTurn` tokens. When operatorBudget > 0 the
// engine-level budget is the brake. A budget-stopped child renders the "[subagent stopped:
// reached its token budget]" note (StopBudget is a clean success-with-note terminal).
//
// Use operatorBudget=0 for an unlimited engine (useful when testing that no budget fires).
func budgetTrippingChild(t *testing.T, perTurn, turns, operatorBudget int) *agent.Engine {
	t.Helper()
	var script []mockllm.Turn
	for i := 0; i < turns; i++ {
		script = append(script,
			mockllm.ChunksTurn(
				mockllm.ToolCallChunk(toolCall("k", "Loop", `{}`)),
				mockllm.UsageChunk(session.Usage{InputTokens: perTurn}),
				mockllm.DoneChunk(session.StopEndTurn),
			),
		)
	}
	// A trailing text turn so an UNBOUNDED child finishes cleanly with a real summary.
	script = append(script, mockllm.TextTurn("CHILD DONE"))
	return agent.NewEngine(agent.Deps{
		LLM:          mockllm.New(script...),
		Catalog:      catalogWith(t, loopTool()),
		Policy:       permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:        "child-model",
		MaxRunTokens: operatorBudget,
	})
}

func TestSubagentTokenBudgetSchemaDescribesActualSemantics(t *testing.T) {
	t.Parallel()

	task := agent.NewSubagentTool(budgetTrippingChild(t, 1, 1, 0))
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(task.Spec().Schema, &schema); err != nil {
		t.Fatalf("decode real Subagent schema: %v", err)
	}

	tests := []struct {
		field string
		want  []string
	}{
		{
			field: "max_run_tokens",
			want: []string{
				"cumulative input+output", "not a provider output-token limit",
				"inherit the operator/engine budget", "bounded or disabled", "tighten-only",
				"25 000 per-call floor", "between turns", "not guaranteed", "on resume",
				"earlier cumulative usage remains spent", "stop before new work",
			},
		},
		{
			field: "max_tokens",
			want: []string{
				"deprecated alias for max_run_tokens", "cumulative input+output",
				"not a provider output-token limit", "inherit the operator/engine budget",
				"bounded or disabled", "tighten-only", "25 000 per-call floor",
				"between turns", "not guaranteed", "resumed child", "stop before new work",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			desc := strings.ToLower(schema.Properties[tc.field].Description)
			if desc == "" {
				t.Fatalf("real Subagent schema has no description for %q", tc.field)
			}
			for _, want := range tc.want {
				if !strings.Contains(desc, want) {
					t.Errorf("%s description must contain %q, got:\n%s", tc.field, want, schema.Properties[tc.field].Description)
				}
			}
		})
	}

	budgetDescriptions := strings.ToLower(schema.Properties["max_run_tokens"].Description + "\n" + schema.Properties["max_tokens"].Description)
	for _, stale := range []string{
		"usually-unlimited", "usually unlimited", "default unlimited",
		"returns its best-effort summary", "guaranteed best-effort summary",
	} {
		if strings.Contains(budgetDescriptions, stale) {
			t.Errorf("Subagent budget schema retains stale promise %q:\n%s", stale, budgetDescriptions)
		}
	}
}

// TestSubagentMaxRunTokensAliasResolvesToOverride proves the PREFERRED max_run_tokens alias
// caps the child exactly as the deprecated max_tokens does: a per-call budget above the
// floor (minSubagentRunTokens) rides RunRequest.MaxRunTokensOverride into the loop brake
// and trips StopBudget. The child engine has NO operator budget so only the per-call
// override stops it.
//
// We use a value above the floor (agent.MinSubagentRunTokens + 500) and script the child
// to spend enough tokens to cross that budget, proving the override is honoured verbatim
// when it is already at or above the floor.
func TestSubagentMaxRunTokensAliasResolvesToOverride(t *testing.T) {
	// Budget just above the floor; child spends 10 000/turn and will cross it after 3 turns.
	budget := agent.MinSubagentRunTokens + 500 // 25 500
	perTurn := 10_000
	// 6 turns × 10 000 = 60 000 tokens available; the 25 500 budget trips after turn 3.
	child := budgetTrippingChild(t, perTurn, 6, 0 /* no operator budget — per-call is the only brake */)
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			fmt.Sprintf(`{"prompt":"loop","max_run_tokens":%d}`, budget))),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("a budget-stopped child is a clean success-with-note, not an error: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "reached its token budget") {
		t.Fatalf("max_run_tokens did not trip the per-call budget; result = %q", results[0].Content)
	}
}

// TestSubagentMaxTokensDeprecatedAliasStillWorks is the backward-compat guard: the
// deprecated max_tokens alone still caps the child via the same budget brake (above-floor
// value, same mechanic as max_run_tokens).
func TestSubagentMaxTokensDeprecatedAliasStillWorks(t *testing.T) {
	budget := agent.MinSubagentRunTokens + 500
	perTurn := 10_000
	child := budgetTrippingChild(t, perTurn, 6, 0)
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			fmt.Sprintf(`{"prompt":"loop","max_tokens":%d}`, budget))),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("a budget-stopped child is a clean success-with-note, not an error: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "reached its token budget") {
		t.Fatalf("the deprecated max_tokens alias did not trip the per-call budget; result = %q", results[0].Content)
	}
}

// TestSubagentMaxRunTokensConflictRejected is the ADVERSARIAL case: the model supplies BOTH
// aliases with DIFFERENT positive values. The call must be rejected with a model-visible
// error tool result, and the child must NEVER run (no token-budget note, no child summary).
func TestSubagentMaxRunTokensConflictRejected(t *testing.T) {
	child := budgetTrippingChild(t, 100, 6, 0)
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","max_tokens":100,"max_run_tokens":200}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("conflicting budget aliases must be a model-visible error, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "set only one of max_run_tokens or") {
		t.Fatalf("error should name the conflicting-alias rule, got %q", results[0].Content)
	}
	// The child must not have run at all — neither a summary nor a budget note.
	if strings.Contains(results[0].Content, "CHILD DONE") || strings.Contains(results[0].Content, "token budget") {
		t.Fatalf("the child ran despite the conflicting-alias rejection: %q", results[0].Content)
	}
}

// TestSubagentMaxRunTokensSameValueAccepted proves that supplying BOTH aliases with the SAME
// positive value is NOT a conflict (they name the same budget) — the call is accepted and
// the shared value caps the child (value must be above the floor).
func TestSubagentMaxRunTokensSameValueAccepted(t *testing.T) {
	budget := agent.MinSubagentRunTokens + 500
	perTurn := 10_000
	child := budgetTrippingChild(t, perTurn, 6, 0)
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			fmt.Sprintf(`{"prompt":"loop","max_tokens":%d,"max_run_tokens":%d}`, budget, budget))),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("equal-value aliases must be accepted, not rejected: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "reached its token budget") {
		t.Fatalf("the shared budget did not cap the child; result = %q", results[0].Content)
	}
}

// TestSubagentBudgetUnsetByDefault is the default-OFF guard (mirrors
// TestSubagentOmittedPerCallArgsUnchanged): with NEITHER alias set the child runs to its
// scripted text answer under an unlimited budget — even though its spend would trip a tiny
// per-call ceiling if one were supplied. A regression that defaulted the budget to a
// non-zero value would stop the child early with a token-budget note instead.
func TestSubagentBudgetUnsetByDefault(t *testing.T) {
	// The child spends 100/turn over 6 turns (600 total) before its summary; with no
	// budget set it must reach "CHILD DONE", not stop on a budget note.
	child := budgetTrippingChild(t, 100, 6, 0)
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("default-budget child must finish cleanly, got error %+v", results[0])
	}
	if strings.Contains(results[0].Content, "token budget") {
		t.Fatalf("no budget was set, yet the child stopped on a token budget (default is NOT off): %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "CHILD DONE") {
		t.Fatalf("default-budget child should run to its summary; got %q", results[0].Content)
	}
}

// TestSubagentMaxRunTokensNonPositiveTreatedAsUnset proves an explicit NON-POSITIVE budget
// (the model emitting max_run_tokens: 0, and the deprecated max_tokens: -1) is treated as
// UNSET — exactly like omitting the arg: the child runs to completion under the inherited
// unlimited budget, with no token-budget note. A regression that read a 0/negative value
// straight into MaxRunTokensOverride would stop the child immediately (a 0 ceiling is
// "already over budget").
func TestSubagentMaxRunTokensNonPositiveTreatedAsUnset(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
	}{
		{"zero max_run_tokens", `{"prompt":"loop","max_run_tokens":0}`},
		{"negative max_run_tokens", `{"prompt":"loop","max_run_tokens":-5}`},
		{"zero deprecated max_tokens", `{"prompt":"loop","max_tokens":0}`},
		{"zero both aliases", `{"prompt":"loop","max_run_tokens":0,"max_tokens":0}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := budgetTrippingChild(t, 100, 6, 0)
			task := agent.NewSubagentTool(child)

			results, _ := subagentParentResults(t, task,
				mockllm.ToolCallTurn(toolCall("p1", "Subagent", tc.args)),
				mockllm.TextTurn("parent done"),
			)
			if len(results) != 1 {
				t.Fatalf("want 1 result, got %d", len(results))
			}
			if results[0].IsError {
				t.Fatalf("a non-positive budget must be treated as unset (no error), got %+v", results[0])
			}
			if strings.Contains(results[0].Content, "token budget") {
				t.Fatalf("a non-positive budget stopped the child on a token budget (should be unset/unlimited): %q", results[0].Content)
			}
			if !strings.Contains(results[0].Content, "CHILD DONE") {
				t.Fatalf("a non-positive budget should let the child run to its summary; got %q", results[0].Content)
			}
		})
	}
}

// TestSubagentMaxRunTokensTightenOnlyCannotLoosen is the token-budget analogue of
// TestSubagentPerCallTightenOnlyCannotLoosen: a per-call max_run_tokens HIGHER than the
// inherited engine budget cannot loosen it. The child engine carries a tight 250-token
// budget; a per-call max_run_tokens=10000 must NOT raise it, so the child still stops on
// the inherited budget rather than running to its summary.
func TestSubagentMaxRunTokensTightenOnlyCannotLoosen(t *testing.T) {
	var script []mockllm.Turn
	for i := 0; i < 6; i++ {
		script = append(script,
			mockllm.ChunksTurn(
				mockllm.ToolCallChunk(toolCall("k", "Loop", `{}`)),
				mockllm.UsageChunk(session.Usage{InputTokens: 100}),
				mockllm.DoneChunk(session.StopEndTurn),
			),
		)
	}
	script = append(script, mockllm.TextTurn("CHILD DONE"))
	// Engine budget 250 (tight); the per-call 10000 must not raise it.
	child := agent.NewEngine(agent.Deps{
		LLM:          mockllm.New(script...),
		Catalog:      catalogWith(t, loopTool()),
		Policy:       permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:        "child-model",
		MaxRunTokens: 250,
	})
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","max_run_tokens":10000}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].Content, "reached its token budget") {
		t.Fatalf("tighten-only violated: a higher per-call budget loosened the inherited ceiling; result = %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "CHILD DONE") {
		t.Fatalf("the child ran to completion — the inherited 250 budget was loosened by the per-call 10000: %q", results[0].Content)
	}
}

// TestSubagentRunTokensFloorRaisesSmallBudget is the PRIMARY regression guard for the
// footgun fix: a per-call max_run_tokens BELOW the minSubagentRunTokens floor (e.g. 6 000)
// is silently raised to minSubagentRunTokens (25 000). This proves buildSubagentRunRequest
// clamps the override up so the child can complete at least one useful turn even when the
// model picks an impractically small value.
//
// The child engine has NO operator budget (unlimited) and a script of 6 turns spending
// 100 tokens each (600 total), well below 25 000. With the floor active the child runs to
// completion; without the floor 6 000 would trip after the first turn.
func TestSubagentRunTokensFloorRaisesSmallBudget(t *testing.T) {
	// 6 000 is below the 25 000 floor — the child's 600-token script should run to "CHILD DONE".
	child := budgetTrippingChild(t, 100, 6, 0)
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","max_run_tokens":6000}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("floor must prevent the child from stopping immediately; got error: %q", results[0].Content)
	}
	// The child's 600-token script is well below the floored 25 000, so it must finish.
	if strings.Contains(results[0].Content, "token budget") {
		t.Fatalf("child stopped on token budget despite floor (600 tokens < 25 000 floor); result = %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "CHILD DONE") {
		t.Fatalf("floored budget should let the 600-token child run to summary; got %q", results[0].Content)
	}
}

// TestSubagentRunTokensFloorDoesNotRaiseAboveFloor proves a value already at or above the
// floor is passed through unchanged: a budget of minSubagentRunTokens+500 must cap a child
// spending 10 000/turn at the expected boundary, not be silently raised further.
func TestSubagentRunTokensFloorDoesNotRaiseAboveFloor(t *testing.T) {
	// budget = floor + 500; child spends 10 000/turn → trips after turn 3 (30 000 > 25 500).
	budget := agent.MinSubagentRunTokens + 500
	perTurn := 10_000
	child := budgetTrippingChild(t, perTurn, 6, 0)
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			fmt.Sprintf(`{"prompt":"loop","max_run_tokens":%d}`, budget))),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("above-floor budget stopped with an error: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "reached its token budget") {
		t.Fatalf("above-floor budget should still cap the child; result = %q", results[0].Content)
	}
}

// TestSubagentRunTokensFloorZeroStaysZero is the no-op guard: an absent/zero budget is
// NOT raised to the floor — it stays as "inherit/unlimited". The floor only applies to
// a positive value that is below minSubagentRunTokens.
func TestSubagentRunTokensFloorZeroStaysZero(t *testing.T) {
	child := budgetTrippingChild(t, 100, 6, 0)
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("absent budget must inherit unlimited; got error: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "CHILD DONE") {
		t.Fatalf("absent budget should run the child to summary; got %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "token budget") {
		t.Fatalf("absent budget should not trip any budget stop; got %q", results[0].Content)
	}
}

// TestSubagentRunTokensFloorOperatorCeilingStillWins is the tighten-only safety invariant
// for the floor: even after the floor raises a tiny per-call value to 25 000, the operator
// ceiling (Deps.MaxRunTokens = 250) must still win. The effective budget is
// min(floored-override=25000, operator=250) = 250.
func TestSubagentRunTokensFloorOperatorCeilingStillWins(t *testing.T) {
	var script []mockllm.Turn
	for i := 0; i < 6; i++ {
		script = append(script,
			mockllm.ChunksTurn(
				mockllm.ToolCallChunk(toolCall("k", "Loop", `{}`)),
				mockllm.UsageChunk(session.Usage{InputTokens: 100}),
				mockllm.DoneChunk(session.StopEndTurn),
			),
		)
	}
	script = append(script, mockllm.TextTurn("CHILD DONE"))
	// Tight operator budget 250; per-call 6 000 (below floor) gets raised to 25 000
	// by the floor — but the operator 250 must still win via tighten-only fold.
	child := agent.NewEngine(agent.Deps{
		LLM:          mockllm.New(script...),
		Catalog:      catalogWith(t, loopTool()),
		Policy:       permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:        "child-model",
		MaxRunTokens: 250,
	})
	task := agent.NewSubagentTool(child)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","max_run_tokens":6000}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	// The operator 250 budget must trip, not the floored 25 000.
	if !strings.Contains(results[0].Content, "reached its token budget") {
		t.Fatalf("operator ceiling must still trip even when per-call is below floor; result = %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "CHILD DONE") {
		t.Fatalf("child ran to completion — floor raised per-call override above operator ceiling: %q", results[0].Content)
	}
}
