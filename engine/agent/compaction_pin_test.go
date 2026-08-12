package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// renderTurn0Fragments returns the REAL turn-0 context fragments (soul + memory
// index) the InstructionAssembler chain injects, rendered via the live assemblers
// — so the compaction pin is exercised against the same bytes a real session
// carries, not hand-rolled stand-ins. They are RoleUser messages and, by the bug,
// precede the user's genuine first instruction.
func renderTurn0Fragments(t *testing.T) []session.Message {
	t.Helper()
	asm := prompt.NewMultiAssembler(
		prompt.SoulAssembler{Src: fakeSoulSrc{body: "PERSONA terse engineer"}},
		prompt.MemoryIndexAssembler{Src: fakeIndexSrc{entries: []tool.MemoryEntry{{
			Key: "pref/runner", Value: "gotestsum", Description: "preferred test runner",
		}}}},
	)
	msgs, err := asm.Assemble(context.Background(), memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("assemble turn-0 fragments: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 injected fragments (soul + memory), got %d", len(msgs))
	}
	// Sanity: both are RoleUser AND recognised as injected — so the only thing
	// keeping the pin off them is the genuine-user skip under test.
	for _, m := range msgs {
		if m.Role != session.RoleUser || !prompt.IsInjectedTurn0Fragment(m.Text) {
			t.Fatalf("fragment not a recognised injected RoleUser message: role=%s\n%s", m.Role, m.Text)
		}
	}
	return msgs
}

