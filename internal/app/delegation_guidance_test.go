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
	full := captureBuiltDelegationPrompt(t, true, true)
	for _, want := range []string{
		"Use Subagent for focused delegation.",
		"issue one Subagent call per task in the same assistant turn so eligible calls run concurrently",
		"Use Parallel only for isolated writable or competing branches that need built-in join or winner selection.",
		"Use Team only for workers that must coordinate through shared tasks or messages over multiple rounds.",
	} {
		if !strings.Contains(full.StablePrefix, want) {
			t.Errorf("fully built engine StablePrefix missing delegation clause %q", want)
		}
	}

	minimal := captureBuiltDelegationPrompt(t, false, false)
	if !strings.Contains(minimal.StablePrefix, "Use Subagent for focused delegation.") {
		t.Error("default built engine StablePrefix omits Subagent guidance")
	}
	for _, unavailable := range []string{"Use Parallel only", "Use Team only"} {
		if strings.Contains(minimal.StablePrefix, unavailable) {
			t.Errorf("default built engine advertises unavailable tool via %q", unavailable)
		}
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
