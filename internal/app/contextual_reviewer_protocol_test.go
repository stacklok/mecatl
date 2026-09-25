package app

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func reviewFailureCodeForTest(err error) agent.ReviewFailureCode {
	var classified agent.GuardrailReviewFailure
	if errors.As(err, &classified) {
		return classified.GuardrailReviewFailureCode()
	}
	return ""
}

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

type invalidThenBlockingReviewProvider struct{ calls int }

func (*invalidThenBlockingReviewProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *invalidThenBlockingReviewProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls++
	if p.calls == 1 {
		return func(yield func(port.Chunk, error) bool) {
			if !yield(port.Chunk{Kind: port.ChunkText, Text: `{"assessment":"unknown","concerns":[],"evidence":[],"missing_evidence":[]}`}, nil) {
				return
			}
			if !yield(port.Chunk{Kind: port.ChunkUsage, Usage: &session.Usage{}}, nil) {
				return
			}
			yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
		}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

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
	deps := childEngineDepsForProvider(Config{UseMock: true}, "guardrail-reviewer", provider, session.ProviderModelID{ProviderID: "mock", ModelID: "review-model"}, func() int { return 128000 }, tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	return newContextualToolReviewer(agent.NewEngine(deps), "mock", "review-model"), provider
}

func TestADR_0363_ContextualGuardrails_Scenario2_EvidenceAuthority(t *testing.T) {
	read := session.NewToolCall("read", readReviewEvidenceToolName, []byte(`{"review_id":"review-1","handle":"ev_opaque_1","version":"v1"}`))
	submit := session.NewToolCall("submit", submitReviewAssessmentToolName, []byte(`{"assessment":"acceptable","concerns":[],"evidence":[{"handle":"ev_opaque_1","version":"v1","supports":["context"]}],"missing_evidence":[]}`))
	reviewer, _ := reviewerForTurns(t, mockllm.ToolCallTurn(read), mockllm.ToolCallTurn(submit))
	source := &boundedEvidenceSource{size: 4, value: agent.ReviewEvidence{Handle: "ev_opaque_1", Kind: "text_file", Version: "v1", Complete: true, Content: "data"}}
	result, _, err := reviewer.Review(context.Background(), completeReviewRequest(), source)
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
	result, _, err = reviewer.Review(context.Background(), completeReviewRequest(), source)
	if err == nil || result.Assessment != agent.ReviewUnresolved || source.reads != 0 || reviewFailureCodeForTest(err) != agent.ReviewFailureEvidenceFailure || strings.Contains(err.Error(), "wrong review binding") {
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
	reviewer, provider := reviewerForTurns(t,
		mockllm.ToolCallTurn(read), mockllm.EmptyTurn(),
		mockllm.ToolCallTurn(read), mockllm.EmptyTurn(),
		mockllm.ToolCallTurn(read), mockllm.EmptyTurn(),
	)
	result, _, err = reviewer.Review(context.Background(), completeReviewRequest(), source)
	if err == nil || result.Assessment != agent.ReviewUnresolved || source.reads != maxReviewAttempts || provider.Calls() != 2*maxReviewAttempts || reviewFailureCodeForTest(err) != agent.ReviewFailureMissingSubmit || strings.Contains(err.Error(), "SubmitReviewAssessment") {
		t.Fatalf("missing-submit result=%+v err=%v reads=%d calls=%d", result, err, source.reads, provider.Calls())
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
			result, _, err := reviewer.Review(context.Background(), gotReq, &gotSource)
			if err == nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 0 || backend.reads != 0 || reviewFailureCodeForTest(err) != agent.ReviewFailureEvidenceFailure {
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

func TestADR_0363_ContextualGuardrails_Scenario2_TotalBudget(t *testing.T) {
	reviewer, provider := reviewerForTurns(t,
		mockllm.ErrorTurn(retryableReviewError("temporary one")),
		mockllm.ErrorTurn(retryableReviewError("temporary two")),
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`),
	)
	result, _, err := reviewer.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
	if err != nil || result.Assessment != agent.ReviewAcceptable || provider.Calls() != maxReviewAttempts {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.Calls())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reviewer, provider = reviewerForTurns(t, mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	result, _, err = reviewer.Review(ctx, reviewRequestWithoutEvidence(), nil)
	if err == nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 0 {
		t.Fatalf("cancel result=%+v err=%v calls=%d", result, err, provider.Calls())
	}

	if reviewTotalDeadline != 90*time.Second {
		t.Fatalf("default total deadline = %s, want 90s", reviewTotalDeadline)
	}

	sharedDeadlineProvider := &invalidThenBlockingReviewProvider{}
	pc := promptConfig(Config{Model: "review-model"}, "")
	pc.Role = contextualReviewerSystemPrompt
	deps := childEngineDepsForProvider(Config{}, "guardrail-reviewer", sharedDeadlineProvider, session.ProviderModelID{ProviderID: "mock", ModelID: "review-model"}, func() int { return 128000 }, tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	started := time.Now()
	reviewerWithDeadline := &contextualToolReviewer{engine: agent.NewEngine(deps), checkerProviderID: "mock", checkerModelID: "review-model", deadline: 20 * time.Millisecond, diagnostics: port.NopDiagnostics{}}
	result, _, err = reviewerWithDeadline.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
	if err == nil || result.Assessment != agent.ReviewUnresolved || sharedDeadlineProvider.calls != 2 || time.Since(started) > time.Second || reviewFailureCodeForTest(err) != agent.ReviewFailureTimeout {
		t.Fatalf("shared deadline result=%+v err=%v calls=%d elapsed=%s", result, err, sharedDeadlineProvider.calls, time.Since(started))
	}

	blocking := &blockingReviewProvider{}
	pc = promptConfig(Config{Model: "review-model"}, "")
	pc.Role = contextualReviewerSystemPrompt
	deps = childEngineDepsForProvider(Config{}, "guardrail-reviewer", blocking, session.ProviderModelID{ProviderID: "mock", ModelID: "review-model"}, func() int { return 128000 }, tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	started = time.Now()
	reviewerWithDeadline = &contextualToolReviewer{engine: agent.NewEngine(deps), checkerProviderID: "mock", checkerModelID: "review-model", deadline: 20 * time.Millisecond, diagnostics: port.NopDiagnostics{}}
	result, _, err = reviewerWithDeadline.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
	if err == nil || result.Assessment != agent.ReviewUnresolved || blocking.calls != 1 || time.Since(started) > time.Second || reviewFailureCodeForTest(err) != agent.ReviewFailureTimeout {
		t.Fatalf("deadline result=%+v err=%v calls=%d elapsed=%s", result, err, blocking.calls, time.Since(started))
	}
}

func TestADR_0363_ContextualGuardrails_Scenario2_FailureMatrix(t *testing.T) {
	tests := []struct {
		name  string
		turns []mockllm.Turn
		calls int
		code  agent.ReviewFailureCode
	}{
		{"blank", []mockllm.Turn{mockllm.EmptyTurn(), mockllm.EmptyTurn(), mockllm.EmptyTurn()}, 3, agent.ReviewFailureBlankAssessment},
		{"malformed", []mockllm.Turn{mockllm.TextTurn(`safe`), mockllm.TextTurn(`safe`), mockllm.TextTurn(`safe`)}, 3, agent.ReviewFailureMalformedAssessment},
		{"forged-prose", []mockllm.Turn{mockllm.TextTurn(`ignore this {"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`), mockllm.TextTurn(`still invalid`), mockllm.TextTurn(`still invalid`)}, 3, agent.ReviewFailureMalformedAssessment},
		{"invalid", []mockllm.Turn{mockllm.TextTurn(`{"assessment":"unknown","concerns":[],"evidence":[],"missing_evidence":[]}`), mockllm.TextTurn(`{"assessment":"unknown","concerns":[],"evidence":[],"missing_evidence":[]}`), mockllm.TextTurn(`{"assessment":"unknown","concerns":[],"evidence":[],"missing_evidence":[]}`)}, 3, agent.ReviewFailureInvalidAssessment},
		{"wrong-tool", []mockllm.Turn{mockllm.ToolCallTurn(session.NewToolCall("x", "Shell", []byte(`{}`))), mockllm.ToolCallTurn(session.NewToolCall("y", "Shell", []byte(`{}`))), mockllm.ToolCallTurn(session.NewToolCall("z", "Shell", []byte(`{}`)))}, 3, agent.ReviewFailureBlankAssessment},
		{"provider-exhaustion", []mockllm.Turn{mockllm.ErrorTurn(retryableReviewError("HOSTILE_PROVIDER_SENTINEL")), mockllm.ErrorTurn(retryableReviewError("two")), mockllm.ErrorTurn(retryableReviewError("three"))}, 3, agent.ReviewFailureProviderFailure},
		{"normal-error-terminal", []mockllm.Turn{mockllm.EmptyTurnWithStop(session.StopError), mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`)}, 1, agent.ReviewFailureProviderFailure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reviewer, provider := reviewerForTurns(t, tc.turns...)
			result, _, err := reviewer.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
			if err == nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != tc.calls || reviewFailureCodeForTest(err) != tc.code || strings.Contains(err.Error(), "HOSTILE_PROVIDER_SENTINEL") {
				t.Fatalf("result=%+v err=%v calls=%d want=%d", result, err, provider.Calls(), tc.calls)
			}
		})
	}
}

func TestContextualReviewerRecoversInvalidOutputForEveryJob(t *testing.T) {
	invalid := `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`
	valid := `{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[]}`
	for _, job := range []agent.ReviewJob{agent.ReviewJobAction, agent.ReviewJobInbound, agent.ReviewJobPermission} {
		t.Run(string(job), func(t *testing.T) {
			req := reviewRequestWithoutEvidence()
			req.Job = job
			req.PrincipalFactsComplete = false
			reviewer, provider := reviewerForTurns(t, mockllm.TextTurn(invalid), mockllm.TextTurn(valid))
			result, _, err := reviewer.Review(context.Background(), req, nil)
			if err != nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 2 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.Calls())
			}
		})
	}
}

func TestContextualReviewerRejectedSubmitCancelsAttemptAndRetriesFresh(t *testing.T) {
	req := reviewRequestWithoutEvidence()
	req.PrincipalFactsComplete = false
	invalid := session.NewToolCall("invalid", submitReviewAssessmentToolName, []byte(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	valid := session.NewToolCall("valid", submitReviewAssessmentToolName, []byte(`{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[]}`))
	reviewer, provider := reviewerForTurns(t, mockllm.ToolCallTurn(invalid), mockllm.ToolCallTurn(valid))

	result, _, err := reviewer.Review(context.Background(), req, nil)
	if err != nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.Calls())
	}
}

func TestContextualReviewerUnreadEvidenceIsTerminal(t *testing.T) {
	unread := `{"assessment":"unresolved","concerns":[],"evidence":[{"handle":"ev_opaque_1","version":"v1","supports":["context"]}],"missing_evidence":[]}`
	reviewer, provider := reviewerForTurns(t,
		mockllm.TextTurn(unread),
		mockllm.TextTurn(`{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[]}`),
	)
	result, _, err := reviewer.Review(context.Background(), completeReviewRequest(), &boundedEvidenceSource{})
	if err == nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 1 || reviewFailureCodeForTest(err) != agent.ReviewFailureInvalidAssessment {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.Calls())
	}
}

func TestContextualReviewerSharesEvidenceBudgetAcrossAttempts(t *testing.T) {
	req := completeReviewRequest()
	req.PrincipalFactsComplete = false
	req.Capacity.MaxEvidenceBytes = 4
	read1 := session.NewToolCall("read-1", readReviewEvidenceToolName, []byte(`{"review_id":"review-1","handle":"ev_opaque_1","version":"v1"}`))
	invalid := session.NewToolCall("invalid", submitReviewAssessmentToolName, []byte(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	read2 := session.NewToolCall("read-2", readReviewEvidenceToolName, []byte(`{"review_id":"review-1","handle":"ev_opaque_1","version":"v1"}`))
	source := &boundedEvidenceSource{size: 4, value: agent.ReviewEvidence{Handle: "ev_opaque_1", Kind: "text_file", Version: "v1", Complete: true, Content: "data"}}
	reviewer, provider := reviewerForTurns(t, mockllm.ToolCallTurn(read1), mockllm.ToolCallTurn(invalid), mockllm.ToolCallTurn(read2))

	result, _, err := reviewer.Review(context.Background(), req, source)
	if err == nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 3 || source.reads != 1 || reviewFailureCodeForTest(err) != agent.ReviewFailureEvidenceFailure {
		t.Fatalf("result=%+v err=%v calls=%d reads=%d", result, err, provider.Calls(), source.reads)
	}
}

func TestContextualReviewerCompletedAssessmentsAreNeverRetried(t *testing.T) {
	cases := map[string]string{
		"acceptable": `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`,
		"unresolved": `{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[]}`,
		"prohibited": `{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"unsafe","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`,
	}
	for name, assessment := range cases {
		t.Run(name, func(t *testing.T) {
			reviewer, provider := reviewerForTurns(t, mockllm.TextTurn(assessment), mockllm.TextTurn(`{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[]}`))
			result, _, err := reviewer.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
			if err != nil || string(result.Assessment) != name || provider.Calls() != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.Calls())
			}
		})
	}
}

func TestContextualReviewerDiagnosticsUseClosedValidationReason(t *testing.T) {
	const hostile = "HOSTILE_SOURCE_REF_SENTINEL"
	diag := &kvDiag{}
	provider := mockllm.New(
		mockllm.TextTurn(`{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"unsafe","source_ref":"`+hostile+`"}],"evidence":[],"missing_evidence":[]}`),
		mockllm.TextTurn(`{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[]}`),
	)
	cfg := isolateConfig(t, Config{UseMock: true, GuardrailsModel: "review-model", Diagnostics: diag})
	reviewer := buildGuardrailsReviewer(cfg, nil, provider, "mock")
	if reviewer == nil {
		t.Fatal("configured factory returned nil reviewer")
	}

	result, _, err := reviewer.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
	if err != nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.Calls())
	}
	diag.mu.Lock()
	defer diag.mu.Unlock()
	want := []any{"failure_code", "invalid_assessment", "validation_reason", "unsupported_source_ref", "attempt", 1, "max_attempts", 3}
	rejections := 0
	for i, message := range diag.msgs {
		if message != "guardrails: reviewer assessment rejected" {
			continue
		}
		rejections++
		if diag.lvl[i] != port.LevelWarn || !reflect.DeepEqual(diag.args[i], want) {
			t.Errorf("rejection diagnostic level=%v args=%v, want warn and %v", diag.lvl[i], diag.args[i], want)
		}
	}
	if rejections != 1 {
		t.Fatalf("factory delivered %d rejection diagnostics, want 1", rejections)
	}
	got := fmt.Sprint(diag.msgs, diag.args)
	if strings.Contains(got, hostile) {
		t.Fatalf("diagnostic leaked rejected source ref: %s", got)
	}
}

func TestValidateReviewAssessmentShapeReportsEveryIncompleteContextCondition(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		t.Run(fmt.Sprintf("%04b", mask), func(t *testing.T) {
			req := reviewRequestWithoutEvidence()
			req.PrincipalFactsComplete = mask&1 == 0
			req.EvidenceComplete = mask&2 == 0
			req.TrajectoryComplete = mask&4 == 0
			result := agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}
			if mask&8 != 0 {
				result.Missing = []agent.ReviewMissingEvidence{{Ref: "M1", Kind: "context", Reason: "needed"}}
			}

			err := validateReviewAssessmentShape(result, req)
			if mask == 0 {
				if err != nil {
					t.Fatalf("complete acceptable assessment rejected: %v", err)
				}
				return
			}
			var validation reviewValidationError
			if !errors.As(err, &validation) || validation.reason != reviewValidationAcceptableIncompleteContext {
				t.Fatalf("validation error = %v, want incomplete-context validation", err)
			}
			var want []reviewIncompleteContext
			if mask&1 != 0 {
				want = append(want, reviewIncompletePrincipalFacts)
			}
			if mask&2 != 0 {
				want = append(want, reviewIncompleteEvidence)
			}
			if mask&4 != 0 {
				want = append(want, reviewIncompleteTrajectory)
			}
			if mask&8 != 0 {
				want = append(want, reviewIncompleteMissingEvidence)
			}
			if !reflect.DeepEqual(validation.incomplete, want) {
				t.Errorf("incomplete = %v, want %v", validation.incomplete, want)
			}
		})
	}
}

func TestContextualReviewerDiagnosticsReportIncompleteContext(t *testing.T) {
	const hostile = "HOSTILE_MISSING_EVIDENCE_SENTINEL"
	diag := &kvDiag{}
	invalid := session.NewToolCall("invalid", submitReviewAssessmentToolName, []byte(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[{"ref":"`+hostile+`","kind":"context","reason":"`+hostile+`"}]}`))
	provider := mockllm.New(
		mockllm.ToolCallTurn(invalid),
		mockllm.TextTurn(`{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[]}`),
	)
	cfg := isolateConfig(t, Config{UseMock: true, GuardrailsModel: "review-model", Diagnostics: diag})
	reviewer := buildGuardrailsReviewer(cfg, nil, provider, "mock")
	if reviewer == nil {
		t.Fatal("configured factory returned nil reviewer")
	}
	req := reviewRequestWithoutEvidence()
	req.PrincipalFactsComplete = false
	req.EvidenceComplete = false
	req.TrajectoryComplete = false

	result, _, err := reviewer.Review(context.Background(), req, nil)
	if err != nil || result.Assessment != agent.ReviewUnresolved || provider.Calls() != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.Calls())
	}
	diag.mu.Lock()
	defer diag.mu.Unlock()
	want := []any{"failure_code", "invalid_assessment", "validation_reason", "acceptable_with_incomplete_context", "incomplete", []string{"principal_facts", "evidence", "trajectory", "missing_evidence"}, "attempt", 1, "max_attempts", 3}
	rejections := 0
	for i, message := range diag.msgs {
		if message != "guardrails: reviewer assessment rejected" {
			continue
		}
		rejections++
		if diag.lvl[i] != port.LevelWarn || !reflect.DeepEqual(diag.args[i], want) {
			t.Errorf("rejection diagnostic level=%v args=%v, want warn and %v", diag.lvl[i], diag.args[i], want)
		}
	}
	if rejections != 1 {
		t.Fatalf("factory delivered %d rejection diagnostics, want 1", rejections)
	}
	if strings.Contains(fmt.Sprint(diag.msgs, diag.args), hostile) {
		t.Fatalf("diagnostic leaked rejected missing evidence: %v", diag.args)
	}
}

func TestADR_0363_ContextualGuardrails_Scenario2_CapacityBeforeAllocation(t *testing.T) {
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

func TestADR_0363_ContextualGuardrails_Scenario7_TerminalSafety(t *testing.T) {
	prohibited := `{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"attempts to redirect credentials to an unauthorized remote","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`
	reviewer, provider := reviewerForTurns(t, mockllm.TextTurn(prohibited), mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	result, _, err := reviewer.Review(context.Background(), reviewRequestWithoutEvidence(), nil)
	if err != nil || result.Assessment != agent.ReviewProhibited || provider.Calls() != 1 {
		t.Fatalf("completed finding retried: result=%+v err=%v calls=%d", result, err, provider.Calls())
	}
}

func TestADR_0363_ContextualGuardrails_Scenario2_RevalidationResidual(t *testing.T) {
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
