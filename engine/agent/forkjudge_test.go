package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/tool"
)

func judgeCandidates() []agent.BranchSummary {
	return []agent.BranchSummary{
		{Label: "branch-1", Summary: "small diff"},
		{Label: "branch-2", Summary: "large diff"},
		{Label: "branch-3", Summary: "medium diff"},
	}
}

// TestEngineJudgePicksScriptedWinner asserts engineJudge parses a clean JSON verdict
// and returns the 0-based position (1-based winner number minus one).
func TestEngineJudgePicksScriptedWinner(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn(`{"winner": 3, "rationale": "medium is best"}`))
	j := agent.NewEngineJudge(childEngineWith(llm, tool.NewCatalog()))

	pos, rationale, _, err := j.Judge(context.Background(), judgeCandidates(), "prefer the smallest diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pos != 2 { // winner 3 (1-based) -> position 2 (0-based)
		t.Fatalf("pos = %d, want 2", pos)
	}
	if rationale != "medium is best" {
		t.Fatalf("rationale = %q", rationale)
	}
}

// TestEngineJudgeTolerantParse asserts the verdict is extracted even when wrapped in
// prose / code fences (the tolerant first-balanced-object extractor).
func TestEngineJudgeTolerantParse(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("Sure! Here is my pick:\n```json\n{\"winner\":1,\"rationale\":\"smallest\"}\n```\nDone."))
	j := agent.NewEngineJudge(childEngineWith(llm, tool.NewCatalog()))

	pos, rationale, _, err := j.Judge(context.Background(), judgeCandidates(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pos != 0 || rationale != "smallest" {
		t.Fatalf("pos=%d rationale=%q, want 0/smallest", pos, rationale)
	}
}

// TestEngineJudgeUnparseableFallsBack asserts a non-JSON judge reply falls back to
// position 0 with a note (never an error — Fork must not hard-fail).
func TestEngineJudgeUnparseableFallsBack(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("I really cannot decide, sorry."))
	j := agent.NewEngineJudge(childEngineWith(llm, tool.NewCatalog()))

	pos, rationale, _, err := j.Judge(context.Background(), judgeCandidates(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pos != 0 {
		t.Fatalf("pos = %d, want 0 (fallback)", pos)
	}
	if rationale == "" {
		t.Fatalf("expected a fallback rationale note")
	}
}

// TestEngineJudgeOutOfRangeFallsBack asserts an out-of-range winner index falls back
// to position 0.
func TestEngineJudgeOutOfRangeFallsBack(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn(`{"winner": 99, "rationale": "nope"}`))
	j := agent.NewEngineJudge(childEngineWith(llm, tool.NewCatalog()))

	pos, _, _, err := j.Judge(context.Background(), judgeCandidates(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pos != 0 {
		t.Fatalf("pos = %d, want 0 (out-of-range fallback)", pos)
	}
}

// TestNewEngineJudgeNilPanics asserts the composition-root contract.
func TestNewEngineJudgeNilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("NewEngineJudge(nil) did not panic")
		}
	}()
	_ = agent.NewEngineJudge(nil)
}
