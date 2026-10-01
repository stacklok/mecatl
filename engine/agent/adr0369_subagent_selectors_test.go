package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0369_Scenario3_SubagentDirectSelectors(t *testing.T) {
	var built []ModelTarget
	resolver := func(provider, model string) (ResolvedModelSelector, error) {
		switch model {
		case "pair-alias":
			return ResolvedModelSelector{Target: ModelTarget{Provider: "second", Model: "pair-model"}, ProviderBearing: true}, nil
		default:
			return ResolvedModelSelector{Target: ModelTarget{Provider: provider, Model: model}, ProviderBearing: provider != ""}, nil
		}
	}
	factory := func(target ModelTarget) (*Engine, bool) {
		built = append(built, target)
		return markerEngine(target.Provider + "/" + target.Model), true
	}
	tool := NewSubagentTool(markerEngine("DEFAULT"),
		WithSubagentProvider("parent"),
		WithSubagentSelectorResolver(resolver),
		WithSubagentTargetEngineFactory(factory)).(*SubagentTool)

	for _, tc := range []struct {
		name string
		args string
		want ModelTarget
	}{
		{name: "literal inherits parent provider", args: `{"prompt":"x","model":"literal"}`, want: ModelTarget{Model: "literal"}},
		{name: "pair alias carries provider", args: `{"prompt":"x","model":"pair-alias"}`, want: ModelTarget{Provider: "second", Model: "pair-model"}},
		{name: "concrete pair", args: `{"prompt":"x","provider":"second","model":"literal"}`, want: ModelTarget{Provider: "second", Model: "literal"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(built)
			res, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall(session.ToolCallID(tc.name), "Subagent", json.RawMessage(tc.args)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()})
			if err != nil || res.IsError {
				t.Fatalf("selector execution = %+v, err=%v", res, err)
			}
			if len(built) != before+1 || built[before] != tc.want {
				t.Fatalf("factory target = %+v, want %+v", built[before:], tc.want)
			}
		})
	}

	classifierCalls := 0
	startProvider := ""
	res, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("omitted", "Subagent", json.RawMessage(`{"prompt":"route me"}`)), memEnv("/ws"), func(ev session.Event) {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			startProvider = ev.Subagent.Provider
		}
	}, parentCaps{
		children: newChildRunRegistry(),
		routeDecision: func(context.Context, string) modelRoutingResult {
			classifierCalls++
			return modelRoutingResult{category: "large", model: "routed", ok: true}
		},
	})
	if err != nil || res.IsError || classifierCalls != 1 || built[len(built)-1] != (ModelTarget{Model: "routed"}) || startProvider != "parent" {
		t.Fatalf("omitted selector did not preserve automatic routing: result=%+v err=%v calls=%d provider=%q built=%+v", res, err, classifierCalls, startProvider, built)
	}
}

func TestADR_0369_Scenario3_SubagentRouterSelector(t *testing.T) {
	for _, target := range []ModelTarget{{Model: "parent-model"}, {Provider: "second", Model: "pair-model"}} {
		t.Run(target.Provider, func(t *testing.T) {
			classifierCalls := 0
			var start *session.SubagentPayload
			tool := NewSubagentTool(markerEngine("DEFAULT"),
				WithSubagentSelectorResolver(func(provider, model string) (ResolvedModelSelector, error) {
					if provider != "model-router" || model != "deep" {
						return ResolvedModelSelector{}, errors.New("unexpected selector")
					}
					return ResolvedModelSelector{Target: target, ProviderBearing: true, ExplicitRouterCategory: "deep", ActualProvider: map[bool]string{true: target.Provider, false: "parent"}[target.Provider != ""]}, nil
				}),
				WithSubagentTargetEngineFactory(func(got ModelTarget) (*Engine, bool) {
					if got != target {
						t.Fatalf("factory target = %+v, want %+v", got, target)
					}
					return markerEngine("ROUTER"), true
				})).(*SubagentTool)
			res, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("router", "Subagent", json.RawMessage(`{"prompt":"x","provider":"model-router","model":"deep"}`)), memEnv("/ws"), func(ev session.Event) {
				if ev.Type == session.EvSubagentStart {
					start = ev.Subagent
				}
			}, parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
				classifierCalls++
				return modelRoutingResult{}
			}})
			if err != nil || res.IsError || classifierCalls != 0 {
				t.Fatalf("router selector result=%+v err=%v classifier calls=%d", res, err, classifierCalls)
			}
			if start == nil || start.ExplicitRouterCategory != "deep" || start.Provider == "" || start.RoutedCategory != "" || start.RoutedModel != "" || start.RoutingReason != "" || start.RoutingDecision != nil {
				t.Fatalf("explicit router start evidence = %+v", start)
			}
		})
	}
}

