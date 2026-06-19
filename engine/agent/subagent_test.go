package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// recordingHook is a port.HookRunner test double that records the phases it was
// invoked with. It never blocks and never errors.
type recordingHook struct {
	mu     sync.Mutex
	phases []governance.HookPhase
}

func (h *recordingHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	h.mu.Lock()
	h.phases = append(h.phases, ev.Phase)
	h.mu.Unlock()
	return governance.HookOutcome{}, nil
}

func (h *recordingHook) saw(p governance.HookPhase) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, got := range h.phases {
		if got == p {
			return true
		}
	}
	return false
}

// childEngineWith builds a child Engine over a scoped catalog and an allow-all
// policy (the recommended non-interactive subagent wiring), driven by the given
// scripted child LLM.
func childEngineWith(llm port.LLMProvider, cat *tool.Catalog) *agent.Engine {
	return childEngineWithModel("child-model", llm, cat)
}

// childEngineWithModel builds a default explorer-style child engine with an explicit
// model id (for the per-call override path, where the engine's Model must reflect the
// override, not the hardcoded default).
func childEngineWithModel(model string, llm port.LLMProvider, cat *tool.Catalog) *agent.Engine {
	return agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   model,
	})
}

// TestSubagentReturnsOnlyFinalString proves gauntlet #7 in its REFRAMED form: the
// real guarantee is CONTENT isolation, not "the parent observes nothing". The
// child's content — its message text, its tool args, its tool result bodies —
// must NEVER enter the parent's session.Conversation (the context sent to the
// LLM). The parent still receives exactly ONE ToolResult (the child's final
// summary, the one piece of child content that folds back by design).
//
// Separately, this test asserts that a REDACTED, metadata-only projection of the
// child's activity (subagent.start/tool/end) IS forwarded to the run's event
// stream and carries only metadata (goal/ids, tool NAMES + counts, usage, stop) —
// no child message text, no child tool args, no child result bodies. Forwarding
// that projection is orthogonal to isolation: it never touches Conversation.
func TestSubagentReturnsOnlyFinalString(t *testing.T) {
	// Child catalog: a read-only Read explorer tool (no Subagent, no mutating tools).
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read the file: package main"), nil
		}}
	childCat := catalogWith(t, childRead)

	// Child script: it reads a file, then summarizes.
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("let me read it"),
			mockllm.ToolCallChunk(toolCall("k1", "Read", `{"path":"main.go"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 7, OutputTokens: 2}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("summary: main.go is package main"),
	)
	childEngine := childEngineWith(childLLM, childCat)

	// Parent catalog contains only the Subagent tool.
	task := agent.NewSubagentTool(childEngine)
	parentCat := catalogWith(t, task)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate main.go"}`)),
		mockllm.TextTurn("parent received the summary"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	// Collect every ToolResult the PARENT observed.
	var parentResults []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult {
			parentResults = append(parentResults, ev.ToolResult)
		}
	}
	if len(parentResults) != 1 {
		t.Fatalf("parent saw %d tool results, want exactly 1: %v", len(parentResults), typesOf(evs))
	}
	got := parentResults[0]
	if got.IsError {
		t.Fatalf("parent tool result is an error: %q", got.Content)
	}
	if !strings.Contains(got.Content, "summary: main.go is package main") {
		t.Fatalf("parent tool result = %q, want it to contain the child's final summary", got.Content)
	}

	// The parent must NEVER see the child's intermediate signals. The child read
	// "main.go" and its tool result content was "child read the file ...".
	for _, ev := range evs {
		if ev.ToolCall != nil && ev.ToolCall.ID == "k1" {
			t.Fatalf("parent observed the child's intermediate tool.call (id k1)")
		}
		if ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, "child read the file") {
			t.Fatalf("parent observed the child's intermediate tool.result")
		}
		if ev.Type == session.EvMessageDelta && strings.Contains(ev.Text, "let me read it") {
			t.Fatalf("parent observed the child's intermediate message.delta")
		}
	}

	// Sanity: the parent's own final text is present and distinct from the child.
	res := lastResult(t, evs)
	if res.Text != "parent received the summary" {
		t.Fatalf("parent final text = %q", res.Text)
	}

	// THE REFRAMED GUARANTEE: none of the child's CONTENT entered the parent's
	// Conversation (the context sent to the LLM). The child's message text ("let
	// me read it"), its tool args ("main.go" in the Read call), and its tool result
	// body ("child read the file …") must appear nowhere in any parent message.
	// Only the child's final summary (folded back as the Subagent tool result) is
	// allowed. We scan every field of every message.
	// The child's distinctive content: its message text, its Read tool CALL (a
	// "Read" tool call never appears in the parent's own history — the parent only
	// calls Subagent), and its tool result body. None may appear anywhere in the parent
	// Conversation. (We do not key off "main.go" because that token also legitimately
	// appears in the parent's own Subagent prompt — only CHILD-specific content counts.)
	const childMsgText = "let me read it"
	const childResultBody = "child read the file"
	for _, msg := range sess.Conversation.Messages {
		if strings.Contains(msg.Text, childMsgText) {
			t.Fatalf("child message text leaked into parent Conversation: %q", msg.Text)
		}
		if strings.Contains(msg.Reasoning, childMsgText) {
			t.Fatalf("child reasoning leaked into parent Conversation: %q", msg.Reasoning)
		}
		for _, tc := range msg.ToolCalls {
			if tc.Name == "Read" {
				t.Fatalf("child Read tool call leaked into parent Conversation: %+v", tc)
			}
		}
		if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, childResultBody) {
			t.Fatalf("child tool result body leaked into parent Conversation: %q", msg.ToolResult.Content)
		}
	}

	// The redacted, metadata-only projection IS forwarded to the event stream.
	var starts, ends int
	var toolEvents []*session.SubagentPayload
	for _, ev := range evs {
		switch ev.Type {
		case session.EvSubagentStart:
			starts++
			if ev.Subagent == nil || ev.Subagent.ParentCallID != "p1" || ev.Subagent.ChildID == "" {
				t.Fatalf("subagent.start malformed: %+v", ev.Subagent)
			}
		case session.EvSubagentTool:
			if ev.Subagent == nil {
				t.Fatalf("subagent.tool missing payload")
			}
			toolEvents = append(toolEvents, ev.Subagent)
		case session.EvSubagentEnd:
			ends++
			if ev.Subagent == nil || ev.Subagent.ParentCallID != "p1" {
				t.Fatalf("subagent.end malformed: %+v", ev.Subagent)
			}
			// End carries aggregate metadata only.
			if ev.Subagent.ToolCount != 1 {
				t.Fatalf("subagent.end tool count = %d, want 1", ev.Subagent.ToolCount)
			}
			if ev.Subagent.Stop != session.StopEndTurn {
				t.Fatalf("subagent.end stop = %q, want end_turn", ev.Subagent.Stop)
			}
			if ev.Subagent.Usage.InputTokens != 7 || ev.Subagent.Usage.OutputTokens != 2 {
				t.Fatalf("subagent.end usage = %+v, want the child's cumulative usage", ev.Subagent.Usage)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("want exactly one subagent.start and one subagent.end; got %d/%d", starts, ends)
	}
	if len(toolEvents) != 1 {
		t.Fatalf("want one subagent.tool event for the child's single Read; got %d", len(toolEvents))
	}
	// The redacted tool event carries the child tool NAME and nothing else that is
	// content: no args, no result body anywhere on the payload.
	tev := toolEvents[0]
	if tev.ToolName != "Read" {
		t.Fatalf("subagent.tool name = %q, want Read", tev.ToolName)
	}
	if tev.IsError {
		t.Fatalf("subagent.tool unexpectedly flagged error")
	}
	// Defensive: the payload struct has no field that could carry child content;
	// confirm the goal-bearing field is empty on a tool event (goal is start-only).
	if tev.Goal != "" {
		t.Fatalf("subagent.tool unexpectedly carried a goal: %q", tev.Goal)
	}
}

// TestSubagentGoalClampedSymmetrically proves the forwarded subagent.start Goal
// stays a single, bounded line even when the model supplies a long, MULTI-LINE
// explicit description — the description path is clamped identically to the prompt
// fallback (newlines collapsed to spaces, truncated to the goal cap with an
// ellipsis), so a long description can't break the one-line Subagent-card title.
func TestSubagentGoalClampedSymmetrically(t *testing.T) {
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("child summary")), catalogWith(t, childRead))
	task := agent.NewSubagentTool(childEngine)

	// A long description with embedded newlines and tabs.
	desc := "line one of a very long description that easily exceeds the cap\nsecond line\tand a tab"
	argsJSON, err := json.Marshal(struct {
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
	}{Prompt: "investigate", Description: desc})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", string(argsJSON))),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	var goal string
	var found bool
	for _, ev := range evs {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			goal, found = ev.Subagent.Goal, true
		}
	}
	if !found {
		t.Fatalf("no subagent.start event observed")
	}
	if strings.ContainsAny(goal, "\n\r\t") {
		t.Fatalf("goal must be a single line (no newlines/tabs): %q", goal)
	}
	if n := len([]rune(goal)); n > 61 { // 60 runes + the trailing ellipsis rune
		t.Fatalf("goal length = %d runes, want <= 61 (cap + ellipsis): %q", n, goal)
	}
	if !strings.HasSuffix(goal, "…") {
		t.Fatalf("an over-cap goal should be truncated with an ellipsis: %q", goal)
	}
	if !strings.HasPrefix(goal, "line one") {
		t.Fatalf("goal should derive from the description: %q", goal)
	}
}

