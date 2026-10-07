package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

type countingParallelForker struct {
	mu    sync.Mutex
	forks int
}

func (f *countingParallelForker) Fork(_ context.Context, _ tool.Environment, label string) (tool.Environment, func() error, string, error) {
	f.mu.Lock()
	f.forks++
	f.mu.Unlock()
	return memEnv("/fork/" + label), func() error { return nil }, "", nil
}

func (f *countingParallelForker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forks
}

func TestADR_0369_Scenario4_ParallelSelector(t *testing.T) {
	t.Run("explicit selector drives branches while judge stays on parent", func(t *testing.T) {
		forker := &countingParallelForker{}
		var resolves, classifies int
		var judgeModels []string
		judgeProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			judgeModels = append(judgeModels, req.Model)
		})}, mockllm.TextTurn(`{"winner":1,"rationale":"first is sufficient"}`))
		judgeEngine := NewEngine(Deps{LLM: judgeProvider, Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "parent-model"})
		tl := NewParallelTool(markerEngine("PARENT"), forker,
			WithParallelProvider("parent"),
			WithParallelJudge(NewEngineJudge(judgeEngine)),
			WithParallelSelectorResolver(func(provider, model string) (ResolvedModelSelector, error) {
				resolves++
				if provider != "other" || model != "chosen" {
					t.Fatalf("resolver input = (%q, %q)", provider, model)
				}
				return ResolvedModelSelector{Target: ModelTarget{Provider: "other", Model: "chosen"}, ActualProvider: "other", ProviderBearing: true}, nil
			}),
			WithParallelEngineFactory(func(target ModelTarget) (*Engine, bool) {
				return markerEngine(target.Provider + "/" + target.Model), true
			}),
		).(*ParallelTool)
		caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
			classifies++
			return modelRoutingResult{model: "automatic", ok: true}
		}}
		args, _ := json.Marshal(parallelArgs{Tasks: []string{"a", "b"}, Join: "judge", Provider: "other", Model: "chosen"})
		var mu sync.Mutex
		selectedStarts := 0
		emit := func(ev session.Event) {
			if ev.Type == session.EvParallelBranch && ev.Parallel != nil && ev.Parallel.Kind == session.ParallelBranchStart {
				mu.Lock()
				if ev.Parallel.Model == "other/chosen" && ev.Parallel.Provider == "other" {
					selectedStarts++
				}
				mu.Unlock()
			}
		}
		_, err := tl.ExecuteWithParent(context.Background(), session.NewToolCall("p", "Parallel", args), memEnv("/ws"), emit, caps)
		if err != nil {
			t.Fatalf("ExecuteWithParent: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if resolves != 1 || classifies != 0 || selectedStarts != 2 {
			t.Fatalf("resolves=%d classifies=%d selected starts=%d, want 1, 0, 2", resolves, classifies, selectedStarts)
		}
		if len(judgeModels) != 1 || judgeModels[0] != "parent-model" {
			t.Fatalf("judge models = %v, want [parent-model]", judgeModels)
		}
	})

	t.Run("invalid selector starts no branch", func(t *testing.T) {
		forker := &countingParallelForker{}
		tl := NewParallelTool(markerEngine("PARENT"), forker,
			WithParallelSelectorResolver(func(string, string) (ResolvedModelSelector, error) {
				return ResolvedModelSelector{}, errors.New("unknown provider")
			}),
		).(*ParallelTool)
		args, _ := json.Marshal(parallelArgs{Tasks: []string{"a", "b"}, Provider: "missing", Model: "x"})
		res, err := tl.Execute(context.Background(), session.NewToolCall("p", "Parallel", args), memEnv("/ws"))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !res.IsError || forker.count() != 0 {
			t.Fatalf("result error=%v forks=%d, want true and 0", res.IsError, forker.count())
		}
	})

	t.Run("omission preserves per-branch routing", func(t *testing.T) {
		forker := &countingParallelForker{}
		var classMu sync.Mutex
		var classifies int
		tl := NewParallelTool(markerEngine("PARENT"), forker,
			WithParallelEngineFactory(func(target ModelTarget) (*Engine, bool) {
				return markerEngine("AUTO:" + target.Model), true
			}),
		).(*ParallelTool)
		caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
			classMu.Lock()
			classifies++
			classMu.Unlock()
			return modelRoutingResult{model: "routed", ok: true}
		}}
		res, err := tl.ExecuteWithParent(context.Background(), session.NewToolCall("p", "Parallel", parallelArgsJSON("a", "b")), memEnv("/ws"), nil, caps)
		if err != nil {
			t.Fatalf("ExecuteWithParent: %v", err)
		}
		classMu.Lock()
		defer classMu.Unlock()
		if classifies != 2 || strings.Count(res.Content, "AUTO:routed") != 2 {
			t.Fatalf("classifies=%d result=%q", classifies, res.Content)
		}
	})
}

