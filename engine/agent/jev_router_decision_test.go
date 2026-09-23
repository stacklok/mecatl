package agent

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestADR_0352_Scenario5_DecisionEvidence(t *testing.T) {
	confidence := 0.7
	minimum := 0.8
	router := &SubagentModelRouter{
		Backend:           "jev",
		ClassifierModel:   "jev-1.13.0",
		MinimumConfidence: &minimum,
		Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{
				Category: "medium", Model: "coder", Usage: session.Usage{InputTokens: 3},
				Reason: RouterMissLowConfidence, Confidence: &confidence,
			}
		},
	}
	breaker := &modelRouterBreaker{max: 3}
	got := routeTaskBody(t.Context(), "review", router, breaker, nil, port.NopDiagnostics{}, func(session.Usage) {})
	if got.ok || got.category != "" || got.model != "" || got.reason != RouterMissLowConfidence {
		t.Fatalf("low-confidence route = %+v", got)
	}
	want := session.RoutingDecision{
		Backend: "jev", ClassifierModel: "jev-1.13.0", CandidateCategory: "medium",
		CandidateModel: "coder", Confidence: &confidence, MinimumConfidence: &minimum,
		Outcome: "fallback", ConsecutiveMisses: 1, MissLimit: 3,
	}
	assertRoutingDecision(t, got.decision, want)

	// A local mapping miss retains its validated category, omits the unresolved model,
	// and counts exactly once before a classifier failure opens the breaker.
	calls := 1
	router.Route = func(context.Context, string) ModelRouteResult {
		calls++
		return ModelRouteResult{Category: "unmapped", Reason: "category-target-unresolvable"}
	}
	mapping := routeTaskBody(t.Context(), "mapping miss", router, breaker, nil, port.NopDiagnostics{}, func(session.Usage) {})
	if mapping.decision == nil || mapping.decision.CandidateCategory != "unmapped" || mapping.decision.CandidateModel != "" || mapping.decision.ConsecutiveMisses != 2 {
		t.Fatalf("mapping-miss evidence = %+v", mapping.decision)
	}
	router.Route = func(context.Context, string) ModelRouteResult {
		calls++
		return ModelRouteResult{Reason: RouterMissClassifierError}
	}
	opened := routeTaskBody(t.Context(), "classifier failure", router, breaker, nil, port.NopDiagnostics{}, func(session.Usage) {})
	skipped := routeTaskBody(t.Context(), "skip", router, breaker, nil, port.NopDiagnostics{}, func(session.Usage) {})
	if calls != 3 || opened.decision == nil || !opened.decision.BreakerOpen || skipped.decision == nil ||
		skipped.decision.Outcome != "skipped" || skipped.decision.ConsecutiveMisses != 3 || !skipped.decision.BreakerOpen {
		t.Fatalf("breaker evidence calls=%d opened=%+v skipped=%+v", calls, opened.decision, skipped.decision)
	}

	// A hit resets the post-decision snapshot; factory rejection changes only the final outcome.
	hitBreaker := &modelRouterBreaker{max: 3, consecutiveMiss: 2}
	router.Route = func(context.Context, string) ModelRouteResult {
		return ModelRouteResult{Category: "deep", Model: "capable", OK: true}
	}
	hit := routeTaskBody(t.Context(), "deep work", router, hitBreaker, nil, port.NopDiagnostics{}, func(session.Usage) {})
	if !hit.ok || hit.decision == nil || hit.decision.Outcome != "routed" || hit.decision.ConsecutiveMisses != 0 {
		t.Fatalf("hit evidence = %+v", hit)
	}
	_, _, finalReason, rejected := reconcileRoutedModel(hit.category, hit.model, hit.reason, false, hit.decision)
	if finalReason != session.RoutingReasonTargetUnavailable || rejected == nil || rejected.Outcome != "fallback" ||
		rejected.CandidateCategory != "deep" || rejected.CandidateModel != "capable" || rejected.ConsecutiveMisses != 0 {
		t.Fatalf("target rejection evidence = reason=%q decision=%+v", finalReason, rejected)
	}

	// A real delegation whose routed target factory rejects the candidate must
	// advertise the inherited model as actual and keep the capable candidate only
	// in decision evidence.
	rejectingTool := NewSubagentTool(markerEngine("inherited-model"), WithSubagentEngineFactory(func(string) (*Engine, bool) {
		return nil, false
	})).(*SubagentTool)
	var rejectedStart *session.SubagentPayload
	_, err := rejectingTool.ExecuteWithParent(t.Context(), session.NewToolCall("factory-reject", "Subagent", json.RawMessage(`{"prompt":"deep work"}`)), memEnv("/ws"), func(ev session.Event) {
		if ev.Type == session.EvSubagentStart {
			rejectedStart = ev.Subagent
		}
	}, parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		return modelRoutingResult{category: hit.category, model: hit.model, ok: true, decision: cloneRoutingDecision(hit.decision)}
	}})
	if err != nil || rejectedStart == nil || rejectedStart.Model != "inherited-model" || rejectedStart.RoutedModel != "" ||
		rejectedStart.RoutingReason != session.RoutingReasonTargetUnavailable || rejectedStart.RoutingDecision == nil ||
		rejectedStart.RoutingDecision.CandidateModel != "capable" || rejectedStart.RoutingDecision.Outcome != "fallback" {
		t.Fatalf("factory-rejection projection = %+v, err=%v", rejectedStart, err)
	}

	// A configured router with a nil Route skips safely and does not increment the breaker.
	nilRouter := &SubagentModelRouter{Backend: "llm", ClassifierModel: "classifier"}
	nilBreaker := &modelRouterBreaker{max: 3, consecutiveMiss: 1}
	nilResult := routeTaskBody(t.Context(), "task", nilRouter, nilBreaker, nil, port.NopDiagnostics{}, func(session.Usage) {})
	if nilResult.decision == nil || nilResult.decision.Outcome != "skipped" || nilResult.decision.ConsecutiveMisses != 1 {
		t.Fatalf("nil Route evidence = %+v", nilResult)
	}

	// Pin/fork/resume and unavailable-family gates snapshot configured metadata without calls.
	gateCaps := parentCaps{skipRoute: func(string) *session.RoutingDecision {
		return routerDecision(router, ModelRouteResult{}, "skipped", hitBreaker)
	}}
	for _, tc := range []struct {
		name     string
		args     subagentArgs
		resuming bool
		want     string
	}{
		{name: "pin", args: subagentArgs{Model: "inherit"}, want: session.RoutingReasonPinnedModel},
		{name: "fork", args: subagentArgs{Fork: true}, want: session.RoutingReasonFork},
		{name: "resume", resuming: true, want: session.RoutingReasonResume},
		{name: "unavailable", args: subagentArgs{}, want: session.RoutingReasonRouterDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, reason, decision := (&SubagentTool{}).maybeRouteModel(t.Context(), tc.args, tc.resuming, false, gateCaps)
			if reason != tc.want || decision == nil || decision.Outcome != "skipped" || calls != 3 {
				t.Fatalf("gate reason=%q decision=%+v calls=%d", reason, decision, calls)
			}
		})
	}

	// Every family receives its own snapshot; mutating one must not change another.
	sub := cloneRoutingDecision(got.decision)
	parallel := cloneRoutingDecision(got.decision)
	teamDecision := cloneRoutingDecision(got.decision)
	sub.CandidateCategory = "changed"
	if parallel.CandidateCategory != "medium" || teamDecision.CandidateCategory != "medium" {
		t.Fatalf("routing decision snapshots alias: parallel=%+v team=%+v", parallel, teamDecision)
	}

	caps := parentCaps{
		children: newChildRunRegistry(),
		routeDecision: func(context.Context, string) modelRoutingResult {
			return modelRoutingResult{reason: RouterMissLowConfidence, decision: cloneRoutingDecision(got.decision)}
		},
	}
	var subStart *session.SubagentPayload
	_, err = routerTool().ExecuteWithParent(t.Context(),
		session.NewToolCall("decision-sub", "Subagent", json.RawMessage(`{"prompt":"review"}`)),
		memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvSubagentStart {
				subStart = ev.Subagent
			}
		}, caps)
	if err != nil || subStart == nil || subStart.RoutingDecision == nil {
		t.Fatalf("Subagent decision projection = %+v, err=%v", subStart, err)
	}

	var parallelStart *session.ParallelPayload
	_, err = routerParallelTool(true).ExecuteWithParent(t.Context(),
		session.NewToolCall("decision-par", "Parallel", parallelArgsJSON("review")), memEnv("/ws"),
		func(ev session.Event) {
			if ev.Type == session.EvParallelBranch && ev.Parallel != nil && ev.Parallel.Kind == session.ParallelBranchStart {
				parallelStart = ev.Parallel
			}
		}, caps)
	if err != nil || parallelStart == nil || parallelStart.RoutingDecision == nil {
		t.Fatalf("Parallel decision projection = %+v, err=%v", parallelStart, err)
	}

	factory := func(tm *team.Team, spec MemberSpec, _ string) MemberBuild {
		catalog := tool.NewCatalog()
		for _, memberTool := range MemberTools(tm, spec.Name, nil) {
			catalog.MustRegister(memberTool)
		}
		return MemberBuild{Engine: NewEngine(Deps{
			LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalog, Policy: allowAllInt(), Model: "member",
		})}
	}
	var teamStart *session.TeamPayload
	teamTool := NewTeamTool(TeamMemberEngineFactory(factory))
	_, err = teamTool.(childCapableTool).ExecuteWithParent(t.Context(),
		session.NewToolCall("decision-team", "Team", json.RawMessage(`{"goal":"work","members":[{"name":"lead","role":"coordinate"}]}`)),
		memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvTeamStart {
				teamStart = ev.Team
			}
		}, caps)
	if err != nil || teamStart == nil || len(teamStart.Roster) != 1 || teamStart.Roster[0].RoutingDecision == nil {
		t.Fatalf("Team decision projection = %+v, err=%v", teamStart, err)
	}
	if subStart.RoutingDecision == parallelStart.RoutingDecision || subStart.RoutingDecision == teamStart.Roster[0].RoutingDecision || parallelStart.RoutingDecision == teamStart.Roster[0].RoutingDecision {
		t.Fatal("delegation families shared a mutable RoutingDecision pointer")
	}

	bad := math.NaN()
	hostile := sanitizedRoutingDecision(&session.RoutingDecision{
		Backend: "unknown\x00backend", ClassifierModel: "model\x00name",
		Confidence: &bad, MinimumConfidence: &bad, Outcome: "invented",
	})
	if hostile.Backend != "" || hostile.Outcome != "" || hostile.Confidence != nil || hostile.MinimumConfidence != nil {
		t.Fatalf("hostile routing metadata was not omitted: %+v", hostile)
	}
}

func assertRoutingDecision(t *testing.T, got *session.RoutingDecision, want session.RoutingDecision) {
	t.Helper()
	if got == nil {
		t.Fatal("routing decision is nil")
	}
	if got.Backend != want.Backend || got.ClassifierModel != want.ClassifierModel ||
		got.CandidateCategory != want.CandidateCategory || got.CandidateModel != want.CandidateModel ||
		got.Outcome != want.Outcome || got.ConsecutiveMisses != want.ConsecutiveMisses ||
		got.MissLimit != want.MissLimit || got.BreakerOpen != want.BreakerOpen {
		t.Fatalf("routing decision = %+v, want %+v", got, want)
	}
	if got.Confidence == nil || want.Confidence == nil || *got.Confidence != *want.Confidence ||
		got.MinimumConfidence == nil || want.MinimumConfidence == nil || *got.MinimumConfidence != *want.MinimumConfidence {
		t.Fatalf("routing confidence = (%v,%v), want (%v,%v)", got.Confidence, got.MinimumConfidence, want.Confidence, want.MinimumConfidence)
	}
}
