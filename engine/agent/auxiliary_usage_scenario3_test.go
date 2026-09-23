package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestAuxiliaryTokenUsage_Scenario3_RouterDoesNotFoldIntoMain(t *testing.T) {
	usage := session.Usage{InputTokens: 7, OutputTokens: 3}
	aux := auxiliaryUsage(session.UsageKindRouter, session.ProviderModelID{ProviderID: "provider-r", ModelID: "classifier"}, usage)
	engine := NewEngine(Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main",
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{Category: "large", Model: "big", Usage: aux, OK: true}
		}},
	})
	parent := runningAuxiliaryParent(t, "router-parent")
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}

	got := engine.parentCaps(run, parent, 0).routeDecision(t.Context(), "classify")
	if !got.ok {
		t.Fatalf("routeDecision ok = false, reason %q", got.reason)
	}
	if main := parent.UsageFor(session.UsageKindMain); main != (session.Usage{}) {
		t.Fatalf("main usage = %+v, want zero", main)
	}
	if router := parent.UsageFor(session.UsageKindRouter); router != usage {
		t.Fatalf("router usage = %+v, want %+v", router, usage)
	}
}

type replayingUsageHook struct {
	usage    session.AuxiliaryUsage
	reporter port.AuxiliaryUsageReporter
}

func (h *replayingUsageHook) Run(ctx context.Context, _ governance.HookEvent) (governance.HookOutcome, error) {
	h.reporter = port.AuxiliaryUsageReporterFromContext(ctx)
	if h.reporter != nil {
		h.reporter(h.usage)
	}
	return governance.HookOutcome{}, nil
}

func TestAuxiliaryTokenUsage_Scenario3_SafetyChecksRecordParentUsage(t *testing.T) {
	reviewerUsage := session.Usage{InputTokens: 4, OutputTokens: 1}
	reviewer := NewEngineAskReviewer(
		reviewerEngine(mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
			{Kind: port.ChunkText, Text: `{"allow":true,"reason":"safe"}`},
			{Kind: port.ChunkUsage, Usage: &reviewerUsage},
			{Kind: port.ChunkDone},
		}})),
	)
	review, reviewUsage, err := reviewer.Review(t.Context(), ChildAskReviewRequest{Ask: shellAsk("git status"), Isolated: true})
	if err != nil || !review.Allowed {
		t.Fatalf("Review() = %+v, %v", review, err)
	}

	guardrailUsage := session.Usage{InputTokens: 6, OutputTokens: 2}
	checker := reviewerEngine(mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: `{"safe":true}`},
		{Kind: port.ChunkUsage, Usage: &guardrailUsage},
		{Kind: port.ChunkDone},
	}}))
	_, checkedUsage, err := RunGuardrailCheck(t.Context(), checker, "check")
	if err != nil {
		t.Fatal(err)
	}

	parent := runningAuxiliaryParent(t, "safety-parent")
	parent.RecordAuxiliaryUsage(reviewUsage)
	hook := &replayingUsageHook{usage: checkedUsage}
	hookEngine := NewEngine(Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main", Hooks: hook})
	if _, err := hookEngine.runOwnedHook(t.Context(), parent, governance.HookEvent{Phase: governance.PhasePreToolUse}); err != nil {
		t.Fatal(err)
	}
	if hook.reporter == nil {
		t.Fatal("Engine did not install AuxiliaryUsageReporter during hook request")
	}
	// A retained callback is inert as soon as HookRunner.Run returns.
	hook.reporter(checkedUsage)
	if got := parent.UsageFor(session.UsageKindAskReviewer); got != reviewerUsage {
		t.Fatalf("ask reviewer usage = %+v, want %+v", got, reviewerUsage)
	}
	if got := parent.UsageFor(session.UsageKindGuardrail); got != guardrailUsage {
		t.Fatalf("guardrail usage = %+v, want %+v", got, guardrailUsage)
	}
	if got := parent.UsageFor(session.UsageKindMain); got != (session.Usage{}) {
		t.Fatalf("main usage = %+v, want zero", got)
	}
}