// TestSubagentStartCarriesResolvedModel asserts the generic Model field (issue #112 /
// ADR 0035) is populated on EvSubagentStart with the child engine's resolved model,
// independent of the opt-in router. Covers the inherited/default case: no router is
// wired, so RoutedCategory/RoutedModel are empty and Model carries the child engine's
// own model id ("child-model"). The model id is bare metadata (gauntlet #7).
func TestSubagentStartCarriesResolvedModel(t *testing.T) {
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("child summary")), catalogWith(t, childRead))
	task := agent.NewSubagentTool(childEngine)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate main.go"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	var found bool
	for _, ev := range evs {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			found = true
			if ev.Subagent.Model != "child-model" {
				t.Fatalf("EvSubagentStart.Model = %q, want %q (the child engine's resolved model)",
					ev.Subagent.Model, "child-model")
			}
			// No router wired: the routed fields stay empty; Model is the sole model surface.
			if ev.Subagent.RoutedCategory != "" || ev.Subagent.RoutedModel != "" {
				t.Fatalf("EvSubagentStart routed fields should be empty without the router: %+v", ev.Subagent)
			}
		}
	}
	if !found {
		t.Fatalf("no subagent.start event observed")
	}
}

// TestSubagentConcurrentAttribution proves the redacted subagent.* events from
// TWO Subagent calls that run concurrently (both read-only → one read batch, parallel
// goroutines) are attributed to the correct parent call. Each child runs a single,
// distinctly-named tool; we assert each subagent.tool event's ParentCallID is
// paired with the tool name that child actually ran, and that start/end counts are
// exactly one per call. This guards the emit closure's per-goroutine binding under
// real concurrency.
func TestSubagentConcurrentAttribution(t *testing.T) {
	// childEngineFor builds a child engine whose single tool is named toolName.
	childEngineFor := func(toolName string) *agent.Engine {
		ct := &fakeTool{name: toolName, readOnly: true,
			exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResult(in.ID, "ok"), nil
			}}
		llm := mockllm.New(
			mockllm.ToolCallTurn(toolCall("ck", toolName, `{}`)),
			mockllm.TextTurn("child done"),
		)
		return childEngineWith(llm, catalogWith(t, ct))
	}

	// Two distinct Subagent tools, each delegating to its own child engine, registered
	// under DISTINCT catalog names so the parent can call both in one turn. (The
	// catalog keys on Spec().Name, which is "Subagent" for both, so we wrap to rename.)
	taskA := agent.NewSubagentTool(childEngineFor("Alpha"), agent.WithChildSessionPrefix("subA"))
	taskB := agent.NewSubagentTool(childEngineFor("Bravo"), agent.WithChildSessionPrefix("subB"))
	parentCat := catalogWith(t, renamedSubagent{Tool: taskA, name: "TaskA"}, renamedSubagent{Tool: taskB, name: "TaskB"})

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("pa", "TaskA", `{"prompt":"investigate alpha"}`),
			toolCall("pb", "TaskB", `{"prompt":"investigate bravo"}`),
		),
		mockllm.TextTurn("both done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	// Expected tool name per parent call id.
	wantTool := map[string]string{"pa": "Alpha", "pb": "Bravo"}
	starts := map[string]int{}
	ends := map[string]int{}
	for _, ev := range evs {
		if ev.Subagent == nil {
			continue
		}
		p := ev.Subagent.ParentCallID
		switch ev.Type {
		case session.EvSubagentStart:
			starts[p]++
		case session.EvSubagentTool:
			if got := ev.Subagent.ToolName; got != wantTool[p] {
				t.Fatalf("subagent.tool for parent %q has tool %q, want %q (cross-attribution)", p, got, wantTool[p])
			}
		case session.EvSubagentEnd:
			ends[p]++
		}
	}
	for _, p := range []string{"pa", "pb"} {
		if starts[p] != 1 || ends[p] != 1 {
			t.Fatalf("parent %q: starts=%d ends=%d, want 1/1", p, starts[p], ends[p])
		}
	}
}

