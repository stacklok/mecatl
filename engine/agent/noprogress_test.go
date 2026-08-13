package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// These are MODEL-FACING e2e tests through the REAL agent loop (Engine.Run), driven
// by ADVERSARIAL / uncooperative mockllm turns (EmptyTurn / ReasoningOnlyTurn) that a
// cooperative happy-path mock can never produce. They pin Workstream A: a completed
// turn with NO tool call AND no meaningful text is NUDGED (bounded) rather than
// silently terminated, and is terminated CLEARLY with StopNoProgress once the budget
// is exhausted. Each assertion FAILS on the pre-fix code (which terminated on zero
// tool calls regardless of text, silently as StopEndTurn).

// nudgeMessagesIn counts the RoleUser messages whose text is the GENTLE continuation
// nudge. Since the nudge is graduated (gentle early, extractive on the final attempt),
// this counts ONLY gentle nudges; use extractiveNudgeMessagesIn for the final one.
func nudgeMessagesIn(msgs []session.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "Make concrete progress on the task using your tools") {
			n++
		}
	}
	return n
}

// extractiveNudgeMessagesIn counts the RoleUser messages whose text is the FINAL
// (extractive) continuation nudge, keyed on a stable substring of the extractive text.
func extractiveNudgeMessagesIn(msgs []session.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "Stop investigating now") {
			n++
		}
	}
	return n
}

// TestNoProgressTurnNudgesThenProgresses: an empty turn must be nudged, not ended.
// Script: EmptyTurn (no text, no call) → ToolCallTurn(Read) → TextTurn("done").
func TestNoProgressTurnNudgesThenProgresses(t *testing.T) {
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "file contents"), nil
		}}
	cat := catalogWith(t, read)

	llm := mockllm.New(
		mockllm.EmptyTurn(),
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "do the task"}))

	// The run did NOT terminate at the empty turn: all three scripted turns ran.
	if got := llm.Calls(); got != 3 {
		t.Fatalf("model calls = %d, want 3 (empty turn must be nudged, not terminate); pre-fix this is 1", got)
	}
	// A visible no-progress event was emitted.
	if !containsType(evs, session.EvNoProgress) {
		t.Fatalf("expected an EvNoProgress event; types=%v", typesOf(evs))
	}
	// Exactly one continuation nudge user message was recorded.
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("continuation nudge messages = %d, want 1", n)
	}
	// The run progressed to a real answer.
	res := lastResult(t, evs)
	if res.Stop != session.StopEndTurn || res.Text != "done" {
		t.Fatalf("final result = {stop:%q text:%q}, want {end_turn done}", res.Stop, res.Text)
	}
}

