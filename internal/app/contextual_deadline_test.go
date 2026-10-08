package app

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
)

type firstTimeoutReviewPreparer struct {
	reviewer *guardrailActionReviewer
}

func (p firstTimeoutReviewPreparer) PrepareReviewEvidence(ctx context.Context, req agent.ReviewEvidencePreparation) (agent.PreparedReviewEvidence, error) {
	if req.Request.EffectiveCall.ID == "first" {
		<-ctx.Done()
		return agent.PreparedReviewEvidence{}, ctx.Err()
	}
	return p.reviewer.PrepareReviewEvidence(ctx, req)
}

type healthOrderReadTool struct {
	stubTool
	executed chan session.ToolCallID
}

func (t healthOrderReadTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	t.executed <- call.ID
	return t.stubTool.Execute(ctx, call, env)
}

func TestReadBatchPreparationFailureDoesNotOverwriteLaterRouteHealth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base, provider := reviewerForTurns(t, mockllm.ToolCallTurn(session.NewToolCall("submit", submitReviewAssessmentToolName, []byte(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))))
		rules, ok := compileGuardrailRules(Config{}, []modelhook.RuleSpec{{Match: "Inspect", Phases: []string{"pre"}, Mode: "block"}})
		if !ok {
			t.Fatal("rules")
		}
		health := &guardrailRouteHealth{}
		reviewer := &guardrailActionReviewer{base: base, rules: rules, health: health, failClosed: true}
		catalog := tool.NewCatalog()
		executed := make(chan session.ToolCallID, 2)
		catalog.MustRegister(healthOrderReadTool{stubTool: stubTool{name: "Inspect"}, executed: executed})
		calls := []session.ToolCall{session.NewToolCall("first", "Inspect", []byte(`{}`)), session.NewToolCall("second", "Inspect", []byte(`{}`))}
		engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: catalog, Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil), ToolReviewer: reviewer, ReviewEvidencePreparer: firstTimeoutReviewPreparer{reviewer: reviewer}})
		env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "health-order", Revision: "r1"}, memfs.NewWorkspace("/ws"), reviewerReadLedger{}, nil)
		sess := session.New("health-order", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
		timeouts, denied, succeeded := 0, 0, 0
		for ev := range engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "inspect"}).Events() {
			if ev.Type == session.EvPermissionAsk {
				t.Error("unexpected approval ask")
			}
			if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.ReasonCode == string(agent.ReviewFailureTimeout) {
				timeouts++
				if ev.Hook.CallID != "first" || ev.Hook.Guardrail.Disposition != "deny" {
					t.Errorf("timeout enforcement=%+v", ev.Hook)
				}
			}
			if ev.Type == session.EvToolResult && ev.ToolResult != nil {
				if ev.ToolResult.CallID == "first" && ev.ToolResult.IsError {
					denied++
				}
				if ev.ToolResult.CallID == "second" && !ev.ToolResult.IsError {
					succeeded++
				}
			}
		}
		if timeouts != 1 || denied != 1 || succeeded != 1 || provider.Calls() != 1 {
			t.Fatalf("timeouts=%d denied=%d succeeded=%d provider calls=%d", timeouts, denied, succeeded, provider.Calls())
		}
		if len(executed) != 1 {
			t.Fatalf("executed %d calls, want only second sibling", len(executed))
		}
		if got := <-executed; got != "second" {
			t.Fatalf("executed %s, want second sibling", got)
		}
		if seen, inspection, assessment, code := health.snapshot(); !seen || inspection != "complete" || assessment != "acceptable" || code != "" {
			t.Fatalf("latest route health=%t %s %s %s; want second sibling's healthy assessment", seen, inspection, assessment, code)
		}
	})
}