// renamedSubagent wraps a Subagent tool to advertise a different catalog name (so two Subagent
// tools can coexist in one catalog), forwarding both the Tool and observableTool
// methods so the dispatcher still routes it through ExecuteObserved.
type renamedSubagent struct {
	tool.Tool
	name string
}

func (rt renamedSubagent) Spec() tool.ToolSpec {
	s := rt.Tool.Spec()
	s.Name = rt.name
	return s
}

func (rt renamedSubagent) ExecuteObserved(ctx context.Context, call session.ToolCall, ws tool.Workspace, emit func(session.Event)) (session.ToolResult, error) {
	return rt.Tool.(interface {
		ExecuteObserved(context.Context, session.ToolCall, tool.Workspace, func(session.Event)) (session.ToolResult, error)
	}).ExecuteObserved(ctx, call, ws, emit)
}

// TestSubagentChildScopeExcludesSubagentAndMutators asserts the child catalog the
// composition root wires excludes Subagent (no recursion) and mutating tools. The
// child here tries to call Subagent and Write; both must come back as unknown-tool
// errors inside the child, and the parent still gets a single clean summary.
func TestSubagentChildScopeExcludesSubagentAndMutators(t *testing.T) {
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	// Scoped child catalog: read-only explorer only. No Subagent, no Write.
	childCat := catalogWith(t, childRead)

	if _, ok := childCat.Lookup("Subagent"); ok {
		t.Fatalf("child catalog must not contain Subagent (recursion risk)")
	}
	if _, ok := childCat.Lookup("Write"); ok {
		t.Fatalf("child catalog must not contain mutating tools")
	}

	// The child tries forbidden tools, then summarizes.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("k1", "Subagent", `{"prompt":"recurse"}`),
			toolCall("k2", "Write", `{"path":"x"}`),
		),
		mockllm.TextTurn("done despite blocks"),
	)
	childEngine := childEngineWith(childLLM, childCat)

	task := agent.NewSubagentTool(childEngine)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	var results []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult {
			results = append(results, ev.ToolResult)
		}
	}
	if len(results) != 1 {
		t.Fatalf("parent saw %d tool results, want 1", len(results))
	}
	if !strings.Contains(results[0].Content, "done despite blocks") {
		t.Fatalf("parent result = %q, want it to contain the child summary", results[0].Content)
	}
}

