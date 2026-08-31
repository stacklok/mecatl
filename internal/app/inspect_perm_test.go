package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// inspectToolNames is the set of read-only child-observability tools that
// defaultRules() pre-approves at the built-in floor (issue #37): the two persisted
// transcript pulls (InspectSubagent/InspectMember) and the live-registry
// collection channel (SubagentStatus). Names are string literals matching the
// catalog names in engine/agent (the constants there are unexported), the same
// convention defaultRules() itself uses.
var inspectToolNames = []string{
	"InspectSubagent",
	"InspectMember",
	"SubagentStatus",
	// BashStatus joins them: the same read-only registry-pull class (the
	// background-Bash jobs' status/collect/cancel channel), floor-scoped
	// alongside SubagentStatus in defaultRules().
	"BashStatus",
	agent.CurrentSessionToolName,
}

// TestInspectToolsDefaultExplicitAllow proves all three child-observability tools
// resolve to Allow — both via the built-in defaultRules() directly AND via the
// PRODUCTION assembly mainRules(Config{}) (the actual ruleset the engine is built
// from). Mirrors TestMemoryToolsDefaultExplicitAllow: the mainRules path catches a
// future wrapper change that drops an inspect allow even if defaultRules() still
// carries it.
func TestInspectToolsDefaultExplicitAllow(t *testing.T) {
	for _, name := range inspectToolNames {
		if got := evalDefault(t, name); got != governance.Allow {
			t.Errorf("inspect tool %q should default to Allow (defaultRules), got %v", name, got)
		}
	}
	// Production assembly: mainRules(Config{}) is what buildEngine feeds the policy.
	prod := permpolicy.NewPolicy(mainRules(Config{}), nil)
	for _, name := range inspectToolNames {
		got := prod.Evaluate(context.Background(), "s1", session.ModeDefault,
			session.NewToolCall("id", name, json.RawMessage(`{}`)), nil).Effect
		if got != governance.Allow {
			t.Errorf("inspect tool %q should default to Allow (mainRules production assembly), got %v", name, got)
		}
	}
}

// TestConfiguredAskOverridesInspectAllow is the INVARIANT-SAFETY test: a
// higher-scope (user) configured Ask on InspectSubagent beats the built-in-floor
// Allow. The floor Allow is the LOWEST scope, so a configured Ask must win — the
// inspect pre-approval can never suppress a configured Ask. Mirrors
// TestConfiguredAskOverridesMemoryAllow.
func TestConfiguredAskOverridesInspectAllow(t *testing.T) {
	rules := append(defaultRules(),
		governance.Rule{Scope: governance.ScopeUser, Tool: "InspectSubagent", Effect: governance.Ask})
	policy := permpolicy.NewPolicy(rules, nil)
	got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", "InspectSubagent", json.RawMessage(`{}`)), nil)
	if got.Effect != governance.Ask {
		t.Fatalf("a configured (ScopeUser) Ask on InspectSubagent must beat the built-in-floor Allow; got %v", got.Effect)
	}
}

// TestConfiguredDenyOverridesInspectAllow proves a configured Deny on
// SubagentStatus wins over the built-in-floor Allow (deny-dominant). This is the
// config gesture that withholds the background-collection channel. Mirrors
// TestConfiguredDenyOverridesSoulApply.
func TestConfiguredDenyOverridesInspectAllow(t *testing.T) {
	rules := append(defaultRules(),
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: "SubagentStatus", Effect: governance.Deny})
	policy := permpolicy.NewPolicy(rules, nil)
	got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", "SubagentStatus", json.RawMessage(`{}`)), nil)
	if got.Effect != governance.Deny {
		t.Fatalf("a configured Deny on SubagentStatus must win over the built-in-floor Allow; got %v", got.Effect)
	}
}

func TestConfiguredRulesOverrideCurrentSessionAllow(t *testing.T) {
	for _, effect := range []governance.Effect{governance.Ask, governance.Deny} {
		rules := append(defaultRules(), governance.Rule{
			Scope: governance.ScopeUser, Tool: agent.CurrentSessionToolName, Effect: effect,
		})
		got := permpolicy.NewPolicy(rules, nil).Evaluate(context.Background(), "s1", session.ModeDefault,
			session.NewToolCall("id", agent.CurrentSessionToolName, json.RawMessage(`{}`)), nil)
		if got.Effect != effect {
			t.Errorf("configured %v on CurrentSession resolved to %v", effect, got.Effect)
		}
	}
}

// No separate TestInspectAllowDoesNotAffectOtherTools: the inspect allows are
// floor-scoped + tool-name-exact, exactly like the memory/soul allows, and
// TestMemoryAllowDoesNotAffectOtherTools already pins that Edit/Write/Bash stay
// Ask against the FULL defaultRules() slice — which now includes the inspect
// entries — so the "no other tool's Ask is loosened" invariant covers them for free.
