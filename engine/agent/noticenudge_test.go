package agent_test

// Turn-boundary background-completion NOTICE injection + the ONCE-per-run
// background-pending nudge (BACKGROUND-SUBAGENTS I3b, amendments A2/A9/D10).
// These tests drive the REAL loop (mockllm + memfs) and assert on what the MODEL
// sees: the exact harness-note user messages recorded into history (ids + stop
// labels ONLY — nothing child-authored), the SubagentStatus collection that
// remains the body's sole channel after a notice, and the terminate/drain
// behaviour when the model collects, ignores, or never reaches the nudge.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// userMessagesContaining returns the indices of RoleUser messages whose text
// contains sub, in history order.
func userMessagesContaining(msgs []session.Message, sub string) []int {
	var idx []int
	for i, m := range msgs {
		if m.Role == session.RoleUser && strings.Contains(m.Text, sub) {
			idx = append(idx, i)
		}
	}
	return idx
}

// userMessageEqual returns the indices of RoleUser messages whose text is
// EXACTLY want.
func userMessageEqual(msgs []session.Message, want string) []int {
	var idx []int
	for i, m := range msgs {
		if m.Role == session.RoleUser && m.Text == want {
			idx = append(idx, i)
		}
	}
	return idx
}

// assistantWithCall returns the index of the assistant message carrying the
// tool call with the given id, or -1.
func assistantWithCall(msgs []session.Message, callID session.ToolCallID) int {
	for i, m := range msgs {
		if m.Role != session.RoleAssistant {
			continue
		}
		for _, c := range m.ToolCalls {
			if c.ID == callID {
				return i
			}
		}
	}
	return -1
}

// countEvents counts events of the given type.
func countEvents(evs []session.Event, ty session.EventType) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == ty {
			n++
		}
	}
	return n
}

// The harness-note prefix every injected I3b message carries; "[harness note:"
// is the single marker tests use to assert ABSENCE of any injection.
const harnessNoteMarker = "[harness note:"

// TestBackgroundNoticeInjectedAtNextBoundary is the headline I3b e2e: a
// background child finishes, and at the NEXT turn boundary the loop records ONE
// harness-framed user message carrying ONLY the id + stop label (A2 — exact
// text pinned), after which SubagentStatus STILL delivers the body
// (noticed ≠ delivered) and the notice never repeats at later boundaries.
func TestBackgroundNoticeInjectedAtNextBoundary(t *testing.T) {
	childLLM := mockllm.New(mockllm.TextTurn("CHILD FINDINGS: the notice path works"))
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate","background":true}`)),
		// Any-child wait (ROSTER — does not collect): parks until the child's
		// terminal lands in the registry, so the next boundary deterministically
		// sees a finished, uncollected background child.
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"wait_ms":30000}`)),
		mockllm.ToolCallTurn(toolCall("p3", "SubagentStatus", `{"agent_id":"subagent-p1"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	// EXACT notice text: ids + stop labels only, one message (A2/A9). Exact
	// equality also proves nothing child-authored (no goal, no findings) leaked.
	wantNotice := "[harness note: 1 background subagent(s) finished: subagent-p1 (end_turn). " +
		"Collect each result with SubagentStatus before relying on it.]"
	noticeIdx := userMessageEqual(sess.Conversation.Messages, wantNotice)
	if len(noticeIdx) != 1 {
		t.Fatalf("exactly ONE exact notice message expected, got %d in history:\n%v",
			len(noticeIdx), sess.Conversation.Messages)
	}
	// The notice never repeats: no other harness-note message exists (boundaries
	// after p3 and the final turn must not re-notice).
	if all := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(all) != 1 {
		t.Fatalf("the notice must never repeat; found %d harness notes", len(all))
	}
	// The notice precedes the model's collection turn (it is what prompts it).
	collectIdx := assistantWithCall(sess.Conversation.Messages, "p3")
	if collectIdx == -1 || noticeIdx[0] > collectIdx {
		t.Fatalf("notice (idx %d) must precede the collection turn (idx %d)", noticeIdx[0], collectIdx)
	}

	// noticed ≠ delivered: collection AFTER the notice still returns the body.
	collected := resultByCallID(evs)["p3"]
	if collected == nil || collected.IsError || !strings.Contains(collected.Content, "CHILD FINDINGS: the notice path works") {
		t.Fatalf("collection after the notice must still deliver the body, got %+v", collected)
	}

	// The injection is NOT a no-progress event and consumes no nudge budget.
	if n := countEvents(evs, session.EvNoProgress); n != 0 {
		t.Fatalf("the notice must be event-silent, got %d EvNoProgress", n)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("run must end cleanly, got %q", got.Stop)
	}
	// The injected notice is ordinary history: it must replay cleanly.
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("history with the injected notice must stay pairing-valid: %v", err)
	}
}

