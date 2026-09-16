package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// resumeArgs builds the args JSON for a resume Subagent call.
func resumeArgs(id, promptText string) string {
	return fmt.Sprintf(`{"resume":%q,"prompt":%q}`, id, promptText)
}

// TestSubagentResumeContinuesPriorConversation proves the core resume contract: a fresh
// run persists, then a resume call reloads that conversation, the child's replayed
// history contains the ORIGINAL prompt+answer AND a new user message that starts with the
// staleness note, the trailer id is the SAME, and the store holds the grown session.
func TestSubagentResumeContinuesPriorConversation(t *testing.T) {
	store := memstore.New()

	// Capture the SECOND child run's replayed history (the resume's first request).
	var mu sync.Mutex
	var resumeReqMsgs []session.Message
	var childRun int
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		childRun++
		// Run 1 = fresh (1 request). Run 2 = resume (its first request is the 2nd overall).
		if childRun == 2 {
			resumeReqMsgs = req.Messages
		}
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("FIRST_ANSWER"),
		mockllm.TextTurn("SECOND_ANSWER"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	// Fresh run (childID = subagent-p1).
	fresh := runOneSubagent(t, task, "p1", `{"prompt":"investigate the FIRST_PROMPT"}`)
	freshID := extractAgentID(t, fresh.Content)
	if freshID != "subagent-p1" {
		t.Fatalf("fresh agentId = %q, want subagent-p1", freshID)
	}

	// Resume run (different parent call id p2, but resume forces childID = subagent-p1).
	resumed := runOneSubagent(t, task, "p2", resumeArgs("subagent-p1", "now do the SECOND_PROMPT"))
	if resumed.IsError {
		t.Fatalf("resume errored: %q", resumed.Content)
	}
	resumedID := extractAgentID(t, resumed.Content)
	if resumedID != "subagent-p1" {
		t.Fatalf("resumed agentId = %q, want subagent-p1 (verbatim, same id)", resumedID)
	}

	// The resume request must replay the prior conversation: original prompt + answer.
	mu.Lock()
	msgs := resumeReqMsgs
	mu.Unlock()
	var sawOrig, sawOrigAnswer, sawStaleNote bool
	for _, m := range msgs {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "FIRST_PROMPT") {
			sawOrig = true
		}
		if m.Role == session.RoleAssistant && strings.Contains(m.Text, "FIRST_ANSWER") {
			sawOrigAnswer = true
		}
		if m.Role == session.RoleUser && strings.HasPrefix(m.Text, "[harness note: your conversation has been resumed") {
			sawStaleNote = true
		}
	}
	if !sawOrig || !sawOrigAnswer {
		t.Fatalf("resume did not replay prior conversation (prompt=%v answer=%v): %+v", sawOrig, sawOrigAnswer, msgs)
	}
	if !sawStaleNote {
		t.Fatalf("resume's new user message must start with the staleness note; msgs=%+v", msgs)
	}

	// Re-save guard: the store holds the GROWN session (both answers in history).
	sess, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("resumed child not re-persisted: %v", err)
	}
	var grownFirst, grownSecond bool
	for _, m := range sess.Conversation.Messages {
		if strings.Contains(m.Text, "FIRST_ANSWER") {
			grownFirst = true
		}
		if strings.Contains(m.Text, "SECOND_ANSWER") {
			grownSecond = true
		}
	}
	if !grownFirst || !grownSecond {
		t.Fatalf("re-saved session is not the grown conversation (first=%v second=%v)", grownFirst, grownSecond)
	}
}

// saveCountingStore wraps an inner store, counting Saves so a test can pin
// WHEN a persist happened relative to the drive.
type saveCountingStore struct {
	inner port.SessionStore
	saves atomic.Int32
}

func (c *saveCountingStore) Save(ctx context.Context, s *session.Session) error {
	c.saves.Add(1)
	return c.inner.Save(ctx, s)
}

func (c *saveCountingStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return c.inner.Load(ctx, id)
}