func TestReviewEvidenceBindingExpiresAtInheritedDeadline(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "review", Revision: "r1"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/review"), reviewerReadLedger{}, nil)
	req := agent.ToolReviewRequest{ReviewID: "deadline-binding", Job: agent.ReviewJobInbound, Event: sessionHookEvent("deadline-binding"), EffectiveCall: session.NewToolCall("call", "Act", []byte(`{}`)), Environment: ref, Capacity: agent.ReviewCapacity{MaxEvidenceHandles: maxReviewEvidenceHandles, MaxEvidenceBytes: maxReviewEvidenceBytes}}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(20*time.Second))
	defer cancel()
	deadline, _ := ctx.Deadline()
	prepared, err := (&guardrailActionReviewer{providerID: "provider", modelID: "model"}).PrepareReviewEvidence(ctx, agent.ReviewEvidencePreparation{Request: req, Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	source, ok := prepared.Source.(*finiteReviewEvidenceSource)
	if !ok || !source.access.ExpiresAt.Equal(deadline) {
		t.Fatalf("expiry=%v, deadline=%v", source, deadline)
	}
}

func TestContextualEvidencePreparationAndCheckerShareBudget(t *testing.T) {
	for _, mode := range []string{"retries", "preparation_timeout", "evidence_read_timeout", "stale_evidence", "terminal_after_deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				files := memfs.NewWorkspace("/review")
				ws := &countingRangeEvidenceWorkspace{Workspace: files, delays: []time.Duration{50 * time.Second, 0, 41 * time.Second}}
				if err := files.Write(context.Background(), "review-script.sh", []byte("SCRIPT_EVIDENCE_MARKER")); err != nil {
					t.Fatal(err)
				}
				if mode == "preparation_timeout" {
					ws.delays[0] = 91 * time.Second
				}
				if mode == "stale_evidence" {
					ws.delays[2] = 0
					ws.beforeRead = func(ctx context.Context, read int) error {
						if read == 3 {
							return files.Write(ctx, "review-script.sh", []byte("changed after inventory"))
						}
						return nil
					}
				}
				deadlineObserved := false
				if mode == "terminal_after_deadline" {
					ws.delays[2] = 0
					ws.completeAfterDeadline = true
					ws.beforeRead = func(ctx context.Context, read int) error {
						if read == 3 {
							<-ctx.Done()
							deadlineObserved = errors.Is(ctx.Err(), context.DeadlineExceeded)
							return files.Write(context.Background(), "review-script.sh", []byte("changed after inventory"))
						}
						return nil
					}
				}
				evidenceProvider := &evidenceBuildProvider{call: 1} // start at its contextual evidence-read turn
				retryProvider := &invalidThenBlockingReviewProvider{firstDelay: 15 * time.Second}
				var provider port.LLMProvider = evidenceProvider
				if mode == "retries" {
					provider = retryProvider
				}
				pc := promptConfig(Config{Model: "review-model"}, "")
				pc.Role = contextualReviewerSystemPrompt
				deps := childEngineDepsForProvider(Config{UseMock: true}, "guardrail-reviewer", provider, session.ProviderModelID{ProviderID: "mock", ModelID: "review-model"}, func() int { return 128000 }, tool.NewCatalog(), pc, nil)
				deps.MaxNoProgressNudges = -1
				rules, ok := compileGuardrailRules(Config{}, []modelhook.RuleSpec{{Match: "Write", Phases: []string{"pre"}, Mode: "block"}})
				if !ok {
					t.Fatal("rules")
				}
				health := &guardrailRouteHealth{}
				reviewer := &guardrailActionReviewer{base: newContextualToolReviewer(agent.NewEngine(deps), "mock", "review-model"), rules: rules, health: health, providerID: "mock", modelID: "review-model"}
				catalog := tool.NewCatalog()
				catalog.MustRegister(rootAuthorityTestTool{name: "Write"})
				catalog.MustRegister(stubTool{name: "Read"})
				call := session.NewToolCall("call", "Write", []byte(`{"path":"review-script.sh","content":"x"}`))
				engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: catalog, Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil), ToolReviewer: reviewer, ReviewEvidencePreparer: reviewer})
				env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "review", Revision: "r1"}, ws, reviewerReadLedger{}, nil)
				sess := session.New("real-evidence-budget", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
				wantCode, wantDisposition := agent.ReviewFailureTimeout, "pass_advisory"
				if mode == "stale_evidence" || mode == "terminal_after_deadline" {
					wantCode, wantDisposition = agent.ReviewFailureEvidenceFailure, "deny"
				}
				found, denied := false, false
				findings := 0
				for ev := range engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "write"}).Events() {
					if ev.Hook != nil && ev.Hook.Guardrail != nil {
						found = true
						findings++
						if ev.Hook.Guardrail.ReasonCode != string(wantCode) || ev.Hook.Guardrail.Disposition != wantDisposition {
							t.Errorf("review=%+v", ev.Hook.Guardrail)
						}
					}
					if ev.ToolResult != nil && ev.ToolResult.IsError {
						denied = true
					}
				}
				seen, inspection, _, code := health.snapshot()
				if !found || !seen || inspection != "operational_failure" || code != wantCode || denied != (mode == "stale_evidence" || mode == "terminal_after_deadline") {
					t.Fatalf("found=%t health=%t %s %s denied=%t", found, seen, inspection, code, denied)
				}
				if mode == "terminal_after_deadline" && (!deadlineObserved || findings != 1 || evidenceProvider.call != 2) {
					t.Fatalf("deadline observed=%t findings=%d provider turns=%d; terminal failure must not retry", deadlineObserved, findings, evidenceProvider.call)
				}
				switch mode {
				case "retries":
					if ws.reads != 2 || retryProvider.calls != 2 || len(retryProvider.remaining) != 2 || retryProvider.remaining[0] != 40*time.Second || retryProvider.remaining[1] != 25*time.Second {
						t.Fatalf("reads=%d calls=%d remaining=%v", ws.reads, retryProvider.calls, retryProvider.remaining)
					}
				case "preparation_timeout":
					if ws.reads != 1 || len(evidenceProvider.remaining) != 0 {
						t.Fatalf("reads=%d remaining=%v", ws.reads, evidenceProvider.remaining)
					}
				default:
					if ws.reads != 3 || len(evidenceProvider.remaining) != 1 || evidenceProvider.remaining[0] != 40*time.Second {
						t.Fatalf("reads=%d remaining=%v", ws.reads, evidenceProvider.remaining)
					}
				}
			})
		})
	}
}