// TestBackgroundNoticeSkipsDeliveredResult pins the delivered-skip decision: a
// background result the model ALREADY collected (SubagentStatus wait_ms before
// any boundary saw it done) is marked noticed silently — no notice message is
// ever injected for it (announcing "collect it" for a body the model holds
// would only provoke an "already delivered" round-trip).
func TestBackgroundNoticeSkipsDeliveredResult(t *testing.T) {
	gate := newBgGateTool()
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("CHILD FINDINGS: collected directly"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	evs := drainObserving(t, r, func(ev session.Event) {
		// Release the child only once the COLLECTING wait is dispatched, so the
		// boundary BEFORE it deterministically saw a still-running child (no
		// notice could have fired first).
		if ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.ID == "p2" {
			gate.releaseOnce()
		}
	})

	if collected := resultByCallID(evs)["p2"]; collected == nil || !strings.Contains(collected.Content, "CHILD FINDINGS: collected directly") {
		t.Fatalf("direct collection must deliver, got %+v", collected)
	}
	if notes := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(notes) != 0 {
		t.Fatalf("an already-delivered result must never be noticed, found %d harness notes", len(notes))
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("run must end cleanly, got %q", got.Stop)
	}
}

// TestBackgroundPendingNudgeOneMoreTurn is the D10 happy path: the model tries
// to finish cleanly while a background child is still LIVE, the loop injects the
// exact ids-only pending nudge (ONCE), the model gets one more turn and collects,
// and the run then ends with its natural stop reason (never relabelled). The
// nudge is event-silent (no EvNoProgress — that taxonomy means "the model
// stalled", not "the harness deferred a clean end").
func TestBackgroundPendingNudgeOneMoreTurn(t *testing.T) {
	gate := newBgGateTool()
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("CHILD FINDINGS: collected after the nudge"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"x","background":true}`),
			toolCall("pw", "AwaitChild", `{}`), // child is genuinely mid-flight at the clean end
		),
		mockllm.TextTurn("interim answer"), // would-be clean end → nudge
		mockllm.ToolCallTurn(toolCall("p3", "SubagentStatus", `{"agent_id":"subagent-p1","wait_ms":30000}`)),
		mockllm.TextTurn("final answer"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: gate.started})
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.ID == "p3" {
			gate.releaseOnce()
		}
	})

	wantNudge := "[harness note: 1 background subagent(s) still running: subagent-p1. " +
		"Collect or wait for them with SubagentStatus, cancel them, or finish — " +
		"anything still running when you finish will be cancelled.]"
	nudgeIdx := userMessageEqual(sess.Conversation.Messages, wantNudge)
	if len(nudgeIdx) != 1 {
		t.Fatalf("exactly ONE exact pending-nudge message expected, got %d:\n%v",
			len(nudgeIdx), sess.Conversation.Messages)
	}
	// Only the nudge was injected — the collected-before-any-boundary result is
	// never noticed (delivered-skip), so no "finished" notice exists.
	if all := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(all) != 1 {
		t.Fatalf("expected only the pending nudge in history, found %d harness notes", len(all))
	}
	// Ordering: the nudge follows the interim answer and precedes the collection.
	collectIdx := assistantWithCall(sess.Conversation.Messages, "p3")
	if collectIdx == -1 || nudgeIdx[0] > collectIdx {
		t.Fatalf("nudge (idx %d) must precede the collection turn (idx %d)", nudgeIdx[0], collectIdx)
	}
	if collected := resultByCallID(evs)["p3"]; collected == nil || !strings.Contains(collected.Content, "CHILD FINDINGS: collected after the nudge") {
		t.Fatalf("the nudged continuation must be able to collect, got %+v", collected)
	}
	if n := countEvents(evs, session.EvNoProgress); n != 0 {
		t.Fatalf("the background-pending nudge must be event-silent, got %d EvNoProgress", n)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("the eventual stop must be the model's own clean end, got %q", got.Stop)
	}
}

// TestBackgroundPendingNudgeIgnoredThenCancelledAtRunEnd is the adversarial
// D10 bound: a model that IGNORES the nudge and ends cleanly again terminates
// normally on the SECOND attempt — the nudge fires exactly once, the run keeps
// its clean stop, and the run-end drain cancels + persists the live child (the
// I3a run-end contract extended through the nudge path).
func TestBackgroundPendingNudgeIgnoredThenCancelledAtRunEnd(t *testing.T) {
	store := memstore.New()
	gate := newBgGateTool() // never released: the child outlives every parent turn
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("never reached"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)),
		agent.WithSubagentStore(store))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"x","background":true}`),
			toolCall("pw", "AwaitChild", `{}`),
		),
		mockllm.TextTurn("done (ignoring the nudge)"),
		mockllm.TextTurn("still done"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: gate.started})
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("the second clean end must terminate normally, got %q", got.Stop)
	}
	if all := userMessagesContaining(sess.Conversation.Messages, "background subagent(s) still running"); len(all) != 1 {
		t.Fatalf("the pending nudge must fire exactly ONCE per run, found %d", len(all))
	}
	// Drain contract: the ignored child is cancelled (subagent.end precedes the
	// terminal result) and persisted resumable.
	endIdx, resultIdx := -1, -1
	var endStop session.StopReason
	for i, ev := range evs {
		switch {
		case ev.Type == session.EvSubagentEnd && ev.Subagent != nil:
			endIdx, endStop = i, ev.Subagent.Stop
		case ev.Type == session.EvResult:
			resultIdx = i
		}
	}
	if endIdx == -1 || endStop != session.StopCancelled || endIdx > resultIdx {
		t.Fatalf("ignored child must be drain-cancelled before the terminal (end idx %d stop %q, result idx %d)",
			endIdx, endStop, resultIdx)
	}
	saved, err := store.Load(context.Background(), "subagent-p1")
	if err != nil || saved == nil || saved.State != session.StateCancelled {
		t.Fatalf("ignored child must persist cancelled, got %v (err %v)", saved, err)
	}
}