func TestEngineJudgeReturnsParallelUsage(t *testing.T) {
	usage := session.Usage{InputTokens: 8, OutputTokens: 2}
	judge := NewEngineJudge(
		reviewerEngine(mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
			{Kind: port.ChunkText, Text: `{"winner":1,"rationale":"best"}`},
			{Kind: port.ChunkUsage, Usage: &usage},
			{Kind: port.ChunkDone},
		}})),
	).(*engineJudge)
	judge.identity = session.ProviderModelID{ProviderID: "provider-p", ModelID: "inherited-model"}
	winner, _, gotUsage, err := judge.Judge(t.Context(), []BranchSummary{{Label: "one", Summary: "one"}, {Label: "two", Summary: "two"}}, "best")
	if err != nil || winner != 0 {
		t.Fatalf("Judge() winner=%d err=%v", winner, err)
	}
	bucket := gotUsage.Buckets[session.UsageKindParallelJudge]
	if got := bucket.Models["provider-p/inherited-model"]; got != usage {
		t.Fatalf("parallel judge attribution = %+v, want %+v", bucket, usage)
	}
}

type zeroUsageReviewer struct{}

func (zeroUsageReviewer) Review(context.Context, ChildAskReviewRequest) (ChildAskReview, session.AuxiliaryUsage, error) {
	return ChildAskReview{Allowed: true}, session.AuxiliaryUsage{}, nil
}

type zeroUsageJudge struct{}

func (zeroUsageJudge) Judge(context.Context, []BranchSummary, string) (int, string, session.AuxiliaryUsage, error) {
	return 0, "custom", session.AuxiliaryUsage{}, nil
}

func TestAuxiliaryTokenUsage_Scenario3_NoUsageFromNonLLMHelpers(t *testing.T) {
	parent := runningAuxiliaryParent(t, "non-llm-parent")
	_, reviewUsage, err := (zeroUsageReviewer{}).Review(t.Context(), ChildAskReviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, judgeUsage, err := (zeroUsageJudge{}).Judge(t.Context(), []BranchSummary{{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.RecordAuxiliaryUsage(reviewUsage.Merge(judgeUsage))
	for _, kind := range []session.UsageKind{session.UsageKindAskReviewer, session.UsageKindParallelJudge} {
		if got := parent.UsageFor(kind); got != (session.Usage{}) {
			t.Fatalf("non-LLM helper fabricated %s usage: %+v", kind, got)
		}
	}
}

func TestAuxiliaryTokenUsage_Scenario3_NormalChildRunsRemainMain(t *testing.T) {
	usage := session.Usage{InputTokens: 9, OutputTokens: 1}
	engine := reviewerEngine(mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: "done"},
		{Kind: port.ChunkUsage, Usage: &usage},
		{Kind: port.ChunkDone},
	}}))
	sess := session.New("ordinary-child", session.ModeDefault, judgeEnvironment.Ref(), session.Limits{MaxTurns: 1}, time.Unix(1, 0))
	_, stop := drainChild(engine.Run(t.Context(), sess, judgeEnvironment, RunRequest{Text: "work"}), childPosture{role: "child"})
	if stop != session.StopEndTurn {
		t.Fatalf("stop = %q", stop)
	}
	if got := sess.UsageFor(session.UsageKindMain); got != usage {
		t.Fatalf("main usage = %+v, want %+v", got, usage)
	}
	for _, kind := range []session.UsageKind{session.UsageKindRouter, session.UsageKindAskReviewer, session.UsageKindGuardrail, session.UsageKindParallelJudge} {
		if got := sess.UsageFor(kind); got != (session.Usage{}) {
			t.Fatalf("%s usage = %+v, want zero", kind, got)
		}
	}
}

func runningAuxiliaryParent(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/", Revision: "test"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.RecordUserPrompt("go", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	return sess
}
