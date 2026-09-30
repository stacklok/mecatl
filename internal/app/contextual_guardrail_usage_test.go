package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
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

type allJobUsageReviewer struct{ agent.ToolReviewer }

func (allJobUsageReviewer) GuardrailReviewPolicy(string, agent.ReviewJob, bool) (bool, bool) {
	return true, true
}

func runContextualUsageParent(t *testing.T, reviewer agent.ToolReviewer, interactive bool) (*session.Session, *agent.Run) {
	t.Helper()
	catalog := tool.NewCatalog()
	catalog.MustRegister(productionPathUsageTool{})
	call := session.NewToolCall("read", "Read", []byte(`{"path":"a"}`))
	engine := agent.NewEngine(agent.Deps{
		LLM:          mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")),
		Catalog:      catalog,
		Policy:       permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
		ToolReviewer: reviewer,
		Interactive:  interactive,
	})
	env := reviewerEnvironment
	sess := session.New("contextual-dispatch-usage", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	return sess, engine.Run(t.Context(), sess, env, agent.RunRequest{Text: "inspect"})
}

func TestContextualGuardrailUsage_DispatcherRecordsRetriedAttemptsOnce(t *testing.T) {
	first := session.Usage{InputTokens: 2}
	second := session.Usage{InputTokens: 3}
	reviewer, _ := reviewerForTurns(t,
		mockllm.ErrorTurn(retryableReviewError("temporary"), mockllm.UsageChunk(first)),
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(session.NewToolCall("submit-retry", submitReviewAssessmentToolName, []byte(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))),
			mockllm.UsageChunk(second),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	sess, run := runContextualUsageParent(t, reviewer, false)
	for range run.Events() {
	}
	bucket := sess.TokenUsageSnapshot()[session.UsageKindGuardrail]
	want := first.Add(second)
	if bucket.Total != want || bucket.Models["mock/review-model"] != want || len(bucket.Models) != 1 {
		t.Fatalf("dispatcher retry usage = %#v, want both physical attempts exactly once", bucket)
	}
}

func TestContextualGuardrailUsage_ResultReleaseRecordsUsageOnce(t *testing.T) {
	actionUsage := session.Usage{InputTokens: 2}
	inboundUsage := session.Usage{InputTokens: 3}
	reviewer, _ := reviewerForTurns(t,
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(session.NewToolCall("submit-action", submitReviewAssessmentToolName, []byte(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))),
			mockllm.UsageChunk(actionUsage),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(session.NewToolCall("submit-inbound", submitReviewAssessmentToolName, []byte(`{"assessment":"prohibited","concerns":[{"ref":"c1","category":"redirect","rationale":"sensitive","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`))),
			mockllm.UsageChunk(inboundUsage),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	sess, run := runContextualUsageParent(t, allJobUsageReviewer{reviewer}, true)
	released := false
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil || ev.Ask.Guardrail == nil {
			continue
		}
		switch ev.Ask.Guardrail.Kind {
		case session.GuardrailApprovalAction:
			// The deliberately minimal test request may require an action override before
			// the independently reviewed result reaches its Release-once boundary.
		case session.GuardrailApprovalResultRelease:
			released = true
		default:
			t.Fatalf("unexpected guardrail ask kind %q", ev.Ask.Guardrail.Kind)
		}
		if err := run.ResolveApproval(agent.ApprovalResolution{
			AskID: ev.Ask.AskID, ReviewID: ev.Ask.Guardrail.ReviewID,
			Kind: ev.Ask.Guardrail.Kind, Verdict: session.VerdictAllowOnce,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !released {
		t.Fatal("inbound prohibited result did not require Release once")
	}
	bucket := sess.TokenUsageSnapshot()[session.UsageKindGuardrail]
	want := actionUsage.Add(inboundUsage)
	if bucket.Total != want || bucket.Models["mock/review-model"] != want || len(bucket.Models) != 1 {
		t.Fatalf("release-once usage = %#v, want action and inbound attempts exactly once", bucket)
	}
}

func TestContextualGuardrailUsage_Scenario1_ComposedWorkerRecordsOwnUsage(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "readme.txt"), []byte("reviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	usage := session.Usage{InputTokens: 4}
	delegate := session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"read readme.txt"}`))
	read := session.NewToolCall("child-read", "Read", []byte(`{"path":"readme.txt"}`))
	provider := mockllm.New(
		mockllm.ToolCallTurn(delegate),
		mockllm.ToolCallTurn(read),
		mockllm.ChunksTurn(
			mockllm.TextChunk(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`),
			mockllm.UsageChunk(usage), mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("child done"),
		mockllm.TextTurn("parent done"),
	)
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: workspace, NoSoul: true, UseMock: true, MockProvider: provider,
		Model: "main-model", GuardrailsModel: "guardrail-model", AllowAllTools: true,
		GuardrailsRules: []GuardrailRule{{Match: "Read", Phases: []string{"pre"}, Mode: "block"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	parent, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(t.Context(), parent.ID, "delegate")
	if err != nil {
		t.Fatal(err)
	}
	var childID session.SessionID
	var childReadOK, delegated bool
	for ev := range run.Events() {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			childID = session.SessionID(ev.Subagent.ChildID)
		}
		if ev.Type == session.EvSubagentTool && ev.Subagent != nil && ev.Subagent.ToolName == "Read" && ev.Subagent.InnerKind == session.EvToolResult && !ev.Subagent.IsError {
			childReadOK = true
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == delegate.ID && !ev.ToolResult.IsError {
			delegated = true
		}
	}
	built.Service.FinishRun(parent.ID, run)
	if childID == "" || !childReadOK || !delegated {
		t.Fatalf("worker not actually reviewed and executed: child=%q read=%v delegated=%v", childID, childReadOK, delegated)
	}
	child, err := built.Service.GetSession(t.Context(), childID)
	if err != nil {
		t.Fatal(err)
	}
	root, err := built.Service.GetSession(t.Context(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	bucket := child.TokenUsageSnapshot()[session.UsageKindGuardrail]
	if bucket.Total != usage || bucket.Models["mock/guardrail-model"] != usage || len(bucket.Models) != 1 {
		t.Fatalf("composed worker guardrail usage = %#v, want mock/guardrail-model=%+v", bucket, usage)
	}
	if got := root.UsageFor(session.UsageKindGuardrail); got != (session.Usage{}) {
		t.Fatalf("worker review charged delegation root: %+v", got)
	}
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

func TestContextualGuardrailUsage_Scenario2_PathEscapeRetryAndRepeatedChecksPersist(t *testing.T) {
	fixture := setupEscapeFS(t)
	args, err := json.Marshal(map[string]string{"path": fixture.target})
	if err != nil {
		t.Fatal(err)
	}
	first, second, repeated := session.Usage{InputTokens: 2}, session.Usage{InputTokens: 3}, session.Usage{InputTokens: 5}
	verdict := `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("first", "Read", args)),
		mockllm.ErrorTurn(retryableReviewError("temporary"), mockllm.UsageChunk(first)),
		mockllm.ChunksTurn(mockllm.TextChunk(verdict), mockllm.UsageChunk(second), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ToolCallTurn(session.NewToolCall("second", "Read", args)),
		mockllm.ChunksTurn(mockllm.TextChunk(verdict), mockllm.UsageChunk(repeated), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.TextTurn("done"),
	)
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: fixture.workspace, NoSoul: true, UseMock: true, MockProvider: provider,
		Posture: PostureAuto, PostureFlagSet: true, GuardrailsModel: "checker-model", GuardrailsEscape: true,
		GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(t.Context(), sess.ID, "read outside twice")
	if err != nil {
		t.Fatal(err)
	}
	var completed int
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && (ev.ToolResult.CallID == "first" || ev.ToolResult.CallID == "second") && !ev.ToolResult.IsError {
			completed++
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if completed != 2 {
		t.Fatalf("completed escape reads = %d, want both physical checks to permit one read each", completed)
	}
	loaded, err := built.Service.GetSession(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := first.Add(second).Add(repeated)
	bucket := loaded.TokenUsageSnapshot()[session.UsageKindGuardrail]
	if bucket.Total != want || bucket.Models["mock/checker-model"] != want || len(bucket.Models) != 1 {
		t.Fatalf("durable repeated path usage = %#v, want mock/checker-model=%+v", bucket, want)
	}
}

func TestContextualGuardrailUsage_Scenario2_PathEscapeTerminalFailurePersistsPartial(t *testing.T) {
	fixture := setupEscapeFS(t)
	args, err := json.Marshal(map[string]string{"path": fixture.target})
	if err != nil {
		t.Fatal(err)
	}
	usage := session.Usage{InputTokens: 7}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("failed", "Read", args)),
		mockllm.ErrorTurn(errors.New("checker unavailable"), mockllm.UsageChunk(usage)),
		mockllm.TextTurn("done"),
	)
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: fixture.workspace, NoSoul: true, UseMock: true, MockProvider: provider,
		Posture: PostureAuto, PostureFlagSet: true, GuardrailsModel: "checker-model", GuardrailsEscape: true,
		GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(t.Context(), sess.ID, "attempt read outside")
	if err != nil {
		t.Fatal(err)
	}
	var denied bool
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			if err := run.Approve(ev.Ask.AskID, session.VerdictDeny); err != nil {
				t.Fatal(err)
			}
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "failed" {
			denied = ev.ToolResult.IsError
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if !denied {
		t.Fatal("failed path-escape checker did not deny the Read")
	}
	loaded, err := built.Service.GetSession(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	bucket := loaded.TokenUsageSnapshot()[session.UsageKindGuardrail]
	if bucket.Total != usage || bucket.Models["mock/checker-model"] != usage || len(bucket.Models) != 1 {
		t.Fatalf("durable failed path usage = %#v, want mock/checker-model=%+v", bucket, usage)
	}
}

func TestContextualGuardrailUsage_Scenario2_PathEscapeReevaluationAfterAwaiting(t *testing.T) {
	fixture := setupEscapeFS(t)
	target := fixture.target + "-new"
	args, err := json.Marshal(map[string]string{"path": target, "content": "approved\n"})
	if err != nil {
		t.Fatal(err)
	}
	usageBefore, usageAfter := session.Usage{InputTokens: 2}, session.Usage{InputTokens: 3}
	verdict := `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`
	storeDir := t.TempDir()
	cfg := func(provider port.LLMProvider) Config {
		return Config{
			Workspace: fixture.workspace, StoreDir: storeDir, NoSoul: true, UseMock: true, MockProvider: provider,
			Posture: PostureAuto, PostureFlagSet: true, GuardrailsModel: "checker-model", GuardrailsEscape: true,
			GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}},
		}
	}
	runCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	firstProvider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("write", "Write", args)),
		mockllm.ChunksTurn(mockllm.TextChunk(verdict), mockllm.UsageChunk(usageBefore), mockllm.DoneChunk(session.StopEndTurn)),
	)
	built1, err := buildIsolated(t, t.Context(), cfg(firstProvider))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := built1.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		built1.Close()
		t.Fatal(err)
	}
	run1, err := built1.Service.StartRun(runCtx, sess.ID, "write outside")
	if err != nil {
		built1.Close()
		t.Fatal(err)
	}
	var askID string
	for ev := range run1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askID = ev.Ask.AskID
			built1.Service.Persist(t.Context(), sess.ID)
			break
		}
	}
	if askID == "" {
		built1.Close()
		t.Fatal("out-of-workspace Write did not park for approval")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		built1.Close()
		t.Fatalf("Write ran before approval: %v", err)
	}
	built1.Close()

	secondProvider := mockllm.New(
		mockllm.ChunksTurn(mockllm.TextChunk(verdict), mockllm.UsageChunk(usageAfter), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.TextTurn("done"),
	)
	built2, err := buildIsolated(t, t.Context(), cfg(secondProvider))
	if err != nil {
		t.Fatal(err)
	}
	defer built2.Close()
	run2, err := built2.Service.ApproveRun(runCtx, sess.ID, askID, session.VerdictAllowOnce, "")
	if err != nil || run2 == nil {
		t.Fatalf("resume awaiting approval: run=%v err=%v", run2, err)
	}
	var executed bool
	for ev := range run2.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "write" && !ev.ToolResult.IsError {
			executed = true
		}
	}
	built2.Service.FinishRun(sess.ID, run2)
	if !executed {
		t.Fatal("approved Write did not execute after resumed permission check")
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "approved\n" {
		t.Fatalf("approved Write content = %q err=%v", content, err)
	}
	loaded, err := built2.Service.GetSession(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := usageBefore.Add(usageAfter)
	bucket := loaded.TokenUsageSnapshot()[session.UsageKindGuardrail]
	if bucket.Total != want || bucket.Models["mock/checker-model"] != want || len(bucket.Models) != 1 {
		t.Fatalf("same-call pre/post-wait usage = %#v, want mock/checker-model=%+v", bucket, want)
	}
}

func TestContextualGuardrailUsage_Scenario2_ComposedChildNeverArmsPathEscape(t *testing.T) {
	fixture := setupEscapeFS(t)
	args, err := json.Marshal(map[string]string{"path": fixture.target})
	if err != nil {
		t.Fatal(err)
	}
	first, second := session.Usage{InputTokens: 2}, session.Usage{InputTokens: 3}
	verdict := `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("parent-first", "Read", args)),
		mockllm.ChunksTurn(mockllm.TextChunk(verdict), mockllm.UsageChunk(first), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"read outside"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("child-read", "Read", args)),
		mockllm.TextTurn("child done"),
		mockllm.ToolCallTurn(session.NewToolCall("parent-second", "Read", args)),
		mockllm.ChunksTurn(mockllm.TextChunk(verdict), mockllm.UsageChunk(second), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.TextTurn("parent done"),
	)
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: fixture.workspace, NoSoul: true, UseMock: true, MockProvider: provider,
		Posture: PostureAuto, PostureFlagSet: true, GuardrailsModel: "checker-model", GuardrailsEscape: true,
		GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(t.Context(), sess.ID, "read outside around delegation")
	if err != nil {
		t.Fatal(err)
	}
	var parentReads int
	var childReadDenied bool
	var childID session.SessionID
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && (ev.ToolResult.CallID == "parent-first" || ev.ToolResult.CallID == "parent-second") && !ev.ToolResult.IsError {
			parentReads++
		}
		if ev.Type == session.EvSubagentTool && ev.Subagent != nil && ev.Subagent.ToolName == "Read" && ev.Subagent.InnerKind == session.EvToolResult && ev.Subagent.IsError {
			childReadDenied = true
		}
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			childID = session.SessionID(ev.Subagent.ChildID)
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if parentReads != 2 || !childReadDenied || childID == "" {
		t.Fatalf("composed escape boundary: parent reads=%d child denied=%v child=%q", parentReads, childReadDenied, childID)
	}
	root, err := built.Service.GetSession(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := built.Service.GetSession(t.Context(), childID)
	if err != nil {
		t.Fatal(err)
	}
	want := first.Add(second)
	bucket := root.TokenUsageSnapshot()[session.UsageKindGuardrail]
	if bucket.Total != want || bucket.Models["mock/checker-model"] != want {
		t.Fatalf("main-only escape checker usage = %#v, want %+v", bucket, want)
	}
	if got := child.UsageFor(session.UsageKindGuardrail); got != (session.Usage{}) {
		t.Fatalf("child armed main escape checker: %+v", got)
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