// TestBackgroundPendingNudgeAbsentWithoutLiveChildren pins the byte-identical
// clean end: a run with no background children records NO harness note of any
// kind and terminates exactly as before I3b.
func TestBackgroundPendingNudgeAbsentWithoutLiveChildren(t *testing.T) {
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Probe", `{}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, &probeTool{})})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("run must end cleanly, got %q", got.Stop)
	}
	if notes := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(notes) != 0 {
		t.Fatalf("no harness note may be injected without background children, found %d", len(notes))
	}
	// Exactly one user message: the original prompt (the clean end is unchanged).
	users := 0
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("history must carry only the original prompt as a user message, got %d", users)
	}
}

// TestBackgroundPendingNudgeSkippedOnBudget pins the non-clean-terminal rule for
// the token budget: a StopBudget boundary terminal with a live background child
// never nudges (and never notices a still-running child) — the drain cancels it
// and the stop reason stays "budget".
func TestBackgroundPendingNudgeSkippedOnBudget(t *testing.T) {
	gate := newBgGateTool() // never released
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("never"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	bgCall := toolCall("p1", "Subagent", `{"prompt":"x","background":true}`)
	awaitCall := toolCall("pw", "AwaitChild", `{}`)
	parentLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(bgCall),
			mockllm.ToolCallChunk(awaitCall),
			mockllm.UsageChunk(session.Usage{InputTokens: 50, OutputTokens: 10}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("never reached"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: gate.started})
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat, MaxRunTokens: 10})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	if got := lastResult(t, evs); got.Stop != session.StopBudget {
		t.Fatalf("budget terminal must not be deferred or relabelled, got %q", got.Stop)
	}
	if notes := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(notes) != 0 {
		t.Fatalf("a non-clean terminal must not nudge/notice, found %d harness notes", len(notes))
	}
	var endStop session.StopReason
	for _, ev := range evs {
		if ev.Type == session.EvSubagentEnd && ev.Subagent != nil {
			endStop = ev.Subagent.Stop
		}
	}
	if endStop != session.StopCancelled {
		t.Fatalf("the live child must be drain-cancelled on the budget terminal, got %q", endStop)
	}
}

