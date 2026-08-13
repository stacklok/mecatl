package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestSubagentForkAndResumeRejected proves fork+resume is a model-addressable
// error (the FIRST conflict in validateFork's order, so it wins even over the
// later forkHistory-nil gate on the plain Execute path).
func TestSubagentForkAndResumeRejected(t *testing.T) {
	task := agent.NewSubagentTool(childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t)))
	res := runOneSubagent(t, task, "p1", `{"prompt":"go","fork":true,"resume":"subagent-old"}`)
	if !res.IsError || !strings.Contains(res.Content, "fork") || !strings.Contains(res.Content, "resume") {
		t.Fatalf("fork+resume result = %+v, want a fork/resume rejection error", res)
	}
}

// TestSubagentForkAndAgentRejected proves fork+agent is rejected (a forked child
// runs on the parent's engine, not a specialist).
func TestSubagentForkAndAgentRejected(t *testing.T) {
	task := agent.NewSubagentTool(childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t)),
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}},
		))
	res := runOneSubagent(t, task, "p1", `{"prompt":"go","fork":true,"agent":"reviewer"}`)
	if !res.IsError || !strings.Contains(res.Content, "fork") || !strings.Contains(res.Content, "agent") {
		t.Fatalf("fork+agent result = %+v, want a fork/agent rejection error", res)
	}
}

// TestSubagentForkAndModelRejected proves fork+model is rejected (a forked child
// inherits the parent's engine/model — a different model could not replay the
// parent's provider-private reasoning/phase blobs).
func TestSubagentForkAndModelRejected(t *testing.T) {
	task := agent.NewSubagentTool(childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t)),
		agent.WithSubagentEngineFactory(func(string) (*agent.Engine, bool) {
			return childEngineWith(mockllm.New(mockllm.TextTurn("m")), catalogWith(t)), true
		}))
	res := runOneSubagent(t, task, "p1", `{"prompt":"go","fork":true,"model":"some-model"}`)
	if !res.IsError || !strings.Contains(res.Content, "fork") || !strings.Contains(res.Content, "model") {
		t.Fatalf("fork+model result = %+v, want a fork/model rejection error", res)
	}
}

// TestSubagentForkUnsupportedWithoutParent proves a fork:true call on the plain
// Execute path (no parent session threaded ⇒ caps.forkHistory == nil) is an honest
// "not supported on this run" error, never a silent fresh-context child.
func TestSubagentForkUnsupportedWithoutParent(t *testing.T) {
	task := agent.NewSubagentTool(childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t)))
	res := runOneSubagent(t, task, "p1", `{"prompt":"go","fork":true}`)
	if !res.IsError || !strings.Contains(res.Content, "fork") || !strings.Contains(res.Content, "not supported") {
		t.Fatalf("fork-without-parent result = %+v, want a 'fork is not supported on this run' error", res)
	}
}

// TestForkChildCannotFork proves the no-nesting invariant holds for a forked child:
// the child engine's catalog is the read-only explorer set, which contains NO
// Subagent tool, so a fork child that TRIES to call Subagent gets an unknown-tool
// error — it cannot recurse. (The composition root never puts Subagent in a child
// catalog; this asserts the property end-to-end via the child loop.)
func TestForkChildCannotFork(t *testing.T) {
	// Child scripts a (would-be) recursive Subagent call, then a summary turn. The
	// child catalog (catalogWith(t)) has no Subagent, so the call is unknown.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Subagent", `{"prompt":"recurse","fork":true}`)),
		mockllm.TextTurn("child could not recurse"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"do it","fork":true}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 parent result, got %d", len(results))
	}
	// The fork child must run (no error) and reach its summary: the recursive
	// Subagent call inside it failed as unknown rather than spawning a grandchild.
	if results[0].IsError {
		t.Fatalf("fork child errored unexpectedly: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "child could not recurse") {
		t.Fatalf("fork child result = %q, want the post-failed-recursion summary", results[0].Content)
	}
}

