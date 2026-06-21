package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// subagentParentResults runs a parent engine whose only tool is the given Subagent tool,
// driving it with the supplied parent turns, and returns every ToolResult the
// parent observed plus the parent's final text.
func subagentParentResults(t *testing.T, task tool.Tool, parentTurns ...mockllm.Turn) ([]*session.ToolResult, string) {
	t.Helper()
	parentLLM := mockllm.New(parentTurns...)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)
	var results []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult {
			results = append(results, ev.ToolResult)
		}
	}
	return results, lastResult(t, evs).Text
}

// TestSubagentRoutesToNamedAgent proves that Subagent(agent="reviewer") runs the named
// engine (distinct behaviour) rather than the default explorer.
func TestSubagentRoutesToNamedAgent(t *testing.T) {
	// Default explorer: a child whose summary is "DEFAULT".
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	// Named specialist "reviewer": a distinct child whose summary is "REVIEWER".
	reviewerEngine := childEngineWith(mockllm.New(mockllm.TextTurn("REVIEWER")), catalogWith(t))

	task := agent.NewSubagentTool(defaultEngine, agent.WithAgentEngines(
		map[string]*agent.Engine{"reviewer": reviewerEngine},
		[]agent.AgentMeta{{Name: "reviewer", Description: "reviews diffs"}},
	))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"check it","agent":"reviewer"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError || !strings.Contains(results[0].Content, "REVIEWER") {
		t.Fatalf("routed result = %+v, want the reviewer engine's summary", results[0])
	}
}

// TestSubagentDefaultExplorerUnchanged proves no regression: a Subagent call with NO
// `agent` arg runs the default explorer even when specialists are configured.
func TestSubagentDefaultExplorerUnchanged(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	reviewerEngine := childEngineWith(mockllm.New(mockllm.TextTurn("REVIEWER")), catalogWith(t))

	task := agent.NewSubagentTool(defaultEngine, agent.WithAgentEngines(
		map[string]*agent.Engine{"reviewer": reviewerEngine},
		[]agent.AgentMeta{{Name: "reviewer", Description: "reviews diffs"}},
	))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"explore"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || !strings.Contains(results[0].Content, "DEFAULT") {
		t.Fatalf("default route result = %+v, want DEFAULT explorer", results[0])
	}
}

// TestSubagentUnknownAgentErrors proves an unknown name yields a model-addressable
// error result listing the valid names (no silent fallback).
func TestSubagentUnknownAgentErrors(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	reviewerEngine := childEngineWith(mockllm.New(mockllm.TextTurn("REVIEWER")), catalogWith(t))

	task := agent.NewSubagentTool(defaultEngine, agent.WithAgentEngines(
		map[string]*agent.Engine{"reviewer": reviewerEngine},
		[]agent.AgentMeta{{Name: "reviewer", Description: "reviews diffs"}},
	))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","agent":"nope"}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("unknown agent should be an error result, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "nope") || !strings.Contains(results[0].Content, "reviewer") {
		t.Fatalf("error should name the bad input and list valid names, got %q", results[0].Content)
	}
}

// TestSubagentSpecEnumeratesAgents proves the available specialists appear in the
// Subagent tool's Spec().Description (progressive disclosure), and that Subagent.ReadOnly
// stays unconditionally true.
func TestSubagentSpecEnumeratesAgents(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine, agent.WithAgentEngines(
		map[string]*agent.Engine{"reviewer": defaultEngine, "doc-writer": defaultEngine},
		[]agent.AgentMeta{
			{Name: "doc-writer", Description: "writes docs"},
			{Name: "reviewer", Description: "reviews diffs"},
		},
	))
	desc := task.Spec().Description
	if !strings.Contains(desc, "reviewer: reviews diffs") || !strings.Contains(desc, "doc-writer: writes docs") {
		t.Fatalf("spec must enumerate specialists, got:\n%s", desc)
	}
	if !task.ReadOnly() {
		t.Fatalf("Subagent.ReadOnly() must stay true")
	}
	// The description must name the agentId discovery channel and every consumer of
	// the trailer id — SubagentStatus (live state / background collection),
	// InspectSubagent (persisted transcript), and the `resume` continuation — plus the
	// `background` detached mode, so the model knows the full id workflow
	// (runtime-discoverability).
	for _, want := range []string{"agentId:", "SubagentStatus", "InspectSubagent", "resume", "background"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("spec must name %q, got:\n%s", want, desc)
		}
	}
}