// TestNoProgressReasoningOnlyPreservesBlob: a reasoning-only empty turn is nudged AND
// its reasoning REPLAY blob is preserved on the recorded empty assistant message and
// replayed in the next request (decision D-4(a)). Script: ReasoningOnlyTurn → TextTurn.
func TestNoProgressReasoningOnlyPreservesBlob(t *testing.T) {
	const blob = "REASONING_REPLAY_BLOB"
	var secondReqMsgs []session.Message
	var nth int
	obs := func(req port.LLMRequest) {
		nth++
		if nth == 2 {
			secondReqMsgs = req.Messages
		}
	}
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.ReasoningOnlyTurn("thinking hard", blob),
		mockllm.TextTurn("answer"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"}))

	if got := llm.Calls(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (reasoning-only turn must be nudged)", got)
	}
	// The empty assistant message carrying the reasoning blob must be in the SECOND
	// request's replayed history (proving it was recorded and replays).
	var sawBlob bool
	for _, m := range secondReqMsgs {
		if m.Role == session.RoleAssistant && m.Reasoning == blob {
			sawBlob = true
		}
	}
	if !sawBlob {
		t.Fatalf("reasoning replay blob %q not found on a replayed assistant message; msgs=%+v", blob, secondReqMsgs)
	}
	if res := lastResult(t, evs); res.Text != "answer" {
		t.Fatalf("final text = %q, want %q", res.Text, "answer")
	}
}

// TestNoProgressBoundedThenTerminatesClearly: N+1 empty turns terminate after exactly
// N nudges with StopNoProgress and a final give-up EvNoProgress — never unbounded.
func TestNoProgressBoundedThenTerminatesClearly(t *testing.T) {
	const nudgeBudget = 2
	llm := mockllm.New(
		mockllm.EmptyTurn(),
		mockllm.EmptyTurn(),
		mockllm.EmptyTurn(),
		// extra turns the loop must NEVER reach (proves the cap, not script exhaustion).
		mockllm.TextTurn("should-never-run"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: nudgeBudget})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"}))

	// Exactly cap+1 model calls: the initial turn plus one per nudge, then give up.
	if got := llm.Calls(); got != nudgeBudget+1 {
		t.Fatalf("model calls = %d, want %d (initial + %d nudges, then give up — never unbounded)", got, nudgeBudget+1, nudgeBudget)
	}
	// The nudge is graduated: with cap=2 the loop injects ONE gentle nudge (attempt 1)
	// then ONE extractive nudge (attempt 2, the final one before give-up).
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("gentle nudge messages = %d, want 1", n)
	}
	if n := extractiveNudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("extractive nudge messages = %d, want 1", n)
	}
	// The code makes TWO distinct advisory EvNoProgress emissions (gentle "nudging to
	// continue" then final "final attempt: requesting a best-effort answer"), then ONE
	// terminal "ending run" give-up event. Pin the counts and the Turn index they carry
	// (the no-progress turn's 0-based index).
	var advisory, terminal int
	for _, ev := range evs {
		if ev.Type != session.EvNoProgress {
			continue
		}
		if ev.Turn < 0 {
			t.Fatalf("EvNoProgress carried a negative Turn index %d", ev.Turn)
		}
		switch {
		case strings.Contains(ev.Text, "nudging to continue"):
			advisory++
		case strings.Contains(ev.Text, "final attempt: requesting a best-effort answer"):
			advisory++
		case strings.Contains(ev.Text, "ending run"):
			terminal++
		default:
			t.Fatalf("unexpected EvNoProgress text %q", ev.Text)
		}
	}
	if advisory != nudgeBudget {
		t.Fatalf("advisory EvNoProgress (nudge) events = %d, want %d", advisory, nudgeBudget)
	}
	if terminal != 1 {
		t.Fatalf("terminal EvNoProgress (give-up) events = %d, want 1", terminal)
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopNoProgress {
		t.Fatalf("stop = %q, want %q (clean give-up terminal)", res.Stop, session.StopNoProgress)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("session state = %q, want completed (StopNoProgress is a clean, reopen-recoverable terminal)", sess.State)
	}
}

// TestEmptyTurnWithTerminalStopNotNudged is the regression guard for the
// streamStop-masking bug. An empty turn caused by a REAL terminal condition
// (max_tokens / refusal / incomplete / failed → StopError on the ChunkDone stop, NOT
// a Go error) must SURFACE that reason — never get nudged "please continue" and never
// be relabeled StopNoProgress. Pre-fix this exact turn was nudged and mislabeled.
func TestEmptyTurnWithTerminalStopNotNudged(t *testing.T) {
	llm := mockllm.New(
		mockllm.EmptyTurnWithStop(session.StopError),
		// must NEVER be reached: a real terminal stop is not nudged.
		mockllm.TextTurn("should-never-run"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 2})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	if got := llm.Calls(); got != 1 {
		t.Fatalf("model calls = %d, want 1 (a real terminal stop must NOT be nudged)", got)
	}
	if containsType(evs, session.EvNoProgress) {
		t.Fatalf("a terminal-stop empty turn must NOT emit EvNoProgress (no nudge); types=%v", typesOf(evs))
	}
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 0 {
		t.Fatalf("nudge messages = %d, want 0 (terminal stop is not a stall)", n)
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want %q (the REAL terminal reason, NOT no_progress)", res.Stop, session.StopError)
	}
	if res.Stop == session.StopNoProgress {
		t.Fatal("the real terminal reason was MASKED as no_progress (the bug)")
	}
	if sess.State != session.StateFailed {
		t.Fatalf("session state = %q, want failed (StopError terminal)", sess.State)
	}
}

// TestEmptyTurnWithNonErrorTerminalStopSurfaced pins the non-StopError branch of the
// masking guard: a non-benign, non-error stop (here a cancelled stop the provider set
// on the chunk) is surfaced verbatim via the completed path, not nudged.
func TestEmptyTurnWithNonErrorTerminalStopSurfaced(t *testing.T) {
	llm := mockllm.New(
		mockllm.EmptyTurnWithStop(session.StopMaxTurns), // a non-benign, non-error stop on the chunk
		mockllm.TextTurn("should-never-run"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 2})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	if got := llm.Calls(); got != 1 {
		t.Fatalf("model calls = %d, want 1 (a non-benign stop must NOT be nudged)", got)
	}
	if containsType(evs, session.EvNoProgress) {
		t.Fatalf("a non-benign-stop empty turn must NOT emit EvNoProgress; types=%v", typesOf(evs))
	}
	if res := lastResult(t, evs); res.Stop != session.StopMaxTurns {
		t.Fatalf("stop = %q, want %q (the provider's reason, surfaced verbatim, NOT no_progress)", res.Stop, session.StopMaxTurns)
	}
}

