package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// The failure-accurate close-out wording session.Session.Recover stamps on a tool call
// orphaned by a FAILED turn, and the cancellation wording it must never use (Interrupt
// owns that one). Both are unexported constants in engine/session; they are model-facing
// REPLAYED history, so the exact attribution is the contract being pinned here — a
// recovered failure must not claim a user cancelled anything.
const (
	recoverCloseOutWording   = "tool call aborted: the run failed before this call's result was recorded"
	interruptCloseOutWording = "tool call interrupted by cancellation"
)

// seedFailedChildWithOrphanedToolCall persists a subagent session in the ADVERSARIAL
// shape: it failed with a trailing assistant message carrying a tool call that never
// received a result. That is the history a turn which failed mid-dispatch leaves behind,
// and the exact shape that makes a naive "just set it back to idle" resume replay a
// dangling tool_use / function_call — a provider 400 rather than a continuation.
//
// It is seeded directly rather than produced by the live loop on purpose: the loop turns
// a tool's Execute error into a tool RESULT (dispatch.timeExecute), so an orphan at the
// moment of failure is a defence-in-depth case the live loop is not the only source of
// (a failing RecordToolResults is another). The seam under test is the resume path's
// repair, not how the orphan got there.
func seedFailedChildWithOrphanedToolCall(t *testing.T, store port.SessionStore, id session.SessionID, promptText string) session.ToolCallID {
	t.Helper()
	const orphanID = session.ToolCallID("orphan-1")
	seed := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "test-v1"}, session.Limits{}, time.Now())
	if err := seed.RecordUserPrompt(promptText, nil); err != nil {
		t.Fatalf("seed RecordUserPrompt: %v", err)
	}
	if err := seed.BeginTurn(); err != nil {
		t.Fatalf("seed BeginTurn: %v", err)
	}
	call := session.NewToolCall(orphanID, "Read", json.RawMessage(`{"path":"a.go"}`))
	if err := seed.RecordAssistant(session.NewAssistantMessage("let me read that file", "", []session.ToolCall{call})); err != nil {
		t.Fatalf("seed RecordAssistant: %v", err)
	}
	// No RecordToolResults: the turn failed before the result was recorded.
	if err := seed.Fail(); err != nil {
		t.Fatalf("seed Fail: %v", err)
	}
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	return orphanID
}

// TestSubagentResumeFailedRepairsOrphanedToolCall is the LOAD-BEARING adversarial test
// for issue #318: making a failed child resumable is only useful if the recovered
// transcript is provider-VALID. A failed turn that orphaned a tool call must, after
// resume, replay with that call answered — and answered with the FAILURE wording, never
// the cancellation wording (a recovered failure must not claim a user action that never
// happened). This is the difference between "resumable" and "resumable without a
// provider 400".
func TestSubagentResumeFailedRepairsOrphanedToolCall(t *testing.T) {
	store := memstore.New()
	orphanID := seedFailedChildWithOrphanedToolCall(t, store, "subagent-p1", "audit the ORIGINAL_TASK")

	var mu sync.Mutex
	var replayed []session.Message
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		replayed = req.Messages
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("CONTINUED_AFTER_REPAIR"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)),
		agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p2", resumeArgs("subagent-p1", "continue"))
	if res.IsError {
		t.Fatalf("resume of a failed child with an orphaned tool call errored: %q", res.Content)
	}
	if !strings.Contains(res.Content, "CONTINUED_AFTER_REPAIR") {
		t.Fatalf("recovered child did not continue: %q", res.Content)
	}

	mu.Lock()
	msgs := replayed
	mu.Unlock()
	if len(msgs) == 0 {
		t.Fatal("child provider never received a request")
	}
	// (a) The replayed history is provider-valid — the domain's own bidirectional
	//     pairing validator, not a hand-rolled approximation.
	if err := session.ValidateToolPairing(msgs); err != nil {
		t.Fatalf("recovered child replays an unpaired history (a provider 400): %v\nmsgs=%+v", err, msgs)
	}
	assertNoOrphanedToolCalls(t, msgs)

	// (b) The synthetic result answers the ORPHANED call with the FAILURE wording.
	var synthetic *session.ToolResult
	for _, m := range msgs {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == orphanID {
			synthetic = m.ToolResult
		}
	}
	if synthetic == nil {
		t.Fatalf("orphaned call %q was never closed out: %+v", orphanID, msgs)
	}
	if !strings.Contains(synthetic.Content, recoverCloseOutWording) {
		t.Fatalf("close-out must carry the FAILURE wording %q, got %q", recoverCloseOutWording, synthetic.Content)
	}
	// (c) …and NOT the cancellation wording: nobody cancelled this run.
	if strings.Contains(synthetic.Content, interruptCloseOutWording) {
		t.Fatalf("a recovered FAILURE must not be attributed to cancellation, got %q", synthetic.Content)
	}
}

