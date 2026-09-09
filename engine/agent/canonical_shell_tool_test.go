package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestCanonicalShellTool_Scenario1_RejectsLegacyToolCall(t *testing.T) {
	runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "executed"}}
	catalog := tool.NewCatalog()
	catalog.MustRegister(NewShellTool())
	legacy := session.NewToolCall("legacy", "Bash", []byte(`{"command":"echo should-not-run"}`))
	eng := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(legacy), mockllm.TextTurn("done")), Catalog: catalog, Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: tool.ShellToolName, Effect: governance.Allow}}, nil)})
	sess := session.New("legacy", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{MaxTurns: 3}, time.Now())
	run := eng.Run(context.Background(), sess, bashEnvRunner(runner), RunRequest{Text: "run"})
	var result session.ToolResult
	for event := range run.Events() {
		if event.Type == session.EvToolResult && event.ToolResult != nil {
			result = *event.ToolResult
		}
	}
	if !result.IsError || !strings.Contains(result.Content, "unknown tool") {
		t.Fatalf("legacy result = %+v, want unknown-tool error", result)
	}
	if runner.command != "" {
		t.Fatalf("legacy Bash call executed command %q", runner.command)
	}
}

// TestCanonicalShellTool_Scenario1_CommandPolicyCoverage pins the command-only
// policy paths to the canonical catalog identity: deny dominance, substitution
// safety, plan-mode denial, and the independently authorized system scope.
func TestCanonicalShellTool_Scenario1_CommandPolicyCoverage(t *testing.T) {
	t.Parallel()
	call := func(id, command, scope string) session.ToolCall {
		return scopedShellCall(id, command, scope)
	}
	for _, tc := range []struct {
		name  string
		mode  session.PermissionMode
		call  session.ToolCall
		rules []governance.Rule
		want  governance.Effect
	}{
		{
			name:  "deny dominates command allow",
			call:  call("deny", "go test ./...", "managed"),
			rules: []governance.Rule{{Tool: tool.ShellToolName, Pattern: "go test*", Effect: governance.Allow}, {Tool: tool.ShellToolName, Pattern: "go test*", Effect: governance.Deny}},
			want:  governance.Deny,
		},
		{
			name:  "substitution is not normalized into an allow",
			call:  call("substitution", "go test $(rm -rf nowhere)", "managed"),
			rules: []governance.Rule{{Tool: tool.ShellToolName, Pattern: "go test*", Effect: governance.Allow}},
			want:  governance.Ask,
		},
		{
			name:  "plan mode denies shell",
			mode:  session.ModePlan,
			call:  call("plan", "go test ./...", "managed"),
			rules: []governance.Rule{{Tool: tool.ShellToolName, Pattern: "go test*", Effect: governance.Allow}},
			want:  governance.Deny,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision := permpolicy.NewPolicy(tc.rules, nil).Evaluate(context.Background(), "policy", tc.mode, tc.call, nil)
			if decision.Effect != tc.want {
				t.Fatalf("decision = %q, want %q (%s)", decision.Effect, tc.want, decision.Reason)
			}
		})
	}

	runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
	_, result := runScopedShell(t, []governance.Rule{
		{Tool: tool.ShellToolName, Pattern: "go test*", Effect: governance.Allow},
		{Tool: shellSystemTempToolName, Effect: governance.Allow},
	}, call("system", "go test ./...", "system"), runner)
	if result.IsError || runner.command == "" || runner.scope != tool.TemporaryScopeSystem {
		t.Fatalf("system Shell call = result:%+v command:%q scope:%q, want independently authorized execution", result, runner.command, runner.scope)
	}
}
