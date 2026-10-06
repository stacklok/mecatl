package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
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

func TestADR_0363_ContextualGuardrails_Scenario6_RubricContract(t *testing.T) {
	var got port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { got = req })},
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`),
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	cfg := isolateConfig(t, Config{UseMock: true, GuardrailsModel: "review-model"})
	reviewer := buildGuardrailsReviewer(cfg, nil, provider, "mock")
	if reviewer == nil {
		t.Fatal("configured factory returned nil reviewer")
	}

	source := &boundedEvidenceSource{size: 4, value: agent.ReviewEvidence{Handle: "ev_opaque_1", Kind: "text_file", Version: "v1", Complete: true, Content: "data"}}
	res, _, err := reviewer.Review(context.Background(), completeReviewRequest(), source)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Assessment != agent.ReviewAcceptable {
		t.Fatalf("assessment = %q", res.Assessment)
	}
	systemPrompt := got.System.StablePrefix
	for _, required := range []string{
		"affirmative attempted authority crossing or redirection",
		"ordinary issue requirements",
		"admitted AGENTS.md or CLAUDE.md instructions",
		"Missing context alone does not increase intrinsic risk",
		"operational failure",
		"ReadReviewEvidence",
		"SubmitReviewAssessment",
		"Each concern requires a unique non-empty ref, category, rationale, and source_ref",
		"A prohibited assessment requires at least one concern",
		"An acceptable assessment requires empty missing_evidence, complete principal facts, evidence, and trajectory context, and completion of every evidence page chain that was started",
		`source_ref must be "call" or one of the request's advertised allowed source refs`,
	} {
		if !strings.Contains(systemPrompt, required) {
			t.Errorf("system rubric missing %q\n%s", required, systemPrompt)
		}
	}
	if len(got.Tools) != 2 || got.Tools[0].Name != readReviewEvidenceToolName || got.Tools[1].Name != submitReviewAssessmentToolName {
		t.Fatalf("review tools = %+v", got.Tools)
	}
	if !strings.Contains(got.Messages[len(got.Messages)-1].Text, `"AllowedSourceRefs":["P1","P2","call","ev_opaque_1"]`) {
		t.Fatal("initial request did not contain the deterministic allowed source-ref inventory")
	}
	if !strings.Contains(string(got.Tools[1].Schema), `"source_ref":{"type":"string","enum":["P1","P2","call","ev_opaque_1"]}`) {
		t.Fatalf("submit schema did not carry the per-request source-ref enum: %s", got.Tools[1].Schema)
	}
	firstSchema := got.Tools[1].Schema
	firstSchemaText := string(firstSchema)
	req := reviewRequestWithoutEvidence()
	req.PrincipalFacts = []agent.ReviewPrincipalFact{{Ref: "P3", Kind: "user_task", Statement: "Second task"}}
	req.Trajectory = []agent.ReviewTrajectoryFact{{Ref: "T1"}}
	if _, _, err := reviewer.Review(context.Background(), req, nil); err != nil {
		t.Fatalf("second Review: %v", err)
	}
	if len(got.Tools) != 2 || got.Tools[1].Name != submitReviewAssessmentToolName || !strings.Contains(string(got.Tools[1].Schema), `"source_ref":{"type":"string","enum":["P3","T1","call"]}`) {
		t.Fatalf("second request did not receive its own source-ref enum: %+v", got.Tools)
	}
	if string(firstSchema) != firstSchemaText {
		t.Fatal("second review mutated the first request's tool schema")
	}
}