// TestSubagentResumePersistsAtResumeStart pins the RESUME-START persist
// (issue #38): children otherwise persist only at their TERMINAL, so a resumed
// child loaded for a new long run would keep its OLD snapshot ModifiedAt and
// the composition layer's child-session GC age pass could delete it MID-RUN.
// The fix re-saves the loaded session at resume start; this test asserts the
// store saw that Save BEFORE the resumed drive's first LLM request (i.e.
// strictly before the resumed run could complete).
func TestSubagentResumePersistsAtResumeStart(t *testing.T) {
	store := &saveCountingStore{inner: memstore.New()}

	// Record how many Saves had landed when the RESUMED drive's first LLM
	// request arrives (child run #2 — run #1 is the fresh child).
	var mu sync.Mutex
	var childRun int
	savesAtResumeDrive := int32(-1)
	obs := func(port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		childRun++
		if childRun == 2 {
			savesAtResumeDrive = store.saves.Load()
		}
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("FIRST_ANSWER"),
		mockllm.TextTurn("SECOND_ANSWER"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)), agent.WithSubagentStore(store))

	fresh := runOneSubagent(t, task, "p1", `{"prompt":"do the first thing"}`)
	if fresh.IsError {
		t.Fatalf("fresh run errored: %q", fresh.Content)
	}
	savesAfterFresh := store.saves.Load()
	if savesAfterFresh < 1 {
		t.Fatalf("fresh run persisted %d times, want at least the terminal save", savesAfterFresh)
	}

	resumed := runOneSubagent(t, task, "p2", resumeArgs("subagent-p1", "continue"))
	if resumed.IsError {
		t.Fatalf("resume errored: %q", resumed.Content)
	}
	mu.Lock()
	got := savesAtResumeDrive
	mu.Unlock()
	if got < 0 {
		t.Fatal("the resumed drive never issued an LLM request")
	}
	if got <= savesAfterFresh {
		t.Fatalf("store saw %d Saves when the resumed drive started (had %d after the fresh run): the resume path must re-persist at RESUME START, before driving — otherwise the GC age pass can delete the stale snapshot mid-run", got, savesAfterFresh)
	}
}

// TestSubagentResumeAfterMaxTurns proves a child that stopped at its max-turns limit
// (terminal completed) is resumable via the Reopen path.
func TestSubagentResumeAfterMaxTurns(t *testing.T) {
	store := memstore.New()
	// A child that always calls a loop tool; MaxTurns:1 trips it on the first turn with
	// no summary. The issue-#48 salvage then drives ONE bounded wrap-up turn (cursor 2),
	// in which the child emits a partial summary; cursor 3 is the RESUME continuation.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Loop", `{}`)),
		mockllm.TextTurn("PARTIAL_SALVAGE"),
		mockllm.TextTurn("RESUMED_DONE"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, loopTool()))
	task := agent.NewSubagentTool(childEngine,
		agent.WithSubagentStore(store),
		agent.WithChildLimits(session.Limits{MaxTurns: 1, MaxToolCalls: 40, MaxConsecutiveFailures: 3}))

	fresh := runOneSubagent(t, task, "p1", `{"prompt":"loop"}`)
	if !strings.Contains(fresh.Content, "max-turns") {
		t.Fatalf("fresh run should have hit max-turns, got %q", fresh.Content)
	}
	// The salvage must have filled in the partial summary rather than the empty placeholder.
	if !strings.Contains(fresh.Content, "PARTIAL_SALVAGE") {
		t.Fatalf("fresh max-turns run should have salvaged a partial summary, got %q", fresh.Content)
	}
	sess, _ := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if sess.State != session.StateCompleted {
		t.Fatalf("max-turns child should be completed (clean terminal), got %q", sess.State)
	}

	resumed := runOneSubagent(t, task, "p2", resumeArgs("subagent-p1", "continue"))
	if resumed.IsError {
		t.Fatalf("resume after max-turns errored: %q", resumed.Content)
	}
	if !strings.Contains(resumed.Content, "RESUMED_DONE") {
		t.Fatalf("resumed child did not continue (want RESUMED_DONE), got %q", resumed.Content)
	}
}

