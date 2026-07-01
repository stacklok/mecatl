package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// memoryToolNames is the full set of memory capability tools that defaultRules()
// pre-approves at the built-in floor (per-project + cross-project).
var memoryToolNames = []string{
	memory.RememberToolName,
	memory.RecallToolName,
	memory.SearchMemoryToolName,
	memory.RememberUserToolName,
	memory.RecallUserToolName,
	memory.SearchUserModelToolName,
}

// evalDefault evaluates tool against the built-in defaultRules() ruleset (no config,
// no learned rules) in default mode, returning the resolved effect.
func evalDefault(t *testing.T, tool string) governance.Effect {
	t.Helper()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	return policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", tool, json.RawMessage(`{}`)), nil).Effect
}

// TestMemoryToolsDefaultExplicitAllow proves all six memory tools resolve to Allow —
// both via the built-in defaultRules() directly AND via the PRODUCTION assembly
// mainRules(Config{}) (the actual ruleset the engine is built from). The mainRules
// path catches a future wrapper change that drops a memory allow even if
// defaultRules() still carries it.
func TestMemoryToolsDefaultExplicitAllow(t *testing.T) {
	for _, name := range memoryToolNames {
		if got := evalDefault(t, name); got != governance.Allow {
			t.Errorf("memory tool %q should default to Allow (defaultRules), got %v", name, got)
		}
	}
	// Production assembly: mainRules(Config{}) is what buildEngine feeds the policy.
	prod := permpolicy.NewPolicy(mainRules(Config{}), nil)
	for _, name := range memoryToolNames {
		got := prod.Evaluate(context.Background(), "s1", session.ModeDefault,
			session.NewToolCall("id", name, json.RawMessage(`{}`)), nil).Effect
		if got != governance.Allow {
			t.Errorf("memory tool %q should default to Allow (mainRules production assembly), got %v", name, got)
		}
	}
}

// TestWebSearchDefaultIsFloorAllow proves WebSearch resolves to Allow via the
// built-in floor (issue #26), both directly and via the production mainRules
// assembly — matching the WebFetch posture (config-overridable; the real egress
// gate is the provider config, not an Ask).
func TestWebSearchDefaultIsFloorAllow(t *testing.T) {
	if got := evalDefault(t, "WebSearch"); got != governance.Allow {
		t.Fatalf("WebSearch should default to Allow (defaultRules), got %v", got)
	}
	prod := permpolicy.NewPolicy(mainRules(Config{}), nil)
	got := prod.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", "WebSearch", json.RawMessage(`{}`)), nil).Effect
	if got != governance.Allow {
		t.Fatalf("WebSearch should default to Allow (mainRules production assembly), got %v", got)
	}
}

// TestFetchMcpResourceDefaultIsFloorAllow proves FetchMcpResource (issue #223
// Phase 2) resolves to Allow via the built-in floor, both directly and via the
// production mainRules assembly — matching the WebFetch/WebSearch posture
// (config-overridable; the SSRF gate is session.ValidateMediaURL, not an Ask).
func TestFetchMcpResourceDefaultIsFloorAllow(t *testing.T) {
	if got := evalDefault(t, "FetchMcpResource"); got != governance.Allow {
		t.Fatalf("FetchMcpResource should default to Allow (defaultRules), got %v", got)
	}
	prod := permpolicy.NewPolicy(mainRules(Config{}), nil)
	got := prod.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", "FetchMcpResource", json.RawMessage(`{}`)), nil).Effect
	if got != governance.Allow {
		t.Fatalf("FetchMcpResource should default to Allow (mainRules production assembly), got %v", got)
	}
}

// TestConfiguredAskAndDenyOverrideFetchMcpResourceAllow proves a configured
// Ask or Deny in a higher scope beats the FetchMcpResource floor-Allow (the
// floor is config-overridable; deny-dominant). Mirrors the WebSearch override.
func TestConfiguredAskAndDenyOverrideFetchMcpResourceAllow(t *testing.T) {
	for _, eff := range []governance.Effect{governance.Ask, governance.Deny} {
		rules := append(defaultRules(),
			governance.Rule{Scope: governance.ScopeUser, Tool: "FetchMcpResource", Effect: eff})
		policy := permpolicy.NewPolicy(rules, nil)
		got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
			session.NewToolCall("id", "FetchMcpResource", json.RawMessage(`{}`)), nil)
		if got.Effect != eff {
			t.Fatalf("a configured (ScopeUser) %v on FetchMcpResource must beat the floor Allow; got %v", eff, got.Effect)
		}
	}
}

// TestCallMcpWithQueryDefaultIsFloorAllow proves CallMcpWithQuery (issue #223)
// resolves to Allow via the built-in floor, both directly and via the production
// mainRules assembly — matching the WebSearch/WebFetch posture (config-overridable;
// the guardrail default block set covers the exfil/injection risk, not an Ask).
func TestCallMcpWithQueryDefaultIsFloorAllow(t *testing.T) {
	if got := evalDefault(t, "CallMcpWithQuery"); got != governance.Allow {
		t.Fatalf("CallMcpWithQuery should default to Allow (defaultRules), got %v", got)
	}
	prod := permpolicy.NewPolicy(mainRules(Config{}), nil)
	got := prod.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", "CallMcpWithQuery", json.RawMessage(`{}`)), nil).Effect
	if got != governance.Allow {
		t.Fatalf("CallMcpWithQuery should default to Allow (mainRules production assembly), got %v", got)
	}
}