// injectedPrefixTaskConversation builds a tool-heavy conversation whose history
// OPENS with the system prompt + the real injected turn-0 fragments (soul, memory
// index), THEN the user's genuine first instruction (the GOAL), then settled
// assistant/tool work, then SEVERAL later genuine user turns. This isolates the
// PIN as the only thing that can save the goal:
//
//   - the goal is outside the count-tail (keep=6 trailing messages are all the
//     later turns + tool work);
//   - the goal sits BELOW the back-snap floor (userSnapFloor = one past the FIRST
//     genuine user) and the later genuine turns fill the recent-user back-snap
//     window (recentUserTurnsKept=3), so the back-snap anchors on a LATER turn,
//     never reaching back to the goal;
//   - therefore only firstUser/preservedHead pinning the GENUINE first user keeps
//     the goal. Under the OLD code the pin anchored on the injected soul fragment
//     (the first RoleUser) and the goal was summarised away — the bug.
func injectedPrefixTaskConversation(t *testing.T) *session.Conversation {
	t.Helper()
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	for _, frag := range renderTurn0Fragments(t) {
		conv.Append(frag)
	}
	// The GENUINE goal — the first real user instruction, after the injected prefix.
	conv.Append(session.NewUserMessage("ACTUAL TASK: rename Foo to Bar"))
	// Settled assistant/tool work after the goal.
	for i := 0; i < 6; i++ {
		id := session.ToolCallID("t" + string(rune('a'+i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"g.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, strings.Repeat("X", 2000))))
	}
	// Several LATER genuine user turns: they fill the recent-user back-snap window
	// (well past recentUserTurnsKept) so the back-snap anchors here, NOT on the goal.
	for i := 0; i < 6; i++ {
		conv.Append(session.NewUserMessage("follow-up chatter about progress"))
		conv.Append(session.NewAssistantMessage("ack", "", nil))
	}
	return conv
}

// assertGenuineGoalPinnedNotFragment checks the GENUINE user instruction survives
// verbatim as a RoleUser message AND that no injected fragment was mistaken for the
// goal in a way that drops the real task. (Injected fragments MAY be absent — they
// are correctly summarised/dropped as harness context — the contract is only that
// the genuine goal survives and pairing holds.)
func assertGenuineGoalPinnedNotFragment(t *testing.T, compacted []session.Message) {
	t.Helper()
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("compacted history is not tool-pairing valid: %v", err)
	}
	var sawGoal bool
	for _, m := range compacted {
		if m.Role == session.RoleUser && m.Text == "ACTUAL TASK: rename Foo to Bar" {
			sawGoal = true
		}
	}
	if !sawGoal {
		t.Fatalf("the GENUINE user goal did not survive compaction (the pin anchored on an injected fragment and the goal was summarised away):\n%+v", compacted)
	}
}

// TestHeuristicCompactorPinsGenuineGoalPastInjectedFragments is the pin-fix repro
// for the heuristic compactor. Reverting firstUser/userSnapFloor to anchor on the
// first RoleUser message (the injected soul fragment) makes sawGoal false → red.
func TestHeuristicCompactorPinsGenuineGoalPastInjectedFragments(t *testing.T) {
	conv := injectedPrefixTaskConversation(t)
	compacted, _, err := agent.HeuristicCompactor{}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	assertGenuineGoalPinnedNotFragment(t, compacted)
}

// TestCascadeCompactorPinsGenuineGoalPastInjectedFragments is the cascade parity
// (offline: BudgetTokens 0 runs every deterministic tier once, no LLM). The
// preservedHead pin must skip the injected fragments and anchor on the genuine
// goal; reverting preservedHead's isGenuineUserTurn skip drops the goal → red.
func TestCascadeCompactorPinsGenuineGoalPastInjectedFragments(t *testing.T) {
	conv := injectedPrefixTaskConversation(t)
	compacted, _, err := agent.CascadeCompactor{}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	assertGenuineGoalPinnedNotFragment(t, compacted)
}

// TestTurn0FragmentsEphemeralAcrossReopen is the ephemeral-fragment invariant (ADR
// 0043, superseding the resume-gate of f31bde54). The turn-0 instruction fragments
// are NO LONGER persisted into the conversation; they are prepended to every
// LLMRequest ephemerally. The contract this pins is twofold:
//
//   - the persisted Conversation.Messages carries ZERO injected fragments after a
//     fresh run AND after Reopen + a second run (no growth across resumes — the
//     bloat the old resume-gate also fixed, now fixed structurally by not persisting);
//   - every run, INCLUDING the resumed one, sends the fragments prepended ahead of
//     the conversation in LLMRequest.Messages (so resume never loses its persona /
//     project instructions / memory — what the resume-gate's "skip on resume" left
//     ambiguous, the ephemeral prepend makes unconditional).
//
// It captures the FIRST request of each run via WithRequestObserver and asserts the
// fragment leads the message slice; assembly fires once per run (the per-run cache).
func TestTurn0FragmentsEphemeralAcrossReopen(t *testing.T) {
	ctx := context.Background()
	const fragText = "Project instructions (AGENTS.md):\n\nthe house style"
	asm := &countingAssembler{msg: fragText}

	var (
		mu      sync.Mutex
		firstOf []port.LLMRequest // the first request observed in each run
		seenRun bool
	)
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		if !seenRun { // capture only the first turn of the current run
			firstOf = append(firstOf, req)
			seenRun = true
		}
	}
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("first done"), mockllm.TextTurn("second done"),
	)
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Instructions: asm})

	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	// Fresh run: fragments are sent (prepended) but NOT persisted.
	drain(e.Run(ctx, sess, ws, agent.RunRequest{Text: "first prompt"}))
	if asm.called != 1 {
		t.Fatalf("fresh run: assembler called %d times, want 1 (once-per-run cache)", asm.called)
	}
	if got := countInjected(sess); got != 0 {
		t.Fatalf("fresh run: %d injected fragments PERSISTED in history, want 0 (fragments are ephemeral)", got)
	}

	// Resume: a completed session is Reopened and re-driven. Fragments are RE-assembled
	// (a new run → new cache) and prepended again; still none persist.
	mu.Lock()
	seenRun = false
	mu.Unlock()
	if err := sess.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	drain(e.Run(ctx, sess, ws, agent.RunRequest{Text: "second prompt"}))
	if asm.called != 2 {
		t.Fatalf("after Reopen: assembler called %d times, want 2 (once per run, re-assembled on resume)", asm.called)
	}
	if got := countInjected(sess); got != 0 {
		t.Fatalf("after Reopen: %d injected fragments PERSISTED in history, want 0 (no growth across resume)", got)
	}

	// Both runs' first request must lead with the ephemeral fragment.
	mu.Lock()
	defer mu.Unlock()
	if len(firstOf) != 2 {
		t.Fatalf("expected one captured request per run, got %d", len(firstOf))
	}
	for i, req := range firstOf {
		if len(req.Messages) == 0 {
			t.Fatalf("run %d: request carried no messages", i)
		}
		lead := req.Messages[0]
		if lead.Role != session.RoleUser || !prompt.IsInjectedTurn0Fragment(lead.Text) || lead.Text != fragText {
			t.Fatalf("run %d: first message is not the prepended turn-0 fragment: role=%s text=%q", i, lead.Role, lead.Text)
		}
	}
}

