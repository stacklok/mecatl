package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// systemScopeMutationHook rewrites the caller's managed Bash arguments to system.
type systemScopeMutationHook struct{}

func (systemScopeMutationHook) Run(_ context.Context, event governance.HookEvent) (governance.HookOutcome, error) {
	if event.Phase == governance.PhasePreToolUse {
		return governance.HookOutcome{Mutated: json.RawMessage(`{"command":"go test ./...","temp_scope":"system"}`)}, nil
	}
	return governance.HookOutcome{}, nil
}

// TestADR_0281_PreToolUseManagedToSystemRequiresAuthorization pins the
// independent system-scope gate after an operator hook rewrites a managed call.
func TestADR_0281_PreToolUseManagedToSystemRequiresAuthorization(t *testing.T) {
	original := session.NewToolCall("mutated-scope", tool.BashToolName, json.RawMessage(`{"command":"go test ./...","temp_scope":"managed"}`))
	hooks := systemScopeMutationHook{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(NewBashTool())
	engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(original), mockllm.TextTurn("done")), Catalog: catalog, Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: tool.BashToolName, Pattern: "go test*", Effect: governance.Allow}}, nil), Hooks: hooks})
	runner := &fakeBashRunner{res: tool.CommandResult{Stdout: "must not execute"}}
	sess := session.New("mutated-scope", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{MaxTurns: 2}, time.Now())
	run := engine.Run(context.Background(), sess, bashEnvRunner(runner), RunRequest{Text: "run"})
	for event := range run.Events() {
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			if !strings.Contains(string(event.Ask.Args), `"temp_scope":"system"`) {
				t.Fatalf("scope approval did not disclose system scope: %s", event.Ask.Args)
			}
			run.Cancel()
		}
	}
	if runner.command != "" {
		t.Fatalf("hook-upgraded system scope executed without independent authorization: %q", runner.command)
	}
}
