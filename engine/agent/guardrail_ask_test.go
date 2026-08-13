package agent_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// hookApprovalStub is a port.HookRunner that returns an ASKABLE block
// (HookOutcome{Block, AskApproval}) on the FIRST PreToolUse for the matched tool,
// counting how many times Run fired the PreToolUse phase. It models the guardrails
// adapter's approve-once refinement (ADR 0062) WITHOUT pulling in the modelhook
// adapter (the engine tree stays self-contained). A non-matching phase/tool returns
// an empty (allow) outcome. When learner is set, it ALSO implements
// port.HookApprovalLearner and records the learned event, modelling the session
// waiver arming.
type hookApprovalStub struct {
	tool        string
	reason      string
	preCalls    atomic.Int32
	postBlock   bool // when true, also block PostToolUse with AskApproval set (must be ignored)
	noPreBlock  bool // when true, never block on Pre (isolates the Post-only path)
	learned     atomic.Int32
	learnedTool atomic.Value // string
	// waive, when true, makes a SECOND matching Pre call pass (modelling a waiver):
	// the stub only asks while waive is unset; after a learn it stops asking.
	stopAfterLearn bool
}

func (h *hookApprovalStub) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	switch ev.Phase {
	case governance.PhasePreToolUse:
		if ev.Tool != h.tool || h.noPreBlock {
			return governance.HookOutcome{}, nil
		}
		h.preCalls.Add(1)
		if h.stopAfterLearn && h.learned.Load() > 0 {
			return governance.HookOutcome{}, nil // waiver in effect: no ask
		}
		return governance.HookOutcome{Block: true, AskApproval: true, Message: h.reason}, nil
	case governance.PhasePostToolUse:
		if h.postBlock && ev.Tool == h.tool {
			// Post must IGNORE AskApproval (PreToolUse-only scope): a Post block is the
			// inert annotation path; AskApproval set here must never surface an ask.
			return governance.HookOutcome{Block: true, AskApproval: true, Message: h.reason}, nil
		}
		return governance.HookOutcome{}, nil
	default:
		return governance.HookOutcome{}, nil
	}
}

// hookApprovalLearnerStub embeds the stub and implements port.HookApprovalLearner.
type hookApprovalLearnerStub struct {
	*hookApprovalStub
}

func (h hookApprovalLearnerStub) LearnHookApproval(_ context.Context, ev governance.HookEvent) {
	h.learned.Add(1)
	h.learnedTool.Store(ev.Tool)
}

// bashGuardTool is a non-read-only Bash-like tool recording whether it ran.
func bashGuardTool(ran *atomic.Bool) *fakeTool {
	return &fakeTool{name: "Bash", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			ran.Store(true)
			return session.NewToolResult(in.ID, "ran"), nil
		}}
}

// driveGuardrailAsk runs ONE prompt issuing a single Bash call under the stub hooks,
// resolving the FIRST hook-originated permission ask with verdict. It returns the
// captured ask (if any), whether the tool ran, and the drained events.
func driveGuardrailAsk(t *testing.T, hooks *hookApprovalStub, interactive bool, cmd string, verdict session.ApprovalVerdict) (ask *session.PendingAsk, ran bool, evs []session.Event) {
	t.Helper()
	var didRun atomic.Bool
	cat := catalogWith(t, bashGuardTool(&didRun))
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Bash", `{"command":"`+cmd+`"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: hooks, Interactive: interactive})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	for ev := range r.Events() {
		evs = append(evs, ev)
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ask == nil {
			a := *ev.Ask
			ask = &a
			r.Approve(ev.Ask.AskID, verdict)
		}
	}
	return ask, didRun.Load(), evs
}

// InteractiveAllow: an interactive engine surfaces a hook-originated ask (with
// Ask.HookOriginated true), and AllowOnce runs the tool and fires EvApproval.
func TestGuardrailAskInteractiveAllowRuns(t *testing.T) {
	stub := &hookApprovalStub{tool: "Bash", reason: "blocked by guardrail: merges a PR"}
	ask, ran, evs := driveGuardrailAsk(t, stub, true, "gh pr merge", session.VerdictAllowOnce)
	if ask == nil {
		t.Fatal("an interactive hook block must surface a permission ask")
	}
	if !ask.HookOriginated {
		t.Fatal("the surfaced ask must carry HookOriginated=true")
	}
	if !ran {
		t.Fatal("AllowOnce must execute the tool")
	}
	if stub.preCalls.Load() != 1 {
		t.Fatalf("preHook must run exactly once (no re-invoke after allow); got %d", stub.preCalls.Load())
	}
	var sawApproval bool
	for _, ev := range evs {
		if ev.Type == session.EvApproval && ev.Approval != nil && ev.Approval.AskID == ask.AskID {
			sawApproval = true
		}
	}
	if !sawApproval {
		t.Fatal("an EvApproval must fire for the resolved hook ask")
	}
}