// TestSubagentParentCancelPropagates asserts that cancelling the parent ctx
// cancels the child run: the child loop ends (cancelled) and Execute returns
// without hanging.
func TestSubagentParentCancelPropagates(t *testing.T) {
	// A child LLM whose stream blocks until ctx is cancelled.
	blocking := &blockingProvider{started: make(chan struct{})}
	childEngine := childEngineWith(blocking, catalogWith(t))

	task := agent.NewSubagentTool(childEngine)

	// The parent calls Subagent; we instrument the child-start by reading the
	// blockingProvider's started channel from the parent goroutine.
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)),
		mockllm.TextTurn("recovered"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	ctx, cancel := context.WithCancel(context.Background())
	r := e.Run(ctx, newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	// Wait for the child's stream to start, then cancel the parent.
	<-blocking.started
	cancel()

	evs := drain(r)
	// The parent run itself ends cancelled (its ctx was cancelled). The key
	// assertion is that draining terminates at all — a child that ignored ctx
	// would hang Execute forever and this test would time out.
	res := lastResult(t, evs)
	if res.Stop != session.StopCancelled {
		t.Fatalf("parent stop = %q, want cancelled", res.Stop)
	}
}

// TestSubagentStopHookFires asserts the SubagentStop hook fires when the child
// finishes.
func TestSubagentStopHookFires(t *testing.T) {
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	childLLM := mockllm.New(mockllm.TextTurn("child summary"))
	childEngine := childEngineWith(childLLM, catalogWith(t, childRead))

	hook := &recordingHook{}
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStopHook(hook))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	drain(r)

	if !hook.saw(governance.PhaseSubagentStop) {
		t.Fatalf("SubagentStop hook did not fire; saw %v", hook.phases)
	}
}

// TestSubagentAutoDeniesAsk proves the non-interactive invariant: even if the
// child's policy returns Ask, Execute auto-denies it so the child cannot block on
// a human, and the parent still gets a clean (single) result.
func TestSubagentAutoDeniesAsk(t *testing.T) {
	var executed atomic.Bool
	// A read-only child tool that, were it allowed, would set executed.
	childTool := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed.Store(true)
			return session.NewToolResult(in.ID, "should not run"), nil
		}}
	// Policy with no rules → Ask by default.
	childEngine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(toolCall("k1", "Read", `{"path":"a"}`)),
			mockllm.TextTurn("child finished after denial"),
		),
		Catalog: catalogWith(t, childTool),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "child-model",
	})

	task := agent.NewSubagentTool(childEngine)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})

	done := make(chan []session.Event, 1)
	go func() {
		r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
		done <- drain(r)
	}()

	var evs []session.Event
	select {
	case evs = <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Subagent did not complete — child likely blocked on a permission ask")
	}

	if executed.Load() {
		t.Fatalf("the denied child tool was executed")
	}
	// Parent must NEVER see a permission.ask: the child's ask is resolved inside
	// the Subagent tool, never surfaced.
	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatalf("child permission.ask leaked to the parent stream")
		}
	}
	var results []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult {
			results = append(results, ev.ToolResult)
		}
	}
	if len(results) != 1 || !strings.Contains(results[0].Content, "child finished after denial") {
		t.Fatalf("parent results = %+v, want single child summary", results)
	}
}

// TestSubagentRejectsEmptyPrompt asserts a missing/empty prompt yields an error
// ToolResult without spinning up a child.
func TestSubagentRejectsEmptyPrompt(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(), tool.NewCatalog())
	task := agent.NewSubagentTool(childEngine)

	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", json.RawMessage(`{"prompt":"   "}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("unexpected harness error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "prompt") {
		t.Fatalf("expected an error result about the missing prompt, got %+v", res)
	}
}

// TestNewSubagentToolNilEnginePanics asserts the composition-root contract: a nil
// child Engine is a programming error.
func TestNewSubagentToolNilEnginePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("NewSubagentTool(nil) did not panic")
		}
	}()
	_ = agent.NewSubagentTool(nil)
}

// recordingSubagentForker is a fake tool.WorkspaceForker that hands out in-memory
// workspaces rooted at a fork-specific path and records the fork labels and cleanup
// calls. It mirrors the team supervisor's recordingForker.
type recordingSubagentForker struct {
	mu       sync.Mutex
	labels   []string
	cleanups int
}

func (f *recordingSubagentForker) Fork(_ context.Context, _ tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	f.mu.Lock()
	f.labels = append(f.labels, label)
	f.mu.Unlock()
	ws := memfs.NewWorkspace("/fork/" + label)
	cleanup := func() error {
		f.mu.Lock()
		f.cleanups++
		f.mu.Unlock()
		return nil
	}
	return ws, cleanup, "", nil
}

// erroringForker always fails Fork. It proves the no-silent-fallback contract: a
// shell-bearing child whose isolation fails must yield a tool error, never run
// against the shared parent ws.
type erroringForker struct{}

func (erroringForker) Fork(_ context.Context, _ tool.Workspace, _ string) (tool.Workspace, func() error, string, error) {
	return nil, nil, "", errors.New("worktree add failed")
}

