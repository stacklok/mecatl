package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

type pathUsageChecker struct {
	result modelhook.CheckResult
	err    error
}

func (c pathUsageChecker) Check(context.Context, modelhook.CheckRequest) (modelhook.CheckResult, error) {
	return c.result, c.err
}

func pathCheckerUsage(tokens int) session.AuxiliaryUsage {
	u := session.Usage{InputTokens: tokens}
	return session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindGuardrail: {Total: u, Models: map[string]session.Usage{"provider/guardrail-model": u}},
	}}
}

func TestContextualGuardrailUsage_Scenario1_RetryAndTerminalUsageCountedOnce(t *testing.T) {
	first := session.Usage{InputTokens: 2}
	second := session.Usage{InputTokens: 3}
	reviewer, _ := reviewerForTurns(t,
		mockllm.ErrorTurn(retryableReviewError("temporary"), mockllm.UsageChunk(first)),
		mockllm.ChunksTurn(
			mockllm.TextChunk(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`),
			mockllm.UsageChunk(second),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	result, usage, err := reviewer.Review(t.Context(), reviewRequestWithoutEvidence(), nil)
	if err != nil || result.Assessment != agent.ReviewAcceptable {
		t.Fatalf("retried review = %+v, err=%v", result, err)
	}
	bucket := usage.Buckets[session.UsageKindGuardrail]
	if bucket.Total.InputTokens != first.InputTokens+second.InputTokens || bucket.Models["mock/review-model"].InputTokens != 5 {
		t.Fatalf("retry usage = %+v, want both physical attempts once", bucket)
	}
}

type productionPathUsageTool struct{}

func (productionPathUsageTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Schema: []byte(`{"type":"object"}`)}
}
func (productionPathUsageTool) ReadOnly() bool { return true }
func (productionPathUsageTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

func TestContextualGuardrailUsage_Scenario2_PathEscapeUsageProductionFlow(t *testing.T) {
	fixture := setupEscapeFS(t)
	usage := session.Usage{InputTokens: 7, OutputTokens: 2}
	policy := buildRoutedEscapePolicy(t, Config{UseMock: true, GuardrailsModel: "checker-model", Posture: PostureAuto},
		mockllm.ChunksTurn(
			mockllm.TextChunk(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`),
			mockllm.UsageChunk(usage),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	workspace, err := osfs.NewWorkspace(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: fixture.workspace, Revision: "test"}, workspace, memledger.New(), nil)
	catalog := tool.NewCatalog()
	catalog.MustRegister(productionPathUsageTool{})
	call := session.NewToolCall("escape", "Read", []byte(`{"path":"`+fixture.target+`"}`))
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: catalog, Policy: policy})
	sess := session.New("path-usage", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
	for range engine.Run(t.Context(), sess, env, agent.RunRequest{Text: "read outside"}).Events() {
	}
	bucket := sess.TokenUsageSnapshot()[session.UsageKindGuardrail]
	if bucket.Total != usage || bucket.Models["mock/checker-model"] != usage {
		t.Fatalf("path-escape production usage = %#v, want mock/checker-model=%+v", bucket, usage)
	}
}

func TestContextualGuardrailUsage_Scenario2_PathEscapeUsage(t *testing.T) {
	safe := true
	route := &escapeGuardrailRoute{checker: pathUsageChecker{result: modelhook.CheckResult{
		Verdict: modelhook.Verdict{Safe: &safe}, Usage: pathCheckerUsage(3),
	}}}
	var reported session.AuxiliaryUsage
	ctx, deactivate := port.WithAuxiliaryUsageReporter(t.Context(), func(usage session.AuxiliaryUsage) {
		reported = reported.Merge(usage)
	})
	defer deactivate()
	verdict, err := route.review(ctx, session.NewToolCall("escape", "Read", []byte(`{"path":"../outside"}`)))
	if err != nil || verdict.Safe == nil || !*verdict.Safe {
		t.Fatalf("path escape verdict = %+v, err=%v", verdict, err)
	}
	if got := reported.Buckets[session.UsageKindGuardrail].Total.InputTokens; got != 3 {
		t.Fatalf("reported path usage = %d, want 3", got)
	}
}

func TestContextualGuardrailUsage_Scenario2_PathEscapeFailureAndChildIsolation(t *testing.T) {
	failure := errors.New("checker unavailable")
	route := &escapeGuardrailRoute{checker: pathUsageChecker{result: modelhook.CheckResult{Usage: pathCheckerUsage(5)}, err: failure}}
	var reported session.AuxiliaryUsage
	ctx, deactivate := port.WithAuxiliaryUsageReporter(t.Context(), func(usage session.AuxiliaryUsage) {
		reported = reported.Merge(usage)
	})
	_, err := route.review(ctx, session.NewToolCall("escape", "Write", []byte(`{"path":"../outside","content":"x"}`)))
	deactivate()
	if !errors.Is(err, failure) {
		t.Fatalf("path escape error = %v, want checker failure", err)
	}
	if got := reported.Buckets[session.UsageKindGuardrail].Total.InputTokens; got != 5 {
		t.Fatalf("partial path usage = %d, want 5", got)
	}
	// Child engines do not install the main request-scoped reporter. A checker call
	// on an unarmed child context therefore cannot mutate or replay parent usage.
	_, _ = route.review(context.Background(), session.NewToolCall("child", "Write", []byte(`{"path":"../outside","content":"x"}`)))
	if got := reported.Buckets[session.UsageKindGuardrail].Total.InputTokens; got != 5 {
		t.Fatalf("child context reported through parent route: %d", got)
	}
}
