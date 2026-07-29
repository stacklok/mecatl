package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestParallelFailedBranchReportsCauseNotLastChatLine is the Parallel half of issue
// #319: a branch that dies on a mid-stream provider error reported its last CHAT LINE as
// the failure reason (or a bare placeholder when it had said nothing). It must report the
// provider cause instead, through the SAME subagentErrorBody chokepoint the Subagent
// result uses — one policy, no second copy to drift.
//
// A single task keeps the scripted mockllm cursor deterministic (concurrent branches
// would race for turns), which is what lets this assert on exact content.
func TestParallelFailedBranchReportsCauseNotLastChatLine(t *testing.T) {
	const chatter = "Looks promising, let me try the other file."
	const causeText = "upstream 502: bad gateway"
	childEngine := childEngineWith(
		chattyThenBrokenChild(chatter, errors.New(causeText)),
		catalogWith(t, failureCauseReadTool()),
	)
	fork := agent.NewParallelTool(childEngine, &memForker{})

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["try approach A"]}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, fork)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	res := firstToolResult(t, drain(r))
	if !strings.Contains(res.Content, "branch-1 [FAILED]") {
		t.Fatalf("expected branch-1 marked FAILED:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, causeText) {
		t.Fatalf("the joined report must carry the branch's provider cause %q, got:\n%s", causeText, res.Content)
	}
	// The cause must be the FIRST line of the reason: the judge/first join paths render
	// only firstLine(failReason), so leading with the cause is what keeps the actionable
	// half from being truncated away.
	i := strings.Index(res.Content, "[FAILED] ===\n")
	if i < 0 {
		t.Fatalf("could not locate the branch-1 failure block:\n%s", res.Content)
	}
	reason := res.Content[i+len("[FAILED] ===\n"):]
	if firstLn, _, _ := strings.Cut(reason, "\n"); !strings.Contains(firstLn, causeText) {
		t.Fatalf("the cause must LEAD the failure reason (firstLine survives the judge join), got first line %q", firstLn)
	}
	if strings.HasPrefix(reason, chatter) {
		t.Fatalf("the branch's last chat line must not lead the failure reason (issue #319), got:\n%s", res.Content)
	}
}

// TestParallelFailedBranchWithNoTextReportsCause closes the no-branch-text half: before
// #319 this rendered an opaque no-summary placeholder instead of the real reason. The
// negative below asserts against subagentErrorBody's CURRENT floor ("failed without
// producing a summary" — caller-neutral, since the helper is shared with the Subagent
// path), so it still fires if the cause is ever dropped again.
func TestParallelFailedBranchWithNoTextReportsCause(t *testing.T) {
	const causeText = "upstream 429: rate limited"
	childEngine := childEngineWith(
		mockllm.New(mockllm.ErrorTurn(errors.New(causeText), mockllm.TextChunk("thinking"))),
		catalogWith(t),
	)
	fork := agent.NewParallelTool(childEngine, &memForker{})

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["try approach A"]}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, fork)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	res := firstToolResult(t, drain(r))
	if !strings.Contains(res.Content, causeText) {
		t.Fatalf("the joined report must carry the cause, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "failed without producing a summary") {
		t.Fatalf("the opaque placeholder must not stand when a cause is available, got:\n%s", res.Content)
	}
}