// InteractiveDeny: VerdictDeny → denied result, tool not executed.
func TestGuardrailAskInteractiveDenyBlocks(t *testing.T) {
	stub := &hookApprovalStub{tool: "Bash", reason: "blocked by guardrail: merges a PR"}
	ask, ran, evs := driveGuardrailAsk(t, stub, true, "gh pr merge", session.VerdictDeny)
	if ask == nil {
		t.Fatal("the block must still surface an ask before the deny")
	}
	if ran {
		t.Fatal("a denied guardrail ask must NOT execute the tool")
	}
	var sawErr bool
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError &&
			strings.Contains(ev.ToolResult.Content, "guardrail") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("a deny must surface the guardrail reason as an error tool result")
	}
}

// HeadlessDegradesToTerminalBlock (adversarial): a non-interactive engine NEVER
// surfaces an ask — the block stands and the tool never runs.
func TestGuardrailAskHeadlessDegradesToBlock(t *testing.T) {
	stub := &hookApprovalStub{tool: "Bash", reason: "blocked by guardrail: merges a PR"}
	// Pass a verdict that would ALLOW if an ask existed — it must never be consulted.
	ask, ran, evs := driveGuardrailAsk(t, stub, false, "gh pr merge", session.VerdictAllowAlways)
	if ask != nil {
		t.Fatal("SECURITY: a headless engine must NOT surface a hook-originated ask")
	}
	if ran {
		t.Fatal("a headless hook block must NOT execute the tool")
	}
	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatal("no EvPermissionAsk may be emitted headless")
		}
	}
	var sawBlock bool
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError &&
			strings.Contains(ev.ToolResult.Content, "guardrail") {
			sawBlock = true
		}
	}
	if !sawBlock {
		t.Fatal("the headless degrade must produce the guardrail block error result")
	}
}