// advisoryForker forks successfully but returns a fixed DEGRADED-fork advisory, so a
// test can prove the advisory reaches the child's prompt (the model-facing channel).
type advisoryForker struct{ advisory string }

func (f advisoryForker) Fork(_ context.Context, _ tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	return memfs.NewWorkspace("/fork/" + label), func() error { return nil }, f.advisory, nil
}

// rootRecordingTool is a read-only child tool that records the Workspace.Root() it
// was executed against, so a test can prove the child ran in the forked worktree (not
// the shared parent base).
type rootRecordingTool struct {
	mu    sync.Mutex
	roots []string
}

func (*rootRecordingTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Probe", Description: "probe: records ws root", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*rootRecordingTool) ReadOnly() bool { return true }
func (rt *rootRecordingTool) Execute(_ context.Context, in session.ToolCall, ws tool.Workspace) (session.ToolResult, error) {
	rt.mu.Lock()
	rt.roots = append(rt.roots, ws.Root())
	rt.mu.Unlock()
	return session.NewToolResult(in.ID, "probed "+ws.Root()), nil
}
func (rt *rootRecordingTool) seenRoots() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]string, len(rt.roots))
	copy(out, rt.roots)
	return out
}

// runSubagentOnce drives a parent engine that calls Subagent once with the given prompt, over
// the given parent workspace, and returns the parent's single Subagent tool.result.
func runSubagentOnce(t *testing.T, task tool.Tool, parentWS tool.Workspace, prompt string) *session.ToolResult {
	t.Helper()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"`+prompt+`"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), parentWS, "go")
	evs := drain(r)
	var got *session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			got = ev.ToolResult
		}
	}
	if got == nil {
		t.Fatalf("parent saw no Subagent tool result; events: %v", typesOf(evs))
	}
	return got
}

// TestSubagentForksBeforeRunning asserts that, with a child forker wired, the Subagent
// child runs in the FORKED workspace (its tools see the fork root, not the parent
// base), the fork is labelled from the goal, and the fork's cleanup runs after the
// child drains.
func TestSubagentForksBeforeRunning(t *testing.T) {
	probe := &rootRecordingTool{}
	childCat := catalogWith(t, probe)
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Probe", `{}`)),
		mockllm.TextTurn("inspected the fork"),
	)
	childEngine := childEngineWith(childLLM, childCat)

	fk := &recordingSubagentForker{}
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(fk))

	got := runSubagentOnce(t, task, memfs.NewWorkspace("/base"), "inspect history")
	if got.IsError {
		t.Fatalf("Subagent result is an error: %q", got.Content)
	}
	if !strings.Contains(got.Content, "inspected the fork") {
		t.Fatalf("Subagent result = %q, want it to contain the child summary", got.Content)
	}

	// The child's tool must have run against the FORK root, never the parent base.
	roots := probe.seenRoots()
	if len(roots) != 1 {
		t.Fatalf("probe ran %d times, want 1: %v", len(roots), roots)
	}
	if got, want := roots[0], "/fork/inspect history"; got != want {
		t.Fatalf("child ran against ws root %q, want the fork %q (not the parent /base)", got, want)
	}

	fk.mu.Lock()
	defer fk.mu.Unlock()
	if len(fk.labels) != 1 || fk.labels[0] != "inspect history" {
		t.Fatalf("fork labels = %v, want [\"inspect history\"] (label from the goal)", fk.labels)
	}
	if fk.cleanups != 1 {
		t.Errorf("fork cleanups = %d, want 1 (cleanup must run after the child drains)", fk.cleanups)
	}
}

// TestSubagentDegradedForkAdvisoryReachesChildPrompt is the model-facing proof for the
// degraded-fork advisory: when the forker returns a non-empty advisory (the dirty
// overlay could not be applied, so the child sees committed HEAD only), that note is
// PREPENDED to the child LLM's first user message — model-visible, not just a log.
// Without it the child would silently report "nothing to review" on a clean-looking
// tree. The child provider's request observer captures the first user message text.
func TestSubagentDegradedForkAdvisoryReachesChildPrompt(t *testing.T) {
	const advisory = "[harness note: the workspace had uncommitted changes, but they could not be overlaid into your isolated checkout — you are seeing the committed HEAD only.]"

	var firstUser string
	var once sync.Once
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			// Capture the FIRST request's last user message (the run prompt).
			once.Do(func() {
				for i := len(req.Messages) - 1; i >= 0; i-- {
					if req.Messages[i].Role == session.RoleUser {
						firstUser = req.Messages[i].Text
						return
					}
				}
			})
		})},
		mockllm.TextTurn("reviewed (saw committed HEAD only)"),
	)
	childEngine := childEngineWith(childLLM, tool.NewCatalog())

	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(advisoryForker{advisory: advisory}))

	got := runSubagentOnce(t, task, memfs.NewWorkspace("/base"), "review my changes")
	if got.IsError {
		t.Fatalf("Subagent result is an error: %q", got.Content)
	}
	if firstUser == "" {
		t.Fatal("child provider observed no user message")
	}
	if !strings.HasPrefix(firstUser, advisory) {
		t.Fatalf("child's first user message did not lead with the degraded-fork advisory.\nadvisory: %q\ngot:      %q", advisory, firstUser)
	}
	// The original prompt must still be present after the advisory.
	if !strings.Contains(firstUser, "review my changes") {
		t.Fatalf("child's prompt lost the original task; got: %q", firstUser)
	}
}