// TestSubagentResumeCancelledInterrupts proves a child cancelled mid-tool-call (a timeout)
// is recovered via Interrupt — and the replayed history has NO dangling tool_use.
func TestSubagentResumeCancelledInterrupts(t *testing.T) {
	store := memstore.New()

	var mu sync.Mutex
	var resumeReqMsgs []session.Message
	var childRun int
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		childRun++
		if childRun == 2 {
			resumeReqMsgs = req.Messages
		}
	}
	// First run: ONE turn calling a tool that blocks until ctx cancel, so the short
	// timeout cancels the child MID-tool-call (an orphaned tool_use). Second run (resume,
	// turn 2 of the shared script): a clean text turn — reached only after Interrupt repairs
	// the orphan. A tool that blocks past the timeout (not sleeps-then-returns) guarantees
	// the cancel lands mid-dispatch deterministically.
	turns := []mockllm.Turn{
		mockllm.ToolCallTurn(toolCall("k", "Block", `{}`)),
		mockllm.TextTurn("RESUMED_AFTER_CANCEL"),
	}
	childLLM := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(obs)}, turns...)
	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	childEngine := childEngineWith(childLLM, catalogWith(t, block))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	// Fresh run with a short timeout: the tool blocks past it → cancelled mid-tool-call.
	timedOut := runOneSubagent(t, task, "p1", `{"prompt":"loop forever","timeout_ms":120}`)
	if !timedOut.IsError || !strings.Contains(timedOut.Content, "time budget") {
		t.Fatalf("fresh run should have timed out, got %+v", timedOut)
	}
	sess, _ := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if sess.State != session.StateCancelled {
		t.Fatalf("timed-out child should be cancelled, got %q", sess.State)
	}

	// Resume: Interrupt repairs the orphaned tool call, then the clean text turn finishes.
	resumed := runOneSubagent(t, task, "p2", resumeArgs("subagent-p1", "continue after cancel"))
	if resumed.IsError {
		t.Fatalf("resume after cancel errored: %q", resumed.Content)
	}
	if !strings.Contains(resumed.Content, "RESUMED_AFTER_CANCEL") {
		t.Fatalf("resumed child did not continue, got %q", resumed.Content)
	}
	// The replayed history (the resume's request) must have no dangling tool_use.
	mu.Lock()
	msgs := resumeReqMsgs
	mu.Unlock()
	assertNoOrphanedToolCalls(t, msgs)
}

// TestSubagentResumeFailedRecovers is the #318 core contract, the inverse of the
// refusal this test used to pin: a child that ended in StopError (persisted `failed`) IS
// resumable. resolveResumeSession recovers it to idle, the child is driven for real, its
// prior conversation is replayed, and the continuation is re-persisted.
func TestSubagentResumeFailedRecovers(t *testing.T) {
	store := memstore.New()

	var mu sync.Mutex
	var resumeReqMsgs []session.Message
	var childRun int
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		childRun++
		if childRun == 2 {
			resumeReqMsgs = req.Messages
		}
	}
	// Turn 1 crashes the child (empty turn + a StopError stop chunk = the shape a
	// terminal provider failure lands in); turn 2 is the resumed child's real answer.
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.EmptyTurnWithStop(session.StopError),
		mockllm.TextTurn("RESUMED_AFTER_FAILURE"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	failed := runOneSubagent(t, task, "p1", `{"prompt":"investigate the FIRST_PROMPT"}`)
	if !failed.IsError {
		t.Fatalf("crashed child should render a tool error, got %+v", failed)
	}
	sess, _ := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if sess.State != session.StateFailed {
		t.Fatalf("errored child should be persisted failed, got %q", sess.State)
	}

	resumed := runOneSubagent(t, task, "p2", resumeArgs("subagent-p1", "continue after the failure"))
	if resumed.IsError {
		t.Fatalf("resume of a FAILED child must now succeed (issue #318), got: %q", resumed.Content)
	}
	if !strings.Contains(resumed.Content, "RESUMED_AFTER_FAILURE") {
		t.Fatalf("resumed child did not continue, got %q", resumed.Content)
	}
	if got := extractAgentID(t, resumed.Content); got != "subagent-p1" {
		t.Fatalf("resumed agentId = %q, want the same subagent-p1", got)
	}

	// The recovered session replays the prior conversation, so the delegation's
	// accumulated context — the whole reason #318 is a bug — actually survives.
	mu.Lock()
	msgs := resumeReqMsgs
	mu.Unlock()
	var sawOrig bool
	for _, m := range msgs {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "FIRST_PROMPT") {
			sawOrig = true
		}
	}
	if !sawOrig {
		t.Fatalf("recovered child did not replay its prior conversation: %+v", msgs)
	}
	assertNoOrphanedToolCalls(t, msgs)

	// And the continuation is re-persisted as a normal (idle-recovered, then completed)
	// session, not left failed.
	after, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("resumed child not re-persisted: %v", err)
	}
	if after.State != session.StateCompleted {
		t.Fatalf("re-persisted state = %q, want completed", after.State)
	}
}

