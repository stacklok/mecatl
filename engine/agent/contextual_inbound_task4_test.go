package agent_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type inboundReviewer struct {
	mu         sync.Mutex
	calls      int
	entered    chan session.ToolCallID
	release    <-chan struct{}
	assessment agent.ReviewAssessment
	enforce    *bool
}

func (r *inboundReviewer) GuardrailReviewPolicy(_ string, job agent.ReviewJob, _ bool) (bool, bool) {
	enforce := true
	if r.enforce != nil {
		enforce = *r.enforce
	}
	return job == agent.ReviewJobInbound, enforce
}

func (r *inboundReviewer) Review(_ context.Context, req agent.ToolReviewRequest, _ agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if r.entered != nil {
		r.entered <- req.EffectiveCall.ID
	}
	if r.release != nil {
		<-r.release
	}
	assessment := r.assessment
	if assessment == "" {
		assessment = agent.ReviewProhibited
	}
	return agent.ToolReviewResult{Assessment: assessment, Concerns: []agent.ReviewConcern{{Ref: "c1", Category: "prompt_injection", Rationale: "attempted redirection", SourceRef: "tool-result"}}, Missing: []agent.ReviewMissingEvidence{{Ref: "actual-source", Kind: "tool_result", Reason: "source was incomplete"}}}, session.AuxiliaryUsage{}, nil
}

type countingPostHook struct {
	mu      sync.Mutex
	posts   int
	message string
}

func (h *countingPostHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePostToolUse {
		h.mu.Lock()
		h.posts++
		h.mu.Unlock()
		return governance.HookOutcome{Message: h.message}, nil
	}
	return governance.HookOutcome{}, nil
}

type resultRecorder struct {
	mu      sync.Mutex
	results []session.ToolResult
}

func (r *resultRecorder) ToolCall(_ session.SessionID, _ session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

func resolveScoped(t *testing.T, run *agent.Run, ask *session.PendingAsk, verdict session.ApprovalVerdict) error {
	t.Helper()
	resolution := agent.ApprovalResolution{AskID: ask.AskID, Verdict: verdict}
	if ask.Guardrail != nil {
		resolution.ReviewID, resolution.Kind = ask.Guardrail.ReviewID, ask.Guardrail.Kind
	}
	return run.ResolveApproval(resolution)
}

func TestADR_0363_ContextualGuardrails_Scenario2_FailureMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interactive bool
		enforce     bool
		wantAsk     bool
		wantRaw     bool
	}{
		{name: "completed_unresolved_enforcing_interactive", interactive: true, enforce: true, wantAsk: true, wantRaw: true},
		{name: "completed_unresolved_enforcing_headless", enforce: true},
		{name: "completed_unresolved_advisory", wantRaw: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reviewer := &inboundReviewer{assessment: agent.ReviewUnresolved, enforce: &tc.enforce}
			read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResult(call.ID, "raw-result"), nil
			}}
			cat := tool.NewCatalog()
			cat.MustRegister(read)
			eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("r1", "Read", []byte(`{"path":"a"}`))), mockllm.TextTurn("done")), Catalog: cat, Policy: staticAllowPolicy{}, ToolReviewer: reviewer, Interactive: tc.interactive})
			run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
			asks := 0
			raw := false
			actualSource := false
			for ev := range run.Events() {
				if ev.Type == session.EvPermissionAsk {
					asks++
					_ = resolveScoped(t, run, ev.Ask, session.VerdictAllowOnce)
				}
				if ev.ToolResult != nil && ev.ToolResult.Content == "raw-result" {
					raw = true
				}
				if ev.Hook != nil && ev.Hook.Guardrail != nil {
					for _, source := range ev.Hook.Guardrail.Sources {
						actualSource = actualSource || source.Ref == "actual-source"
					}
				}
			}
			if (asks == 1) != tc.wantAsk || raw != tc.wantRaw || !actualSource {
				t.Fatalf("asks=%d raw=%v actualSource=%v", asks, raw, actualSource)
			}
		})
	}
}