// TestWhitespaceOnlyTextIsNoProgress pins that the load-bearing TrimSpace predicate
// treats whitespace-only assistant text as NOT meaningful → it IS nudged.
func TestWhitespaceOnlyTextIsNoProgress(t *testing.T) {
	llm := mockllm.New(
		// benign StopEndTurn, only whitespace text, no tool call.
		mockllm.ChunksTurn(
			mockllm.TextChunk("   \n\t "),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("real answer"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 2})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	if !containsType(evs, session.EvNoProgress) {
		t.Fatalf("whitespace-only text must be treated as no-progress (nudged); types=%v", typesOf(evs))
	}
	if got := llm.Calls(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (whitespace-only turn nudged, then real answer)", got)
	}
	if res := lastResult(t, evs); res.Text != "real answer" {
		t.Fatalf("final text = %q, want %q", res.Text, "real answer")
	}
}

// TestEmptyTextWithToolCallNotNudged closes the branch: a turn with a tool call and
// EMPTY text must dispatch normally and never enter the no-progress path.
func TestEmptyTextWithToolCallNotNudged(t *testing.T) {
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "contents"), nil
		}}
	llm := mockllm.New(
		// tool call, zero text — the loop must dispatch, not nudge.
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, read), MaxNoProgressNudges: 2})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	if containsType(evs, session.EvNoProgress) {
		t.Fatalf("a turn WITH a tool call must never enter the no-progress path; types=%v", typesOf(evs))
	}
	if !containsType(evs, session.EvToolResult) {
		t.Fatalf("the tool call must have dispatched; types=%v", typesOf(evs))
	}
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn || res.Text != "done" {
		t.Fatalf("final = {stop:%q text:%q}, want {end_turn done}", res.Stop, res.Text)
	}
}

// TestNoProgressReopenRecoverable proves StopNoProgress leaves the session reopen-able
// (it is a non-error completed terminal), so a follow-up prompt is accepted.
func TestNoProgressReopenRecoverable(t *testing.T) {
	llm := mockllm.New(mockllm.EmptyTurn(), mockllm.EmptyTurn(), mockllm.EmptyTurn())
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 2})
	sess := newSession(t, session.Limits{})
	_ = drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	if sess.State != session.StateCompleted {
		t.Fatalf("state = %q, want completed", sess.State)
	}
	if err := sess.Reopen(); err != nil {
		t.Fatalf("Reopen after StopNoProgress: %v (must be reopen-recoverable)", err)
	}
}

// TestMeaningfulTextStillTerminatesImmediately guards against over-eager nudging: a
// real text answer must NOT be nudged.
func TestMeaningfulTextStillTerminatesImmediately(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("hi"), mockllm.TextTurn("should-never-run"))
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"}))

	if got := llm.Calls(); got != 1 {
		t.Fatalf("model calls = %d, want 1 (a real answer must terminate immediately)", got)
	}
	if containsType(evs, session.EvNoProgress) {
		t.Fatalf("a meaningful text turn must NOT emit EvNoProgress; types=%v", typesOf(evs))
	}
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn", res.Stop)
	}
}

// TestNoProgressDisabledTerminatesImmediately pins the negative-cap "disable" sentinel:
// MaxNoProgressNudges < 0 ends an empty turn at once with StopNoProgress (no nudge).
func TestNoProgressDisabledTerminatesImmediately(t *testing.T) {
	llm := mockllm.New(mockllm.EmptyTurn(), mockllm.TextTurn("should-never-run"))
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: -1})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	if got := llm.Calls(); got != 1 {
		t.Fatalf("model calls = %d, want 1 (nudging disabled)", got)
	}
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 0 {
		t.Fatalf("nudge messages = %d, want 0 (disabled)", n)
	}
	if res := lastResult(t, evs); res.Stop != session.StopNoProgress {
		t.Fatalf("stop = %q, want %q", res.Stop, session.StopNoProgress)
	}
}

