package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeIndexSrc is a scripted prompt.MemoryIndexSource: the turn-0 ordering tests
// only need an index with one recognisable entry, not a real memory store (the
// file-backed adapter is integration-tested in internal/adapter/memory).
type fakeIndexSrc struct{ entries []tool.MemoryEntry }

func (f fakeIndexSrc) Index(context.Context) ([]tool.MemoryEntry, error) {
	return f.entries, nil
}

// recordingHooks is a fake port.HookRunner that records every phase it is asked
// to run, in order, and blocks on a configured set of phases.
type recordingHooks struct {
	mu     sync.Mutex
	phases []governance.HookPhase
	block  map[governance.HookPhase]string // phase → block message
}

func newRecordingHooks(block map[governance.HookPhase]string) *recordingHooks {
	return &recordingHooks{block: block}
}

func (h *recordingHooks) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	h.mu.Lock()
	h.phases = append(h.phases, ev.Phase)
	h.mu.Unlock()
	if msg, ok := h.block[ev.Phase]; ok {
		return governance.HookOutcome{Block: true, Message: msg}, nil
	}
	return governance.HookOutcome{}, nil
}

func (h *recordingHooks) recorded() []governance.HookPhase {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]governance.HookPhase, len(h.phases))
	copy(out, h.phases)
	return out
}

func (h *recordingHooks) count(p governance.HookPhase) int {
	n := 0
	for _, ph := range h.recorded() {
		if ph == p {
			n++
		}
	}
	return n
}

func runWithHooks(t *testing.T, llm *mockllm.Provider, hooks *recordingHooks) []session.Event {
	t.Helper()
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: hooks})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")
	return drain(e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "hi"}))
}

func okExec(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, "ok"), nil
}

// TestSessionStartFiresOnceBeforeTurns asserts SessionStart fires exactly once,
// before the first UserPromptSubmit and before any model call.
func TestSessionStartFiresOnceBeforeTurns(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	hooks := newRecordingHooks(nil)
	runWithHooks(t, llm, hooks)

	if got := hooks.count(governance.PhaseSessionStart); got != 1 {
		t.Fatalf("SessionStart fired %d times, want 1", got)
	}
	rec := hooks.recorded()
	if len(rec) == 0 || rec[0] != governance.PhaseSessionStart {
		t.Fatalf("first phase = %v, want SessionStart; all=%v", rec, rec)
	}
	// SessionStart must precede UserPromptSubmit.
	idxStart, idxPrompt := -1, -1
	for i, p := range rec {
		if p == governance.PhaseSessionStart && idxStart < 0 {
			idxStart = i
		}
		if p == governance.PhaseUserPromptSubmit && idxPrompt < 0 {
			idxPrompt = i
		}
	}
	if idxStart > idxPrompt {
		t.Fatalf("SessionStart(%d) did not precede UserPromptSubmit(%d)", idxStart, idxPrompt)
	}
}

// TestUserPromptSubmitFiresBeforeModel asserts UserPromptSubmit fires before the
// first model call on a normal (non-blocking) run.
func TestUserPromptSubmitFiresBeforeModel(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	hooks := newRecordingHooks(nil)
	runWithHooks(t, llm, hooks)

	if got := hooks.count(governance.PhaseUserPromptSubmit); got != 1 {
		t.Fatalf("UserPromptSubmit fired %d times, want 1", got)
	}
	if llm.Calls() != 1 {
		t.Fatalf("model called %d times, want 1", llm.Calls())
	}
}

// TestUserPromptSubmitBlockAbortsBeforeLLM asserts a Block on UserPromptSubmit
// rejects the prompt and the model is NEVER called.
func TestUserPromptSubmitBlockAbortsBeforeLLM(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("should never run"))
	hooks := newRecordingHooks(map[governance.HookPhase]string{
		governance.PhaseUserPromptSubmit: "prompt rejected: policy violation",
	})
	evs := runWithHooks(t, llm, hooks)

	if llm.Calls() != 0 {
		t.Fatalf("model was called %d times; want 0 (prompt should be rejected pre-call)", llm.Calls())
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want error (rejected prompt)", res.Stop)
	}
	if res.Error == "" {
		t.Fatalf("expected a rejection error message")
	}
	if !containsType(evs, session.EvHook) {
		t.Fatalf("expected a hook event explaining the rejection")
	}
	// Stop still fires on the rejection (terminal) path.
	if got := hooks.count(governance.PhaseStop); got != 1 {
		t.Fatalf("Stop fired %d times on rejection, want 1", got)
	}
}