func TestADR_0363_ContextualGuardrails_Scenario3_ExactResultRelease(t *testing.T) {
	const secret = "SECRET_SENTINEL"
	reviewer := &inboundReviewer{}
	hook := &countingPostHook{}
	recorder := &resultRecorder{}
	executions := 0
	read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResultWithParts(call.ID, secret, []session.Content{{BlockKind: session.BlockText, Text: secret + "-part"}}), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(read)
	call := session.NewToolCall("read-1", "Read", []byte(`{"path":"a"}`))
	sess := newSession(t, session.Limits{})
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: cat, Policy: staticAllowPolicy{}, Hooks: hook, ToolReviewer: reviewer, ToolCallRecorder: recorder, Interactive: true})
	run := eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	var streamed []session.ToolResult
	asks := 0
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk {
			asks++
			if ev.Ask.Guardrail == nil || ev.Ask.Guardrail.Kind != session.GuardrailApprovalResultRelease {
				t.Fatalf("release scope = %+v", ev.Ask.Guardrail)
			}
			if err := resolveScoped(t, run, ev.Ask, session.ApprovalVerdict(99)); !errors.Is(err, agent.ErrApprovalGrantIneligible) {
				t.Fatalf("unknown verdict = %v", err)
			}
			recorder.mu.Lock()
			recordedBeforeRelease := len(recorder.results)
			recorder.mu.Unlock()
			if recordedBeforeRelease != 0 {
				t.Fatalf("unknown verdict released held result to audit: count=%d", recordedBeforeRelease)
			}
			if err := resolveScoped(t, run, ev.Ask, session.VerdictAllowAlways); err == nil {
				t.Fatal("allow-always released a held result")
			}
			if err := run.ResolveApproval(agent.ApprovalResolution{AskID: ev.Ask.AskID, ReviewID: ev.Ask.Guardrail.ReviewID, Kind: session.GuardrailApprovalAction, Verdict: session.VerdictAllowOnce}); err == nil {
				t.Fatal("wrong approval kind released a held result")
			}
			if err := run.Approve(ev.Ask.AskID, session.VerdictAllowOnce); err == nil {
				t.Fatal("compatibility approval released a result without acknowledgement")
			}
			if err := run.Approve("unknown", session.VerdictAllowOnce); err == nil {
				t.Fatal("unknown approval did not report an actionable error")
			}
			if err := resolveScoped(t, run, ev.Ask, session.VerdictAllowOnce); err != nil {
				t.Fatalf("valid release: %v", err)
			}
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			streamed = append(streamed, *ev.ToolResult)
		}
	}
	if executions != 1 || reviewer.calls != 1 || hook.posts != 1 || asks != 1 {
		t.Fatalf("executions=%d reviews=%d posts=%d asks=%d", executions, reviewer.calls, hook.posts, asks)
	}
	if len(streamed) != 1 || streamed[0].Content != secret || len(recorder.results) != 1 || !reflect.DeepEqual(streamed[0], recorder.results[0]) {
		t.Fatalf("stream=%+v recorder=%+v", streamed, recorder.results)
	}
	last := sess.Conversation.Messages[len(sess.Conversation.Messages)-2]
	if last.ToolResult == nil || !reflect.DeepEqual(*last.ToolResult, streamed[0]) {
		t.Fatalf("history result = %+v, stream = %+v", last.ToolResult, streamed[0])
	}
}

func TestADR_0363_ContextualGuardrails_Scenario3_HeldResultCleanup(t *testing.T) {
	const secret = "NEVER_DELIVER_THIS"
	reviewer := &inboundReviewer{}
	hook := &countingPostHook{message: secret}
	executions := 0
	read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult(call.ID, secret), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(read)
	sess := newSession(t, session.Limits{})
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("r1", "Read", []byte(`{"path":"a"}`))), mockllm.TextTurn("done")), Catalog: cat, Policy: staticAllowPolicy{}, Hooks: hook, ToolReviewer: reviewer})
	events := drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"}))
	if executions != 1 || reviewer.calls != 1 {
		t.Fatalf("executions=%d reviews=%d", executions, reviewer.calls)
	}
	for _, ev := range events {
		if (ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, secret)) || strings.Contains(ev.Text, secret) {
			t.Fatalf("held secret escaped in event: %+v", ev)
		}
	}
	for _, msg := range sess.Conversation.Messages {
		if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, secret) {
			t.Fatalf("held secret escaped in history: %+v", msg.ToolResult)
		}
	}
}

