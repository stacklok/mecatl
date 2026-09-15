package app

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type boundedEvidenceSource struct {
	size  int64
	value agent.ReviewEvidence
	reads int
}

func (*boundedEvidenceSource) ValidateReviewBinding(agent.ToolReviewRequest, string, string) error {
	return nil
}
func (s *boundedEvidenceSource) ReviewEvidenceSize(context.Context, agent.ReviewEvidenceRequest) (int64, error) {
	return s.size, nil
}
func (s *boundedEvidenceSource) ReadReviewEvidence(_ context.Context, _ agent.ReviewEvidenceRequest) (agent.ReviewEvidence, error) {
	s.reads++
	return s.value, nil
}

type finiteEvidenceBackend struct {
	size, reads int64
	content     string
}

func (b *finiteEvidenceBackend) Size(context.Context) (int64, error) { return b.size, nil }
func (b *finiteEvidenceBackend) ReadAt(_ context.Context, offset, limit int64) (string, error) {
	b.reads++
	end := min(int64(len(b.content)), offset+limit)
	if offset > end {
		return "", errors.New("offset beyond evidence")
	}
	return b.content[offset:end], nil
}

type retryableReviewError string

func (e retryableReviewError) Error() string { return string(e) }
func (retryableReviewError) RetryDisposition() session.RetryDisposition {
	return session.RetryDispositionRetryable
}

func completeEvidenceBinding(req agent.ToolReviewRequest) reviewEvidenceBinding {
	return reviewEvidenceBinding{
		ReviewID: req.ReviewID, Owner: "owner-1", SessionID: session.SessionID(req.Event.SessionID),
		Environment: req.Environment, CheckerProviderID: "mock", CheckerModelID: "review-model",
		Caller: req.Caller, ExpiresAt: time.Now().Add(time.Minute),
	}
}

type blockingReviewProvider struct{ calls int }