// TestSubagentFailedResultAdvertisesResume is the DISCOVERABILITY half of issue #318,
// asserted through the REAL render path (a live parent loop's RECORDED tool result —
// what the model actually reads), not the helper in isolation: a failed delegation now
// TELLS the model it can be resumed, and names the handle to resume it with. Wiring
// Recover without this would ship a capability the model cannot discover (ADR 0070).
//
// It also pins the ORDERING the hint's own wording depends on: the agentId line comes
// BEFORE the hint, so "the agentId above" is literally accurate on the StopError layout
// (where the trailer is last, unlike the success family's leading trailer).
//
// The store is wired deliberately: it is validateResume's first precondition, so it is
// the configuration in which the advertised action can actually succeed. The store-less
// counterpart is TestStorelessSubagentFailureDoesNotAdvertiseResume.
func TestSubagentFailedResultAdvertisesResume(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(mockllm.EmptyTurnWithStop(session.StopError)), catalogWith(t))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(memstore.New()))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	body := results[0].Content
	if !strings.Contains(body, "resume it with the agentId above to continue") {
		t.Fatalf("a failed delegation must advertise the resume path (issue #318), got:\n%s", body)
	}
	idAt := strings.Index(body, "agentId: ")
	hintAt := strings.Index(body, "resume it with the agentId above")
	if idAt < 0 {
		t.Fatalf("the error result must keep the agentId trailer, got:\n%s", body)
	}
	if hintAt < idAt {
		t.Fatalf("the hint says \"the agentId above\" but the agentId line comes after it:\n%s", body)
	}
}

// TestStorelessSubagentFailureDoesNotAdvertiseResume is the SECOND negative of the
// discoverability rule, on the other precondition. validateResume's FIRST check is that a
// session store is wired; a SubagentTool built without WithSubagentStore is a supported
// construction for an engine-module consumer (ADR 0036), and in that deployment every
// `resume` call is refused with "not supported in this deployment". Advertising the resume
// path there would instruct the model to take an action that cannot succeed — the same
// defect as advertising it for a Parallel branch id, just a different precondition.
//
// The failure body itself must be UNCHANGED apart from the hint: the cause and the agentId
// trailer (for InspectSubagent) still ride every terminal.
func TestStorelessSubagentFailureDoesNotAdvertiseResume(t *testing.T) {
	const causeText = "upstream 503: model overloaded"
	childEngine := childEngineWith(mockllm.New(
		mockllm.ErrorTurn(errors.New(causeText), mockllm.TextChunk("thinking")),
	), catalogWith(t))
	// No WithSubagentStore: `resume` is unavailable in this deployment.
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	body := results[0].Content
	if strings.Contains(body, "resume it with the agentId") {
		t.Fatalf("a store-less deployment must NOT advertise resume (validateResume would refuse it), got:\n%s", body)
	}
	// The rest of the failed-delegation contract is untouched.
	if !strings.Contains(body, causeText) {
		t.Fatalf("the failure cause must still lead the body, got:\n%s", body)
	}
	if !strings.Contains(body, "agentId: ") {
		t.Fatalf("the agentId trailer rides every terminal (InspectSubagent's handle), got:\n%s", body)
	}
}

