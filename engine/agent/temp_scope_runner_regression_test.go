package agent

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// unscopedShellRunner deliberately lacks CommandTemporaryScopeRunner.
type unscopedShellRunner struct {
	res     tool.CommandResult
	command string
}

func (r *unscopedShellRunner) Run(_ context.Context, command string) (tool.CommandResult, error) {
	r.command = command
	return r.res, nil
}

// TestADR_0281_ShellScopeRequiresScopedRunner pins that the advertised managed
// default cannot silently degrade to an unscoped command runner.
func TestADR_0281_ShellScopeRequiresScopedRunner(t *testing.T) {
	runner := &unscopedShellRunner{res: tool.CommandResult{Stdout: "unexpected"}}
	result, err := NewShellTool().Execute(context.Background(), scopedShellCall("scope", "echo unexpected", "managed"), shellEnvRunner(runner))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatalf("unscoped runner executed managed Shell: %+v", result)
	}
	if runner.command != "" {
		t.Fatalf("unscoped runner received command %q", runner.command)
	}
}