func TestADR_0369_Scenario3_ResumeProviderSafety(t *testing.T) {
	t.Run("cross-provider child resumes on its persisted target", func(t *testing.T) {
		store := memstore.New()
		fallback := mockllm.New(mockllm.TextTurn("WRONG"))
		pair := mockllm.New(mockllm.TextTurn("FIRST"), mockllm.TextTurn("SECOND"))
		pairEngine := NewEngine(Deps{LLM: pair, Catalog: markerEngine("x").deps.Catalog, Policy: allowAllInt(), Model: "pair-model"})
		tool := NewSubagentTool(
			NewEngine(Deps{LLM: fallback, Catalog: markerEngine("x").deps.Catalog, Policy: allowAllInt(), Model: "default-model"}),
			WithSubagentStore(store),
			WithSubagentProvider("default"),
			WithSubagentSelectorResolver(func(provider, model string) (ResolvedModelSelector, error) {
				return ResolvedModelSelector{Target: ModelTarget{Provider: provider, Model: model}, ActualProvider: provider, ProviderBearing: true}, nil
			}),
			WithSubagentTargetEngineFactory(func(target ModelTarget) (*Engine, bool) {
				if target != (ModelTarget{Provider: "second", Model: "pair-model"}) {
					return nil, false
				}
				return pairEngine, true
			}),
		).(*SubagentTool)

		fresh, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"first","provider":"second","model":"pair-model"}`)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()})
		if err != nil || fresh.IsError {
			t.Fatalf("fresh pair child = %+v, err=%v", fresh, err)
		}
		persisted, err := store.Load(t.Context(), "subagent-p1")
		if err != nil {
			t.Fatalf("load pair child: %v", err)
		}
		if persisted.ProviderID != "second" || persisted.ModelID != "pair-model" {
			t.Fatalf("persisted target = %s/%s", persisted.ProviderID, persisted.ModelID)
		}
		resumed, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("p2", "Subagent", json.RawMessage(`{"prompt":"continue","resume":"subagent-p1"}`)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()})
		if err != nil || resumed.IsError || !strings.Contains(resumed.Content, "SECOND") {
			t.Fatalf("cross-provider resume = %+v, err=%v", resumed, err)
		}
		if fallback.Calls() != 0 || pair.Calls() != 2 {
			t.Fatalf("provider calls default=%d pair=%d, want 0/2", fallback.Calls(), pair.Calls())
		}
	})

	t.Run("unknown persisted target fails closed", func(t *testing.T) {
		store := memstore.New()
		fallback := mockllm.New(mockllm.TextTurn("WRONG"))
		pair := mockllm.New(mockllm.TextTurn("FIRST"))
		catalog := markerEngine("x").deps.Catalog
		tool := NewSubagentTool(NewEngine(Deps{LLM: fallback, Catalog: catalog, Policy: allowAllInt(), Model: "default-model"}),
			WithSubagentStore(store), WithSubagentProvider("default"),
			WithSubagentSelectorResolver(func(provider, model string) (ResolvedModelSelector, error) {
				return ResolvedModelSelector{Target: ModelTarget{Provider: provider, Model: model}, ActualProvider: provider, ProviderBearing: true}, nil
			}),
			WithSubagentTargetEngineFactory(func(target ModelTarget) (*Engine, bool) {
				if target.Provider == "second" {
					return NewEngine(Deps{LLM: pair, Catalog: catalog, Policy: allowAllInt(), Model: target.Model}), true
				}
				return nil, false
			})).(*SubagentTool)
		fresh, _ := tool.ExecuteWithParent(t.Context(), session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"first","provider":"second","model":"pair-model"}`)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()})
		if fresh.IsError {
			t.Fatalf("fresh pair child = %+v", fresh)
		}
		persisted, _ := store.Load(t.Context(), "subagent-p1")
		persisted.ProviderID = "missing"
		if err := store.Save(t.Context(), persisted); err != nil {
			t.Fatalf("save unavailable target: %v", err)
		}
		resumed, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("p2", "Subagent", json.RawMessage(`{"prompt":"continue","resume":"subagent-p1"}`)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()})
		if err != nil || !resumed.IsError || len(resumed.Content) > 512 {
			t.Fatalf("unavailable resume = %+v, err=%v", resumed, err)
		}
		if fallback.Calls() != 0 || pair.Calls() != 1 {
			t.Fatalf("unavailable resume made provider call: default=%d pair=%d", fallback.Calls(), pair.Calls())
		}
	})

	t.Run("legacy empty target retains default behavior", func(t *testing.T) {
		store := memstore.New()
		fallback := mockllm.New(mockllm.TextTurn("FIRST"), mockllm.TextTurn("SECOND"))
		tool := NewSubagentTool(NewEngine(Deps{LLM: fallback, Catalog: markerEngine("x").deps.Catalog, Policy: allowAllInt(), Model: "default-model"}),
			WithSubagentStore(store), WithSubagentProvider("default"),
			WithSubagentTargetEngineFactory(func(ModelTarget) (*Engine, bool) { t.Fatal("legacy resume called target factory"); return nil, false })).(*SubagentTool)
		fresh, _ := tool.ExecuteWithParent(t.Context(), session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"first"}`)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()})
		if fresh.IsError {
			t.Fatalf("fresh legacy seed = %+v", fresh)
		}
		persisted, _ := store.Load(t.Context(), "subagent-p1")
		persisted.ProviderID, persisted.ModelID = "", ""
		if err := store.Save(t.Context(), persisted); err != nil {
			t.Fatalf("save legacy snapshot: %v", err)
		}
		resumed, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("p2", "Subagent", json.RawMessage(`{"prompt":"continue","resume":"subagent-p1"}`)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()})
		if err != nil || resumed.IsError || !strings.Contains(resumed.Content, "SECOND") {
			t.Fatalf("legacy resume = %+v, err=%v", resumed, err)
		}
	})
}

func TestADR_0369_Scenario3_InvalidSelectors(t *testing.T) {
	defaultLLM := mockllm.New(mockllm.TextTurn("DEFAULT"))
	resolver := func(provider, model string) (ResolvedModelSelector, error) {
		switch model {
		case "pair-alias":
			if provider != "" && provider != "second" {
				return ResolvedModelSelector{}, errors.New("provider conflicts with alias provider")
			}
			return ResolvedModelSelector{Target: ModelTarget{Provider: "second", Model: "pair"}, ProviderBearing: true, ActualProvider: "second"}, nil
		case "parent-pair-alias":
			return ResolvedModelSelector{Target: ModelTarget{Model: "pair"}, ProviderBearing: true, ActualProvider: "parent"}, nil
		case "bad", "missing-category":
			return ResolvedModelSelector{}, errors.New("selector unavailable")
		default:
			return ResolvedModelSelector{Target: ModelTarget{Provider: provider, Model: model}, ProviderBearing: provider != "", ActualProvider: provider}, nil
		}
	}
	reviewer := markerEngine("REVIEWER")
	factoryBuilds, specialistBuilds := 0, 0
	tool := NewSubagentTool(NewEngine(Deps{LLM: defaultLLM, Catalog: reviewer.deps.Catalog, Policy: allowAllInt(), Model: "DEFAULT"}),
		WithSubagentSelectorResolver(resolver),
		WithSubagentTargetEngineFactory(func(target ModelTarget) (*Engine, bool) {
			factoryBuilds++
			return markerEngine(target.Model), true
		}),
		WithAgentEngines(map[string]*Engine{"reviewer": reviewer}, []AgentMeta{{Name: "reviewer"}}),
		WithAgentModelEngineFactory(func(name, model string) (*Engine, bool) {
			specialistBuilds++
			if name == "reviewer" && model == "literal" {
				return markerEngine("SPECIALIST:" + model), true
			}
			return nil, false
		})).(*SubagentTool)

	cases := []string{
		`{"prompt":"x","provider":"second"}`,
		`{"prompt":"x","provider":"first","model":"pair-alias"}`,
		`{"prompt":"x","agent":"reviewer","provider":"second","model":"literal"}`,
		`{"prompt":"x","agent":"reviewer","model":"pair-alias"}`,
		`{"prompt":"x","agent":"reviewer","model":"parent-pair-alias"}`,
		`{"prompt":"x","provider":"unknown","model":"bad"}`,
		`{"prompt":"x","provider":"model-router","model":"missing-category"}`,
		`{"prompt":"x","fork":true,"provider":"second","model":"literal"}`,
		`{"prompt":"x","resume":"subagent-old","provider":"second","model":"literal"}`,
	}
	for i, args := range cases {
		res, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall(session.ToolCallID(string(rune('a'+i))), "Subagent", json.RawMessage(args)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry(), forkHistory: func() []session.Message { return nil }})
		if err != nil || !res.IsError || len(res.Content) > 512 {
			t.Fatalf("invalid selector %s = %+v, err=%v", args, res, err)
		}
	}
	if defaultLLM.Calls() != 0 || factoryBuilds != 0 || specialistBuilds != 0 {
		t.Fatalf("invalid selectors crossed child creation: default calls=%d target builds=%d specialist builds=%d", defaultLLM.Calls(), factoryBuilds, specialistBuilds)
	}

	res, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("specialist", "Subagent", json.RawMessage(`{"prompt":"x","agent":"reviewer","model":"literal"}`)), memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()})
	if err != nil || res.IsError || !strings.Contains(res.Content, "SPECIALIST:literal") {
		t.Fatalf("model-only specialist override = %+v, err=%v", res, err)
	}
}

func TestADR_0369_Scenario1_NamedAutomaticRouterPreservesProviderTarget(t *testing.T) {
	var built []ModelTarget
	tool := NewSubagentTool(markerEngine("default"),
		WithAgentEngines(map[string]*Engine{"reviewer": markerEngine("specialist")}, []AgentMeta{{Name: "reviewer", Provider: "parent"}}),
		WithRoutableAgents([]string{"reviewer"}),
		WithAgentTargetEngineFactory(func(name string, target ModelTarget) (*Engine, bool) {
			if name != "reviewer" {
				t.Fatalf("target factory agent = %q, want reviewer", name)
			}
			built = append(built, target)
			return markerEngine(target.Model), true
		}),
	).(*SubagentTool)

	res, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("named-route", "Subagent", json.RawMessage(`{"prompt":"review","agent":"reviewer"}`)), memEnv("/ws"), func(ev session.Event) {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			if ev.Subagent.Provider != "second" || ev.Subagent.Model != "opaque-model" || ev.Subagent.RoutedCategory != "large" || ev.Subagent.RoutedModel != "opaque-model" {
				t.Fatalf("routing evidence = %+v", ev.Subagent)
			}
		}
	}, parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		return modelRoutingResult{category: "large", provider: "second", model: "opaque-model", ok: true}
	}})
	if err != nil || res.IsError {
		t.Fatalf("named automatic route = %+v, err=%v", res, err)
	}
	if len(built) != 1 || built[0] != (ModelTarget{Provider: "second", Model: "opaque-model"}) {
		t.Fatalf("target factory calls = %+v, want second/opaque-model", built)
	}
}

func TestADR_0369_Scenario1_NamedAutomaticRouterUnavailableTargetFallsSoft(t *testing.T) {
	tool := NewSubagentTool(markerEngine("default"),
		WithAgentEngines(map[string]*Engine{"reviewer": markerEngine("specialist")}, []AgentMeta{{Name: "reviewer", Provider: "parent"}}),
		WithRoutableAgents([]string{"reviewer"}),
		WithAgentTargetEngineFactory(func(string, ModelTarget) (*Engine, bool) { return nil, false }),
	).(*SubagentTool)
	var start *session.SubagentPayload
	res, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("unavailable-route", "Subagent", json.RawMessage(`{"prompt":"review","agent":"reviewer"}`)), memEnv("/ws"), func(ev session.Event) {
		if ev.Type == session.EvSubagentStart {
			start = ev.Subagent
		}
	}, parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		return modelRoutingResult{category: "large", provider: "second", model: "opaque-model", ok: true}
	}})
	if err != nil || res.IsError {
		t.Fatalf("unavailable automatic target = %+v, err=%v", res, err)
	}
	if start == nil || start.Provider != "parent" || start.Model != "specialist" || start.RoutedCategory != "" || start.RoutedModel != "" || start.RoutingReason != session.RoutingReasonTargetUnavailable {
		t.Fatalf("fail-soft routing evidence = %+v", start)
	}
}
