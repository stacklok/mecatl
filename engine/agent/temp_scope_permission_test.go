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

func scopedShellCall(id, command, scope string) session.ToolCall {
	return session.NewToolCall(session.ToolCallID(id), tool.ShellToolName, []byte(`{"command":`+quoteJSON(command)+`,"temp_scope":`+quoteJSON(scope)+`}`))
}

func quoteJSON(s string) string {
	// The fixed test inputs contain no control characters; this deliberately keeps
	// the test fixture readable rather than duplicating the production parser.
	return `"` + s + `"`
}

func runScopedShell(t *testing.T, rules []governance.Rule, call session.ToolCall, runner *fakeShellRunner) ([]session.Event, session.ToolResult) {
	t.Helper()
	provider := mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done"))
	catalog := tool.NewCatalog()
	catalog.MustRegister(NewShellTool())
	eng := NewEngine(Deps{LLM: provider, Catalog: catalog, Policy: permpolicy.NewPolicy(rules, nil)})
	sess := session.New("scope", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{MaxTurns: 3}, time.Now())
	run := eng.Run(context.Background(), sess, shellEnvRunner(runner), RunRequest{Text: "run"})
	var events []session.Event
	var result session.ToolResult
	asked := false
	for event := range run.Events() {
		events = append(events, event)
		if event.Type == session.EvPermissionAsk && event.Ask != nil && !asked {
			asked = true
			run.Cancel()
		}
		if event.Type == session.EvToolResult && event.ToolResult != nil {
			result = *event.ToolResult
		}
	}
	return events, result
}

// TestADR_0281_ShellManagedScopeDefaultAndValidation pins managed as the enabled
// default and ensures an unrecognised scope never reaches a command runner.
func TestADR_0281_ShellManagedScopeDefaultAndValidation(t *testing.T) {
	t.Parallel()
	rules := []governance.Rule{{Tool: tool.ShellToolName, Effect: governance.Allow}}
	for _, tc := range []struct{ name, args string }{
		{"default", `{"command":"echo default"}`},
		{"managed", `{"command":"echo managed","temp_scope":"managed"}`},
		{"unknown", `{"command":"echo should-not-run","temp_scope":"other"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
			call := session.NewToolCall("c", tool.ShellToolName, []byte(tc.args))
			_, result := runScopedShell(t, rules, call, runner)
			if tc.name == "unknown" {
				if runner.command != "" || !result.IsError || !strings.Contains(result.Content, "temp_scope") {
					t.Fatalf("unknown scope executed or was not rejected: command=%q result=%+v", runner.command, result)
				}
				return
			}
			if runner.command == "" || result.IsError {
				t.Fatalf("%s scope did not execute normally: command=%q result=%+v", tc.name, runner.command, result)
			}
			if runner.scope != tool.TemporaryScopeManaged {
				t.Fatalf("%s scope selected %q, want managed", tc.name, runner.scope)
			}
		})
	}
}

// TestADR_0281_SystemScopeRequiresIndependentCapability pins the AND gate and
// deny dominance for the synthetic capability.
func TestADR_0281_SystemScopeRequiresIndependentCapability(t *testing.T) {
	t.Parallel()
	call := scopedShellCall("c", "go test ./...", "system")
	for _, tc := range []struct {
		name  string
		rules []governance.Rule
		allow bool
	}{
		{"bash alone is insufficient", []governance.Rule{{Tool: "Shell", Pattern: "go test*", Effect: governance.Allow}}, false},
		{"both allows", []governance.Rule{{Tool: "Shell", Pattern: "go test*", Effect: governance.Allow}, {Tool: shellSystemTempToolName, Effect: governance.Allow}}, true},
		{"capability deny dominates", []governance.Rule{{Tool: "Shell", Pattern: "go test*", Effect: governance.Allow}, {Tool: shellSystemTempToolName, Effect: governance.Allow}, {Tool: shellSystemTempToolName, Effect: governance.Deny}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
			_, result := runScopedShell(t, tc.rules, call, runner)
			if got := runner.command != ""; got != tc.allow {
				t.Fatalf("system scope command executed=%t, want %t; result=%+v", got, tc.allow, result)
			}
			if tc.allow && runner.scope != tool.TemporaryScopeSystem {
				t.Fatalf("system scope selected %q, want system", runner.scope)
			}
		})
	}
}

// TestADR_0281_SystemTempCapabilityIsGlobalButNotShellAllow pins that the
// synthetic capability does not itself grant Shell, while its explicit allow is
// independent of the ordinary command pattern.
func TestADR_0281_SystemTempCapabilityIsGlobalButNotShellAllow(t *testing.T) {
	t.Parallel()
	systemCapability := governance.Rule{Tool: shellSystemTempToolName, Effect: governance.Allow}
	for _, tc := range []struct {
		command string
		allow   bool
	}{
		{"go test ./...", true},
		{"rm -rf nowhere", false},
	} {
		t.Run(tc.command, func(t *testing.T) {
			runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
			rules := []governance.Rule{systemCapability}
			if tc.allow {
				rules = append(rules, governance.Rule{Tool: "Shell", Pattern: "go test*", Effect: governance.Allow})
			}
			_, result := runScopedShell(t, rules, scopedShellCall("c", tc.command, "system"), runner)
			if got := runner.command != ""; got != tc.allow {
				t.Fatalf("command executed=%t, want %t; result=%+v", got, tc.allow, result)
			}
		})
	}
}

// TestADR_0281_SystemTempApprovalDoesNotLeakPath pins the approval projection:
// it names system scope and a safe command summary, never runner-owned paths or
// temporary environment values.
func TestADR_0281_SystemTempApprovalDoesNotLeakPath(t *testing.T) {
	t.Parallel()
	runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
	events, _ := runScopedShell(t, []governance.Rule{{Tool: "Shell", Pattern: "go test*", Effect: governance.Allow}}, scopedShellCall("c", "go test ./...", "system"), runner)
	for _, event := range events {
		if event.Type != session.EvPermissionAsk || event.Ask == nil {
			continue
		}
		body := string(event.Ask.Args)
		if !strings.Contains(body, `"temp_scope":"system"`) || !strings.Contains(body, "go test") {
			t.Fatalf("system approval does not disclose scope and command summary: %s", body)
		}
		for _, forbidden := range []string{"/tmp/", "TMPDIR", "GOTMPDIR", "MECATL_TEST_TEMP_LEASE"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("system approval leaks %q: %s", forbidden, body)
			}
		}
		return
	}
	t.Fatal("system scope did not surface the independent capability approval")
}