// TestParallelBranchFailureDoesNotAdvertiseResume is the negative of the test above, and
// the reason the resume hint lives in renderSubagentResult rather than inside the shared
// subagentErrorBody: a Parallel branch id is `parallel-<callID>-<n>`, which validateResume
// REJECTS (only `subagent-` ids resume; a branch is inspectable, not resumable). Telling
// the model to resume one would instruct it to take an action that cannot succeed — the
// inverse of the discoverability rule.
func TestParallelBranchFailureDoesNotAdvertiseResume(t *testing.T) {
	branchEngine := childEngineWith(mockllm.New(mockllm.EmptyTurnWithStop(session.StopError)), catalogWith(t))
	par := agent.NewParallelTool(branchEngine, &memForker{})

	results, _ := subagentParentResults(t, par,
		mockllm.ToolCallTurn(toolCall("c1", "Parallel", `{"tasks":["try approach A"]}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 recorded tool result, got %+v", results)
	}
	if !strings.Contains(results[0].Content, "[FAILED]") {
		t.Fatalf("expected the branch to be marked FAILED (the precondition for this guard):\n%s", results[0].Content)
	}
	if strings.Contains(results[0].Content, "resume it with the agentId") {
		t.Fatalf("a Parallel branch failure must NOT advertise resume (branch ids are not resumable), got:\n%s", results[0].Content)
	}
}

// TestParentResumesFailedSubagentByTrailerID is the model-facing e2e of the whole #318
// loop: the parent delegates, the child DIES on a provider failure, the parent reads the
// agentId off the failed result and resumes it, and the recovered child finishes the
// work. Nothing here reaches past the tool boundary — the parent uses only what the
// result text told it.
func TestParentResumesFailedSubagentByTrailerID(t *testing.T) {
	store := memstore.New()
	childLLM := mockllm.New(
		mockllm.EmptyTurnWithStop(session.StopError),
		mockllm.TextTurn("CHILD_FINISHED_AFTER_RESUME"),
	)
	subTool := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)),
		agent.WithSubagentStore(store))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent",
			json.RawMessage(`{"prompt":"trace the FIRST code path"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("p2", "Subagent",
			json.RawMessage(`{"resume":"subagent-s1-p1","prompt":"continue"}`))),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, subTool)})
	evs := drain(e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	var results []*session.ToolResult
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results = append(results, ev.ToolResult)
		}
	}
	if len(results) != 2 {
		t.Fatalf("parent saw %d Subagent results, want 2: %v", len(results), typesOf(evs))
	}
	if !results[0].IsError {
		t.Fatalf("the first delegation must fail: %+v", results[0])
	}
	failedID := extractAgentID(t, results[0].Content)
	if failedID != "subagent-s1-p1" {
		t.Fatalf("failed trailer id = %q, want subagent-s1-p1", failedID)
	}
	if results[1].IsError {
		t.Fatalf("resuming the failed child must succeed (issue #318), got: %q", results[1].Content)
	}
	if !strings.Contains(results[1].Content, "CHILD_FINISHED_AFTER_RESUME") {
		t.Fatalf("the resumed child did not finish the work: %q", results[1].Content)
	}
	if got := extractAgentID(t, results[1].Content); got != failedID {
		t.Fatalf("resumed trailer id = %q, want the same %q", got, failedID)
	}
}