// mutatingHooks is a port.HookRunner that returns a Mutated payload for a
// configured phase (otherwise allows). It records phases like recordingHooks.
type mutatingHooks struct {
	mu     sync.Mutex
	phases []governance.HookPhase
	mutate map[governance.HookPhase]json.RawMessage
}

func (h *mutatingHooks) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	h.mu.Lock()
	h.phases = append(h.phases, ev.Phase)
	h.mu.Unlock()
	if m, ok := h.mutate[ev.Phase]; ok {
		return governance.HookOutcome{Mutated: m}, nil
	}
	return governance.HookOutcome{}, nil
}

// capturingHookRunner is a port.HookRunner that records the last HookEvent.Input
// seen for a configured phase (never blocks/mutates) — used to inspect exactly
// what content a hook was shown, as opposed to the effective/recorded result.
type capturingHookRunner struct {
	mu    sync.Mutex
	phase governance.HookPhase
	last  json.RawMessage
}

func (h *capturingHookRunner) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == h.phase {
		h.mu.Lock()
		h.last = ev.Input
		h.mu.Unlock()
	}
	return governance.HookOutcome{}, nil
}

func (h *capturingHookRunner) input() json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

// TestUserPromptSubmitMutationIsApplied asserts a UserPromptSubmit hook that
// returns a mutated {"prompt": ...} payload rewrites the EFFECTIVE prompt: the
// model receives the mutated text and the recorded conversation reflects it.
// It reuses capturingProvider (disclosure_test.go), which records the first
// LLMRequest and ends the turn.
func TestUserPromptSubmitMutationIsApplied(t *testing.T) {
	prov := &capturingProvider{}
	mutated, _ := json.Marshal(struct {
		Prompt string `json:"prompt"`
	}{Prompt: "MUTATED PROMPT"})
	hooks := &mutatingHooks{mutate: map[governance.HookPhase]json.RawMessage{
		governance.PhaseUserPromptSubmit: mutated,
	}}
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: prov, Catalog: cat, Hooks: hooks})
	sess := newSession(t, session.Limits{})
	drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "original prompt"}))

	// The model must have received the mutated text, never the original.
	var userTexts []string
	for _, m := range prov.req.Messages {
		if m.Role == session.RoleUser {
			userTexts = append(userTexts, m.Text)
		}
	}
	foundMutated, foundOriginal := false, false
	for _, txt := range userTexts {
		if strings.Contains(txt, "MUTATED PROMPT") {
			foundMutated = true
		}
		if strings.Contains(txt, "original prompt") {
			foundOriginal = true
		}
	}
	if !foundMutated {
		t.Fatalf("model did not receive the mutated prompt; user texts = %v", userTexts)
	}
	if foundOriginal {
		t.Fatalf("model received the ORIGINAL prompt; mutation was not applied: %v", userTexts)
	}

	// The recorded conversation must reflect the mutated text, not the original.
	recMutated, recOriginal := false, false
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "MUTATED PROMPT") {
			recMutated = true
		}
		if m.Role == session.RoleUser && strings.Contains(m.Text, "original prompt") {
			recOriginal = true
		}
	}
	if !recMutated {
		t.Fatalf("recorded conversation does not contain the mutated prompt")
	}
	if recOriginal {
		t.Fatalf("recorded conversation still contains the original prompt")
	}
}