// TestConfiguredAskAndDenyOverrideCallMcpWithQueryAllow proves a configured
// Ask or Deny in a higher scope beats the CallMcpWithQuery floor-Allow (the
// floor is config-overridable; deny-dominant). Mirrors the FetchMcpResource override.
func TestConfiguredAskAndDenyOverrideCallMcpWithQueryAllow(t *testing.T) {
	for _, eff := range []governance.Effect{governance.Ask, governance.Deny} {
		rules := append(defaultRules(),
			governance.Rule{Scope: governance.ScopeUser, Tool: "CallMcpWithQuery", Effect: eff})
		policy := permpolicy.NewPolicy(rules, nil)
		got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
			session.NewToolCall("id", "CallMcpWithQuery", json.RawMessage(`{}`)), nil)
		if got.Effect != eff {
			t.Fatalf("a configured (ScopeUser) %v on CallMcpWithQuery must beat the floor Allow; got %v", eff, got.Effect)
		}
	}
}

// TestConfiguredAskAndDenyOverrideWebSearchAllow proves a configured Ask or Deny in
// a higher scope beats the WebSearch floor-Allow (the floor is config-overridable;
// deny-dominant). Mirrors the memory-allow override invariant.
func TestConfiguredAskAndDenyOverrideWebSearchAllow(t *testing.T) {
	for _, eff := range []governance.Effect{governance.Ask, governance.Deny} {
		rules := append(defaultRules(),
			governance.Rule{Scope: governance.ScopeUser, Tool: "WebSearch", Effect: eff})
		policy := permpolicy.NewPolicy(rules, nil)
		got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
			session.NewToolCall("id", "WebSearch", json.RawMessage(`{}`)), nil)
		if got.Effect != eff {
			t.Fatalf("a configured (ScopeUser) %v on WebSearch must beat the floor Allow; got %v", eff, got.Effect)
		}
	}
}

// TestSoulApplyDefaultIsAllow proves the synthetic soul:apply action resolves to
// Allow via the built-in defaultRules(), so the soul is applied by default.
func TestSoulApplyDefaultIsAllow(t *testing.T) {
	if got := evalDefault(t, SoulApplyAction); got != governance.Allow {
		t.Fatalf("soul:apply should default to Allow, got %v", got)
	}
}

// TestConfiguredAskOverridesMemoryAllow is the INVARIANT-SAFETY test: a higher-scope
// (user) configured Ask on Remember beats the built-in-floor Allow. The floor Allow
// is the LOWEST scope, so a configured Ask must win — the memory pre-approval can
// never suppress a configured Ask. Mirrors TestHigherScopeAskBeatsLowerScopeAllow.
func TestConfiguredAskOverridesMemoryAllow(t *testing.T) {
	rules := append(defaultRules(),
		governance.Rule{Scope: governance.ScopeUser, Tool: memory.RememberToolName, Effect: governance.Ask})
	policy := permpolicy.NewPolicy(rules, nil)
	got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", memory.RememberToolName, json.RawMessage(`{}`)), nil)
	if got.Effect != governance.Ask {
		t.Fatalf("a configured (ScopeUser) Ask on Remember must beat the built-in-floor Allow; got %v", got.Effect)
	}
}

// TestConfiguredDenyOverridesSoulApply proves a configured Deny on soul:apply wins
// over the built-in-floor Allow (deny-dominant). This is the config gesture that
// withholds the soul.
func TestConfiguredDenyOverridesSoulApply(t *testing.T) {
	rules := append(defaultRules(),
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: SoulApplyAction, Effect: governance.Deny})
	policy := permpolicy.NewPolicy(rules, nil)
	got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", SoulApplyAction, json.RawMessage(`{}`)), nil)
	if got.Effect != governance.Deny {
		t.Fatalf("a configured Deny on soul:apply must win over the built-in-floor Allow; got %v", got.Effect)
	}
}

// TestMemoryAllowDoesNotAffectOtherTools proves the floor-scoped, tool-name-exact
// memory/soul allows change resolution ONLY for those keys: Edit/Write/Bash stay
// Ask. This pins the "no other tool's Ask is loosened" invariant.
func TestMemoryAllowDoesNotAffectOtherTools(t *testing.T) {
	for _, name := range []string{"Edit", "Write", "Bash"} {
		if got := evalDefault(t, name); got != governance.Ask {
			t.Errorf("%s must stay Ask (memory/soul allows must not loosen it), got %v", name, got)
		}
	}
}

// TestMemoryConfiguredRuleSurvivesYolo pins the deny-dominant / configured-ask
// invariant under the --yolo (AllowAllTools) posture for the new memory keys:
// mainRules(Config{AllowAllTools:true}) prepends a ScopeCLI allow-all, which loosens
// only the built-in floor — it must NEVER suppress a configured Ask or Deny on a
// memory tool. A user-scope Ask on Remember still asks; a user-scope Deny still
// denies, even with yolo on.
func TestMemoryConfiguredRuleSurvivesYolo(t *testing.T) {
	cases := []struct {
		name   string
		effect governance.Effect
	}{
		{"configured Ask survives yolo", governance.Ask},
		{"configured Deny survives yolo", governance.Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := append(mainRules(Config{AllowAllTools: true}),
				governance.Rule{Scope: governance.ScopeUser, Tool: memory.RememberToolName, Effect: tc.effect})
			policy := permpolicy.NewPolicy(rules, nil)
			got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
				session.NewToolCall("id", memory.RememberToolName, json.RawMessage(`{}`)), nil)
			if got.Effect != tc.effect {
				t.Fatalf("a configured %v on Remember must survive --yolo (allow-all loosens only the floor); got %v", tc.effect, got.Effect)
			}
		})
	}
}
