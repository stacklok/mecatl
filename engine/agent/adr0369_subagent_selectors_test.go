package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
	res, err := tool.ExecuteWithParent(t.Context(), session.NewToolCall("omitted", "Subagent", json.RawMessage(`{"prompt":"route me"}`)), memEnv("/ws"), nil, parentCaps{
		children: newChildRunRegistry(),
		routeDecision: func(context.Context, string) modelRoutingResult {
			classifierCalls++
			return modelRoutingResult{category: "large", provider: "second", model: "routed", ok: true}
		},
	})
	if err != nil || res.IsError || classifierCalls != 1 || built[len(built)-1] != (ModelTarget{Provider: "second", Model: "routed"}) {
		t.Fatalf("omitted selector did not preserve automatic routing: result=%+v err=%v calls=%d built=%+v", res, err, classifierCalls, built)
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

func TestADR_0369_Scenario3_InvalidSelectors(t *testing.T) {
	defaultLLM := mockllm.New(mockllm.TextTurn("DEFAULT"))
	resolver := func(provider, model string) (ResolvedModelSelector, error) {
		switch model {
		case "pair-alias":
			if provider != "" && provider != "second" {
				return ResolvedModelSelector{}, errors.New("provider conflicts with alias provider")
			}
			return ResolvedModelSelector{Target: ModelTarget{Provider: "second", Model: "pair"}, ProviderBearing: true, ActualProvider: "second"}, nil
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