// PostToolUseHookAskApprovalIgnored: an AskApproval-set Post outcome is ignored —
// no ask surfaces, the tool runs, and the result is the inert annotation path.
func TestGuardrailAskPostApprovalIgnored(t *testing.T) {
	// Pre is NOT matched (so no Pre block); only Post sets AskApproval, which must be
	// ignored. Use a tool name the Pre arm does not match.
	stub := &hookApprovalStub{tool: "WebFetch", reason: "blocked by guardrail", postBlock: true, noPreBlock: true}
	var ran atomic.Bool
	wf := &fakeTool{name: "WebFetch", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			ran.Store(true)
			return session.NewToolResult(in.ID, "page"), nil
		}}
	cat := catalogWith(t, wf)
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "WebFetch", `{"url":"x"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: stub, Interactive: true})
	evs := drain(e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatal("a PostToolUse AskApproval must NOT surface a permission ask (PreToolUse-only scope)")
		}
	}
	if !ran.Load() {
		t.Fatal("the tool must run (Post is after execution; AskApproval is inert there)")
	}
}

// ChildGuardrailAskDegrades: a non-interactive (child) engine degrades a hook ask to
// a terminal block — children never surface a guardrail ask. (Children are always
// Interactive:false; this is the same code path as the headless test but named for
// the child-isolation guarantee.)
func TestGuardrailAskChildDegrades(t *testing.T) {
	stub := &hookApprovalStub{tool: "Bash", reason: "blocked by guardrail"}
	ask, ran, _ := driveGuardrailAsk(t, stub, false, "gh pr merge", session.VerdictAllowOnce)
	if ask != nil {
		t.Fatal("a child (Interactive:false) engine must never surface a guardrail ask")
	}
	if ran {
		t.Fatal("a child hook block must terminate, not run the tool")
	}
}

// WaiverAllowAlways: an interactive AllowAlways runs the tool AND arms the waiver
// (LearnHookApproval fires); the stub then stops asking for a matching command in
// the same session. A non-matching command still asks.
func TestGuardrailAskAllowAlwaysArmsWaiver(t *testing.T) {
	base := &hookApprovalStub{tool: "Bash", reason: "blocked by guardrail", stopAfterLearn: true}
	learner := hookApprovalLearnerStub{hookApprovalStub: base}

	// First matching block under an interactive engine, AllowAlways.
	var ran1 atomic.Bool
	cat1 := catalogWith(t, bashGuardTool(&ran1))
	llm1 := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Bash", `{"command":"gh pr merge"}`)), mockllm.TextTurn("done"))
	sess := newSession(t, session.Limits{})
	e1 := newEngine(agent.Deps{LLM: llm1, Catalog: cat1, Hooks: learner, Interactive: true})
	r1 := e1.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	var firstAsk bool
	for ev := range r1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			firstAsk = true
			r1.Approve(ev.Ask.AskID, session.VerdictAllowAlways)
		}
	}
	if !firstAsk {
		t.Fatal("the first matching block must surface an ask")
	}
	if !ran1.Load() {
		t.Fatal("AllowAlways must run the tool")
	}
	if base.learned.Load() != 1 {
		t.Fatalf("AllowAlways must arm the waiver (LearnHookApproval); learned=%d", base.learned.Load())
	}
	if got, _ := base.learnedTool.Load().(string); got != "Bash" {
		t.Fatalf("the learned event must carry the tool; got %q", got)
	}

	// SECOND matching call in the same session: the stub no longer asks (waiver in
	// effect, modelled by stopAfterLearn) — the tool runs with no ask.
	if err := sess.Reopen(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var ran2 atomic.Bool
	cat2 := catalogWith(t, bashGuardTool(&ran2))
	llm2 := mockllm.New(mockllm.ToolCallTurn(toolCall("c2", "Bash", `{"command":"gh pr merge"}`)), mockllm.TextTurn("done"))
	e2 := newEngine(agent.Deps{LLM: llm2, Catalog: cat2, Hooks: learner, Interactive: true})
	r2 := e2.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	var secondAsk bool
	for ev := range r2.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			secondAsk = true
			r2.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	if secondAsk {
		t.Fatal("a waived command must NOT re-ask in the same session")
	}
	if !ran2.Load() {
		t.Fatal("a waived command must run without an ask")
	}
}

// ResumeFromAwaiting: drive to StateAwaiting on a hook ask, snapshot-restore (model
// process death), then ResumeApproval(AllowOnce) executes the tool EXACTLY ONCE with
// the preHook NOT re-run (the HookOriginated skip).
func TestGuardrailAskResumeFromAwaitingAllow(t *testing.T) {
	stub := &hookApprovalStub{tool: "Bash", reason: "blocked by guardrail"}
	var ran atomic.Bool
	cat := catalogWith(t, bashGuardTool(&ran))
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Bash", `{"command":"gh pr merge"}`)), mockllm.TextTurn("done"))
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: stub, Interactive: true})

	// Drive to the awaiting ask, snapshot, then cancel (process death).
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	var askID string
	var snap sessnap.Snapshot
	var snapErr error
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			if !ev.Ask.HookOriginated {
				t.Fatal("the awaiting ask must be HookOriginated")
			}
			snap, snapErr = sessnap.Of(sess)
			r.Cancel()
		}
	}
	if askID == "" {
		t.Fatal("no hook ask surfaced")
	}
	if snapErr != nil {
		t.Fatalf("snapshot: %v", snapErr)
	}
	if snap.State != session.StateAwaiting {
		t.Fatalf("snapshot state = %q, want awaiting", snap.State)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The HookOriginated marker must survive the snapshot round-trip.
	ra, ok := restored.PendingAsk()
	if !ok || !ra.HookOriginated {
		t.Fatalf("HookOriginated must round-trip the snapshot; ok=%v ask=%+v", ok, ra)
	}

	// Fresh engine (a new process): resume the ask with AllowOnce. The tool must run
	// WITHOUT preHook being consulted again (the stub's preCalls stays at its
	// pre-restart count of 1).
	preBefore := stub.preCalls.Load()
	cat2 := catalogWith(t, bashGuardTool(&ran))
	e2 := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: cat2, Hooks: stub, Interactive: true})
	rr := e2.ResumeApproval(context.Background(), restored, agent.MemEnv("/ws"), askID, session.VerdictAllowOnce)
	drain(rr)
	if !ran.Load() {
		t.Fatal("resume AllowOnce must execute the hook-blocked tool")
	}
	if stub.preCalls.Load() != preBefore {
		t.Fatalf("the resume must NOT re-run preHook (HookOriginated skip); pre before=%d after=%d", preBefore, stub.preCalls.Load())
	}
}

// ResumeFromAwaiting AllowAlways must NOT double-arm the learner (asymmetry vs the
// live askHookApproval path): resolvePendingCall executes the call but does NOT call
// LearnHookApproval (the live verdict already armed it; the resume path only finishes
// the parked turn). Pins that the learner is wired ONLY at the live verdict site.
func TestGuardrailAskResumeAllowAlwaysDoesNotReArm(t *testing.T) {
	base := &hookApprovalStub{tool: "Bash", reason: "blocked by guardrail"}
	learner := hookApprovalLearnerStub{hookApprovalStub: base}
	var ran atomic.Bool
	cat := catalogWith(t, bashGuardTool(&ran))
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Bash", `{"command":"gh pr merge"}`)), mockllm.TextTurn("done"))
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: learner, Interactive: true})

	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	var askID string
	var snap sessnap.Snapshot
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			snap, _ = sessnap.Of(sess)
			r.Cancel()
		}
	}
	if askID == "" {
		t.Fatal("no hook ask surfaced")
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// The live ask was parked BEFORE any verdict, so the learner was never armed.
	if base.learned.Load() != 0 {
		t.Fatalf("pre-resume: learner must be un-armed; learned=%d", base.learned.Load())
	}

	// Resume with AllowAlways: the call executes, but the resume path must NOT re-arm.
	cat2 := catalogWith(t, bashGuardTool(&ran))
	e2 := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: cat2, Hooks: learner, Interactive: true})
	rr := e2.ResumeApproval(context.Background(), restored, agent.MemEnv("/ws"), askID, session.VerdictAllowAlways)
	drain(rr)
	if !ran.Load() {
		t.Fatal("resume AllowAlways must execute the call")
	}
	if base.learned.Load() != 0 {
		t.Fatalf("resolvePendingCall must NOT call LearnHookApproval (no double-arm); learned=%d", base.learned.Load())
	}
}

// A NON-HookOriginated (policy) ask resumed with Allow re-runs preHook (the
// HookOriginated skip does NOT apply); if that re-run returns an ASKABLE block, the
// resume path FAILS SAFE to a terminal block — the tool is NOT executed and the model
// gets the block (dispatch.go's resolvePendingCall askApproval fail-safe). It must
// never silently run the call unasked mid-resume.
func TestGuardrailAskResumePolicyAskRefinedBlockFailsSafe(t *testing.T) {
	// preHook is INERT on the first pass (noPreBlock) so the LIVE ask is the POLICY
	// ask, not a hook ask; on the resume re-run we flip it to return an askable block.
	stub := &hookApprovalStub{tool: "Bash", reason: "blocked by guardrail", noPreBlock: true}
	var ran atomic.Bool
	cat := catalogWith(t, bashGuardTool(&ran))
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Bash", `{"command":"gh pr merge"}`)), mockllm.TextTurn("done"))
	// A policy that ASKS for Bash (ModeDefault, mutating tool, no allow rule).
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: stub, Policy: policy, Interactive: true})

	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	var askID string
	var snap sessnap.Snapshot
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			if ev.Ask.HookOriginated {
				t.Fatal("the live ask must be the POLICY ask, NOT hook-originated")
			}
			snap, _ = sessnap.Of(sess)
			r.Cancel()
		}
	}
	if askID == "" {
		t.Fatal("no policy ask surfaced")
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Now make the resume's preHook re-run return an askable block.
	stub.noPreBlock = false
	cat2 := catalogWith(t, bashGuardTool(&ran))
	policy2 := permpolicy.NewPolicy(nil, permstore.New())
	e2 := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: cat2, Hooks: stub, Policy: policy2, Interactive: true})
	rr := e2.ResumeApproval(context.Background(), restored, agent.MemEnv("/ws"), askID, session.VerdictAllowOnce)
	evs := drain(rr)
	if ran.Load() {
		t.Fatal("a refined-block on the resume re-run must FAIL SAFE — the tool must NOT execute")
	}
	var sawBlock bool
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError &&
			strings.Contains(ev.ToolResult.Content, "guardrail") {
			sawBlock = true
		}
	}
	if !sawBlock {
		t.Fatal("the fail-safe must surface the guardrail block as an error tool result")
	}
}