// TestSubagentResumeFailedTightensLimits proves the recovery arm still runs the SAME
// downstream contract as the other two terminals: the loaded session's preserved Limits
// are tightened by the per-call args (tighten-only), never loosened. A stored
// MaxToolCalls of 1 must still bind a recovered child even when the resume call asks for
// more.
func TestSubagentResumeFailedTightensLimits(t *testing.T) {
	store := memstore.New()
	// Seed a FAILED child whose stored limits are tighter than the resume call's.
	seed := session.New("subagent-p1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		session.Limits{MaxTurns: 2, MaxToolCalls: 1}, time.Now())
	if err := seed.BeginTurn(); err != nil {
		t.Fatalf("seed BeginTurn: %v", err)
	}
	if err := seed.RecordAssistant(session.NewAssistantMessage("seed answer", "", nil)); err != nil {
		t.Fatalf("seed RecordAssistant: %v", err)
	}
	if err := seed.Fail(); err != nil {
		t.Fatalf("seed Fail: %v", err)
	}
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	childLLM := mockllm.New(mockllm.TextTurn("RECOVERED"))
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p2",
		`{"resume":"subagent-p1","prompt":"continue","max_turns":50,"max_tool_calls":99}`)
	if res.IsError {
		t.Fatalf("resume of a failed child errored: %q", res.Content)
	}
	after, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("resumed child not re-persisted: %v", err)
	}
	if after.Limits.MaxTurns != 2 || after.Limits.MaxToolCalls != 1 {
		t.Fatalf("recovered child limits = %+v, want the stored (tighter) {MaxTurns:2 MaxToolCalls:1} — a per-call arg must only tighten", after.Limits)
	}
}

// TestSubagentResumeWithAgentRejected proves resume+agent is rejected with the exact
// exclusivity copy and no child runs.
func TestSubagentResumeWithAgentRejected(t *testing.T) {
	store := memstore.New()
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("X")), catalogWith(t))
	reviewerEngine := childEngineWith(mockllm.New(mockllm.TextTurn("R")), catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine,
		agent.WithSubagentStore(store),
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": reviewerEngine},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}))

	res := runOneSubagent(t, task, "p1", `{"resume":"subagent-x","prompt":"go","agent":"reviewer"}`)
	if !res.IsError {
		t.Fatalf("resume+agent must error, got %+v", res)
	}
	want := "Subagent: `resume` cannot be combined with `agent` or `model` — a resumed subagent continues on the default explorer engine"
	if !strings.Contains(res.Content, want) {
		t.Fatalf("exclusivity copy mismatch, got %q", res.Content)
	}
}

// TestSubagentResumeWithModelRejected proves resume+model is rejected with the exact
// exclusivity copy and no child runs.
func TestSubagentResumeWithModelRejected(t *testing.T) {
	store := memstore.New()
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("X")), catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine,
		agent.WithSubagentStore(store),
		agent.WithSubagentEngineFactory(func(string) (*agent.Engine, bool) {
			return childEngineWith(mockllm.New(mockllm.TextTurn("M")), catalogWith(t)), true
		}))

	res := runOneSubagent(t, task, "p1", `{"resume":"subagent-x","prompt":"go","model":"gpt-x"}`)
	if !res.IsError {
		t.Fatalf("resume+model must error, got %+v", res)
	}
	want := "Subagent: `resume` cannot be combined with `agent` or `model` — a resumed subagent continues on the default explorer engine"
	if !strings.Contains(res.Content, want) {
		t.Fatalf("exclusivity copy mismatch, got %q", res.Content)
	}
}

// TestSubagentResumeUnknownIDErrors proves a well-formed-but-unknown subagent id is a
// clean not-found error.
func TestSubagentResumeUnknownIDErrors(t *testing.T) {
	store := memstore.New()
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("X")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p1", resumeArgs("subagent-nope", "continue"))
	if !res.IsError {
		t.Fatalf("resume of an unknown id must error, got %+v", res)
	}
	want := `Subagent: no subagent found for resume id "subagent-nope"; use the id exactly as shown on the 'agentId:' line of a previous Subagent result`
	if !strings.Contains(res.Content, want) {
		t.Fatalf("not-found copy mismatch, got %q", res.Content)
	}
}

// TestSubagentResumeNoStoreRejected proves resume is unsupported when no store is wired.
func TestSubagentResumeNoStoreRejected(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("X")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine) // no WithSubagentStore

	res := runOneSubagent(t, task, "p1", resumeArgs("subagent-x", "continue"))
	if !res.IsError {
		t.Fatalf("resume without a store must error, got %+v", res)
	}
	want := "Subagent: `resume` is not supported in this deployment (no session store wired)"
	if !strings.Contains(res.Content, want) {
		t.Fatalf("no-store copy mismatch, got %q", res.Content)
	}
}