// TestSubagentNoAdvisoryLeavesPromptUnchanged is the negative control: a forker that
// returns an EMPTY advisory (normal fork) leaves the child's prompt as just the task —
// no harness note is prepended.
func TestSubagentNoAdvisoryLeavesPromptUnchanged(t *testing.T) {
	var firstUser string
	var once sync.Once
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			once.Do(func() {
				for i := len(req.Messages) - 1; i >= 0; i-- {
					if req.Messages[i].Role == session.RoleUser {
						firstUser = req.Messages[i].Text
						return
					}
				}
			})
		})},
		mockllm.TextTurn("done"),
	)
	childEngine := childEngineWith(childLLM, tool.NewCatalog())
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(advisoryForker{advisory: ""}))

	got := runSubagentOnce(t, task, memfs.NewWorkspace("/base"), "plain task")
	if got.IsError {
		t.Fatalf("Subagent result is an error: %q", got.Content)
	}
	if firstUser != "plain task" {
		t.Fatalf("child's prompt = %q, want exactly the task %q (no advisory prepended)", firstUser, "plain task")
	}
}

// TestSubagentNilForkerRunsAgainstParent asserts the unchanged legacy behaviour: with
// NO child forker wired, the child runs against the parent workspace (its tools see
// the parent root) — no fork, exactly as before Phase 2.
func TestSubagentNilForkerRunsAgainstParent(t *testing.T) {
	probe := &rootRecordingTool{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Probe", `{}`)),
		mockllm.TextTurn("inspected the base"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, probe))

	task := agent.NewSubagentTool(childEngine) // no WithChildForker

	got := runSubagentOnce(t, task, memfs.NewWorkspace("/base"), "look around")
	if got.IsError {
		t.Fatalf("Subagent result is an error: %q", got.Content)
	}
	roots := probe.seenRoots()
	if len(roots) != 1 || roots[0] != "/base" {
		t.Fatalf("child ran against roots %v, want [\"/base\"] (the shared parent, no fork)", roots)
	}
}

// TestSubagentForkErrorIsToolError asserts the no-silent-fallback contract: when a
// child forker is wired but Fork fails, Subagent returns a tool ERROR and the child NEVER
// runs (so its Bash cannot land in the shared parent base).
func TestSubagentForkErrorIsToolError(t *testing.T) {
	probe := &rootRecordingTool{}
	// Script a child that WOULD run a probe if it ever started — it must not.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Probe", `{}`)),
		mockllm.TextTurn("should never run"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, probe))

	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(erroringForker{}))

	got := runSubagentOnce(t, task, memfs.NewWorkspace("/base"), "inspect")
	if !got.IsError {
		t.Fatalf("Subagent result = %+v, want an error result (fork failure must not silently fall back)", got)
	}
	if !strings.Contains(got.Content, "workspace isolation failed") {
		t.Fatalf("Subagent error = %q, want it to mention workspace isolation failure", got.Content)
	}
	if roots := probe.seenRoots(); len(roots) != 0 {
		t.Fatalf("child ran (roots=%v) despite the fork failure; it must NOT run against the parent base", roots)
	}
}

// concurrencyForker counts how many forks are simultaneously live (between Fork and
// cleanup) and records the peak, so a test can assert the shell-concurrency gate caps
// concurrent worktrees.
type concurrencyForker struct {
	mu      sync.Mutex
	live    int
	peak    int
	gate    chan struct{} // closed by the test to release blocked children
	entered chan struct{} // signals each Fork has incremented live
}

func (f *concurrencyForker) Fork(_ context.Context, _ tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	f.mu.Lock()
	f.live++
	if f.live > f.peak {
		f.peak = f.live
	}
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	// Block inside the held slot until the test releases, maximising overlap.
	if f.gate != nil {
		<-f.gate
	}
	ws := memfs.NewWorkspace("/fork/" + label)
	cleanup := func() error {
		f.mu.Lock()
		f.live--
		f.mu.Unlock()
		return nil
	}
	return ws, cleanup, "", nil
}

// TestSubagentShellGateCapsConcurrentForks asserts WithMaxConcurrentChildren bounds
// how many shell-bearing Subagent children hold a forked worktree at once: with a cap of
// N and more than N concurrent Subagent calls, no more than N forks are ever live.
func TestSubagentShellGateCapsConcurrentForks(t *testing.T) {
	const maxShells = 2
	const fanout = 6

	fk := &concurrencyForker{
		gate:    make(chan struct{}),
		entered: make(chan struct{}, fanout),
	}
	// Each child does nothing but summarize; the contention is purely in Fork.
	newChild := func() *agent.Engine {
		return childEngineWith(mockllm.New(mockllm.TextTurn("done")), catalogWith(t))
	}
	// One Subagent tool, shared across concurrent calls (the gate is per-tool).
	task := agent.NewSubagentTool(newChild(),
		agent.WithChildForker(fk),
		agent.WithMaxConcurrentChildren(maxShells),
	)

	var wg sync.WaitGroup
	for i := 0; i < fanout; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = task.Execute(context.Background(),
				session.NewToolCall(session.ToolCallID("c"+string(rune('0'+n))), "Subagent",
					json.RawMessage(`{"prompt":"go"}`)),
				memfs.NewWorkspace("/base"))
		}(i)
	}

	// Wait until exactly `maxShells` children have entered Fork; the rest must be blocked on
	// the gate. Drain `maxShells` entered signals, then assert no more than maxShells are live.
	for i := 0; i < maxShells; i++ {
		<-fk.entered
	}
	// Give any (incorrectly) unbounded children a chance to enter, then check the peak.
	// We can't deterministically wait for "nothing else happens", so we assert the gate
	// holds while still blocked: live must be <= maxShells right now.
	fk.mu.Lock()
	live := fk.live
	fk.mu.Unlock()
	if live > maxShells {
		close(fk.gate)
		wg.Wait()
		t.Fatalf("%d forks live at once, want <= maxShells=%d (shell gate must bound concurrency)", live, maxShells)
	}

	// Release the held slots in waves and let everything finish.
	close(fk.gate)
	wg.Wait()

	if fk.peak > maxShells {
		t.Fatalf("peak concurrent forks = %d, want <= maxShells=%d", fk.peak, maxShells)
	}
	if fk.live != 0 {
		t.Errorf("forks still live after completion: %d (cleanup must release every slot)", fk.live)
	}
}

