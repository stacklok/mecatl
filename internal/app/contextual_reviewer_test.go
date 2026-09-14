package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func completeReviewRequest() agent.ToolReviewRequest {
	return agent.ToolReviewRequest{
		ReviewID:      "review-1",
		Job:           agent.ReviewJobInbound,
		Event:         governance.HookEvent{SessionID: "session-1", CallID: "call-1", Tool: "Read", Phase: governance.PhasePostToolUse},
		EffectiveCall: session.NewToolCall("call-1", "Read", []byte(`{"path":"issue.md"}`)),
		PrincipalFacts: []agent.ReviewPrincipalFact{
			{Kind: "user_task", Ref: "P1", Statement: "Review the issue requirements", PositiveVerdict: true},
			{Kind: "operator_task_risk_policy", Ref: "P2", Statement: "Treat publication as high risk"},
		},
		PrincipalFactsComplete: true,
		Caller:                 agent.ReviewCaller{Role: "main", Capabilities: []string{"Read"}},
		Environment:            session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "r1"},
		Evidence: []agent.ReviewEvidenceMeta{
			{Handle: "ev_opaque_1", Kind: "text_file", Display: "issue requirements", Version: "v1", Complete: true},
		},
		EvidenceComplete:   true,
		TrajectoryComplete: true,
		Capacity: agent.ReviewCapacity{
			MaxEvidenceHandles: maxReviewEvidenceHandles,
			MaxEvidenceBytes:   maxReviewEvidenceBytes,
		},
	}
}

func TestADR_0342_ContextualGuardrails_Scenario6_RubricContract(t *testing.T) {
	var got port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { got = req })},
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	pc := promptConfig(Config{Model: "review-model"}, "")
	pc.Role = contextualReviewerSystemPrompt
	deps := childEngineDepsForProvider(Config{UseMock: true}, "guardrail-reviewer", provider, "review-model", func() int { return 128000 }, tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	reviewer := newContextualToolReviewer(agent.NewEngine(deps), "mock", "review-model")

	source := &boundedEvidenceSource{size: 4, value: agent.ReviewEvidence{Handle: "ev_opaque_1", Kind: "text_file", Version: "v1", Complete: true, Content: "data"}}
	res, err := reviewer.Review(context.Background(), completeReviewRequest(), source)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Assessment != agent.ReviewAcceptable {
		t.Fatalf("assessment = %q", res.Assessment)
	}
	prompt := got.System.Render()
	for _, required := range []string{
		"affirmative attempted authority crossing or redirection",
		"ordinary issue requirements",
		"admitted AGENTS.md or CLAUDE.md instructions",
		"Missing context alone does not increase intrinsic risk",
		"operational failure",
		"ReadReviewEvidence",
		"SubmitReviewAssessment",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("system rubric missing %q\n%s", required, prompt)
		}
	}
	if len(got.Tools) != 2 || got.Tools[0].Name != readReviewEvidenceToolName || got.Tools[1].Name != submitReviewAssessmentToolName {
		t.Fatalf("review tools = %+v", got.Tools)
	}
	if !strings.Contains(got.Messages[len(got.Messages)-1].Text, "ev_opaque_1") {
		t.Fatal("initial request did not contain the finite evidence handle inventory")
	}
}

func TestContextualReviewerFactoryPromptAndJobSeparation(t *testing.T) {
	var got port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { got = req })},
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	reviewer := buildGuardrailsReviewer(Config{UseMock: true, GuardrailsModel: "review-model"}, nil, provider, "mock")
	if reviewer == nil {
		t.Fatal("configured factory returned nil reviewer")
	}
	req := reviewRequestWithoutEvidence()
	req.Job = agent.ReviewJobAction
	incoming := strings.Repeat("incoming-result-is-not-evidence-capped-", 1_000)
	req.Event.Input = []byte(incoming)
	if _, err := reviewer.Review(context.Background(), req, nil); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !strings.Contains(got.System.StablePrefix, "fixed harness security contract") || !strings.Contains(got.Messages[len(got.Messages)-1].Text, "Apply the ACTION rubric") || !strings.Contains(got.Messages[len(got.Messages)-1].Text, incoming) {
		t.Fatalf("factory prompt lost rubric, action contract, or uncapped incoming result: system=%q message_bytes=%d", got.System.StablePrefix, len(got.Messages[len(got.Messages)-1].Text))
	}
	actionPrompt := buildContextualReviewPrompt(req)
	req.Job = agent.ReviewJobInbound
	inboundPrompt := buildContextualReviewPrompt(req)
	if actionPrompt == inboundPrompt || !strings.Contains(inboundPrompt, "Apply the INBOUND rubric") || strings.Contains(inboundPrompt, "Apply the ACTION rubric") {
		t.Fatalf("action and inbound jobs did not receive distinct prompts\naction=%s\ninbound=%s", actionPrompt, inboundPrompt)
	}
}

func TestADR_0342_ContextualGuardrails_Scenario6_AdditiveRulePrompt(t *testing.T) {
	var got port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { got = req })},
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	pc := promptConfig(Config{Model: "review-model"}, "")
	pc.Role = contextualReviewerSystemPrompt
	deps := childEngineDepsForProvider(Config{UseMock: true}, "guardrail-reviewer", provider, "review-model", func() int { return 128000 }, tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	source := &boundedEvidenceSource{size: 4, value: agent.ReviewEvidence{Handle: "ev_opaque_1", Kind: "text_file", Version: "v1", Complete: true, Content: "data"}}
	_, err := newContextualToolReviewer(agent.NewEngine(deps), "mock", "review-model").Review(context.Background(), completeReviewRequest(), source)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !strings.Contains(got.System.Render(), "fixed harness security contract") || !strings.Contains(got.Messages[len(got.Messages)-1].Text, "Treat publication as high risk") {
		t.Fatal("operator policy did not remain additive beneath the fixed rubric")
	}
}