// TestNoProgressBoundedByMaxTurns proves the nudge loop is ALSO independently bounded
// by Limits.MaxTurns (defense in depth): a low MaxTurns wins over a high nudge cap.
func TestNoProgressBoundedByMaxTurns(t *testing.T) {
	llm := mockllm.New(
		mockllm.EmptyTurn(), mockllm.EmptyTurn(), mockllm.EmptyTurn(),
		mockllm.EmptyTurn(), mockllm.EmptyTurn(),
	)
	// High nudge cap, but MaxTurns=2 must cap the loop first.
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 99})
	sess := newSession(t, session.Limits{MaxTurns: 2})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	if got := llm.Calls(); got > 2 {
		t.Fatalf("model calls = %d, want <= 2 (MaxTurns bounds the nudge loop)", got)
	}
	if res := lastResult(t, evs); res.Stop != session.StopMaxTurns {
		t.Fatalf("stop = %q, want %q (turn limit wins over nudge cap)", res.Stop, session.StopMaxTurns)
	}
}

// TestUnknownToolEmitsCardBeforeResult (Workstream E) pins the card-before-result
// ordering for an UNKNOWN tool: the loop must emit an EvToolCall "open card" for the
// unknown name BEFORE its error EvToolResult, so a client can render the card and then
// mark it failed. Pre-fix the loop emitted ONLY the error result (no card).
func TestUnknownToolEmitsCardBeforeResult(t *testing.T) {
	// catalog with one real tool so the run can proceed; the scripted call names a
	// tool NOT in the catalog.
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Nonexistent", `{}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, read)})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	var cardIdx, resultIdx = -1, -1
	for i, ev := range evs {
		switch ev.Type {
		case session.EvToolCall:
			if ev.ToolCall != nil && ev.ToolCall.Name == "Nonexistent" {
				cardIdx = i
			}
		case session.EvToolResult:
			if ev.ToolResult != nil && ev.ToolResult.CallID == "c1" {
				resultIdx = i
			}
		}
	}
	if cardIdx < 0 {
		t.Fatalf("no EvToolCall card for the unknown tool; types=%v", typesOf(evs))
	}
	if resultIdx < 0 {
		t.Fatalf("no EvToolResult for the unknown tool call id; types=%v", typesOf(evs))
	}
	if cardIdx >= resultIdx {
		t.Fatalf("EvToolCard (idx %d) must precede EvToolResult (idx %d) for the unknown tool", cardIdx, resultIdx)
	}
	// The result must be an unknown-tool error.
	if r := evs[resultIdx].ToolResult; !r.IsError || !strings.Contains(r.Content, "unknown tool") {
		t.Fatalf("unknown-tool result = {err:%v content:%q}, want an 'unknown tool' error", r.IsError, r.Content)
	}
}

// indexOfUserMessage returns the index of the first RoleUser message containing sub, or
// -1 if none. Used to assert the gentle nudge precedes the extractive one in history.
func indexOfUserMessage(msgs []session.Message, sub string) int {
	for i, m := range msgs {
		if m.Role == session.RoleUser && strings.Contains(m.Text, sub) {
			return i
		}
	}
	return -1
}

// TestGraduatedNudgeGentleThenExtractive: with cap=2 the loop nudges GENTLY on attempt 1
// then EXTRACTIVELY on attempt 2 (the final attempt before give-up), then gives up with
// StopNoProgress on the third empty turn. Exactly 3 model calls, never 4. FAILS on the
// pre-change code: it injected the gentle text on BOTH attempts, so the extractive text
// never appears and nudgeMessagesIn would be 2.
func TestGraduatedNudgeGentleThenExtractive(t *testing.T) {
	llm := mockllm.New(
		mockllm.EmptyTurn(),
		mockllm.EmptyTurn(),
		mockllm.EmptyTurn(),
		// must NEVER be reached: the cap (not script exhaustion) bounds the loop.
		mockllm.TextTurn("should-never-run"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 2})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"}))

	if got := llm.Calls(); got != 3 {
		t.Fatalf("model calls = %d, want 3 (initial + gentle nudge + extractive nudge, then give up)", got)
	}
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("gentle nudge messages = %d, want 1 (only attempt 1 is gentle)", n)
	}
	if n := extractiveNudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("extractive nudge messages = %d, want 1 (the final attempt before give-up)", n)
	}
	// The gentle nudge must precede the extractive nudge in replayed history.
	gentleIdx := indexOfUserMessage(sess.Conversation.Messages, "Make concrete progress on the task using your tools")
	extractIdx := indexOfUserMessage(sess.Conversation.Messages, "Stop investigating now")
	if gentleIdx < 0 || extractIdx < 0 || gentleIdx >= extractIdx {
		t.Fatalf("gentle nudge (idx %d) must precede extractive nudge (idx %d)", gentleIdx, extractIdx)
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopNoProgress {
		t.Fatalf("stop = %q, want %q (give up after the extractive attempt)", res.Stop, session.StopNoProgress)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("session state = %q, want completed", sess.State)
	}
}