func TestDelegatedSessionPersistenceUsesActualEngineIdentity(t *testing.T) {
	t.Run("Parallel branch falls back from declined routed target", func(t *testing.T) {
		store := memstore.New()
		parallel := NewParallelTool(markerEngine("fallback-model"), &countingParallelForker{},
			WithParallelProvider("fallback-provider"),
			WithParallelStore(store),
			WithParallelEngineFactory(func(ModelTarget) (*Engine, bool) { return nil, false }),
		).(*ParallelTool)
		var start *session.ParallelPayload
		result, err := parallel.ExecuteWithParent(t.Context(), session.NewToolCall("parallel", "Parallel", parallelArgsJSON("inspect")), memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvParallelBranch && ev.Parallel != nil && ev.Parallel.Kind == session.ParallelBranchStart {
				start = ev.Parallel
			}
		}, parentCaps{
			children: newChildRunRegistry(), parentSessionID: "parent", parentIncarnation: session.NewIncarnationID(),
			routeDecision: func(context.Context, string) modelRoutingResult {
				return modelRoutingResult{category: "large", provider: "routed-provider", model: "routed-model", ok: true}
			},
		})
		if err != nil || result.IsError || start == nil {
			t.Fatalf("ExecuteWithParent = (%+v, %v), start=%+v", result, err, start)
		}
		if start.Provider != "fallback-provider" || start.Model != "fallback-model" ||
			start.RoutedModel != "" || start.RoutingReason != session.RoutingReasonTargetUnavailable {
			t.Fatalf("branch event lost fallback evidence: %+v", start)
		}
		persisted, err := store.Load(t.Context(), "parallel-parent-parallel-0")
		if err != nil {
			t.Fatalf("load persisted branch: %v", err)
		}
		if persisted.ProviderID != "fallback-provider" || persisted.ModelID != "fallback-model" {
			t.Fatalf("persisted branch identity = %q/%q, want fallback-provider/fallback-model", persisted.ProviderID, persisted.ModelID)
		}
	})

	t.Run("Team member falls back from declined routed target", func(t *testing.T) {
		store := memstore.New()
		var factoryCalls int
		teamTool := NewTeamTool(func(_ *team.Team, spec MemberSpec, _ string) MemberBuild {
			factoryCalls++
			if spec.Selector != nil {
				return MemberBuild{}
			}
			return MemberBuild{Engine: NewEngine(Deps{
				LLM: mockllm.New(mockllm.TextTurn("work"), mockllm.TextTurn("synthesis")), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "fallback-model",
			}), Provider: "fallback-provider"}
		}, WithTeamToolStore(store)).(*TeamTool)
		var start *session.TeamPayload
		args, _ := json.Marshal(teamArgs{Goal: "goal", Members: []TeamMemberArg{{Name: "lead", Role: "inspect"}}})
		result, err := teamTool.ExecuteWithParent(t.Context(), session.NewToolCall("team", "Team", args), memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvTeamStart {
				start = ev.Team
			}
		}, parentCaps{
			children: newChildRunRegistry(), parentSessionID: "parent", parentIncarnation: session.NewIncarnationID(),
			routeDecision: func(context.Context, string) modelRoutingResult {
				return modelRoutingResult{category: "large", provider: "routed-provider", model: "routed-model", ok: true}
			},
		})
		if err != nil || result.IsError || start == nil || len(start.Roster) != 1 {
			t.Fatalf("ExecuteWithParent = (%+v, %v), start=%+v", result, err, start)
		}
		if factoryCalls != 2 || start.Roster[0].Provider != "fallback-provider" || start.Roster[0].Model != "fallback-model" ||
			start.Roster[0].RoutedModel != "" || start.Roster[0].RoutingReason != session.RoutingReasonTargetUnavailable {
			t.Fatalf("factory calls=%d team event lost fallback evidence: %+v", factoryCalls, start.Roster[0])
		}
		persisted, err := store.Load(t.Context(), "team-parent-team-lead")
		if err != nil {
			t.Fatalf("load persisted member: %v", err)
		}
		if persisted.ProviderID != "fallback-provider" || persisted.ModelID != "fallback-model" {
			t.Fatalf("persisted member identity = %q/%q, want fallback-provider/fallback-model", persisted.ProviderID, persisted.ModelID)
		}
	})
}

