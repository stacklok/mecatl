package agent_test

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type scenario1Policy struct{ order *[]string }

func (p scenario1Policy) Evaluate(_ context.Context, _ session.SessionID, _ session.PermissionMode, _ session.ToolCall, _ tool.WorkspaceReader) governance.PermissionDecision {
	*p.order = append(*p.order, "permission")
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (scenario1Policy) Learn(session.SessionID, session.ToolCall) {}

type scenario1Hook struct{ order *[]string }

func (h scenario1Hook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePreToolUse {
		*h.order = append(*h.order, "mutation")
		return governance.HookOutcome{Mutated: []byte(`{"value":"effective"}`)}, nil
	}
	if ev.Phase == governance.PhasePostToolUse {
		*h.order = append(*h.order, "post")
	}
	return governance.HookOutcome{}, nil
}

type scenario1Reviewer struct {
	mu       sync.Mutex
	order    *[]string
	requests []agent.ToolReviewRequest
}

func (r *scenario1Reviewer) Review(_ context.Context, req agent.ToolReviewRequest, _ agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.order = append(*r.order, "review")
	r.requests = append(r.requests, req)
	return agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, session.AuxiliaryUsage{}, nil
}

func TestADR_0363_ContextualGuardrails_Scenario1_EffectiveCallOrder(t *testing.T) {
	var order []string
	reviewer := &scenario1Reviewer{order: &order}
	act := &genericAuthorizationTool{fakeTool: fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		order = append(order, "execute")
		if string(call.Args) != `{"value":"effective"}` {
			t.Fatalf("executed args = %s", call.Args)
		}
		return session.NewToolResult(call.ID, "ok"), nil
	}}, order: &order}
	cat := tool.NewCatalog()
	cat.MustRegister(act)
	call := session.NewToolCall("call-1", "Act", []byte(`{"value":"requested"}`))
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: cat, Policy: scenario1Policy{order: &order}, Hooks: scenario1Hook{order: &order}, ToolReviewer: reviewer})
	drain(eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "do it", CanPresentAuthorization: true}))
	want := []string{"permission", "mutation", "permission", "review", "authorization", "execute", "post"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if len(reviewer.requests) != 1 || string(reviewer.requests[0].EffectiveCall.Args) != `{"value":"effective"}` {
		t.Fatalf("review requests = %s", fmt.Sprint(reviewer.requests))
	}
}

type noExternalEvidenceSource struct{}

func (noExternalEvidenceSource) ReadReviewEvidence(context.Context, agent.ReviewEvidenceRequest) (agent.ReviewEvidence, error) {
	return agent.ReviewEvidence{}, fmt.Errorf("no evidence handles were advertised")
}

type noExternalEvidencePreparer struct{}

func (noExternalEvidencePreparer) PrepareReviewEvidence(context.Context, agent.ReviewEvidencePreparation) (agent.PreparedReviewEvidence, error) {
	return agent.PreparedReviewEvidence{Source: noExternalEvidenceSource{}, Complete: true}, nil
}

type scenario1GrantReviewer struct {
	mu      sync.Mutex
	reviews int
	armed   map[string]bool
}

func (r *scenario1GrantReviewer) Review(_ context.Context, _ agent.ToolReviewRequest, _ agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reviews++
	return agent.ToolReviewResult{Assessment: agent.ReviewProhibited}, session.AuxiliaryUsage{}, nil
}
func (*scenario1GrantReviewer) GrantDigest(req agent.ToolReviewRequest) (string, bool) {
	return req.EffectiveCall.Name + ":" + string(req.EffectiveCall.Args) + ":" + req.Environment.Revision, req.EvidenceComplete
}
func (r *scenario1GrantReviewer) AllowsGrant(digest string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.armed[digest]
}
func (r *scenario1GrantReviewer) ArmGrant(digest, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.armed == nil {
		r.armed = make(map[string]bool)
	}
	r.armed[digest] = true
}

type noLearnPolicy struct{ learns int }