// TestSubagentSpecShellDisabledNoteOption pins both sides of the
// WithSubagentShellDisabledNote seam (issue #40): WITH the note set, Spec()'s
// isolated-worktree-shell clause is REPLACED by an honest read-only-only description
// carrying the reason verbatim; WITHOUT it (and with an empty reason, the no-op), the
// load-bearing shell clause stays byte-identical to the historical description, so a
// shell-bearing deployment's prompt-cache-stable spec never shifts.
func TestSubagentSpecShellDisabledNoteOption(t *testing.T) {
	// The historical shell clause, pinned byte-for-byte (the model plans build/test/git
	// delegation off this exact promise). The wording is precise: the child CAN write
	// scratch files via Bash, but the worktree is DISCARDED AND there are no Edit/Write
	// tools, so file changes never reach the parent.
	const shellClause = "(Read/Grep/Glob) plus a full shell in an isolated, throwaway git worktree — " +
		"it can build, test, inspect history, and write scratch files, but the worktree is DISCARDED " +
		"after the run (no Edit/Write tools; use Parallel when you need the diff kept) and it cannot " +
		"delegate further."
	const reason = "no shell on this workspace because it is untrusted (run with --trust-project to enable it)"

	eng := childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t))

	// Without the option: byte-stable historical clause, no note.
	plain := agent.NewSubagentTool(eng).Spec().Description
	if !strings.Contains(plain, shellClause) {
		t.Fatalf("spec without the note must keep the historical shell clause byte-identical, got:\n%s", plain)
	}
	// An empty reason is the documented no-op: byte-identical to no option at all.
	if noop := agent.NewSubagentTool(eng, agent.WithSubagentShellDisabledNote("")).Spec().Description; noop != plain {
		t.Fatalf("an empty note must be a no-op; descriptions differ:\n%s\n---\n%s", noop, plain)
	}

	// With the note: the worktree-shell promise is GONE and the reason rides verbatim.
	noted := agent.NewSubagentTool(eng, agent.WithSubagentShellDisabledNote(reason)).Spec().Description
	if strings.Contains(noted, "throwaway git worktree") {
		t.Fatalf("spec with the note must not still promise the worktree shell, got:\n%s", noted)
	}
	if !strings.Contains(noted, reason) {
		t.Fatalf("spec with the note must carry the reason verbatim, got:\n%s", noted)
	}
	// It still describes the read-only surface and the no-delegation rule.
	for _, want := range []string{"(Read/Grep/Glob) ONLY", "no Edit/Write", "cannot delegate further"} {
		if !strings.Contains(noted, want) {
			t.Fatalf("spec with the note must still state %q, got:\n%s", want, noted)
		}
	}
}

// TestSubagentSpecNoFSNoteOption pins both sides of the WithSubagentNoFSNote
// seam (the "no-fs" session profile): WITH the option, Spec()'s WHOLE
// tool-surface description is replaced — no Read/Grep/Glob claim, no worktree
// shell, no Parallel alternative, the honest MCP/memory/web-fetch surface
// instead; WITHOUT it the description is byte-identical to the historical one
// (the same shell clause TestSubagentSpecShellDisabledNoteOption pins), so a
// default-profile deployment's prompt-cache-stable spec never shifts.
func TestSubagentSpecNoFSNoteOption(t *testing.T) {
	eng := childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t))

	// Without the option: the historical file-tool surface, byte-stable.
	plain := agent.NewSubagentTool(eng).Spec().Description
	if !strings.Contains(plain, "(Read/Grep/Glob)") {
		t.Fatalf("precondition: the historical spec names the read-only file tools, got:\n%s", plain)
	}

	// With the option: every file-bound claim is gone.
	noFS := agent.NewSubagentTool(eng, agent.WithSubagentNoFSNote()).Spec().Description
	for _, banned := range []string{"Read/Grep/Glob", "worktree", "use Parallel", "build/test/git", "fresh workspace"} {
		if strings.Contains(noFS, banned) {
			t.Errorf("no-FS spec must not claim %q, got:\n%s", banned, noFS)
		}
	}
	// And the honest surface + the unchanged plumbing are described.
	for _, want := range []string{"NO filesystem", "NO file tools and NO shell", "MCP tools, memory, and web fetch",
		"cannot delegate further", "agentId:", "SubagentStatus", "InspectSubagent", "`resume`"} {
		if !strings.Contains(noFS, want) {
			t.Errorf("no-FS spec must state %q, got:\n%s", want, noFS)
		}
	}
}