// TestWritableResumeNoteSaysEditsSurvive pins the harness note a resumed WRITABLE child
// reads. A read-write child never forks (ADR 0041 — it edits the real tree in place), so
// on resume its earlier edits are STILL THERE. The read-only note ("file changes … are
// GONE") would be false, and false in the direction that defeats the point of #318: a
// direct-write child recovered from a transient failure must build ON its partial edits,
// not distrust or redo them.
func TestWritableResumeNoteSaysEditsSurvive(t *testing.T) {
	store := memstore.New()
	seedFailedChildWithOrphanedToolCall(t, store, "subagent-p1", "start implementing")

	var mu sync.Mutex
	var prompts []string
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range req.Messages {
			if m.Role == session.RoleUser {
				prompts = append(prompts, m.Text)
			}
		}
	}
	writable := childEngineWith(mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("KEPT_GOING"),
	), catalogWith(t))
	task := newWritableSubagent(t, writable,
		agent.WithSubagentStore(store),
		// A writable child must not fork, on resume as much as on a fresh call.
		agent.WithChildForker(&failingForker{t: t}))

	res := runOneSubagent(t, task, "p2",
		`{"resume":"subagent-p1","prompt":"continue","mode":"read-write"}`)
	if res.IsError {
		t.Fatalf("writable resume of a failed child errored: %q", res.Content)
	}

	mu.Lock()
	got := strings.Join(prompts, "\n")
	mu.Unlock()
	if !strings.Contains(got, "the file edits you already made are STILL IN PLACE") {
		t.Fatalf("a resumed WRITABLE child must be told its edits survived, got:\n%s", got)
	}
	if strings.Contains(got, "FRESH workspace checkout") {
		t.Fatalf("a resumed WRITABLE child must NOT be told its workspace is a fresh checkout, got:\n%s", got)
	}
}

// TestReadOnlyResumeNoteKeepsFreshCheckoutWording is the sibling non-regression: the
// read-only resume path still gets the fresh-checkout staleness note (its worktree really
// was torn down), so the writable carve-out above did not widen into the default path.
func TestReadOnlyResumeNoteKeepsFreshCheckoutWording(t *testing.T) {
	store := memstore.New()
	seedFailedChildWithOrphanedToolCall(t, store, "subagent-p1", "look around")

	var mu sync.Mutex
	var prompts []string
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range req.Messages {
			if m.Role == session.RoleUser {
				prompts = append(prompts, m.Text)
			}
		}
	}
	childLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("LOOKED_AGAIN"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)),
		agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p2", resumeArgs("subagent-p1", "continue"))
	if res.IsError {
		t.Fatalf("read-only resume errored: %q", res.Content)
	}
	mu.Lock()
	got := strings.Join(prompts, "\n")
	mu.Unlock()
	if !strings.Contains(got, "FRESH workspace checkout") {
		t.Fatalf("a resumed READ-ONLY child must keep the fresh-checkout staleness note, got:\n%s", got)
	}
	if strings.Contains(got, "STILL IN PLACE") {
		t.Fatalf("the writable resume note must not reach a read-only child, got:\n%s", got)
	}
}

// seedFailedChildInForkRoot persists a failed subagent session whose recorded workspace is
// a THROWAWAY FORK ROOT rather than the parent tree — the shape a READ-ONLY child leaves
// behind (its git worktree is created per run and torn down at the end). It is the
// precondition for the resume-note mismatch below: the path is what tells the harness that
// this child's earlier file changes did NOT survive.
func seedFailedChildInForkRoot(t *testing.T, store port.SessionStore, id session.SessionID, forkRoot string) {
	t.Helper()
	seed := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: forkRoot, Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := seed.RecordUserPrompt("investigate the parser", nil); err != nil {
		t.Fatalf("seed RecordUserPrompt: %v", err)
	}
	if err := seed.BeginTurn(); err != nil {
		t.Fatalf("seed BeginTurn: %v", err)
	}
	if err := seed.RecordAssistant(session.NewAssistantMessage("patched it with a shell one-liner", "", nil)); err != nil {
		t.Fatalf("seed RecordAssistant: %v", err)
	}
	if err := seed.Fail(); err != nil {
		t.Fatalf("seed Fail: %v", err)
	}
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}
}

