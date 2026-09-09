package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
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

func TestCanonicalShellTool_Scenario3_SystemPromptAndAuthoritySeparation(t *testing.T) {
	var description string
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) {
		for _, spec := range request.Tools {
			if spec.Name == tool.ShellToolName {
				description = spec.Description
			}
		}
	})}, mockllm.TextTurn("done"))
	catalog := tool.NewCatalog()
	catalog.MustRegister(NewShellTool())
	engine := NewEngine(Deps{
		LLM:     llm,
		Catalog: catalog,
		Policy:  permpolicy.NewPolicy([]governance.Rule{{Tool: tool.ShellToolName, Effect: governance.Allow}}, nil),
	})
	sess := session.New("prompt", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{MaxTurns: 1}, time.Now())
	for range engine.Run(context.Background(), sess, bashEnvRunner(&fakeShellRunner{shell: "/bin/sh"}), RunRequest{Text: "describe the available tools"}).Events() {
	}
	for _, want := range []string{"shell:", "sh", "dash", "diagnostic", "permission"} {
		if !strings.Contains(description, want) {
			t.Fatalf("model-visible Shell description lacks %q: %q", want, description)
		}
	}
}

func TestCanonicalShellTool_Scenario3_EffectiveCommandBytePreservation(t *testing.T) {
	command := "printf '  exact\\n' # preserve whitespace"
	runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}, shell: "/bin/sh"}
	catalog := tool.NewCatalog()
	catalog.MustRegister(NewShellTool())
	engine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(bashCall("exact", "ignored", 0, false)), mockllm.TextTurn("done")),
		Catalog: catalog,
		Policy:  permpolicy.NewPolicy([]governance.Rule{{Tool: tool.ShellToolName, Effect: governance.Allow}}, nil),
		Hooks:   effectiveCommandHook{command: command},
	})
	sess := session.New("effective", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{MaxTurns: 2}, time.Now())
	for range engine.Run(context.Background(), sess, bashEnvRunner(runner), RunRequest{Text: "run"}).Events() {
	}
	if runner.command != command {
		t.Fatalf("runner command = %q, want post-hook bytes %q", runner.command, command)
	}
}

type effectiveCommandHook struct {
	command string
}

func (h effectiveCommandHook) Run(_ context.Context, event governance.HookEvent) (governance.HookOutcome, error) {
	if event.Phase != governance.PhasePreToolUse {
		return governance.HookOutcome{}, nil
	}
	return governance.HookOutcome{Mutated: json.RawMessage(`{"command":` + strconv.Quote(h.command) + `}`)}, nil
}