// TestBackgroundPendingNudgeSkippedOnCancel pins the non-clean-terminal rule for
// a whole-run cancel: StopCancelled terminates immediately, no nudge.
func TestBackgroundPendingNudgeSkippedOnCancel(t *testing.T) {
	gate := newBgGateTool() // never released
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("never"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		// The parent parks in AwaitChild (a never-closed channel: only the run
		// cancel can release it), so the cancel DETERMINISTICALLY lands mid-turn —
		// the run can never reach a clean end where the nudge would be considered.
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"x","background":true}`),
			toolCall("pw", "AwaitChild", `{}`),
		),
		mockllm.TextTurn("never reached"),
	)
	never := make(chan struct{})
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: never})
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	go func() {
		<-gate.started // the background child is genuinely mid-flight
		r.Cancel()
	}()
	evs := drainObserving(t, r, nil)

	if got := lastResult(t, evs); got.Stop != session.StopCancelled {
		t.Fatalf("cancel must terminate immediately, got %q", got.Stop)
	}
	// Only the NUDGE is asserted absent: the cancel races the child's own
	// (cancelled) terminal, and a child that lands it before the final boundary
	// may legitimately be NOTICED there (Step 2a deliberately precedes the
	// terminal checks so the notice is durable history) — but a non-clean
	// terminal must never be deferred by the pending nudge.
	if notes := userMessagesContaining(sess.Conversation.Messages, "background subagent(s) still running"); len(notes) != 0 {
		t.Fatalf("a cancelled run must not draw the pending nudge, found %d", len(notes))
	}
}

// TestBackgroundPendingNudgeSkippedOnErrorStopWithText is the bg-nudge analogue
// of TestEmptyTurnWithTerminalStopNotNudged, and the kill test for the nudge's
// clean-stop guard (proven needed by live mutation: with the
// `stop == StopEndTurn` condition removed, the whole suite still passed): a turn
// with MEANINGFUL TEXT that the provider terminated with streamStop=StopError is
// a REAL terminal relay, not a clean end — it must surface verbatim even while a
// background child is live, with ZERO harness notes injected (no nudge deferring
// the error, and no relabel).
func TestBackgroundPendingNudgeSkippedOnErrorStopWithText(t *testing.T) {
	gate := newBgGateTool() // never released: the child is live at the error relay
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("never"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"x","background":true}`),
			toolCall("pw", "AwaitChild", `{}`),
		),
		// Meaningful text on a TERMINAL stop: the provider relayed a real failure.
		mockllm.ChunksTurn(
			mockllm.TextChunk("partial answer before the provider failed"),
			mockllm.DoneChunk(session.StopError),
		),
		mockllm.TextTurn("never reached"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: gate.started})
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	if got := lastResult(t, evs); got.Stop != session.StopError {
		t.Fatalf("the provider's terminal stop must be relayed verbatim, got %q", got.Stop)
	}
	if notes := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(notes) != 0 {
		t.Fatalf("a non-clean terminal with text must not be nudged/deferred, found %d harness notes", len(notes))
	}
	var endStop session.StopReason
	for _, ev := range evs {
		if ev.Type == session.EvSubagentEnd && ev.Subagent != nil {
			endStop = ev.Subagent.Stop
		}
	}
	if endStop != session.StopCancelled {
		t.Fatalf("the live child must be drain-cancelled on the error relay, got %q", endStop)
	}
}