// TestSubagentConcurrentResumeGuard proves the in-flight guard rejects a SECOND concurrent
// run on the same id with the exact already-running copy, and that a fresh run can be
// concurrently resume-guarded too.
func TestSubagentConcurrentResumeGuard(t *testing.T) {
	store := memstore.New()
	// Seed a completed child so the resume can load it.
	seed := session.New("subagent-p1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := seed.BeginTurn(); err != nil {
		t.Fatalf("seed BeginTurn: %v", err)
	}
	if err := seed.RecordAssistant(session.NewAssistantMessage("seed answer", "", nil)); err != nil {
		t.Fatalf("seed RecordAssistant: %v", err)
	}
	if err := seed.Complete(); err != nil {
		t.Fatalf("seed Complete: %v", err)
	}
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	// A blocking child tool so the first resume stays in-flight while the second arrives.
	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	childLLM := mockllm.New(mockllm.ToolCallTurn(toolCall("k", "Block", `{}`)))
	childEngine := childEngineWith(childLLM, catalogWith(t, block))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type out struct {
		res session.ToolResult
		err error
	}
	results := make(chan out, 1)
	go func() {
		res, err := task.Execute(ctx,
			session.NewToolCall("a1", "Subagent", json.RawMessage(resumeArgs("subagent-p1", "first"))),
			agent.MemEnv("/ws"))
		results <- out{res, err}
	}()

	// Wait until the first resume is in-flight (its child is blocked in the tool).
	select {
	case <-block.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first resume never entered the blocking tool")
	}

	// Second concurrent resume on the SAME id must be rejected immediately.
	second, err := task.Execute(context.Background(),
		session.NewToolCall("a2", "Subagent", json.RawMessage(resumeArgs("subagent-p1", "second"))),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("second resume transport error: %v", err)
	}
	if !second.IsError {
		t.Fatalf("second concurrent resume must be an error, got %+v", second)
	}
	want := `Subagent: subagent "subagent-p1" is already running; wait for its result before resuming it`
	if !strings.Contains(second.Content, want) {
		t.Fatalf("already-running copy mismatch, got %q", second.Content)
	}

	// Release the first resume.
	cancel()
	<-results
}