// TestSubagentForkChildSeesParentHistory is the MODEL-FACING e2e: a parent runs a
// multi-turn scripted conversation that records a SENTINEL in an earlier turn, then
// forks a child. The child's mockllm captures its replayed LLMRequest.Messages and
// asserts they INCLUDE the parent-turn sentinel — proving the child was seeded from
// a copy of the parent conversation rather than starting empty. The child returns a
// result whose trailer carries the agentId.
//
// MUTATION-VERIFY anchor: revert the seeding (make the child start empty) and this
// test must fail on the missing-sentinel assertion.
func TestSubagentForkChildSeesParentHistory(t *testing.T) {
	const sentinel = "PARENT_SENTINEL_42"

	var mu sync.Mutex
	var childReqMsgs []session.Message
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		// Capture the FIRST child request (the only one — child does one text turn).
		if childReqMsgs == nil {
			childReqMsgs = req.Messages
		}
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("forked child reporting in"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	// A benign parent tool so turn 1 can record an assistant message (carrying the
	// sentinel in its text) AND keep the loop alive for the turn-2 fork. The fork
	// snapshot copies the parent's earlier turns, so the sentinel rides into the
	// child request.
	note := &fakeTool{name: "Note", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "noted"), nil
		}}
	parentLLM := mockllm.New(
		// Turn 1: assistant text carries the sentinel + a tool call (keeps the loop going).
		mockllm.ChunksTurn(
			mockllm.TextChunk("noting the "+sentinel+" for later"),
			mockllm.ToolCallChunk(toolCall("n1", "Note", `{}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		// Turn 2: fork the child.
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"continue the investigation","fork":true}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, note)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)
	var results []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "p1" {
			results = append(results, ev.ToolResult)
		}
	}
	if len(results) != 1 {
		t.Fatalf("want 1 fork result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("fork child result is an error: %q", results[0].Content)
	}
	// The result carries the agentId trailer on its first line.
	id := extractAgentID(t, results[0].Content)
	if id == "" {
		t.Fatalf("fork result missing agentId trailer: %q", results[0].Content)
	}

	mu.Lock()
	msgs := childReqMsgs
	mu.Unlock()
	var sawSentinel, sawForkPrompt bool
	for _, m := range msgs {
		if strings.Contains(m.Text, sentinel) {
			sawSentinel = true
		}
		if strings.Contains(m.Text, "continue the investigation") {
			sawForkPrompt = true
		}
	}
	if !sawSentinel {
		t.Fatalf("forked child did NOT see the parent sentinel %q in its replayed history: %+v", sentinel, msgs)
	}
	if !sawForkPrompt {
		t.Fatalf("forked child did not see its own fork prompt: %+v", msgs)
	}
}

// TestSubagentForkBackgroundChildSeesParentHistory is the fork+background e2e: it
// guards the LOAD-BEARING design claim that the fork snapshot SLICE is captured
// SYNCHRONOUSLY in run() (validateFork → caps.forkHistory()) and threaded into
// backgroundChild.forkHistory BEFORE the detach — NOT taken from a closure called
// inside the detached driveBackground goroutine (where the parent has kept
// appending, so the slice would be wrong or racy). A regression moving the capture
// into the background goroutine would still seed SOME history, but this test pins
// that the DETACHED child sees the parent's pre-fork SENTINEL.
//
// The parent records the sentinel in turn 1, forks a BACKGROUND child in turn 2
// (which returns immediately), then collects via SubagentStatus{wait_ms} in turn 3.
// The child's replayed LLMRequest.Messages (captured via WithRequestObserver) must
// contain the sentinel; the started-result and the collected body both carry the
// agentId trailer.
//
// MUTATION-VERIFY anchor: move the snapshot capture so it happens POST-detach (or
// null the synchronously-captured slice and re-take it inside driveBackground), and
// this test must fail on the missing-sentinel (or racy) assertion.
func TestSubagentForkBackgroundChildSeesParentHistory(t *testing.T) {
	const sentinel = "PARENT_SENTINEL_BG_77"

	var mu sync.Mutex
	var childReqMsgs []session.Message
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		// Capture the FIRST child request (the only one — child does one text turn).
		if childReqMsgs == nil {
			childReqMsgs = req.Messages
		}
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("forked background child reporting in"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	// A benign parent tool so turn 1 records an assistant message carrying the
	// sentinel AND keeps the loop alive for the turn-2 background fork.
	note := &fakeTool{name: "Note", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "noted"), nil
		}}
	parentLLM := mockllm.New(
		// Turn 1: assistant text carries the sentinel + a tool call (keeps the loop going).
		mockllm.ChunksTurn(
			mockllm.TextChunk("noting the "+sentinel+" for later"),
			mockllm.ToolCallChunk(toolCall("n1", "Note", `{}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		// Turn 2: fork the child IN THE BACKGROUND (returns immediately with the started-result).
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"continue in the background","fork":true,"background":true}`)),
		// Turn 3: collect the detached child's result.
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-s1-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool(), note)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var sawBackgroundStart bool
	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil && ev.Subagent.Background {
			sawBackgroundStart = true
		}
	})
	if !sawBackgroundStart {
		t.Fatalf("the fork child must start in the BACKGROUND (subagent.start Background=true)")
	}
	results := resultByCallID(evs)

	// The fork+background call returned immediately with the started-result + agentId.
	started := results["p1"]
	if started == nil || started.IsError {
		t.Fatalf("fork+background Subagent call must return a non-error started-result, got %+v", started)
	}
	startedID := extractAgentID(t, started.Content)
	if startedID == "" {
		t.Fatalf("started-result missing agentId trailer: %q", started.Content)
	}

	// SubagentStatus collected the detached child's body, with its own agentId trailer.
	collected := results["p2"]
	if collected == nil || collected.IsError {
		t.Fatalf("SubagentStatus collection of the detached fork child must succeed, got %+v", collected)
	}
	if !strings.Contains(collected.Content, "forked background child reporting in") {
		t.Fatalf("collected body must carry the child's findings, got %q", collected.Content)
	}
	if id := extractAgentID(t, collected.Content); id == "" {
		t.Fatalf("collected body missing agentId trailer: %q", collected.Content)
	}

	// The load-bearing assertion: the DETACHED child's replayed history carries the
	// parent's pre-fork sentinel — proving the snapshot slice was captured
	// synchronously in run() before the detach, not from a post-detach closure.
	mu.Lock()
	msgs := childReqMsgs
	mu.Unlock()
	var sawSentinel, sawForkPrompt bool
	for _, m := range msgs {
		if strings.Contains(m.Text, sentinel) {
			sawSentinel = true
		}
		if strings.Contains(m.Text, "continue in the background") {
			sawForkPrompt = true
		}
	}
	if !sawSentinel {
		t.Fatalf("detached fork child did NOT see the parent sentinel %q in its replayed history: %+v", sentinel, msgs)
	}
	if !sawForkPrompt {
		t.Fatalf("detached fork child did not see its own fork prompt: %+v", msgs)
	}
}