// TestUserPromptSubmitBlockAbortsBeforeRecordingAndLLM is a regression that a
// Block on UserPromptSubmit ends the run before any model call AND before the
// prompt is recorded (the prompt now fires pre-record).
func TestUserPromptSubmitBlockAbortsBeforeRecordingAndLLM(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("should never run"))
	hooks := newRecordingHooks(map[governance.HookPhase]string{
		governance.PhaseUserPromptSubmit: "prompt rejected: policy violation",
	})
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: hooks})
	sess := newSession(t, session.Limits{})
	evs := drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "blocked prompt"}))

	if llm.Calls() != 0 {
		t.Fatalf("model was called %d times; want 0", llm.Calls())
	}
	if res := lastResult(t, evs); res.Stop != session.StopError {
		t.Fatalf("stop = %q, want error", res.Stop)
	}
	if !containsType(evs, session.EvHook) {
		t.Fatalf("expected a hook event explaining the rejection")
	}
	// Nothing should have been recorded into the conversation as a user prompt.
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "blocked prompt") {
			t.Fatalf("blocked prompt was recorded into the conversation")
		}
	}
}

// TestSessionStartBlockAbortsRun asserts a SessionStart Block now ABORTS the run
// with StopError, emits a hook event, and NEVER calls the model.
func TestSessionStartBlockAbortsRun(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("should never run"))
	hooks := newRecordingHooks(map[governance.HookPhase]string{
		governance.PhaseSessionStart: "session rejected: disallowed environment",
	})
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: hooks})
	sess := newSession(t, session.Limits{})
	evs := drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "hi"}))

	if llm.Calls() != 0 {
		t.Fatalf("model was called %d times; want 0 (SessionStart should abort)", llm.Calls())
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want error", res.Stop)
	}
	if res.Error == "" {
		t.Fatalf("expected a rejection error message")
	}
	if !containsType(evs, session.EvHook) {
		t.Fatalf("expected a hook event explaining the SessionStart rejection")
	}
	// UserPromptSubmit must NEVER fire when SessionStart aborts first.
	if got := hooks.count(governance.PhaseUserPromptSubmit); got != 0 {
		t.Fatalf("UserPromptSubmit fired %d times after SessionStart abort, want 0", got)
	}
	// Stop still fires on the terminal path.
	if got := hooks.count(governance.PhaseStop); got != 1 {
		t.Fatalf("Stop fired %d times on SessionStart abort, want 1", got)
	}
}

// TestSessionStartNoBlockProceeds is a regression that a non-blocking
// SessionStart lets the run proceed normally (model is called, run completes).
func TestSessionStartNoBlockProceeds(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	hooks := newRecordingHooks(nil) // no block on any phase
	evs := runWithHooks(t, llm, hooks)

	if got := hooks.count(governance.PhaseSessionStart); got != 1 {
		t.Fatalf("SessionStart fired %d times, want 1", got)
	}
	if llm.Calls() != 1 {
		t.Fatalf("model called %d times, want 1", llm.Calls())
	}
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn", res.Stop)
	}
}

// TestStopFiresOnceOnNormalCompletion asserts Stop fires exactly once at terminal
// end of a successful run.
func TestStopFiresOnceOnNormalCompletion(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	hooks := newRecordingHooks(nil)
	runWithHooks(t, llm, hooks)

	if got := hooks.count(governance.PhaseStop); got != 1 {
		t.Fatalf("Stop fired %d times, want 1", got)
	}
	rec := hooks.recorded()
	if rec[len(rec)-1] != governance.PhaseStop {
		t.Fatalf("last phase = %v, want Stop", rec[len(rec)-1])
	}
}

// TestStopFiresOnLimitTermination asserts Stop fires once when a stop-condition
// (MaxTurns) terminates the run.
func TestStopFiresOnLimitTermination(t *testing.T) {
	// One scripted turn that calls a tool, with MaxTurns=1 so the loop stops.
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a"}`)))
	hooks := newRecordingHooks(nil)
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: hooks})
	sess := newSession(t, session.Limits{MaxTurns: 1})
	drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "hi"}))

	if got := hooks.count(governance.PhaseStop); got != 1 {
		t.Fatalf("Stop fired %d times on limit termination, want 1", got)
	}
}

