package governance

import (
	"encoding/json"
	"testing"
)

// TestBuiltinToolRulesMatchTheExecutedOperand pins that a built-in tool's rules
// see the argument the tool acts on, never another policy-looking argument the
// tool ignores. Each call names an allowed decoy in a field that the generic
// precedence would have read first, and the executed operand in the field the
// tool runs: a Deny on the executed operand must win, and an Allow written for
// the decoy must not admit the call.
//
// MUTATION-VERIFY: drop a tool from builtinResourceArgs and its row fails.
func TestBuiltinToolRulesMatchTheExecutedOperand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tool, args, executed, decoy string
	}{
		{tool: "WebFetch", args: `{"path":"https://allowed.example/","url":"https://blocked.example/"}`, executed: "https://blocked.example/", decoy: "https://allowed.example/"},
		{tool: "WebSearch", args: `{"pattern":"allowed","query":"blocked"}`, executed: "blocked", decoy: "allowed"},
		{tool: "Glob", args: `{"path":"allowed/**","pattern":"blocked/**"}`, executed: "blocked/**", decoy: "allowed/**"},
		{tool: "Read", args: `{"file_path":"allowed.txt","path":"blocked.txt"}`, executed: "blocked.txt", decoy: "allowed.txt"},
		{tool: "Grep", args: `{"pattern":"allowed","path":"blocked/**"}`, executed: "blocked/**", decoy: "allowed"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			deny := NewEvaluator([]Rule{
				{Scope: ScopeUser, Tool: tc.tool, Pattern: tc.decoy, Effect: Allow},
				{Scope: ScopeUser, Tool: tc.tool, Pattern: tc.executed, Effect: Deny},
			})
			if got := deny.Evaluate(tc.tool, json.RawMessage(tc.args), false); got.Effect != Deny {
				t.Errorf("Deny on the executed operand: got %v (%s), want Deny", got.Effect, got.Reason)
			}
			decoyOnly := NewEvaluator([]Rule{{Scope: ScopeUser, Tool: tc.tool, Pattern: tc.decoy, Effect: Allow}})
			if got := decoyOnly.Evaluate(tc.tool, json.RawMessage(tc.args), false); got.Effect == Allow {
				t.Errorf("Allow written for the ignored decoy admitted the call: %s", got.Reason)
			}
			rule, ok := deny.LearnableRule(tc.tool, json.RawMessage(tc.args))
			if !ok || rule.Pattern != tc.executed {
				t.Errorf("LearnableRule = %+v, %v; want the executed operand %q", rule, ok, tc.executed)
			}
		})
	}
}

// TestGrepWithoutPathMatchesToolWideRulesOnly pins that a Grep call without a
// path, which searches the whole workspace, is not matched as if its regular
// expression were a path: only tool-wide rules apply.
func TestGrepWithoutPathMatchesToolWideRulesOnly(t *testing.T) {
	t.Parallel()
	e := NewEvaluator([]Rule{{Scope: ScopeUser, Tool: "Grep", Pattern: "TODO", Effect: Allow}})
	if got := e.Evaluate("Grep", json.RawMessage(`{"pattern":"TODO"}`), false); got.Effect == Allow {
		t.Fatalf("a path rule matched Grep's regular expression: %s", got.Reason)
	}
	if _, ok := e.LearnableRule("Grep", json.RawMessage(`{"pattern":"TODO"}`)); ok {
		t.Fatal("a Grep call without a path must not be learnable as a tool-wide allow")
	}
}

// TestUndeclaredToolRulesCoverEveryResourceOperand pins the deny-dominant
// matching for a tool whose argument contract governance does not know, such as
// an MCP tool: the call is evaluated on its first resource argument, and a
// pattern rule on any other resource argument can only make it stricter.
func TestUndeclaredToolRulesCoverEveryResourceOperand(t *testing.T) {
	t.Parallel()
	const name = "mcp__web__fetch"
	args := json.RawMessage(`{"path":"https://allowed.example/","url":"https://blocked.example/"}`)
	for _, tc := range []struct {
		name  string
		rules []Rule
		want  Effect
	}{
		{
			name: "deny on another operand",
			rules: []Rule{
				{Scope: ScopeUser, Tool: name, Pattern: "https://allowed.example/*", Effect: Allow},
				{Scope: ScopeUser, Tool: name, Pattern: "https://blocked.example/*", Effect: Deny},
			},
			want: Deny,
		},
		{
			name: "ask on another operand",
			rules: []Rule{
				{Scope: ScopeUser, Tool: name, Pattern: "https://allowed.example/*", Effect: Allow},
				{Scope: ScopeUser, Tool: name, Pattern: "https://blocked.example/*", Effect: Ask},
			},
			want: Ask,
		},
		{
			name:  "no rule on another operand",
			rules: []Rule{{Scope: ScopeUser, Tool: name, Pattern: "https://allowed.example/*", Effect: Allow}},
			want:  Allow,
		},
		{
			name: "allow on another operand",
			rules: []Rule{
				{Scope: ScopeUser, Tool: name, Pattern: "https://blocked.example/*", Effect: Allow},
			},
			want: Ask,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NewEvaluator(tc.rules).Evaluate(name, args, false); got.Effect != tc.want {
				t.Fatalf("Evaluate = %v (%s), want %v", got.Effect, got.Reason, tc.want)
			}
		})
	}
}
