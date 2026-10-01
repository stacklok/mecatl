package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

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
	t.Run("explicit selector resolves once and applies to every branch", func(t *testing.T) {
		forker := &countingParallelForker{}
		var resolves, classifies int
		tl := NewParallelTool(markerEngine("PARENT"), forker,
			WithParallelProvider("parent"),
			WithParallelSelectorResolver(func(provider, model string) (ResolvedModelSelector, error) {
				resolves++
				if provider != "other" || model != "chosen" {
					t.Fatalf("resolver input = (%q, %q)", provider, model)
				}
				return ResolvedModelSelector{Target: ModelTarget{Provider: "other", Model: "chosen"}, ActualProvider: "other", ProviderBearing: true}, nil
			}),
			WithParallelTargetEngineFactory(func(target ModelTarget) (*Engine, bool) {
				return markerEngine(target.Provider + "/" + target.Model), true
			}),
		).(*ParallelTool)
		caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
			classifies++
			return modelRoutingResult{model: "automatic", ok: true}
		}}
		args, _ := json.Marshal(parallelArgs{Tasks: []string{"a", "b"}, Provider: "other", Model: "chosen"})
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
			WithParallelEngineFactory(func(model string) (*Engine, bool) {
				return markerEngine("AUTO:" + model), true
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

func TestADR_0369_Scenario4_TeamMemberSelector(t *testing.T) {
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

	t.Run("selectors resolve once at add and named provider intent is rejected", func(t *testing.T) {
		var builds, routes, resolves int
		tm := team.New("team")
		preflight := &TeamTool{selectorResolver: func(provider, model string) (ResolvedModelSelector, error) {
			resolves++
			providerBearing := provider != "" || model == "pair"
			target := ModelTarget{Model: model}
			if model == "pair" {
				target = ModelTarget{Provider: "other", Model: "paired"}
			}
			return ResolvedModelSelector{Target: target, ActualProvider: "parent", ProviderBearing: providerBearing}, nil
		}}
		specs, err := preflight.resolveMemberSelectors([]TeamMemberArg{{Name: "specialist", Role: "review", Agent: "reviewer", Model: "override"}})
		if err != nil {
			t.Fatalf("resolveMemberSelectors: %v", err)
		}
		factory := func(MemberSpec, string) MemberBuild {
			return MemberBuild{Engine: markerEngine("default"), Provider: "parent"}
		}
		selectorFactory := func(spec MemberSpec, selected ResolvedModelSelector) MemberBuild {
			builds++
			if selected.Target.Model != "override" {
				t.Fatalf("factory target = %#v", selected)
			}
			return MemberBuild{Engine: markerEngine("override"), Provider: "parent"}
		}
		sup := NewSupervisor(tm, memEnv("/ws"), factory, WithTeamMemberSelectorFactory(selectorFactory), withParentCaps(parentCaps{
			children: newChildRunRegistry(),
			routeDecision: func(context.Context, string) modelRoutingResult {
				routes++
				return modelRoutingResult{model: "automatic", ok: true}
			},
		}))
		if err := sup.AddMember(context.Background(), specs[0]); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
		if builds != 1 || resolves != 1 || routes != 0 || sup.MemberModel("specialist") != "override" || sup.MemberProvider("specialist") != "parent" || sup.members["specialist"].engine == nil {
			t.Fatalf("builds=%d resolves=%d routes=%d model=%q provider=%q", builds, resolves, routes, sup.MemberModel("specialist"), sup.MemberProvider("specialist"))
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