// countingAssembler counts Assemble calls and returns one injected-shaped fragment.
type countingAssembler struct {
	called int
	msg    string
}

func (a *countingAssembler) Assemble(context.Context, tool.Workspace) ([]session.Message, error) {
	a.called++
	return []session.Message{session.NewUserMessage(a.msg)}, nil
}

func countInjected(sess *session.Session) int {
	n := 0
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && prompt.IsInjectedTurn0Fragment(m.Text) {
			n++
		}
	}
	return n
}

// erroringAssembler always fails, so a test can drive the fail-soft branch in
// buildRequest's once-per-run fragment assembly.
type erroringAssembler struct{ err error }

func (a erroringAssembler) Assemble(context.Context, tool.Workspace) ([]session.Message, error) {
	return nil, a.err
}

// TestTurn0FragmentAssembleErrorIsFailSoft proves the ephemeral fragment assembly
// (ADR 0043) is fail-soft: an Instructions.Assemble error does NOT abort the run, no
// fragment is prepended to the request, and a single WARN fires on the run-scoped
// diagnostics. This is the branch TestDefaultAssemblerWhenNil does NOT cover (that
// one is nil-assembler → RootAssembler; this is assemble-ERROR).
func TestTurn0FragmentAssembleErrorIsFailSoft(t *testing.T) {
	ctx := context.Background()
	diag := newCapturingDiag()
	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("done"))
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	e := newEngine(agent.Deps{
		LLM:          llm,
		Catalog:      cat,
		Instructions: erroringAssembler{err: errAssembleFailed},
		Diagnostics:  diag,
	})

	sess := newSession(t, session.Limits{})
	run := e.Run(ctx, sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "do the thing"})

	// The run must COMPLETE despite the assemble error.
	var terminal *session.Event
	for ev := range run.Events() {
		if ev.Type == session.EvResult {
			r := ev
			terminal = &r
		}
	}
	if terminal == nil || terminal.Result == nil {
		t.Fatal("run never reached a terminal result (assemble error must be fail-soft, not fatal)")
	}
	if terminal.Result.Stop != session.StopEndTurn {
		t.Fatalf("run stop = %q, want a clean end_turn (assemble error must not change the terminal)", terminal.Result.Stop)
	}

	// No fragment was prepended to the request.
	req, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}
	for _, m := range req.Messages {
		if m.Role == session.RoleUser && prompt.IsInjectedTurn0Fragment(m.Text) {
			t.Fatalf("a fragment was prepended despite the assemble error:\n%q", m.Text)
		}
	}

	// Exactly the fail-soft WARN fired (and no fragment persisted, by construction).
	var warns int
	for _, rec := range diag.snapshot() {
		if rec.level == port.LevelWarn && strings.Contains(rec.msg, "instruction-fragment assembly failed") {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("fail-soft WARN fired %d times, want exactly 1", warns)
	}
}

var errAssembleFailed = errors.New("assemble boom")