func (*noLearnPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (p *noLearnPolicy) Learn(session.SessionID, session.ToolCall) { p.learns++ }

func TestADR_0363_ContextualGuardrails_Scenario1_ExactRepeatGrant(t *testing.T) {
	reviewer := &scenario1GrantReviewer{}
	policy := &noLearnPolicy{}
	executions := 0
	write := &fakeTool{name: "Write", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult(call.ID, "ok"), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(write)
	args := []byte(`{"path":"new.txt","content":"x"}`)
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("first", "Write", args)),
		mockllm.ToolCallTurn(session.NewToolCall("second", "Write", args)),
		mockllm.TextTurn("done")), Catalog: cat, Policy: policy, ToolReviewer: reviewer, ReviewEvidencePreparer: noExternalEvidencePreparer{}, Interactive: true})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "write it"})
	asks := 0
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			asks++
			if !ev.Ask.Guardrail.RepeatAvailable || ev.Ask.Guardrail.GrantDigest == "" {
				t.Fatalf("repeat scope = %+v", ev.Ask.Guardrail)
			}
			run.Approve(ev.Ask.AskID, session.VerdictAllowAlways)
		}
	}
	if asks != 1 || reviewer.reviews != 1 || executions != 2 {
		t.Fatalf("asks=%d reviews=%d executions=%d", asks, reviewer.reviews, executions)
	}
	if policy.learns != 0 {
		t.Fatalf("permission Learn called %d times for guardrail approval", policy.learns)
	}
}

func TestADR_0363_ContextualGuardrails_Scenario1_ConcurrentActionReviews(t *testing.T) {
	reviewEntered := make(chan session.ToolCallID, 2)
	reviewRelease := make(chan struct{})
	reviewer := toolReviewerFunc(func(_ context.Context, req agent.ToolReviewRequest, _ agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
		reviewEntered <- req.EffectiveCall.ID
		<-reviewRelease
		return agent.ToolReviewResult{Assessment: agent.ReviewProhibited}, nil
	})

	execEntered := make(chan session.ToolCallID, 2)
	execRelease := make(chan struct{})
	var execMu sync.Mutex
	executions := make(map[session.ToolCallID]int)
	mkTool := func(name string) *fakeTool {
		return &fakeTool{name: name, readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			execMu.Lock()
			executions[call.ID]++
			execMu.Unlock()
			execEntered <- call.ID
			<-execRelease
			return session.NewToolResult(call.ID, "ok"), nil
		}}
	}
	cat := tool.NewCatalog()
	cat.MustRegister(mkTool("Read"))
	cat.MustRegister(mkTool("Grep"))
	calls := []session.ToolCall{
		session.NewToolCall("call-1", "Read", []byte(`{"path":"a"}`)),
		session.NewToolCall("call-2", "Grep", []byte(`{"path":"b","pattern":"x"}`)),
	}
	eng := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: cat,
		Policy: staticAllowPolicy{}, ToolReviewer: reviewer, Interactive: true,
	})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "inspect both"})

	firstReview := <-reviewEntered
	secondReview := <-reviewEntered
	if firstReview == secondReview {
		t.Fatalf("reviewed the same call twice: %q", firstReview)
	}
	close(reviewRelease)

	executionsOverlapped := make(chan struct{})
	go func() {
		<-execEntered
		<-execEntered
		close(execRelease)
		close(executionsOverlapped)
	}()
	var askOrder []session.ToolCallID
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Guardrail != nil {
			askOrder = append(askOrder, ev.Ask.Call)
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	<-executionsOverlapped
	if want := []session.ToolCallID{"call-1", "call-2"}; !reflect.DeepEqual(askOrder, want) {
		t.Fatalf("action ask order = %v, want %v", askOrder, want)
	}
	execMu.Lock()
	defer execMu.Unlock()
	for _, call := range calls {
		if executions[call.ID] != 1 {
			t.Fatalf("tool %s executed %d times, want exactly once", call.ID, executions[call.ID])
		}
	}
}

type toolReviewerFunc func(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, error)

func (f toolReviewerFunc) Review(ctx context.Context, req agent.ToolReviewRequest, source agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	result, err := f(ctx, req, source)
	return result, session.AuxiliaryUsage{}, err
}

type staticAllowPolicy struct{}

func (staticAllowPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (staticAllowPolicy) Learn(session.SessionID, session.ToolCall) {}

func TestADR_0363_ContextualGuardrails_Scenario4_RootTrajectory(t *testing.T) {
	var order []string
	reviewer := &scenario1Reviewer{order: &order}
	read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(call.ID, "data"), nil
	}}
	send := &fakeTool{name: "WebFetch", readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(call.ID, "sent"), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(read)
	cat.MustRegister(send)
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"path":"a"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("send", "WebFetch", []byte(`{"url":"https://example.invalid"}`))),
		mockllm.TextTurn("done")), Catalog: cat, Policy: scenario1Policy{order: &order}, ToolReviewer: reviewer})
	drain(eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "read then send"}))
	if len(reviewer.requests) != 2 {
		t.Fatalf("review count = %d", len(reviewer.requests))
	}
	facts := reviewer.requests[1].Trajectory
	if len(facts) != 1 || facts[0].DataClass != "sensitive_read" || facts[0].Decision != "acceptable" {
		t.Fatalf("second review trajectory = %+v", facts)
	}
}
