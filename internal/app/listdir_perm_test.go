package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// TestListDirDefaultExplicitAllow proves ListDir resolves to Allow — both via the
// built-in defaultRules() directly AND via the PRODUCTION assembly mainRules(Config{})
// (the actual ruleset the engine is built from). ListDir is ReadOnly()==true
// (engine/adapter/fstools/listdir.go), the same class as Read/Grep/Glob, so it
// belongs at the built-in floor alongside them; without an explicit rule it fell
// through to the evaluator's unmatched-defaults-to-Ask floor.
func TestListDirDefaultExplicitAllow(t *testing.T) {
	if got := evalDefault(t, "ListDir"); got != governance.Allow {
		t.Errorf("ListDir should default to Allow (defaultRules), got %v", got)
	}
	// Production assembly: mainRules(Config{}) is what buildEngine feeds the policy.
	prod := permpolicy.NewPolicy(mainRules(Config{}), nil)
	got := prod.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", "ListDir", json.RawMessage(`{}`)), nil).Effect
	if got != governance.Allow {
		t.Errorf("ListDir should default to Allow (mainRules production assembly), got %v", got)
	}
}

// TestConfiguredAskOverridesListDirAllow is the INVARIANT-SAFETY test: a
// higher-scope (user) configured Ask on ListDir beats the built-in-floor Allow.
// The floor Allow is the LOWEST scope, so a configured Ask must win — the
// ListDir pre-approval can never suppress a configured Ask.
func TestConfiguredAskOverridesListDirAllow(t *testing.T) {
	rules := append(defaultRules(),
		governance.Rule{Scope: governance.ScopeUser, Tool: "ListDir", Effect: governance.Ask})
	policy := permpolicy.NewPolicy(rules, nil)
	got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", "ListDir", json.RawMessage(`{}`)), nil)
	if got.Effect != governance.Ask {
		t.Fatalf("a configured (ScopeUser) Ask on ListDir must beat the built-in-floor Allow; got %v", got.Effect)
	}
}

// TestConfiguredDenyOverridesListDirAllow proves a configured Deny on ListDir
// wins over the built-in-floor Allow (deny-dominant).
func TestConfiguredDenyOverridesListDirAllow(t *testing.T) {
	rules := append(defaultRules(),
		governance.Rule{Scope: governance.ScopeSharedProject, Tool: "ListDir", Effect: governance.Deny})
	policy := permpolicy.NewPolicy(rules, nil)
	got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", "ListDir", json.RawMessage(`{}`)), nil)
	if got.Effect != governance.Deny {
		t.Fatalf("a configured Deny on ListDir must win over the built-in-floor Allow; got %v", got.Effect)
	}
}