// noticeSpyStore wraps memstore and records whether some Save ran while the
// session was still RUNNING with the completion notice as the LAST message of
// the conversation — the e.save-after-injection placement witness (A2: the
// notice is durable IMMEDIATELY). The last-message requirement is load-bearing:
// only the injection's own save can satisfy it — every later save (Step 6 after
// tool results, a nudge save, the terminal save) runs with a tool-result /
// assistant / nudge message appended after the notice. A weaker "notice present
// in a RUNNING-state save" spy survives dropping the e.save inside
// injectBackgroundNotice (the next turn's Step-6 save also qualifies) — proven
// by live mutation.
type noticeSpyStore struct {
	inner *memstore.Store

	mu                       sync.Mutex
	runningSaveEndedOnNotice bool
}

func (s *noticeSpyStore) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	if msgs := sess.Conversation.Messages; sess.State == session.StateRunning && len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role == session.RoleUser && strings.Contains(last.Text, "background subagent(s) finished") {
			s.runningSaveEndedOnNotice = true
		}
	}
	s.mu.Unlock()
	return s.inner.Save(ctx, sess)
}

func (s *noticeSpyStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.inner.Load(ctx, id)
}

var _ port.SessionStore = (*noticeSpyStore)(nil)

// TestBackgroundNoticeDurableAcrossSave pins the e.save placement: the notice is
// persisted by a save that happens WHILE THE RUN IS STILL RUNNING (immediately
// after the injection records it), and a session re-loaded from the store
// carries it in replay-valid history.
func TestBackgroundNoticeDurableAcrossSave(t *testing.T) {
	childLLM := mockllm.New(mockllm.TextTurn("CHILD FINDINGS: durable"))
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","background":true}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"wait_ms":30000}`)),
		mockllm.ToolCallTurn(toolCall("p3", "SubagentStatus", `{"agent_id":"subagent-p1"}`)),
		mockllm.TextTurn("parent done"),
	)
	spy := &noticeSpyStore{inner: memstore.New()}
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool()), Store: spy})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	drainObserving(t, r, nil)

	spy.mu.Lock()
	durable := spy.runningSaveEndedOnNotice
	spy.mu.Unlock()
	if !durable {
		t.Fatalf("the notice must be persisted by the injection's OWN save (a RUNNING-state save whose last message IS the notice), not by a later turn's save")
	}

	loaded, err := spy.Load(context.Background(), sess.ID)
	if err != nil || loaded == nil {
		t.Fatalf("reload: %v", err)
	}
	if len(userMessagesContaining(loaded.Conversation.Messages, "background subagent(s) finished")) != 1 {
		t.Fatalf("the reloaded history must carry the notice exactly once")
	}
	if err := session.ValidateToolPairing(loaded.Conversation.Messages); err != nil {
		t.Fatalf("reloaded history must be replayable: %v", err)
	}
}