func TestEngineReviewBudgetAndCallerCancellationHaveDistinctRouteHealth(t *testing.T) {
	for _, job := range []agent.ReviewJob{agent.ReviewJobAction, agent.ReviewJobInbound} {
		for _, stop := range []string{"cancel", "parent_deadline", "review_budget"} {
			t.Run(string(job)+"/"+stop, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					provider := &blockingReviewProvider{}
					pc := promptConfig(Config{Model: "review-model"}, "")
					pc.Role = contextualReviewerSystemPrompt
					deps := childEngineDepsForProvider(Config{UseMock: true}, "guardrail-reviewer", provider, session.ProviderModelID{ProviderID: "mock", ModelID: "review-model"}, func() int { return 128000 }, tool.NewCatalog(), pc, nil)
					base := newContextualToolReviewer(agent.NewEngine(deps), "mock", "review-model")
					health := &guardrailRouteHealth{}
					health.record(agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, nil)
					phase := "pre"
					if job == agent.ReviewJobInbound {
						phase = "post"
					}
					rules, ok := compileGuardrailRules(Config{}, []modelhook.RuleSpec{{Match: "Act", Phases: []string{phase}, Mode: "block"}})
					if !ok {
						t.Fatal("rules")
					}
					reviewer := &guardrailActionReviewer{base: base, rules: rules, health: health, failClosed: true}
					catalog := tool.NewCatalog()
					catalog.MustRegister(rootAuthorityTestTool{name: "Act"})
					engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")), Catalog: catalog, Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil), ToolReviewer: reviewer})
					env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "deadline", Revision: "r1"}, memfs.NewWorkspace("/ws"), reviewerReadLedger{}, nil)
					sess := session.New("route-health", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
					ctx, cancel := context.WithCancel(context.Background())
					if stop == "parent_deadline" {
						cancel()
						ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
					}
					if stop == "cancel" {
						timer := time.AfterFunc(15*time.Second, cancel)
						defer timer.Stop()
					}
					defer cancel()
					timeouts := 0
					for ev := range engine.Run(ctx, sess, env, agent.RunRequest{Text: "act"}).Events() {
						if ev.Type == session.EvPermissionAsk {
							t.Error("unexpected approval ask")
						}
						if ev.Hook != nil && ev.Hook.Guardrail != nil {
							if ev.Hook.Guardrail.ReasonCode == string(agent.ReviewFailureTimeout) {
								timeouts++
							}
							if stop != "review_budget" {
								t.Error("cancelled review hook published")
							}
						}
					}
					seen, inspection, assessment, code := health.snapshot()
					if stop == "review_budget" {
						if !seen || inspection != "operational_failure" || code != agent.ReviewFailureTimeout || timeouts != 1 {
							t.Fatalf("budget health=%t %s %s %s timeouts=%d", seen, inspection, assessment, code, timeouts)
						}
					} else if !seen || inspection != "complete" || assessment != "acceptable" || code != "" || timeouts != 0 {
						t.Fatalf("caller changed health=%t %s %s %s timeouts=%d", seen, inspection, assessment, code, timeouts)
					}
					if provider.calls != 1 {
						t.Fatalf("provider calls=%d", provider.calls)
					}
				})
			})
		}
	}
}

func TestContextualReviewerDoesNotEnterProviderWithExpiredParent(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			reviewer, provider := reviewerForTurns(t)
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			}
			cancel()
			result, _, err := reviewer.Review(ctx, reviewRequestWithoutEvidence(), nil)
			if result.Assessment != agent.ReviewUnresolved || !errors.Is(err, want) || reviewFailureCodeForTest(err) != "" || provider.Calls() != 0 {
				t.Fatalf("result=%+v error=%v calls=%d", result, err, provider.Calls())
			}
		})
	}
}