// TestSubagentResumeBudgetTightenOnly mirrors TestSubagentPerCallMaxTokensTightenOnly for
// the resume path: a tight operator budget still trips even with a generous per-call
// max_run_tokens on the resume call.
func TestSubagentResumeBudgetTightenOnly(t *testing.T) {
	store := memstore.New()
	// Seed a completed child.
	seed := session.New("subagent-p1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	_ = seed.BeginTurn()
	_ = seed.RecordAssistant(session.NewAssistantMessage("seed", "", nil))
	_ = seed.Complete()
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	childLLM := &runawayProvider{perTurn: session.Usage{InputTokens: 60, OutputTokens: 40}}
	childEngine := agent.NewEngine(agent.Deps{
		LLM:          childLLM,
		Catalog:      catalogWith(t, loopTool()),
		Policy:       allowAll(),
		Model:        "child-model",
		MaxRunTokens: 250, // tight operator budget
	})
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p2", `{"resume":"subagent-p1","prompt":"run forever","max_run_tokens":100000}`)
	if res.IsError {
		t.Fatalf("budget-stopped resume should be a clean result, got %+v", res)
	}
	if !strings.Contains(res.Content, "token budget") {
		t.Fatalf("the tight operator budget must still trip on resume (tighten-only), got %q", res.Content)
	}
	if got := childLLM.calls.Load(); got > 10 {
		t.Fatalf("resumed child made %d model calls; per-call max_run_tokens must not loosen the operator budget", got)
	}
}

// TestSubagentResumeStructuredOutput proves resume + a fresh output_schema works and that
// the staleness note rides INSIDE the structured-output wrap.
func TestSubagentResumeStructuredOutput(t *testing.T) {
	store := memstore.New()
	// Seed a completed child.
	seed := session.New("subagent-p1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	_ = seed.BeginTurn()
	_ = seed.RecordAssistant(session.NewAssistantMessage("seed", "", nil))
	_ = seed.Complete()
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	var mu sync.Mutex
	var firstReqText string
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		if firstReqText == "" {
			// The resume's first request: its LAST user message is the structured prompt.
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == session.RoleUser {
					firstReqText = req.Messages[i].Text
					break
				}
			}
		}
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.ToolCallTurn(toolCall("k1", "SubmitResult", `{"name":"Ada","age":36}`)),
		mockllm.TextTurn("done"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p2",
		`{"resume":"subagent-p1","prompt":"profile Ada","output_schema":`+personSchema+`}`)
	if res.IsError {
		t.Fatalf("resume + structured output errored: %q", res.Content)
	}
	if !strings.Contains(res.Content, `"Ada"`) {
		t.Fatalf("resume structured result missing payload, got %q", res.Content)
	}
	mu.Lock()
	txt := firstReqText
	mu.Unlock()
	if !strings.Contains(txt, "[harness note: your conversation has been resumed") {
		t.Fatalf("staleness note must ride inside the structured-output prompt, got %q", txt)
	}
}

// TestSubagentResumeTeamMemberIDRejected is the ADVERSARIAL test: a team member session is
// seeded under "team-p1-worker"; resuming it through Subagent is rejected by the prefix
// gate (only subagent ids are resumable) and nothing is driven.
func TestSubagentResumeTeamMemberIDRejected(t *testing.T) {
	store := memstore.New()
	member := session.New("team-p1-worker", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	_ = member.BeginTurn()
	_ = member.RecordAssistant(session.NewAssistantMessage("member work", "", nil))
	_ = member.Complete()
	if err := store.Save(context.Background(), member); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	// A child engine whose run would be observable if (wrongly) driven.
	childLLM := mockllm.New(mockllm.TextTurn("SHOULD_NOT_RUN"))
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p1", resumeArgs("team-p1-worker", "continue"))
	if !res.IsError {
		t.Fatalf("resuming a team member id must error, got %+v", res)
	}
	want := `Subagent: resume id "team-p1-worker" is not a subagent session; only ids from a Subagent result's 'agentId:' line can be resumed (team member transcripts are read-only via InspectMember)`
	if !strings.Contains(res.Content, want) {
		t.Fatalf("non-subagent-session copy mismatch, got %q", res.Content)
	}
	// The prefix gate fires BEFORE any load/drive: the child engine never ran.
	if childLLM.Calls() != 0 {
		t.Fatalf("child engine was driven (%d calls) despite a rejected team-member resume", childLLM.Calls())
	}
}

// TestParentResumesSubagentByTrailerID is the MODEL-FACING e2e: the parent (turn 1) calls
// Subagent; (turn 2) extracts the trailer id and calls Subagent with resume=<id>; (turn 3)
// finishes. Both trailers are byte-identical, and the child's resume request replays the
// prior context.
func TestParentResumesSubagentByTrailerID(t *testing.T) {
	store := memstore.New()

	var mu sync.Mutex
	var resumeReqMsgs []session.Message
	var childRun int
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		childRun++
		if childRun == 2 {
			resumeReqMsgs = req.Messages
		}
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("CHILD_FIRST_MARKER"),
		mockllm.TextTurn("CHILD_RESUMED_MARKER"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	subTool := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))
	parentCat := catalogWith(t, subTool)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent",
			json.RawMessage(`{"prompt":"trace the FIRST code path"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("p2", "Subagent",
			json.RawMessage(`{"resume":"subagent-s1-p1","prompt":"continue"}`))),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	sess := newSession(t, session.Limits{})

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	// Collect BOTH Subagent tool results in order.
	var subResults []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			subResults = append(subResults, ev.ToolResult)
		}
	}
	if len(subResults) != 2 {
		t.Fatalf("parent saw %d Subagent results, want 2: %v", len(subResults), typesOf(evs))
	}
	firstID := extractAgentID(t, subResults[0].Content)
	secondID := extractAgentID(t, subResults[1].Content)
	if firstID != "subagent-s1-p1" {
		t.Fatalf("first trailer id = %q, want subagent-s1-p1", firstID)
	}
	if secondID != firstID {
		t.Fatalf("second trailer id = %q, want byte-identical to the first %q", secondID, firstID)
	}
	if subResults[1].IsError {
		t.Fatalf("resume result errored: %q", subResults[1].Content)
	}
	if !strings.Contains(subResults[1].Content, "CHILD_RESUMED_MARKER") {
		t.Fatalf("resume result is not the continued child output: %q", subResults[1].Content)
	}

	// The observer proves the resume's request replayed the prior context.
	mu.Lock()
	msgs := resumeReqMsgs
	mu.Unlock()
	var sawFirst bool
	for _, m := range msgs {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "FIRST code path") {
			sawFirst = true
		}
	}
	if !sawFirst {
		t.Fatalf("resume request did not replay the prior context: %+v", msgs)
	}
}

// TestSubagentResumeForkerRehomesAndFailsFast pins the two forker-facing resume
// behaviours with a recording stub forker (fresh fork root ≠ the seed's recorded
// workspace):
//
//	(a) after a successful resume, the RE-PERSISTED session's Workspace equals the NEW
//	    fork root — Session.Rehome's actual effect (field consistency: without it the
//	    snapshot would keep recording the dead original root);
//	(b) a resume with an unknown or failed id NEVER invokes the forker — the load +
//	    terminal recovery fail fast BEFORE the fork;
//	(c) the resumed child's prompt cwd comes from the engine's PromptConfig (the BASE
//	    cwd), NOT from the re-homed Workspace — pinning the real prompt behaviour so a
//	    future loop.go "simplification" can't silently change it.
func TestSubagentResumeForkerRehomesAndFailsFast(t *testing.T) {
	store := memstore.New()
	// Seed a completed child whose recorded workspace is the (now-dead) original root.
	seed := session.New("subagent-p1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/dead/original-worktree", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	_ = seed.BeginTurn()
	_ = seed.RecordAssistant(session.NewAssistantMessage("seed answer", "", nil))
	_ = seed.Complete()
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	// And a still-RUNNING child (the shape a process that died mid-turn leaves behind)
	// for the fail-fast case: StateRunning hits resolveResumeSession's default arm, the
	// one state that is still not resumable. (StateFailed is NOT usable here anymore —
	// issue #318 made it recover, so it forks like any other resume.)
	runningSeed := session.New("subagent-pr", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/dead/original-worktree", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := runningSeed.BeginTurn(); err != nil {
		t.Fatalf("running-seed BeginTurn: %v", err)
	}
	if err := store.Save(context.Background(), runningSeed); err != nil {
		t.Fatalf("running-seed save: %v", err)
	}

	// Mirror the real composition: the child engine's PromptConfig pre-populates the
	// prompt Env.Cwd with the BASE workspace; durable placement identity is not a
	// prompt-path fallback (assertion (c) depends on this wired shape).
	const baseCwd = "/base-cwd"
	var mu sync.Mutex
	var suffixes []string
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		suffixes = append(suffixes, req.System.VolatileSuffix)
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("RESUMED_IN_FORK"),
	)
	childEngine := agent.NewEngine(agent.Deps{
		LLM:          childLLM,
		Catalog:      catalogWith(t),
		Policy:       allowAll(),
		Model:        "child-model",
		PromptConfig: prompt.Config{Env: prompt.Env{Cwd: baseCwd}},
	})
	forker := &recordingSubagentForker{}
	task := agent.NewSubagentTool(childEngine,
		agent.WithSubagentStore(store),
		agent.WithChildForker(forker))

	// (b) FAIL FAST: an unknown id and a non-resumable (still running) id must never
	// reach the forker.
	unknown := runOneSubagent(t, task, "p2", resumeArgs("subagent-nope", "continue"))
	if !unknown.IsError {
		t.Fatalf("unknown-id resume must error, got %+v", unknown)
	}
	runningRes := runOneSubagent(t, task, "p3", resumeArgs("subagent-pr", "continue"))
	if !runningRes.IsError {
		t.Fatalf("running-id resume must error, got %+v", runningRes)
	}
	forker.mu.Lock()
	preForks := len(forker.labels)
	forker.mu.Unlock()
	if preForks != 0 {
		t.Fatalf("forker was invoked %d time(s) for unresumable ids; load + recovery must fail BEFORE the fork", preForks)
	}

	// (a) Successful resume: the fork root is handed out and the RE-PERSISTED session's
	// Workspace records IT, not the dead original root. The description pins the fork
	// label (the recording forker roots forks at "/fork/<label>").
	res := runOneSubagent(t, task, "p4",
		`{"resume":"subagent-p1","prompt":"continue","description":"resume probe"}`)
	if res.IsError {
		t.Fatalf("forker-wired resume errored: %q", res.Content)
	}
	forker.mu.Lock()
	forks := len(forker.labels)
	forker.mu.Unlock()
	if forks != 1 {
		t.Fatalf("forker invoked %d time(s) for a successful resume, want 1", forks)
	}
	const forkRoot = "/fork/resume probe"
	persisted, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("resumed child not re-persisted: %v", err)
	}
	wantRef := session.EnvironmentRef{Kind: session.EnvKindMem, ID: forkRoot, Revision: "test-v1"}
	if persisted.EnvironmentRef != wantRef {
		t.Fatalf("re-persisted EnvironmentRef = %+v, want %+v (Rehome's effect)", persisted.EnvironmentRef, wantRef)
	}

	// (c) The resumed child's prompt cwd is the BASE cwd from PromptConfig — the
	// re-homed Workspace must NOT leak into the prompt env.
	mu.Lock()
	defer mu.Unlock()
	if len(suffixes) == 0 {
		t.Fatal("child provider never received a request")
	}
	for i, sfx := range suffixes {
		if !strings.Contains(sfx, baseCwd) {
			t.Errorf("request %d prompt env missing the configured base cwd %q:\n%s", i, baseCwd, sfx)
		}
		if strings.Contains(sfx, forkRoot) {
			t.Errorf("request %d prompt env leaked the fork root %q (cwd must come from PromptConfig):\n%s", i, forkRoot, sfx)
		}
	}
}

// TestSubagentResumePreservesStoredLimits is the limits-preserved nuance (item 12): the
// resumed session keeps its STORED (tighter) Limits; a per-call arg only tightens. A
// stored MaxToolCalls:1 must still bound the resumed run even with a looser per-call value.
func TestSubagentResumePreservesStoredLimits(t *testing.T) {
	store := memstore.New()
	// Seed a completed child with a TIGHT stored MaxToolCalls of 1.
	seed := session.New("subagent-p1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		session.Limits{MaxTurns: 12, MaxToolCalls: 1, MaxConsecutiveFailures: 3}, time.Now())
	_ = seed.BeginTurn()
	_ = seed.RecordAssistant(session.NewAssistantMessage("seed", "", nil))
	_ = seed.Complete()
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// A child that keeps calling a loop tool; the stored MaxToolCalls:1 must stop it.
	var turns []mockllm.Turn
	for i := 0; i < 20; i++ {
		turns = append(turns, mockllm.ToolCallTurn(toolCall(fmt.Sprintf("k%d", i), "Loop", `{}`)))
	}
	turns = append(turns, mockllm.TextTurn("never reached"))
	var loopCalls atomic.Int64
	countingLoop := &fakeTool{name: "Loop", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			loopCalls.Add(1)
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	childEngine := childEngineWith(mockllm.New(turns...), catalogWith(t, countingLoop))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	// A LOOSER per-call max_tool_calls must NOT raise the stored bound (tighten-only).
	res := runOneSubagent(t, task, "p2", `{"resume":"subagent-p1","prompt":"continue","max_tool_calls":100}`)
	if !strings.Contains(res.Content, "max-tool-calls") {
		t.Fatalf("stored tight MaxToolCalls must still trip (per-call only tightens), got %q", res.Content)
	}
	if got := loopCalls.Load(); got > 2 {
		t.Fatalf("loop tool ran %d times; stored MaxToolCalls:1 should have bounded it", got)
	}
}

// TestSubagentResumeBudgetCarriesPriorSpend is the resume-leg of the restart-budget
// property (cloud-native Phase 1, QA SHOULD-ADD): a persisted child whose cumulative
// Usage is already at/over the engine's MaxRunTokens ceiling, when RESUMED, must trip
// StopBudget at the FIRST boundary — starting from its PRIOR spend, never re-granting a
// fresh budget. This is the same property the main e2e proves for the parent, exercised
// through the subagent resume path (reload + Reopen, where resetToIdle preserves Usage).
// Mutation: making resetToIdle zero Usage clears the reloaded spend on the resume Reopen,
// so the resumed child re-grants a full budget, runs its turn, and ends StopEndTurn
// instead of StopBudget.
func TestSubagentResumeBudgetCarriesPriorSpend(t *testing.T) {
	const budget = 350
	store := memstore.New()

	// Persist a COMPLETED child carrying prior spend over the ceiling (the snapshot a
	// prior, budget-heavy run would have saved). 400 >= 350.
	prior := session.New("subagent-p1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		session.Limits{}, time.Unix(0, 0))
	for _, step := range []struct {
		op  string
		err error
	}{
		{"BeginTurn", prior.BeginTurn()},
		{"RecordUsage", prior.RecordUsage(session.Usage{InputTokens: 250, OutputTokens: 150})},
		{"RecordAssistant", prior.RecordAssistant(session.NewAssistantMessage("prior work", "", nil))},
		{"Complete", prior.Complete()},
	} {
		if step.err != nil {
			t.Fatalf("seed %s: %v", step.op, step.err)
		}
	}
	if err := store.Save(context.Background(), prior); err != nil {
		t.Fatalf("seed persist: %v", err)
	}

	// The resume engine carries the SAME ceiling. Count model calls: a budget trip at the
	// first boundary means ZERO model calls on the resumed run.
	var calls atomic.Int64
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { calls.Add(1) })},
		mockllm.TextTurn("should-not-run"),
	)
	childEngine := agent.NewEngine(agent.Deps{
		LLM:          childLLM,
		Catalog:      catalogWith(t),
		Policy:       allowAll(),
		Model:        "child-model",
		MaxRunTokens: budget,
	})
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p2", resumeArgs("subagent-p1", "continue the work"))
	if res.IsError {
		t.Fatalf("budget-stopped resume must be a clean result, got error: %q", res.Content)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("resumed child made %d model call(s); want 0 (the carried-over budget must trip at the first boundary)", got)
	}
	// The resumed child re-persisted carries the prior spend (it was not zeroed on reload).
	reloaded, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("resumed child not re-persisted: %v", err)
	}
	if reloaded.Usage.TotalTokens() < budget {
		t.Fatalf("re-persisted resumed child usage = %d, want >= prior spend %d (the budget did not carry)", reloaded.Usage.TotalTokens(), budget)
	}
}
