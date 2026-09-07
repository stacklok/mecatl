package agent

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// unscopedBashRunner deliberately lacks CommandTemporaryScopeRunner.
type unscopedBashRunner struct {
	res     tool.CommandResult
	command string
}

func (r *unscopedBashRunner) Run(_ context.Context, command string) (tool.CommandResult, error) {
	r.command = command
	return r.res, nil
}

// TestADR_0281_BashScopeRequiresScopedRunner pins that the advertised managed
// default cannot silently degrade to an unscoped command runner.
func TestADR_0281_BashScopeRequiresScopedRunner(t *testing.T) {
	runner := &unscopedBashRunner{res: tool.CommandResult{Stdout: "unexpected"}}
	result, err := NewBashTool().Execute(context.Background(), scopedBashCall("scope", "echo unexpected", "managed"), bashEnvRunner(runner))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatalf("unscoped runner executed managed Bash: %+v", result)
	}
	if runner.command != "" {
		t.Fatalf("unscoped runner received command %q", runner.command)
	}
}