// TestReadOnlyChildResumedAsWritableIsNotToldItsEditsSurvived is the third member of the
// resume-note family, and the one that closes an assertion the harness could make FALSELY.
//
// `writable` comes only from the CURRENT call's `mode`, and validateMode deliberately lets
// `mode` COMPOSE with `resume` — so "the read-only investigator stalled, resume it with
// write access so it can apply the fix" is a legal and natural parent move. Keying the note
// on `writable` alone then hands that child resumeWritableNote: "the file edits you already
// made are STILL IN PLACE". Its earlier run was in a git worktree that no longer exists, and
// a read-only child — while it has no Edit/Write — DOES have Bash in that worktree, so it
// may genuinely have applied edits that are now gone. That is exactly the falsehood
// resumeWritableNote exists to prevent, inverted, and it is worse than the "GONE" wording
// it replaces: a child that trusts absent edits builds on nothing.
//
// The note is therefore keyed on whether this run executes in the SAME tree the prior run
// recorded, which the persisted workspace answers without any new field.
//
// It asserts BOTH axes, because they are independent and this cell is the only one where
// they disagree. The earlier oracle asserted the edits axis and then required the
// fresh-CHECKOUT wording — which is the OTHER axis, and false here: a mode:"read-write" call
// never forks (prepareChildSession passes forker=nil, ADR 0041), so this child holds
// Edit/Write on the operator's REAL repository while being told it is in a scratch
// checkout, and a child that believes that may rewrite or delete files to "start clean".
//
// This is `package agent_test` and cannot read the unexported note constants, so each
// absence check is paired with a POSITIVE control on the same live phrase elsewhere in this
// file: "STILL IN PLACE" is required to be present by TestWritableResumeNoteSaysEditsSurvive
// and "FRESH workspace checkout" by TestReadOnlyResumeNoteKeepsFreshCheckoutWording. A
// reworded constant therefore reddens one of those instead of quietly voiding these.
func TestReadOnlyChildResumedAsWritableIsNotToldItsEditsSurvived(t *testing.T) {
	store := memstore.New()
	// The prior run lived in a throwaway worktree, NOT the parent root the resume runs in.
	seedFailedChildInForkRoot(t, store, "subagent-p1", "/ws-worktree-abc123")

	var mu sync.Mutex
	var prompts []string
	obs := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range req.Messages {
			if m.Role == session.RoleUser {
				prompts = append(prompts, m.Text)
			}
		}
	}
	writable := childEngineWith(mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs)},
		mockllm.TextTurn("CONTINUED_WITH_WRITE_ACCESS"),
	), catalogWith(t))
	task := newWritableSubagent(t, writable, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p2",
		`{"resume":"subagent-p1","prompt":"now apply the fix","mode":"read-write"}`)
	if res.IsError {
		t.Fatalf("resuming a read-only child with write access must be allowed: %q", res.Content)
	}

	mu.Lock()
	got := strings.Join(prompts, "\n")
	mu.Unlock()
	// Axis 1 — WHAT SURVIVED (the earlier run's mode): nothing did.
	if strings.Contains(got, "STILL IN PLACE") {
		t.Fatalf("a child whose prior run was in a torn-down worktree must NOT be told its edits survived, got:\n%s", got)
	}
	if !strings.Contains(got, "are GONE") {
		t.Fatalf("it must be told the earlier run's file changes are GONE, got:\n%s", got)
	}
	// Axis 2 — WHERE IT RUNS (this call's mode): the operator's real tree, no isolation.
	if strings.Contains(got, "FRESH workspace checkout") {
		t.Fatalf("a mode:\"read-write\" child runs in the REAL workspace (no fork) and must not be told it is in a fresh checkout, got:\n%s", got)
	}
	if !strings.Contains(got, "DIRECTLY in the real workspace") {
		t.Fatalf("it must be told its writes land in the real workspace, got:\n%s", got)
	}
}