// TestGraduatedNudgeRescue is the whole point: a model that stalls then ANSWERS on the
// turn after the extractive nudge completes StopEndTurn with its answer — NOT
// StopNoProgress. Script (cap=2): EmptyTurn, EmptyTurn, TextTurn("best-effort answer").
// The 3rd request's replayed history must contain the extractive nudge as a RoleUser
// message (proving it was injected and got one more turn). FAILS on the pre-change code:
// the 3rd request would carry the gentle text again, never the extractive text.
func TestGraduatedNudgeRescue(t *testing.T) {
	var thirdReqMsgs []session.Message
	var nth int
	obs := func(req port.LLMRequest) {
		nth++
		if nth == 3 {
			thirdReqMsgs = req.Messages
		}
	}
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.EmptyTurn(),
		mockllm.EmptyTurn(),
		mockllm.TextTurn("best-effort answer"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 2})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"}))

	if got := llm.Calls(); got != 3 {
		t.Fatalf("model calls = %d, want 3 (the extractive nudge gets one more turn, which answers)", got)
	}
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("gentle nudge messages = %d, want 1", n)
	}
	if n := extractiveNudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("extractive nudge messages = %d, want 1", n)
	}
	// The extractive nudge must be a RoleUser message in the 3rd request's history.
	if idx := indexOfUserMessage(thirdReqMsgs, "Stop investigating now"); idx < 0 {
		t.Fatalf("extractive nudge not found as a user message in the 3rd request; msgs=%+v", thirdReqMsgs)
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want %q (a post-extractive answer is a real end_turn, NOT no_progress)", res.Stop, session.StopEndTurn)
	}
	if res.Text != "best-effort answer" {
		t.Fatalf("final text = %q, want %q", res.Text, "best-effort answer")
	}
}

// TestGraduatedNudgeCapOneIsExtractive: with cap=1 the single nudge IS the final one, so
// it is EXTRACTIVE with no gentle attempt. Then give up on the next empty turn. Exactly 2
// model calls. Pins the cap=1 off-by-one.
func TestGraduatedNudgeCapOneIsExtractive(t *testing.T) {
	llm := mockllm.New(
		mockllm.EmptyTurn(),
		mockllm.EmptyTurn(),
		// must NEVER be reached.
		mockllm.TextTurn("should-never-run"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 1})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"}))

	if got := llm.Calls(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (initial + one extractive nudge, then give up)", got)
	}
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 0 {
		t.Fatalf("gentle nudge messages = %d, want 0 (cap=1 has no gentle attempt)", n)
	}
	if n := extractiveNudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("extractive nudge messages = %d, want 1 (the single nudge is the final one)", n)
	}
	if res := lastResult(t, evs); res.Stop != session.StopNoProgress {
		t.Fatalf("stop = %q, want %q", res.Stop, session.StopNoProgress)
	}
}

// TestGraduatedNudgeFinalAdvisoryText pins the EvNoProgress advisory text decision: with
// cap=2 and three empty turns, exactly one gentle advisory ("nudging to continue"), one
// final advisory ("final attempt: requesting a best-effort answer"), and one terminal
// ("ending run") event.
func TestGraduatedNudgeFinalAdvisoryText(t *testing.T) {
	llm := mockllm.New(mockllm.EmptyTurn(), mockllm.EmptyTurn(), mockllm.EmptyTurn())
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), MaxNoProgressNudges: 2})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	var gentle, final, terminal, other int
	for _, ev := range evs {
		if ev.Type != session.EvNoProgress {
			continue
		}
		switch {
		case strings.Contains(ev.Text, "nudging to continue"):
			gentle++
		case strings.Contains(ev.Text, "final attempt: requesting a best-effort answer"):
			final++
		case strings.Contains(ev.Text, "ending run"):
			terminal++
		default:
			other++
		}
	}
	if gentle != 1 || final != 1 || terminal != 1 || other != 0 {
		t.Fatalf("EvNoProgress advisory texts = {gentle:%d final:%d terminal:%d other:%d}, want {1 1 1 0}", gentle, final, terminal, other)
	}
}