// TestBackgroundNudgeRespectsMaxTurns pins the MaxTurns interplay: the nudge
// message itself consumes no turn, but the nudged continuation goes through
// BeginTurn — so a turn cap already reached at the next boundary wins, the run
// ends StopMaxTurns (never relabelled), and the drain still cancels the child.
func TestBackgroundNudgeRespectsMaxTurns(t *testing.T) {
	gate := newBgGateTool() // never released
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("never"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"x","background":true}`),
			toolCall("pw", "AwaitChild", `{}`),
		),
		mockllm.TextTurn("answer at the cap"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: gate.started})
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat})
	sess := newSession(t, session.Limits{MaxTurns: 2})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	if got := lastResult(t, evs); got.Stop != session.StopMaxTurns {
		t.Fatalf("the nudged continuation must still respect MaxTurns, got %q", got.Stop)
	}
	// The nudge DID fire (the cap is enforced at the next boundary, not by
	// suppressing the nudge).
	if all := userMessagesContaining(sess.Conversation.Messages, "background subagent(s) still running"); len(all) != 1 {
		t.Fatalf("expected exactly one pending nudge before the cap, found %d", len(all))
	}
	var endStop session.StopReason
	for _, ev := range evs {
		if ev.Type == session.EvSubagentEnd && ev.Subagent != nil {
			endStop = ev.Subagent.Stop
		}
	}
	if endStop != session.StopCancelled {
		t.Fatalf("the live child must be drain-cancelled at the cap terminal, got %q", endStop)
	}
}

// TestNoProgressPrecedesBackgroundPendingNudge pins the deliberate precedence
// between the two nudge families in ONE run: an EMPTY (reasoning-only) turn with
// live background children is handled by the NO-PROGRESS machinery first (its
// nudge, its EvNoProgress); the background-pending nudge fires only on a REAL
// clean end (meaningful text, benign stop) — and stays event-silent.
func TestNoProgressPrecedesBackgroundPendingNudge(t *testing.T) {
	gate := newBgGateTool() // never released: live across the whole run
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("never"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"x","background":true}`),
			toolCall("pw", "AwaitChild", `{}`),
		),
		// EMPTY turn: no text, no tools → the no-progress nudge, NOT the bg nudge.
		mockllm.ChunksTurn(mockllm.DoneChunk(session.StopEndTurn)),
		// REAL clean end with the child still live → the bg-pending nudge.
		mockllm.TextTurn("a real answer"),
		mockllm.TextTurn("final"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: gate.started})
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	noProgIdx := userMessagesContaining(sess.Conversation.Messages, "Make concrete progress on the task")
	bgIdx := userMessagesContaining(sess.Conversation.Messages, "background subagent(s) still running")
	if len(noProgIdx) != 1 || len(bgIdx) != 1 {
		t.Fatalf("expected exactly one no-progress nudge and one bg-pending nudge, got %d/%d",
			len(noProgIdx), len(bgIdx))
	}
	if noProgIdx[0] > bgIdx[0] {
		t.Fatalf("the no-progress nudge (idx %d) must handle the empty turn BEFORE the bg-pending nudge (idx %d)",
			noProgIdx[0], bgIdx[0])
	}
	// Exactly the no-progress family emits EvNoProgress; the bg nudge adds none.
	if n := countEvents(evs, session.EvNoProgress); n != 1 {
		t.Fatalf("expected exactly 1 EvNoProgress (the no-progress nudge only), got %d", n)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("run must end on the model's clean end, got %q", got.Stop)
	}
}

// TestNoProgressGiveUpDoesNotBackgroundNudge pins the other half of the
// precedence: a StopNoProgress give-up is NOT a real clean end — the bg-pending
// nudge must not defer it (the drain cancels the live child as on any terminal).
func TestNoProgressGiveUpDoesNotBackgroundNudge(t *testing.T) {
	gate := newBgGateTool() // never released
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("never"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, gate)))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"x","background":true}`),
			toolCall("pw", "AwaitChild", `{}`),
		),
		mockllm.ChunksTurn(mockllm.DoneChunk(session.StopEndTurn)), // empty turn
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool(), &awaitSignalTool{ch: gate.started})
	// Nudging disabled: the empty turn gives up immediately with StopNoProgress.
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat, MaxNoProgressNudges: -1})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	if got := lastResult(t, evs); got.Stop != session.StopNoProgress {
		t.Fatalf("the give-up terminal must stand, got %q", got.Stop)
	}
	if notes := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(notes) != 0 {
		t.Fatalf("a StopNoProgress give-up must not draw the bg-pending nudge, found %d harness notes", len(notes))
	}
	var endStop session.StopReason
	for _, ev := range evs {
		if ev.Type == session.EvSubagentEnd && ev.Subagent != nil {
			endStop = ev.Subagent.Stop
		}
	}
	if endStop != session.StopCancelled {
		t.Fatalf("the live child must be drain-cancelled on the give-up terminal, got %q", endStop)
	}
}
