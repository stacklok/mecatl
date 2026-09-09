package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestCanonicalShellTool_Scenario3_SystemPromptAndAuthoritySeparation proves
// the real composition path advertises the bounded diagnostic without
// presenting it as an authorization boundary.
func TestCanonicalShellTool_Scenario3_SystemPromptAndAuthoritySeparation(t *testing.T) {
	var description string
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) {
		for _, spec := range request.Tools {
			if spec.Name == tool.ShellToolName {
				description = spec.Description
			}
		}
	})}, mockllm.TextTurn("done"))
	built, err := Build(context.Background(), Config{
		Workspace:    t.TempDir(),
		Model:        "mock",
		MockProvider: provider,
		NoSoul:       true,
		Shell:        "/bin/sh",
		Diagnostics:  port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRunContent(context.Background(), sess.ID, "describe the available tools", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	for range run.Events() {
	}

	for _, want := range []string{"shell:", "sh", "dash", "diagnostic", "permission", "guardrail", "trust", "secret-scrubbing"} {
		if !strings.Contains(description, want) {
			t.Errorf("model-visible Shell description lacks %q: %q", want, description)
		}
	}
}