func TestTeamMemberSelectorValidationAndLifetime(t *testing.T) {
	t.Run("invalid roster is atomic", func(t *testing.T) {
		var builds int
		factory := func(_ *team.Team, _ MemberSpec, _ string) MemberBuild {
			builds++
			return MemberBuild{Engine: markerEngine("member")}
		}
		toolUnderTest := NewTeamTool(factory, WithTeamSelectorResolver(func(provider, model string) (ResolvedModelSelector, error) {
			if provider == "bad" {
				return ResolvedModelSelector{}, errors.New("unknown provider")
			}
			return ResolvedModelSelector{Target: ModelTarget{Model: model}, ActualProvider: "parent"}, nil
		}))
		args, _ := json.Marshal(teamArgs{Goal: "goal", Members: []TeamMemberArg{
			{Name: "lead", Role: "lead", Model: "ok"},
			{Name: "worker", Role: "work", Provider: "bad", Model: "x"},
		}})
		res, err := toolUnderTest.Execute(context.Background(), session.NewToolCall("t", "Team", args), memEnv("/ws"))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !res.IsError || builds != 0 {
			t.Fatalf("result error=%v builds=%d, want true and 0", res.IsError, builds)
		}
	})

	t.Run("selected member keeps one engine across rounds", func(t *testing.T) {
		tm := team.New("team")
		leadProvider := mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("l1", "AddTask", json.RawMessage(`{"description":"inspect"}`))),
			mockllm.TextTurn("delegated"),
			mockllm.TextTurn("synthesized"),
		)
		workerProvider := mockllm.New(
			mockllm.ToolCallTurn(
				session.NewToolCall("w1", "CompleteTask", json.RawMessage(`{"task_id":"task-1"}`)),
				session.NewToolCall("w2", "RecordFinding", json.RawMessage(`{"finding":"done"}`)),
			),
			mockllm.TextTurn("complete"),
		)
		memberEngine := func(spec MemberSpec, provider *mockllm.Provider, model string) *Engine {
			catalog := tool.NewCatalog()
			for _, memberTool := range MemberTools(tm, spec.Name, nil) {
				catalog.MustRegister(memberTool)
			}
			return NewEngine(Deps{LLM: provider, Catalog: catalog, Policy: allowAllInt(), Model: model})
		}
		builds := 0
		sup := NewSupervisor(tm, memEnv("/ws"), func(spec MemberSpec, _ string) MemberBuild {
			if spec.Selector != nil {
				builds++
				selected := *spec.Selector
				if selected.Target != (ModelTarget{Provider: "other", Model: "selected-model"}) {
					t.Fatalf("selected target = %+v", selected.Target)
				}
				return MemberBuild{Engine: memberEngine(spec, leadProvider, "selected-model"), Provider: "other"}
			}
			return MemberBuild{Engine: memberEngine(spec, workerProvider, "worker-model"), Provider: "parent"}
		}, WithMaxRounds(10))
		selected := ResolvedModelSelector{Target: ModelTarget{Provider: "other", Model: "selected-model"}, ActualProvider: "other", ProviderBearing: true}
		if err := sup.AddMember(t.Context(), MemberSpec{Name: "lead", Lead: true, InitialPrompt: "delegate", Selector: &selected}); err != nil {
			t.Fatalf("AddMember(lead): %v", err)
		}
		if err := sup.AddMember(t.Context(), MemberSpec{Name: "worker"}); err != nil {
			t.Fatalf("AddMember(worker): %v", err)
		}
		out := sup.Run(t.Context(), nil)
		if !out.Quiescent || out.Rounds < 2 {
			t.Fatalf("team outcome = %+v, want multiple quiescent rounds", out)
		}
		if builds != 1 || leadProvider.Calls() != 3 {
			t.Fatalf("selected engine builds=%d calls=%d, want one build retained for three calls", builds, leadProvider.Calls())
		}
	})

	t.Run("automatic target unavailable falls back with truthful evidence", func(t *testing.T) {
		tm := team.New("fallback")
		var attempts int
		sup := NewSupervisor(tm, memEnv("/ws"), func(spec MemberSpec, _ string) MemberBuild {
			attempts++
			if spec.Selector != nil {
				if spec.Selector.Target != (ModelTarget{Provider: "other", Model: "same-model"}) {
					t.Fatalf("routed target = %+v", spec.Selector.Target)
				}
				return MemberBuild{}
			}
			return MemberBuild{Engine: markerEngine("same-model"), Provider: "parent"}
		}, withParentCaps(parentCaps{routeDecision: func(context.Context, string) modelRoutingResult {
			return modelRoutingResult{category: "large", provider: "other", model: "same-model", ok: true}
		}}))
		if err := sup.AddMember(t.Context(), MemberSpec{Name: "lead", Lead: true}); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
		category, model, reason := sup.MemberRouting("lead")
		if attempts != 2 || category != "" || model != "" || reason != session.RoutingReasonTargetUnavailable || sup.members["lead"].provider != "parent" {
			t.Fatalf("attempts=%d route=%q/%q reason=%q provider=%q", attempts, category, model, reason, sup.members["lead"].provider)
		}
		sup.cleanupAll()
	})

	t.Run("selectors resolve once at add and named provider intent is rejected", func(t *testing.T) {
		var builds, routes, resolves int
		tm := team.New("team")
		preflight := &TeamTool{selectorResolver: func(provider, model string) (ResolvedModelSelector, error) {
			resolves++
			providerBearing := provider != "" || model == "pair"
			target := ModelTarget{Model: model}
			if model == "pair" {
				target = ModelTarget{Model: "paired"}
			}
			return ResolvedModelSelector{Target: target, ActualProvider: "parent", ProviderBearing: providerBearing}, nil
		}}
		specs, err := preflight.resolveMemberSelectors([]TeamMemberArg{{Name: "specialist", Role: "review", Agent: "reviewer", Model: "override"}})
		if err != nil {
			t.Fatalf("resolveMemberSelectors: %v", err)
		}
		factory := func(spec MemberSpec, _ string) MemberBuild {
			if spec.Selector != nil {
				builds++
				if spec.Selector.Target.Model != "override" {
					t.Fatalf("factory target = %#v", spec.Selector)
				}
				return MemberBuild{Engine: markerEngine("override"), Provider: "parent"}
			}
			return MemberBuild{Engine: markerEngine("default"), Provider: "parent"}
		}
		sup := NewSupervisor(tm, memEnv("/ws"), factory, withParentCaps(parentCaps{
			children: newChildRunRegistry(),
			routeDecision: func(context.Context, string) modelRoutingResult {
				routes++
				return modelRoutingResult{model: "automatic", ok: true}
			},
		}))
		if err := sup.AddMember(context.Background(), specs[0]); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
		if builds != 1 || resolves != 1 || routes != 0 || sup.MemberModel("specialist") != "override" || sup.members["specialist"].provider != "parent" || sup.members["specialist"].engine == nil {
			t.Fatalf("builds=%d resolves=%d routes=%d model=%q provider=%q", builds, resolves, routes, sup.MemberModel("specialist"), sup.members["specialist"].provider)
		}

		for _, badMember := range []TeamMemberArg{
			{Name: "pair", Role: "review", Agent: "reviewer", Model: "pair"},
			{Name: "router", Role: "review", Agent: "reviewer", Provider: "model-router", Model: "deep"},
		} {
			if _, err := preflight.resolveMemberSelectors([]TeamMemberArg{badMember}); err == nil {
				t.Fatalf("provider-bearing named specialist selector %#v unexpectedly accepted", badMember)
			}
		}
		if builds != 1 {
			t.Fatalf("invalid named selector reached factory; builds=%d", builds)
		}
	})
}

