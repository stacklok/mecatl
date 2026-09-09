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

// TestCanonicalShellTool_Scenario3_NonPortableASTDoesNotExecute pins the bounded
// sh/dash compatibility diagnostic on every model-facing process path.
func TestCanonicalShellTool_Scenario3_NonPortableASTDoesNotExecute(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
	}{
		{"test clause", "[[ -n x ]]"},
		{"process substitution", "cat <(printf x)"},
		{"array", "a=(x)"},
		{"ansi quote", "printf $'x'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			foreground := &fakeShellRunner{res: tool.CommandResult{Stdout: "executed"}, shell: "/bin/sh"}
			result, err := NewShellTool().Execute(context.Background(), bashCall("foreground", tc.command, 0, false), bashEnvRunner(foreground))
			if err != nil || !result.IsError || !strings.Contains(result.Content, "not portable") {
				t.Fatalf("foreground result = %+v, err = %v; want portability error", result, err)
			}
			if foreground.command != "" {
				t.Fatalf("foreground executed %q", foreground.command)
			}

			temporary := &fakeShellRunner{res: tool.CommandResult{Stdout: "executed"}, shell: "/bin/dash"}
			result, err = NewShellTool().Execute(context.Background(), scopedShellCall("temporary", tc.command, "system"), bashEnvRunner(temporary))
			if err != nil || !result.IsError || !strings.Contains(result.Content, "not portable") {
				t.Fatalf("temporary result = %+v, err = %v; want portability error", result, err)
			}
			if temporary.command != "" {
				t.Fatalf("temporary scope executed %q", temporary.command)
			}

			background := &fakeStreamingRunner{shell: "/bin/sh", started: make(chan struct{})}
			registry := newChildRunRegistry()
			result, err = NewShellTool().(childCapableTool).ExecuteWithParent(context.Background(), bashCall("background", tc.command, 0, true), bashEnvRunner(background), nil, parentCaps{children: registry})
			if err != nil || !result.IsError || !strings.Contains(result.Content, "not portable") {
				t.Fatalf("background result = %+v, err = %v; want portability error", result, err)
			}
			select {
			case <-background.started:
				t.Fatal("background process started")
			case <-time.After(20 * time.Millisecond):
			}
			if got := registry.statusSnapshot(); len(got) != 0 {
				t.Fatalf("background registry = %+v, want no spawned job", got)
			}
		})
	}
}

func TestCanonicalShellTool_Scenario3_BashAndUnknownShellPassThrough(t *testing.T) {
	for _, shell := range []string{"/bin/bash", "/opt/custom-shell"} {
		t.Run(shell, func(t *testing.T) {
			command := "[[ -n x ]] && set -o pipefail; printf $'x'"
			runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}, shell: shell}
			result, err := NewShellTool().Execute(context.Background(), bashCall("pass", command, 0, false), bashEnvRunner(runner))
			if err != nil || result.IsError {
				t.Fatalf("result = %+v, err = %v; want pass-through", result, err)
			}
			if runner.command != command {
				t.Fatalf("runner command = %q, want %q", runner.command, command)
			}
		})
	}
}

func TestCanonicalShellTool_Scenario3_SyntaxContextAndParseFailure(t *testing.T) {
	for _, command := range []string{
		"printf '%s' '[[ x ]] <(x) a=(x) $'\"'",
		"# [[ x ]]\nprintf x",
		"printf \\[[",
		"cat <<'EOF'\n[[ x ]] <(x) a=(x) $'x'\nEOF",
		"[[", // parser failure passes through.
	} {
		runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}, shell: "/bin/sh"}
		result, err := NewShellTool().Execute(context.Background(), bashCall("context", command, 0, false), bashEnvRunner(runner))
		if err != nil || result.IsError {
			t.Fatalf("command %q: result = %+v, err = %v; want pass-through", command, result, err)
		}
		if runner.command != command {
			t.Fatalf("command %q: runner received %q", command, runner.command)
		}
	}
}