// TestNilHooksAreNoOp asserts a run with no HookRunner configured (the default)
// works unchanged and never panics.
func TestNilHooksAreNoOp(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat}) // Hooks left nil
	sess := newSession(t, session.Limits{})
	evs := drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "hi"}))
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn", res.Stop)
	}
}

// --- P3: InstructionAssembler seam ------------------------------------------

// fakeAssembler records that it was invoked and returns a fixed message.
type fakeAssembler struct {
	called int
	msg    string
}

func (a *fakeAssembler) Assemble(_ context.Context, _ tool.Workspace) ([]session.Message, error) {
	a.called++
	return []session.Message{session.NewUserMessage(a.msg)}, nil
}

// TestLoopUsesInjectedAssembler asserts the loop calls the injected
// InstructionAssembler instead of the default and prepends its output to the
// request (ephemerally — fragments are not persisted; ADR 0043).
func TestLoopUsesInjectedAssembler(t *testing.T) {
	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("done"))
	asm := &fakeAssembler{msg: "INJECTED INSTRUCTIONS"}
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Instructions: asm})
	sess := newSession(t, session.Limits{})
	drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "hi"}))

	if asm.called != 1 {
		t.Fatalf("assembler invoked %d times, want 1", asm.called)
	}
	req, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}
	// The injected instruction message must be in the REQUEST, never the conversation.
	found := false
	for _, m := range req.Messages {
		if m.Text == "INJECTED INSTRUCTIONS" {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected instruction message not prepended to the request")
	}
	for _, m := range sess.Conversation.Messages {
		if m.Text == "INJECTED INSTRUCTIONS" {
			t.Fatalf("injected instruction message must NOT be persisted into the conversation (it is ephemeral)")
		}
	}
}

// TestTurn0InjectsMemoryIndexAfterAgentsMD is the D2 end-to-end proof: with a
// non-empty per-project memory store and the composed MultiAssembler
// (RootAssembler + MemoryIndexAssembler), the turn-0 REQUEST contains the memory
// index as a USER message, ordered AFTER the AGENTS.md instruction message and
// before the user prompt. As of ADR 0043 the fragments are EPHEMERAL — prepended to
// the LLMRequest per-run, NEVER persisted into the conversation — so the ordering is
// observed on the request the provider received, not on sess.Conversation.Messages.
func TestTurn0InjectsMemoryIndexAfterAgentsMD(t *testing.T) {
	ctx := context.Background()
	store := fakeIndexSrc{entries: []tool.MemoryEntry{{
		Key: "pref/test-runner", Value: "gotestsum", Description: "preferred test runner",
	}}}

	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("done"))
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	asm := prompt.NewMultiAssembler(prompt.RootAssembler{}, prompt.MemoryIndexAssembler{Src: store})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Instructions: asm})

	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(ctx, "AGENTS.md", []byte("project rule")); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	sess := newSession(t, session.Limits{})
	drain(e.Run(ctx, sess, ws, agent.RunRequest{Text: "the user prompt"}))

	req, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}

	// Find the order of the three turn-0 user messages in the REQUEST.
	var agentsIdx, memoryIdx, promptIdx = -1, -1, -1
	for i, m := range req.Messages {
		if m.Role != session.RoleUser {
			continue
		}
		switch {
		case strings.Contains(m.Text, "project rule"):
			agentsIdx = i
		case strings.Contains(m.Text, "pref/test-runner") && strings.Contains(m.Text, "preferred test runner"):
			memoryIdx = i
		case strings.Contains(m.Text, "the user prompt"):
			promptIdx = i
		}
	}
	if agentsIdx < 0 || memoryIdx < 0 || promptIdx < 0 {
		t.Fatalf("missing a turn-0 message: agents=%d memory=%d prompt=%d", agentsIdx, memoryIdx, promptIdx)
	}
	if agentsIdx >= memoryIdx || memoryIdx >= promptIdx {
		t.Fatalf("turn-0 order wrong: agents=%d memory=%d prompt=%d (want agents < memory < prompt)", agentsIdx, memoryIdx, promptIdx)
	}
	// The fragments are ephemeral: none persist into the conversation.
	if n := countInjected(sess); n != 0 {
		t.Fatalf("%d injected fragments PERSISTED in history, want 0 (fragments are ephemeral)", n)
	}
}