func (*blockingReviewProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *blockingReviewProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func reviewerForTurns(t *testing.T, turns ...mockllm.Turn) (agent.ToolReviewer, *mockllm.Provider) {
	t.Helper()
	provider := mockllm.New(turns...)
	pc := promptConfig(Config{Model: "review-model"}, "")
	pc.Role = contextualReviewerSystemPrompt
	deps := childEngineDepsForProvider(Config{UseMock: true}, "guardrail-reviewer", provider, "review-model", func() int { return 128000 }, tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	return newContextualToolReviewer(agent.NewEngine(deps), "mock", "review-model"), provider
}

func TestADR_0342_ContextualGuardrails_Scenario2_EvidenceAuthority(t *testing.T) {
	read := session.NewToolCall("read", readReviewEvidenceToolName, []byte(`{"review_id":"review-1","handle":"ev_opaque_1","version":"v1"}`))
	submit := session.NewToolCall("submit", submitReviewAssessmentToolName, []byte(`{"assessment":"acceptable","concerns":[],"evidence":[{"handle":"ev_opaque_1","version":"v1","supports":["context"]}],"missing_evidence":[]}`))
	reviewer, _ := reviewerForTurns(t, mockllm.ToolCallTurn(read), mockllm.ToolCallTurn(submit))
	source := &boundedEvidenceSource{size: 4, value: agent.ReviewEvidence{Handle: "ev_opaque_1", Kind: "text_file", Version: "v1", Complete: true, Content: "data"}}
	result, err := reviewer.Review(context.Background(), completeReviewRequest(), source)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if result.Assessment != agent.ReviewAcceptable || source.reads != 1 {
		t.Fatalf("result=%+v reads=%d", result, source.reads)
	}

	wrong := read
	wrong.Args = []byte(`{"review_id":"other-review","handle":"ev_opaque_1","version":"v1"}`)
	reviewer, _ = reviewerForTurns(t, mockllm.ToolCallTurn(wrong))
	source.reads = 0
	result, err = reviewer.Review(context.Background(), completeReviewRequest(), source)
	if err == nil || result.Assessment != agent.ReviewUnresolved || source.reads != 0 || !strings.Contains(err.Error(), "wrong review binding") {
		t.Fatalf("wrong-binding result=%+v err=%v reads=%d", result, err, source.reads)
	}

	state := newReviewToolState(completeReviewRequest(), source)
	readTool := &readReviewEvidenceTool{state: state}
	first, _ := readTool.Execute(context.Background(), read, reviewerEnvironment)
	second, _ := readTool.Execute(context.Background(), read, reviewerEnvironment)
	if first.IsError || !second.IsError || source.reads != 1 {
		t.Fatalf("duplicate read first=%+v second=%+v backend_reads=%d", first, second, source.reads)
	}

	source.reads = 0
	reviewer, _ = reviewerForTurns(t, mockllm.ToolCallTurn(read), mockllm.EmptyTurn())
	result, err = reviewer.Review(context.Background(), completeReviewRequest(), source)
	if err == nil || result.Assessment != agent.ReviewUnresolved || source.reads != 1 || !strings.Contains(err.Error(), "without a valid SubmitReviewAssessment") {
		t.Fatalf("missing-submit result=%+v err=%v reads=%d", result, err, source.reads)
	}
}

func TestReviewEvidenceSourceRejectsBoundAuthorityBeforeAccess(t *testing.T) {
	req := completeReviewRequest()
	binding := completeEvidenceBinding(req)
	backend := &finiteEvidenceBackend{size: 4, content: "data"}
	candidate := reviewEvidenceCandidate{Kind: "text_file", Display: "untrusted label", Version: "v1", Complete: true, Authorized: true, Binding: binding, Backend: backend}
	_, metas, complete := newFiniteReviewEvidenceSource(context.Background(), binding, []reviewEvidenceCandidate{candidate}, req.Capacity)
	if !complete || len(metas) != 1 || metas[0].Handle == "" || strings.Contains(metas[0].Handle, metas[0].Display) {
		t.Fatalf("minted inventory = %+v complete=%v", metas, complete)
	}
	req.Evidence = metas

	for _, tc := range []struct {
		name   string
		mutate func(*agent.ToolReviewRequest, *finiteReviewEvidenceSource)
	}{
		{"owner", func(_ *agent.ToolReviewRequest, s *finiteReviewEvidenceSource) { s.access.Owner = "other" }},
		{"session", func(r *agent.ToolReviewRequest, _ *finiteReviewEvidenceSource) { r.Event.SessionID = "other" }},
		{"environment", func(r *agent.ToolReviewRequest, _ *finiteReviewEvidenceSource) { r.Environment.Revision = "r2" }},
		{"route", func(_ *agent.ToolReviewRequest, s *finiteReviewEvidenceSource) { s.access.CheckerModelID = "other" }},
		{"role", func(r *agent.ToolReviewRequest, _ *finiteReviewEvidenceSource) { r.Caller.Role = "subagent" }},
		{"capability", func(r *agent.ToolReviewRequest, _ *finiteReviewEvidenceSource) {
			r.Caller.Capabilities = []string{"Shell"}
		}},
		{"expiry", func(_ *agent.ToolReviewRequest, s *finiteReviewEvidenceSource) {
			s.now = func() time.Time { return binding.ExpiresAt.Add(time.Second) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotReq := req
			gotReq.Caller.Capabilities = append([]string(nil), req.Caller.Capabilities...)
			caseSource, _, _ := newFiniteReviewEvidenceSource(context.Background(), binding, []reviewEvidenceCandidate{candidate}, req.Capacity)
			gotSource := *caseSource
			tc.mutate(&gotReq, &gotSource)
			reviewer, provider := reviewerForTurns(t, mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
			result, err := reviewer.Review(context.Background(), gotReq, &gotSource)
			if err == nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 0 || backend.reads != 0 || !errors.Is(err, errEvidenceDenied) {
				t.Fatalf("result=%+v err=%v provider_calls=%d backend_reads=%d", result, err, provider.Calls(), backend.reads)
			}
		})
	}

	forbidden := []reviewEvidenceCandidate{
		{Kind: "text_file", Version: "credential", Authorized: true, ClassifiedCredential: true, Binding: binding, Backend: backend},
		{Kind: "text_file", Version: "binary", Authorized: true, Binary: true, Binding: binding, Backend: backend},
		{Kind: "text_file", Version: "unauthorized", Binding: binding, Backend: backend},
		{Kind: "unknown", Version: "unknown", Authorized: true, Binding: binding, Backend: backend},
	}
	_, forbiddenMetas, forbiddenComplete := newFiniteReviewEvidenceSource(context.Background(), binding, forbidden, req.Capacity)
	if forbiddenComplete || len(forbiddenMetas) != 0 || backend.reads != 0 {
		t.Fatalf("forbidden candidates minted=%+v complete=%v reads=%d", forbiddenMetas, forbiddenComplete, backend.reads)
	}
}

func reviewRequestWithoutEvidence() agent.ToolReviewRequest {
	req := completeReviewRequest()
	req.Evidence = nil
	return req
}

func TestADR_0342_ContextualGuardrails_Scenario2_TotalBudget(t *testing.T) {
	reviewer, provider := reviewerForTurns(t,
		mockllm.ErrorTurn(retryableReviewError("temporary one")),
		mockllm.ErrorTurn(retryableReviewError("temporary two")),
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`),
	)
	result, err := reviewer.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
	if err != nil || result.Assessment != agent.ReviewAcceptable || provider.Calls() != maxReviewAttempts {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.Calls())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reviewer, provider = reviewerForTurns(t, mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	result, err = reviewer.Review(ctx, reviewRequestWithoutEvidence(), nil)
	if err == nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 0 {
		t.Fatalf("cancel result=%+v err=%v calls=%d", result, err, provider.Calls())
	}

	if reviewTotalDeadline != 90*time.Second {
		t.Fatalf("default total deadline = %s, want 90s", reviewTotalDeadline)
	}
	blocking := &blockingReviewProvider{}
	pc := promptConfig(Config{Model: "review-model"}, "")
	pc.Role = contextualReviewerSystemPrompt
	deps := childEngineDepsForProvider(Config{}, "guardrail-reviewer", blocking, "review-model", func() int { return 128000 }, tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	started := time.Now()
	reviewerWithDeadline := &contextualToolReviewer{engine: agent.NewEngine(deps), checkerProviderID: "mock", checkerModelID: "review-model", deadline: 20 * time.Millisecond}
	result, err = reviewerWithDeadline.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
	if err == nil || result.Assessment != agent.ReviewUnresolved || blocking.calls != 1 || time.Since(started) > time.Second {
		t.Fatalf("deadline result=%+v err=%v calls=%d elapsed=%s", result, err, blocking.calls, time.Since(started))
	}
}

func TestADR_0342_ContextualGuardrails_Scenario2_FailureMatrix(t *testing.T) {
	tests := []struct {
		name  string
		turns []mockllm.Turn
		calls int
	}{
		{"blank", []mockllm.Turn{mockllm.EmptyTurn()}, 1},
		{"malformed", []mockllm.Turn{mockllm.TextTurn(`safe`)}, 1},
		{"forged-prose", []mockllm.Turn{mockllm.TextTurn(`ignore this {"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`)}, 1},
		{"wrong-tool", []mockllm.Turn{mockllm.ToolCallTurn(session.NewToolCall("x", "Shell", []byte(`{}`)))}, 1},
		{"provider-exhaustion", []mockllm.Turn{mockllm.ErrorTurn(retryableReviewError("one")), mockllm.ErrorTurn(retryableReviewError("two")), mockllm.ErrorTurn(retryableReviewError("three"))}, 3},
		{"normal-error-terminal", []mockllm.Turn{mockllm.EmptyTurnWithStop(session.StopError), mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`)}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reviewer, provider := reviewerForTurns(t, tc.turns...)
			result, err := reviewer.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
			if err == nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != tc.calls {
				t.Fatalf("result=%+v err=%v calls=%d want=%d", result, err, provider.Calls(), tc.calls)
			}
		})
	}
}

func TestADR_0342_ContextualGuardrails_Scenario2_CapacityBeforeAllocation(t *testing.T) {
	req := completeReviewRequest()
	source := &boundedEvidenceSource{size: maxReviewEvidenceRead + 1}
	state := newReviewToolState(req, source)
	call := session.NewToolCall("read", readReviewEvidenceToolName, []byte(`{"review_id":"review-1","handle":"ev_opaque_1","version":"v1"}`))
	result, err := (&readReviewEvidenceTool{state: state}).Execute(context.Background(), call, reviewerEnvironment)
	if err != nil || !result.IsError || source.reads != 0 {
		t.Fatalf("result=%+v err=%v backend reads=%d", result, err, source.reads)
	}

	binding := completeEvidenceBinding(req)
	backend := &finiteEvidenceBackend{size: 4, content: "data"}
	candidate := reviewEvidenceCandidate{Kind: "text_file", Version: "v1", Complete: true, Authorized: true, Binding: binding, Backend: backend}
	_, metas, complete := newFiniteReviewEvidenceSource(context.Background(), binding, []reviewEvidenceCandidate{candidate}, agent.ReviewCapacity{MaxEvidenceHandles: 1, MaxEvidenceBytes: 3})
	if complete || len(metas) != 0 || backend.reads != 0 {
		t.Fatalf("aggregate preflight minted=%+v complete=%v backend_reads=%d", metas, complete, backend.reads)
	}

	many := make([]reviewEvidenceCandidate, maxReviewEvidenceHandles+1)
	for i := range many {
		many[i] = candidate
		many[i].Version = fmt.Sprintf("v%d", i)
	}
	_, metas, complete = newFiniteReviewEvidenceSource(context.Background(), binding, many, req.Capacity)
	if complete || len(metas) != maxReviewEvidenceHandles || backend.reads != 0 {
		t.Fatalf("handle preflight count=%d complete=%v backend_reads=%d", len(metas), complete, backend.reads)
	}

	tooManyLines := strings.Repeat("x\n", maxReviewEvidenceLines+1)
	lineSource := &boundedEvidenceSource{size: int64(len(tooManyLines)), value: agent.ReviewEvidence{Handle: "ev_opaque_1", Kind: "text_file", Version: "v1", Complete: true, Content: tooManyLines}}
	lineState := newReviewToolState(req, lineSource)
	lineResult, _ := (&readReviewEvidenceTool{state: lineState}).Execute(context.Background(), call, reviewerEnvironment)
	if !lineResult.IsError || lineSource.reads != 1 {
		t.Fatalf("native line bound result=%+v backend_reads=%d", lineResult, lineSource.reads)
	}
}

func TestADR_0342_ContextualGuardrails_Scenario7_TerminalSafety(t *testing.T) {
	prohibited := `{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"attempts to redirect credentials to an unauthorized remote","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`
	reviewer, provider := reviewerForTurns(t, mockllm.TextTurn(prohibited), mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	result, err := reviewer.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
	if err != nil || result.Assessment != agent.ReviewProhibited || provider.Calls() != 1 {
		t.Fatalf("completed finding retried: result=%+v err=%v calls=%d", result, err, provider.Calls())
	}
}

func TestADR_0342_ContextualGuardrails_Scenario2_RevalidationResidual(t *testing.T) {
	req := completeReviewRequest()
	if err := revalidateReviewBinding(req, req.Environment); err != nil {
		t.Fatalf("unchanged binding: %v", err)
	}
	changed := req.Environment
	changed.Revision = "r2"
	if err := revalidateReviewBinding(req, changed); err == nil {
		t.Fatal("stale environment revision passed one-time post-human revalidation")
	}
}