// TestInspectSubagentSpecNamesProvenance proves the InspectSubagent description names the
// 'agentId:' line provenance and the ~40-message bound (kept in sync with
// maxInspectMessages).
func TestInspectSubagentSpecNamesProvenance(t *testing.T) {
	desc := agent.NewInspectSubagentTool(memstore.New()).Spec().Description
	if !strings.Contains(desc, "'agentId:' line") || !strings.Contains(desc, "~40") {
		t.Fatalf("InspectSubagent spec must name the 'agentId:' line and the ~40-message bound, got:\n%s", desc)
	}
}

// readLoopChild builds a child engine + its mockllm whose script ALWAYS emits
// another Read tool call (never a terminal text turn), so an unbounded run would
// keep issuing model calls. The number of model calls it actually makes equals the
// child session's MaxTurns, which lets a test assert the per-def turn cap bites.
// turns is the number of scripted tool-call turns (must exceed any limit under
// test so the cap, not script exhaustion, is what stops the run).
func readLoopChild(t *testing.T, turns int) (*agent.Engine, *mockllm.Provider) {
	t.Helper()
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "read ok"), nil
		}}
	script := make([]mockllm.Turn, 0, turns)
	for i := 0; i < turns; i++ {
		script = append(script, mockllm.ToolCallTurn(toolCall("k", "Read", `{"path":"x"}`)))
	}
	llm := mockllm.New(script...)
	return childEngineWith(llm, catalogWith(t, read)), llm
}

// TestSubagentNamedAgentLimitsBindChildSession proves a def's per-run limits (carried
// on AgentMeta.Limits) bound the child session: a def with MaxTurns=2 makes exactly
// 2 model calls even though the child would otherwise loop far longer.
func TestSubagentNamedAgentLimitsBindChildSession(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	boundedEngine, boundedLLM := readLoopChild(t, 8)

	task := agent.NewSubagentTool(defaultEngine, agent.WithAgentEngines(
		map[string]*agent.Engine{"bounded": boundedEngine},
		[]agent.AgentMeta{{
			Name:        "bounded",
			Description: "a tightly-bounded specialist",
			Limits:      session.Limits{MaxTurns: 2, MaxToolCalls: 40, MaxConsecutiveFailures: 3},
		}},
	))

	subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","agent":"bounded"}`)),
		mockllm.TextTurn("parent done"),
	)
	// MaxTurns=2 binds the loop; the child emits no summary, so the issue-#48 salvage
	// adds ONE bounded wrap-up turn → 2 + 1 = 3 model calls.
	if got := boundedLLM.Calls(); got != 3 {
		t.Fatalf("bounded child made %d model calls, want 3 (MaxTurns=2 bound + 1 bounded salvage turn)", got)
	}
}

// TestSubagentNamedAgentNoLimitsUsesDefault proves a def with NO per-run limits (a zero
// AgentMeta.Limits) runs under the Subagent tool's DEFAULT child limits, unchanged: the
// child loops past 2 turns up to the default MaxTurns (100).
func TestSubagentNamedAgentNoLimitsUsesDefault(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	// Script more turns than the default MaxTurns (100) so the DEFAULT cap, not script
	// exhaustion, is what stops the run.
	looseEngine, looseLLM := readLoopChild(t, 120)

	task := agent.NewSubagentTool(defaultEngine, agent.WithAgentEngines(
		map[string]*agent.Engine{"loose": looseEngine},
		[]agent.AgentMeta{{Name: "loose", Description: "no pinned limits"}}, // zero Limits
	))

	subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","agent":"loose"}`)),
		mockllm.TextTurn("parent done"),
	)
	// The default MaxTurns bounds the loop; the child emits no summary, so the issue-#48
	// salvage adds ONE bounded wrap-up turn on top of the default cap.
	if want := agent.DefaultChildLimits().MaxTurns + 1; looseLLM.Calls() != want {
		t.Fatalf("loose child made %d model calls, want the default MaxTurns=%d + 1 salvage turn = %d (no per-def override)",
			looseLLM.Calls(), agent.DefaultChildLimits().MaxTurns, want)
	}
}

// TestSubagentNoAgentsNoEnumeration proves the spec is unchanged when no specialists
// are configured (the default-explorer-only case).
func TestSubagentNoAgentsNoEnumeration(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine)
	if strings.Contains(task.Spec().Description, "Available specialist agents") {
		t.Fatalf("no agents configured: spec must not have an enumeration tail")
	}
}