// forkLabels returns the labels Fork was called with (concurrency-safe).
func (f *recordingSubagentForker) forkLabels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.labels))
	copy(out, f.labels)
	return out
}

// blockingForker holds its one slot: the FIRST Fork blocks (signalling it has
// entered) until released, so a test can keep the shell gate FULL while it fires a
// second, pre-cancelled call. It records whether Fork was called more than once.
type blockingForker struct {
	entered chan struct{} // signalled when the first Fork is in-flight
	release chan struct{} // closed by the test to let the first Fork return
	mu      sync.Mutex
	calls   int
}

func (f *blockingForker) Fork(_ context.Context, _ tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n == 1 {
		close(f.entered)
		<-f.release // hold the single gate slot until the test releases it
	}
	return memfs.NewWorkspace("/fork/" + label), func() error { return nil }, "", nil
}

func (f *blockingForker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestSubagentCtxCancelledBeforeIsolation asserts the abort-don't-fork contract: when
// the shell gate is FULL and the call's ctx is cancelled, acquireShellSlot returns nil,
// Execute surfaces the "cancelled before workspace isolation" tool error, and crucially
// Fork is NEVER called for that call and its child NEVER runs (so no worktree is created
// and no Bash lands in the shared base). A blocking forker holds the single gate slot so
// the second (cancelled) call can ONLY take the ctx.Done() branch of the select —
// deterministic, not a racy "both cases ready" pick.
func TestSubagentCtxCancelledBeforeIsolation(t *testing.T) {
	probe := &rootRecordingTool{}
	// The child WOULD run a probe if it ever started — it must not, for the cancelled call.
	childLLM := mockllm.New(
		mockllm.TextTurn("first child done"),
		mockllm.ToolCallTurn(toolCall("k1", "Probe", `{}`)),
		mockllm.TextTurn("should never run"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, probe))

	fk := &blockingForker{entered: make(chan struct{}), release: make(chan struct{})}
	// Gate capacity 1: the first (blocking) Fork fills it, so the second call's
	// acquireShellSlot finds the send blocked and its cancelled ctx is the only ready case.
	task := agent.NewSubagentTool(childEngine,
		agent.WithChildForker(fk),
		agent.WithMaxConcurrentChildren(1),
	)

	// First call: acquires the only slot and blocks inside Fork (holding the gate).
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = task.Execute(context.Background(),
			session.NewToolCall("c0", "Subagent", json.RawMessage(`{"prompt":"hold the slot"}`)),
			memfs.NewWorkspace("/base"))
	}()
	<-fk.entered // the gate is now full and held

	// Second call: pre-cancelled ctx, gate full ⇒ acquireShellSlot must abort (nil).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := task.Execute(ctx,
		session.NewToolCall("c1", "Subagent", json.RawMessage(`{"prompt":"inspect"}`)),
		memfs.NewWorkspace("/base"))
	if err != nil {
		t.Fatalf("Subagent.Execute returned a transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("Subagent result = %+v, want an error result (ctx cancelled before isolation)", res)
	}
	if !strings.Contains(res.Content, "cancelled before acquiring a concurrency slot") {
		t.Fatalf("Subagent error = %q, want it to mention cancellation before the slot acquire", res.Content)
	}
	// Only the FIRST call ever forked; the cancelled call must NOT have forked.
	if got := fk.callCount(); got != 1 {
		t.Fatalf("Fork called %d times, want exactly 1 (the cancelled call must NOT fork)", got)
	}
	// The cancelled call's child must NEVER run: the probe saw no workspace.
	if roots := probe.seenRoots(); len(roots) != 0 {
		t.Fatalf("a child ran (roots=%v) despite the pre-isolation cancellation; the cancelled call must NOT run", roots)
	}

	// Release the first call so the test (and its goroutine) can finish cleanly.
	close(fk.release)
	<-firstDone
}

