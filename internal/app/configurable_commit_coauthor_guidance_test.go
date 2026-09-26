package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const commitCoauthorTrailer = "Co-authored-by: Mecatl <noreply@mecatl.dev>"

// TestConfigurableCommitCoauthorGuidance_Scenario3_MainEngine proves the real
// application composition path gives the root session engine the default
// standard-prompt guidance.
func TestConfigurableCommitCoauthorGuidance_Scenario3_MainEngine(t *testing.T) {
	prefixes := captureCommitCoauthorPrompts(t, mockllm.TextTurn("done"))
	if len(prefixes) != 1 {
		t.Fatalf("captured prompt count = %d, want 1", len(prefixes))
	}
	assertCommitCoauthorGuidance(t, prefixes[0], true)
}

// TestConfigurableCommitCoauthorGuidance_Scenario3_DelegatedEngine proves a
// real default Subagent child receives the same standard-prompt guidance.
func TestConfigurableCommitCoauthorGuidance_Scenario3_DelegatedEngine(t *testing.T) {
	prefixes := captureCommitCoauthorPrompts(t,
		mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"inspect the change"}`))),
		mockllm.TextTurn("child done"),
		mockllm.TextTurn("parent done"),
	)
	if len(prefixes) != 3 {
		t.Fatalf("captured prompt count = %d, want 3", len(prefixes))
	}
	assertCommitCoauthorGuidance(t, prefixes[1], true)
}

func TestConfigurableCommitCoauthorGuidanceCompositionHonorsOperatorSetting(t *testing.T) {
	settings := writeOperatorSettingsFile(t, "system_prompt:\n  commit_coauthor: false\n")
	prefixes := captureCommitCoauthorPromptsWithConfig(t, []string{settings}, mockllm.TextTurn("done"))
	if len(prefixes) != 1 {
		t.Fatalf("captured prompt count = %d, want 1", len(prefixes))
	}
	assertCommitCoauthorGuidance(t, prefixes[0], false)
}

func captureCommitCoauthorPrompts(t *testing.T, turns ...mockllm.Turn) []string {
	return captureCommitCoauthorPromptsWithConfig(t, nil, turns...)
}

func captureCommitCoauthorPromptsWithConfig(t *testing.T, permissionConfigs []string, turns ...mockllm.Turn) []string {
	t.Helper()

	var prefixes []string
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) {
			prefixes = append(prefixes, req.System.StablePrefix)
		}),
	}, turns...)
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:         t.TempDir(),
		Model:             "mock",
		MockProvider:      provider,
		PermissionConfigs: permissionConfigs,
		Diagnostics:       port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRunContent(context.Background(), sess.ID, "do the work", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	for range run.Events() {
	}
	return prefixes
}

func assertCommitCoauthorGuidance(t *testing.T, prefix string, want bool) {
	t.Helper()
	got := strings.Count(prefix, commitCoauthorTrailer)
	if want && got != 1 {
		t.Fatalf("StablePrefix has %d copies of %q, want exactly 1\nprefix=%q", got, commitCoauthorTrailer, prefix)
	}
	if !want && got != 0 {
		t.Fatalf("StablePrefix has %d copies of %q, want none\nprefix=%q", got, commitCoauthorTrailer, prefix)
	}
}