func TestCanonicalShellTool_Scenario3_EffectiveCommandBytePreservation(t *testing.T) {
	const command = "printf '  exact\\n' # preserve whitespace"

	t.Run("temporary scope", func(t *testing.T) {
		runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}, shell: "/bin/sh"}
		result := runEffectiveShellCommand(t, scopedShellCall("temporary", "ignored", "system"), runner, command)
		if result.IsError {
			t.Fatalf("result = %+v, want success", result)
		}
		if runner.command != command {
			t.Fatalf("temporary runner command = %q, want post-hook bytes %q", runner.command, command)
		}
		if runner.scope != tool.TemporaryScopeSystem {
			t.Fatalf("temporary runner scope = %q, want system", runner.scope)
		}
	})

	t.Run("background", func(t *testing.T) {
		runner := &fakeStreamingRunner{shell: "/bin/sh", started: make(chan struct{})}
		result := runEffectiveShellCommand(t, bashCall("background", "ignored", 0, true), runner, command)
		if result.IsError {
			t.Fatalf("result = %+v, want started background job", result)
		}
		if runner.command != command {
			t.Fatalf("background runner command = %q, want post-hook bytes %q", runner.command, command)
		}
	})

	for _, tc := range []struct {
		name   string
		call   session.ToolCall
		runner tool.CommandRunner
	}{
		{
			name:   "temporary scope",
			call:   scopedShellCall("nonportable-temporary", "printf safe", "system"),
			runner: &fakeShellRunner{res: tool.CommandResult{Stdout: "must not execute"}, shell: "/bin/sh"},
		},
		{
			name:   "background",
			call:   bashCall("nonportable-background", "printf safe", 0, true),
			runner: &fakeStreamingRunner{shell: "/bin/sh", started: make(chan struct{})},
		},
	} {
		t.Run("mutated non-portable "+tc.name, func(t *testing.T) {
			result := runEffectiveShellCommand(t, tc.call, tc.runner, "[[ -n x ]]")
			if !result.IsError || !strings.Contains(result.Content, "not portable") {
				t.Fatalf("result = %+v, want portability error", result)
			}
			switch runner := tc.runner.(type) {
			case *fakeShellRunner:
				if runner.command != "" {
					t.Fatalf("temporary runner executed %q", runner.command)
				}
			case *fakeStreamingRunner:
				if runner.command != "" {
					t.Fatalf("background runner executed %q", runner.command)
				}
				select {
				case <-runner.started:
					t.Fatal("background process started")
				default:
				}
			}
		})
	}
}

// runEffectiveShellCommand drives the real dispatch and PreToolUse seam. The
// second scripted turn lets a permitted background job reach its runner before
// the parent run's normal cleanup joins it.
func runEffectiveShellCommand(t *testing.T, call session.ToolCall, runner tool.CommandRunner, command string) session.ToolResult {
	t.Helper()
	catalog := tool.NewCatalog()
	catalog.MustRegister(NewShellTool())
	engine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")),
		Catalog: catalog,
		Policy: permpolicy.NewPolicy([]governance.Rule{
			{Tool: tool.ShellToolName, Effect: governance.Allow},
			{Tool: shellSystemTempToolName, Effect: governance.Allow},
		}, nil),
		Hooks: effectiveCommandHook{command: command},
	})
	sess := session.New(session.SessionID("effective-"+string(call.ID)), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{MaxTurns: 2}, time.Now())
	var result session.ToolResult
	for event := range engine.Run(context.Background(), sess, bashEnvRunner(runner), RunRequest{Text: "run"}).Events() {
		if event.Type == session.EvToolResult && event.ToolResult != nil && event.ToolResult.CallID == call.ID {
			result = *event.ToolResult
		}
	}
	return result
}

type effectiveCommandHook struct {
	command string
}

func (h effectiveCommandHook) Run(_ context.Context, event governance.HookEvent) (governance.HookOutcome, error) {
	if event.Phase != governance.PhasePreToolUse {
		return governance.HookOutcome{}, nil
	}
	var args bashArgs
	if err := json.Unmarshal(event.Input, &args); err != nil {
		return governance.HookOutcome{}, err
	}
	args.Command = h.command
	mutated, err := json.Marshal(args)
	if err != nil {
		return governance.HookOutcome{}, err
	}
	return governance.HookOutcome{Mutated: mutated}, nil
}