// fakeSoulSrc is a scripted prompt.SoulSource for the e2e ordering test.
type fakeSoulSrc struct{ body string }

func (f fakeSoulSrc) Load(context.Context) (string, error) { return f.body, nil }

// TestTurn0InjectsSoulAfterAgentsMD is the issue #14 end-to-end proof: with a
// composed MultiAssembler (RootAssembler + SoulAssembler + MemoryIndexAssembler),
// the turn-0 REQUEST contains the persona/soul as a USER message, ordered AFTER the
// AGENTS.md instruction message, BEFORE the memory index (identity before saved
// facts), and all before the user prompt. As of ADR 0043 the fragments are
// EPHEMERAL — observed on the request the provider received, not on the persisted
// conversation.
func TestTurn0InjectsSoulAfterAgentsMD(t *testing.T) {
	ctx := context.Background()
	store := fakeIndexSrc{entries: []tool.MemoryEntry{{
		Key: "pref/test-runner", Value: "gotestsum", Description: "preferred test runner",
	}}}

	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("done"))
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	asm := prompt.NewMultiAssembler(
		prompt.RootAssembler{},
		prompt.SoulAssembler{Src: fakeSoulSrc{body: "PERSONA-MARKER terse engineer"}},
		prompt.MemoryIndexAssembler{Src: store},
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Instructions: asm})

	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(ctx, "AGENTS.md", []byte("project rule")); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	sess := newSession(t, session.Limits{})
	drain(e.Run(ctx, sess, ws, agent.RunRequest{Text: "the user prompt"}))

	req, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}

	// Find the order of the four turn-0 user messages in the REQUEST.
	var agentsIdx, soulIdx, memoryIdx, promptIdx = -1, -1, -1, -1
	for i, m := range req.Messages {
		if m.Role != session.RoleUser {
			continue
		}
		switch {
		case strings.Contains(m.Text, "project rule"):
			agentsIdx = i
		case strings.Contains(m.Text, "PERSONA-MARKER terse engineer"):
			soulIdx = i
		case strings.Contains(m.Text, "pref/test-runner") && strings.Contains(m.Text, "preferred test runner"):
			memoryIdx = i
		case strings.Contains(m.Text, "the user prompt"):
			promptIdx = i
		}
	}
	if agentsIdx < 0 || soulIdx < 0 || memoryIdx < 0 || promptIdx < 0 {
		t.Fatalf("missing a turn-0 message: agents=%d soul=%d memory=%d prompt=%d", agentsIdx, soulIdx, memoryIdx, promptIdx)
	}
	if agentsIdx >= soulIdx || soulIdx >= memoryIdx || memoryIdx >= promptIdx {
		t.Fatalf("turn-0 order wrong: agents=%d soul=%d memory=%d prompt=%d (want agents < soul < memory < prompt)", agentsIdx, soulIdx, memoryIdx, promptIdx)
	}
	// The fragments are ephemeral: none persist into the conversation.
	if n := countInjected(sess); n != 0 {
		t.Fatalf("%d injected fragments PERSISTED in history, want 0 (fragments are ephemeral)", n)
	}
}

// TestDefaultAssemblerWhenNil asserts NewEngine defaults the assembler so a run
// with no Instructions field set still discovers root instructions and prepends
// them to the request (ephemerally — they are not persisted; ADR 0043).
func TestDefaultAssemblerWhenNil(t *testing.T) {
	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("done"))
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat}) // Instructions nil → RootAssembler
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(context.Background(), "AGENTS.md", []byte("project rule")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	drain(e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "hi"}))

	req, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}
	found := false
	for _, m := range req.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "project rule") {
			found = true
		}
	}
	if !found {
		t.Fatalf("default RootAssembler did not discover AGENTS.md (it must be prepended to the request)")
	}
}

// ensure prompt import is used (RootAssembler default wiring is exercised
// indirectly; reference it to document the default explicitly).
var _ prompt.InstructionAssembler = prompt.RootAssembler{}