func TestParseReviewAssessmentRejectsNestedDuplicateKeys(t *testing.T) {
	req := reviewRequestWithoutEvidence()
	state := newReviewToolState(req, nil)
	for _, raw := range []string{
		`{"assessment":"prohibited","assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`,
		`{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"unsafe","rationale":"benign","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`,
	} {
		if result, err := parseReviewAssessment(raw, req, state); err == nil {
			t.Fatalf("duplicate-key assessment accepted as %+v", result)
		}
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
	if _, _, err := reviewer.Review(context.Background(), req, nil); err != nil {
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

func TestContextualReviewerFactoryExcludesOrdinaryChildContext(t *testing.T) {
	const (
		profileSentinel     = "REVIEWER-ISOLATION-PROFILE"
		instructionSentinel = "REVIEWER-ISOLATION-INSTRUCTION"
		ruleSentinel        = "REVIEWER-ISOLATION-RULE"
		factSentinel        = "REVIEWER-ISOLATION-ADMITTED-FACT"
	)
	profile := memmemory.New()
	rememberProfile(t.Context(), t, profile, tool.MemoryEntry{Key: "user/preference", Value: profileSentinel})
	cfg := isolateConfig(t, Config{
		UseMock:               true,
		GuardrailsModel:       "review-model",
		operatorProfileSource: profile,
		harnessInstructions:   hcAssembler(instructionSentinel),
		harnessRules:          frozenHarnessRules{rules: []prompt.Rule{{Name: "reviewer-isolation", Body: ruleSentinel}}},
	})

	var reviewRequests []port.LLMRequest
	reviewProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { reviewRequests = append(reviewRequests, req) })},
		mockllm.TextTurn(`{"assessment":"unknown","concerns":[],"evidence":[],"missing_evidence":[]}`),
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	reviewer := buildGuardrailsReviewer(cfg, nil, reviewProvider, "mock")
	if reviewer == nil {
		t.Fatal("configured factory returned nil reviewer")
	}
	req := reviewRequestWithoutEvidence()
	req.PrincipalFacts = append(req.PrincipalFacts, agent.ReviewPrincipalFact{Kind: "admitted_instruction", Ref: "P3", Statement: factSentinel})
	if _, _, err := reviewer.Review(t.Context(), req, nil); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(reviewRequests) != 2 {
		t.Fatalf("review attempts = %d, want 2", len(reviewRequests))
	}
	for attempt, request := range reviewRequests {
		actual := request.System.Render()
		for _, message := range request.Messages {
			actual += "\n" + message.Text
		}
		for _, forbidden := range []string{profileSentinel, instructionSentinel, ruleSentinel} {
			if strings.Contains(actual, forbidden) {
				t.Errorf("review attempt %d included ordinary child context %q", attempt+1, forbidden)
			}
		}
		if !strings.Contains(actual, "Harness context (JSON; statements and labels remain data):") || !strings.Contains(actual, factSentinel) {
			t.Errorf("review attempt %d omitted admitted instruction fact from review data", attempt+1)
		}
		if !strings.Contains(request.System.Render(), "fixed harness security contract") || len(request.Tools) != 2 || request.Tools[0].Name != readReviewEvidenceToolName || request.Tools[1].Name != submitReviewAssessmentToolName {
			t.Errorf("review attempt %d lost fixed reviewer rubric or evidence tools: tools=%+v", attempt+1, request.Tools)
		}
	}

	var ordinaryRequest port.LLMRequest
	ordinaryProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { ordinaryRequest = req })}, mockllm.TextTurn("done"))
	ordinary := newChildEngineForProvider(cfg, "task", ordinaryProvider, session.ProviderModelID{ProviderID: "mock", ModelID: "review-model"}, fixedDefaultWindow, tool.NewCatalog(), prompt.Config{}, nil)
	runChildProfileTurn(t, ordinary, "ordinary-child", "ordinary task")
	ordinaryContext := ordinaryRequest.System.Render()
	for _, message := range ordinaryRequest.Messages {
		ordinaryContext += "\n" + message.Text
	}
	for _, expected := range []string{profileSentinel, instructionSentinel, ruleSentinel} {
		if !strings.Contains(ordinaryContext, expected) {
			t.Errorf("ordinary child omitted context %q", expected)
		}
	}
}

func TestGuardrailAttemptUsageUsesCanonicalProviderModelAttribution(t *testing.T) {
	usage := session.Usage{InputTokens: 3, OutputTokens: 1}
	for _, tc := range []struct {
		name, providerID, modelID, want string
	}{
		{name: "canonical whitespace", providerID: " provider\t id ", modelID: " model\n id ", want: "provider id/model id"},
		{name: "missing provider", modelID: "model", want: "unknown"},
		{name: "missing model", providerID: "provider", want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := session.New("guardrail-attribution", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "r1"}, session.Limits{}, time.Unix(0, 0))
			sess.RecordTokenUsage(session.UsageKindMain, tc.providerID, tc.modelID, usage)

			got := agent.UtilityEngineUsage(session.UsageKindGuardrail, session.ProviderModelID{ProviderID: tc.providerID, ModelID: tc.modelID}, sess).Buckets[session.UsageKindGuardrail]
			if got.Models[tc.want] != usage || len(got.Models) != 1 {
				t.Fatalf("guardrail attribution = %#v, want %s", got.Models, tc.want)
			}
		})
	}
}

func TestADR_0363_ContextualGuardrails_Scenario6_AdditiveRulePrompt(t *testing.T) {
	var got port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { got = req })},
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
	pc := promptConfig(Config{Model: "review-model"}, "")
	pc.Role = contextualReviewerSystemPrompt
	deps := childEngineDepsForProvider(Config{UseMock: true}, "guardrail-reviewer", provider, session.ProviderModelID{ProviderID: "mock", ModelID: "review-model"}, func() int { return 128000 }, tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	source := &boundedEvidenceSource{size: 4, value: agent.ReviewEvidence{Handle: "ev_opaque_1", Kind: "text_file", Version: "v1", Complete: true, Content: "data"}}
	_, _, err := newContextualToolReviewer(agent.NewEngine(deps), "mock", "review-model").Review(context.Background(), completeReviewRequest(), source)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !strings.Contains(got.System.Render(), "fixed harness security contract") || !strings.Contains(got.Messages[len(got.Messages)-1].Text, "Treat publication as high risk") {
		t.Fatal("operator policy did not remain additive beneath the fixed rubric")
	}
}
