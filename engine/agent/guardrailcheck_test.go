package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/tool"
)

// checkerEngine builds a tool-less one-turn checker engine over llm, with the
// no-progress nudge disabled (so an empty turn ends in exactly one provider call) —
// the shape the composition's guardrail checker engine uses.
func checkerEngine(llm *mockllm.Provider) *agent.Engine {
	return agent.NewEngine(agent.Deps{
		LLM:                 llm,
		Catalog:             tool.NewCatalog(),
		Policy:              permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:               "checker-model",
		MaxNoProgressNudges: -1,
	})
}

// RunGuardrailCheck returns the raw final text for a completed run; the composition
// parses it. A clean verdict object passes straight through.
func TestRunGuardrailCheckReturnsText(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn(`{"safe":false,"reason":"exfil"}`))
	got, _, err := agent.RunGuardrailCheck(context.Background(), checkerEngine(llm), "inspect this")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `"safe":false`) {
		t.Fatalf("checker text not returned verbatim; got %q", got)
	}
}

// An EMPTY (verdict-less) checker turn must end in a clean terminal whose text is
// empty — the composition's ParseVerdict then rejects it (→ fail-open/closed). It is
// NOT an error here (a clean no-progress stop is a completed run), but the returned
// text carries no verdict, which is the safe outcome the caller needs.
func TestRunGuardrailCheckEmptyTurnNoVerdict(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn(""))
	got, _, err := agent.RunGuardrailCheck(context.Background(), checkerEngine(llm), "inspect this")
	if err != nil {
		t.Fatalf("an empty turn is a clean terminal, not an error: %v", err)
	}
	if strings.TrimSpace(got) != "" {
		t.Fatalf("an empty checker turn must yield no verdict text; got %q", got)
	}
}

// A garbage (non-verdict) reply returns its text; the composition's ParseVerdict
// rejects it. RunGuardrailCheck never fabricates a verdict — it only relays text.
func TestRunGuardrailCheckGarbageRelayed(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("I cannot decide, sorry."))
	got, _, err := agent.RunGuardrailCheck(context.Background(), checkerEngine(llm), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "cannot decide") {
		t.Fatalf("garbage reply must be relayed verbatim for the caller to reject; got %q", got)
	}
}

// A nil engine is an ERROR (a leaf helper, not a panic), so the caller takes its
// fail-open/closed path.
func TestRunGuardrailCheckNilEngineErrors(t *testing.T) {
	if _, _, err := agent.RunGuardrailCheck(context.Background(), nil, "x"); err == nil {
		t.Fatal("a nil checker engine must be an error, never a fabricated reply")
	}
}

// A cancelled context yields an error (the run does not complete), so the caller
// fails safe rather than trusting a partial reply.
func TestRunGuardrailCheckCancelledErrors(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn(`{"safe":true}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := agent.RunGuardrailCheck(ctx, checkerEngine(llm), "x"); err == nil {
		t.Fatal("a cancelled checker run must be an error")
	}
}