func TestADR_0369_Scenario4_DelegationEvidence(t *testing.T) {
	assertExplicit := func(t *testing.T, provider, model, category, routedCategory, routedModel, routingReason string, decision *session.RoutingDecision) {
		t.Helper()
		if provider != "other" || model != "selected-model" || category != "deep" {
			t.Fatalf("actual selection = %q/%q explicit %q", provider, model, category)
		}
		if routedCategory != "" || routedModel != "" || routingReason != "" || decision != nil {
			t.Fatalf("explicit selection carried classifier evidence: category=%q model=%q reason=%q decision=%+v", routedCategory, routedModel, routingReason, decision)
		}
	}
	resolver := func(provider, model string) (ResolvedModelSelector, error) {
		if provider != "model-router" || model != "deep" {
			return ResolvedModelSelector{}, errors.New("unexpected selector")
		}
		return ResolvedModelSelector{
			Target: ModelTarget{Provider: "other", Model: "selected-model"}, ActualProvider: "other",
			ProviderBearing: true, ExplicitRouterCategory: "deep",
		}, nil
	}

	t.Run("Subagent producer", func(t *testing.T) {
		var start *session.SubagentPayload
		toolUnderTest := NewSubagentTool(markerEngine("parent"), WithSubagentSelectorResolver(resolver),
			WithSubagentEngineFactory(func(target ModelTarget) (*Engine, bool) {
				return markerEngine(target.Model), true
			})).(*SubagentTool)
		res, err := toolUnderTest.ExecuteWithParent(t.Context(), session.NewToolCall("s", "Subagent", json.RawMessage(`{"prompt":"private task","provider":"model-router","model":"deep"}`)), memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvSubagentStart {
				start = ev.Subagent
			}
		}, parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
			t.Fatal("explicit selection invoked classifier")
			return modelRoutingResult{}
		}})
		if err != nil || res.IsError || start == nil {
			t.Fatalf("Subagent execution = %+v, err=%v, start=%+v", res, err, start)
		}
		assertExplicit(t, start.Provider, start.Model, start.ExplicitRouterCategory, start.RoutedCategory, start.RoutedModel, start.RoutingReason, start.RoutingDecision)
	})

	t.Run("Parallel producer", func(t *testing.T) {
		var mu sync.Mutex
		var starts []*session.ParallelPayload
		toolUnderTest := NewParallelTool(markerEngine("parent"), &countingParallelForker{},
			WithParallelSelectorResolver(resolver),
			WithParallelEngineFactory(func(target ModelTarget) (*Engine, bool) {
				return markerEngine(target.Model), true
			})).(*ParallelTool)
		args, _ := json.Marshal(parallelArgs{Tasks: []string{"one", "two"}, Provider: "model-router", Model: "deep"})
		res, err := toolUnderTest.ExecuteWithParent(t.Context(), session.NewToolCall("p", "Parallel", args), memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvParallelBranch && ev.Parallel.Kind == session.ParallelBranchStart {
				mu.Lock()
				starts = append(starts, ev.Parallel)
				mu.Unlock()
			}
		}, parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
			t.Fatal("explicit selection invoked classifier")
			return modelRoutingResult{}
		}})
		mu.Lock()
		defer mu.Unlock()
		if err != nil || res.IsError || len(starts) != 2 {
			t.Fatalf("Parallel execution = %+v, err=%v, starts=%d", res, err, len(starts))
		}
		for _, start := range starts {
			assertExplicit(t, start.Provider, start.Model, start.ExplicitRouterCategory, start.RoutedCategory, start.RoutedModel, start.RoutingReason, start.RoutingDecision)
		}
	})

	t.Run("Team producer", func(t *testing.T) {
		var start *session.TeamPayload
		toolUnderTest := NewTeamTool(func(_ *team.Team, spec MemberSpec, _ string) MemberBuild {
			if spec.Selector != nil {
				selected := *spec.Selector
				return MemberBuild{Engine: NewEngine(Deps{LLM: mockllm.New(mockllm.TextTurn("work"), mockllm.TextTurn("summary")), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: selected.Target.Model}), Provider: selected.ActualProvider}
			}
			return MemberBuild{Engine: markerEngine("parent"), Provider: "parent"}
		}, WithTeamSelectorResolver(resolver)).(*TeamTool)
		args, _ := json.Marshal(teamArgs{Goal: "private goal", Members: []TeamMemberArg{{Name: "lead", Role: "lead", Provider: "model-router", Model: "deep"}}})
		res, err := toolUnderTest.ExecuteWithParent(t.Context(), session.NewToolCall("t", "Team", args), memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvTeamStart {
				start = ev.Team
			}
		}, parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
			t.Fatal("explicit selection invoked classifier")
			return modelRoutingResult{}
		}})
		if err != nil || res.IsError || start == nil || len(start.Roster) != 1 {
			t.Fatalf("Team execution = %+v, err=%v, start=%+v", res, err, start)
		}
		member := start.Roster[0]
		assertExplicit(t, member.Provider, member.Model, member.ExplicitRouterCategory, member.RoutedCategory, member.RoutedModel, member.RoutingReason, member.RoutingDecision)
	})
}