// TestSubagentMaxConcurrentChildrenZeroClamps asserts that
// WithMaxConcurrentChildren(0) clamps the gate to capacity 1 rather than building a
// zero-capacity channel (which would DEADLOCK the very first acquire). A single forking
// Subagent call must complete; a short-ctx deadlock backstop fails the test if it ever
// blocks forever.
func TestSubagentMaxConcurrentChildrenZeroClamps(t *testing.T) {
	probe := &rootRecordingTool{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, probe))

	fk := &recordingSubagentForker{}
	task := agent.NewSubagentTool(childEngine,
		agent.WithChildForker(fk),
		agent.WithMaxConcurrentChildren(0), // clamps to 1; a zero-cap gate would deadlock
	)

	// Short ctx as a deadlock backstop: a zero-cap (unclamped) gate would block the
	// first acquire forever and trip this deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := task.Execute(ctx,
		session.NewToolCall("c1", "Subagent", json.RawMessage(`{"prompt":"go"}`)),
		memfs.NewWorkspace("/base"))
	if err != nil {
		t.Fatalf("Subagent.Execute returned a transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Subagent result is an error: %q (a clamped gate must let a single call complete)", res.Content)
	}
	// The single call must have acquired its slot, forked, run, and cleaned up.
	if labels := fk.forkLabels(); len(labels) != 1 {
		t.Fatalf("Fork called %d times, want exactly 1 (the single Subagent call must proceed): %v", len(labels), labels)
	}
	if fk.cleanups != 1 {
		t.Errorf("fork cleanups = %d, want 1 (the slot must be released after the call)", fk.cleanups)
	}
}

// barrierChildTool is a read-only child tool that signals each Execute entry on
// `entered` and blocks until `release` is closed, so a test can hold N children
// in-flight at once and observe the peak concurrency. It is the FORKER-LESS analogue
// of concurrencyForker's contention point.
type barrierChildTool struct {
	mu      sync.Mutex
	live    int
	peak    int
	entered chan struct{}
	release chan struct{}
}

func (*barrierChildTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Barrier", Description: "barrier", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*barrierChildTool) ReadOnly() bool { return true }
func (b *barrierChildTool) Execute(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	b.mu.Lock()
	b.live++
	if b.live > b.peak {
		b.peak = b.live
	}
	b.mu.Unlock()
	b.entered <- struct{}{}
	<-b.release
	b.mu.Lock()
	b.live--
	b.mu.Unlock()
	return session.NewToolResult(in.ID, "done"), nil
}
func (b *barrierChildTool) peakLive() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peak
}

// TestSubagentChildGateCapsForkerlessConcurrency closes the genuinely-unbounded path
// the plan identified: a FORKER-LESS Subagent fan-out. With a cap of N and more than N
// concurrent Subagent calls (no forker wired), at most N children ever run at once. Each
// child blocks inside a barrier tool so the test can hold the in-flight set and read
// the peak. Pre-fix the gate was acquired only on the forking path, so a forker-less
// fan-out was unbounded and this would observe peak == fanout.
func TestSubagentChildGateCapsForkerlessConcurrency(t *testing.T) {
	const maxChildren = 2
	const fanout = 6

	barrier := &barrierChildTool{entered: make(chan struct{}, fanout), release: make(chan struct{})}
	// Each child calls Barrier (blocks) then summarises. The child engine is SHARED
	// across calls; the per-tool childGate is what bounds concurrency, not the engine.
	childCat := catalogWith(t, barrier)
	newChildLLM := func() *mockllm.Provider {
		return mockllm.New(
			mockllm.ToolCallTurn(toolCall("b1", "Barrier", `{}`)),
			mockllm.TextTurn("child done"),
		)
	}
	// One Subagent tool, NO forker (the forker-less path), shared across concurrent calls.
	// A distinct child engine per call so the shared mockllm cursor can't interleave.
	engines := map[string]*agent.Engine{}
	var meta []agent.AgentMeta
	for i := 0; i < fanout; i++ {
		name := "a" + string(rune('0'+i))
		engines[name] = childEngineWith(newChildLLM(), childCat)
		meta = append(meta, agent.AgentMeta{Name: name, Description: "d"})
	}
	task := agent.NewSubagentTool(
		childEngineWith(newChildLLM(), childCat),
		agent.WithAgentEngines(engines, meta),
		agent.WithMaxConcurrentChildren(maxChildren),
	)

	var wg sync.WaitGroup
	for i := 0; i < fanout; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := "a" + string(rune('0'+n))
			_, _ = task.Execute(context.Background(),
				session.NewToolCall(session.ToolCallID("c"+string(rune('0'+n))), "Subagent",
					json.RawMessage(`{"prompt":"go","agent":"`+name+`"}`)),
				memfs.NewWorkspace("/base"))
		}(i)
	}

	// Wait until exactly `cap` children have entered the barrier; the rest must be
	// blocked on the childGate (not on the barrier).
	for i := 0; i < maxChildren; i++ {
		<-barrier.entered
	}
	if got := barrier.peakLive(); got > maxChildren {
		close(barrier.release)
		wg.Wait()
		t.Fatalf("%d children live at once, want <= maxChildren=%d (the child gate must bound a forker-less fan-out)", got, maxChildren)
	}

	// Release everything; the remaining children pass the (now-open) barrier as the
	// gate admits them. `entered` is buffered to fanout, so no Execute blocks on it.
	close(barrier.release)
	wg.Wait()

	if peak := barrier.peakLive(); peak > maxChildren {
		t.Fatalf("peak concurrent forker-less children = %d, want <= maxChildren=%d", peak, maxChildren)
	}
}
