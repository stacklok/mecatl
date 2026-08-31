package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

func TestDelegationGuidanceLandsInBuiltEnginePrompt(t *testing.T) {
	const (
		subagentGuidance = "Use Subagent for focused delegation."
		parallelGuidance = "Use Parallel only for isolated writable or competing branches that need built-in join or winner selection."
		teamGuidance     = "Use Team only for workers that must coordinate through shared tasks or messages over multiple rounds."
	)

	for _, tc := range []struct {
		name            string
		parallel, teams bool
	}{
		{"neither", false, false},
		{"parallel only", true, false},
		{"teams only", false, true},
		{"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := captureBuiltDelegationPrompt(t, tc.parallel, tc.teams).StablePrefix
			if !strings.Contains(prefix, subagentGuidance) {
				t.Errorf("StablePrefix omits Subagent guidance")
			}
			for _, guidance := range []struct {
				text    string
				enabled bool
			}{
				{parallelGuidance, tc.parallel},
				{teamGuidance, tc.teams},
			} {
				if strings.Contains(prefix, guidance.text) != guidance.enabled {
					t.Errorf("StablePrefix guidance %q present = %t, want %t", guidance.text, strings.Contains(prefix, guidance.text), guidance.enabled)
				}
			}
		})
	}
}

func captureBuiltDelegationPrompt(t *testing.T, parallel, teams bool) prompt.Layered {
	t.Helper()

	var captured prompt.Layered
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req.System }),
	}, mockllm.TextTurn("done"))
	workspace := t.TempDir()
	built, err := Build(context.Background(), Config{
		Workspace:      workspace,
		Model:          "mock",
		MockProvider:   provider,
		EnableParallel: parallel,
		EnableTeams:    teams,
		Diagnostics:    port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRunContent(context.Background(), sess.ID, "delegate the work", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	for range run.Events() {
	}
	if captured.StablePrefix == "" {
		t.Fatal("provider did not receive a built system prompt")
	}
	return captured
}