func TestADR_0363_ContextualGuardrails_Scenario3_ApprovalClassIsolation(t *testing.T) {
	// Exact result release rejects AllowAlways while retaining the item; the valid
	// follow-up Release once in the exact-release proof succeeds and no permission
	// learner is called there. This sentinel pins the accepted verdict taxonomy.
	if session.VerdictAllowAlways == session.VerdictAllowOnce || session.GuardrailApprovalAction == session.GuardrailApprovalResultRelease {
		t.Fatal("action and result-release approval classes collapsed")
	}
}

func TestADR_0363_ContextualGuardrails_Scenario3_ConcurrentInboundReleaseOrdering(t *testing.T) {
	reviewEntered := make(chan session.ToolCallID, 2)
	reviewRelease := make(chan struct{})
	reviewer := &inboundReviewer{entered: reviewEntered, release: reviewRelease}
	execEntered := make(chan session.ToolCallID, 2)
	execRelease := make(chan struct{})
	mk := func(name string) *fakeTool {
		return &fakeTool{name: name, readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			execEntered <- call.ID
			<-execRelease
			return session.NewToolResult(call.ID, "secret-"+string(call.ID)), nil
		}}
	}
	cat := tool.NewCatalog()
	cat.MustRegister(mk("Read"))
	cat.MustRegister(mk("Grep"))
	calls := []session.ToolCall{session.NewToolCall("one", "Read", []byte(`{"path":"a"}`)), session.NewToolCall("two", "Grep", []byte(`{"path":"b","pattern":"x"}`))}
	sess := newSession(t, session.Limits{})
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: cat, Policy: staticAllowPolicy{}, ToolReviewer: reviewer, Interactive: true})
	run := eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "inspect"})
	if a, b := <-execEntered, <-execEntered; a == b {
		t.Fatal("tools did not execute independently")
	}
	close(execRelease)
	if a, b := <-reviewEntered, <-reviewEntered; a == b {
		t.Fatal("inbound reviews did not overlap")
	}
	close(reviewRelease)
	var asks []session.ToolCallID
	var results []session.ToolResult
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk {
			asks = append(asks, ev.Ask.Call)
			if len(asks) == 1 {
				_ = resolveScoped(t, run, ev.Ask, session.VerdictAllowOnce)
			} else {
				_ = resolveScoped(t, run, ev.Ask, session.VerdictDeny)
			}
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results = append(results, *ev.ToolResult)
		}
	}
	if want := []session.ToolCallID{"one", "two"}; !reflect.DeepEqual(asks, want) {
		t.Fatalf("ask order=%v want=%v", asks, want)
	}
	if len(results) != 2 || results[0].Content != "secret-one" || !results[1].IsError || strings.Contains(results[1].Content, "secret-two") {
		t.Fatalf("results=%+v", results)
	}
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("paired history: %v", err)
	}

	cancelledSession := newSession(t, session.Limits{})
	cancelEngine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("unreachable")), Catalog: cat, Policy: staticAllowPolicy{}, ToolReviewer: reviewer, Interactive: true})
	cancelRun := cancelEngine.Run(context.Background(), cancelledSession, agent.MemEnv("/ws"), agent.RunRequest{Text: "inspect then cancel"})
	var cancelledResults []session.ToolResult
	cancelAsks := 0
	for ev := range cancelRun.Events() {
		if ev.Type == session.EvPermissionAsk {
			cancelAsks++
			if cancelAsks == 1 {
				_ = resolveScoped(t, cancelRun, ev.Ask, session.VerdictAllowOnce)
			} else {
				cancelRun.Cancel()
			}
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			cancelledResults = append(cancelledResults, *ev.ToolResult)
		}
	}
	if len(cancelledResults) != 2 || cancelledResults[0].Content != "secret-one" || !cancelledResults[1].IsError || strings.Contains(cancelledResults[1].Content, "secret-two") {
		t.Fatalf("cancelled results=%+v", cancelledResults)
	}
	if err := session.ValidateToolPairing(cancelledSession.Conversation.Messages); err != nil {
		t.Fatalf("cancelled paired history: %v", err)
	}
}

var _ port.ToolCallRecorder = (*resultRecorder)(nil)
