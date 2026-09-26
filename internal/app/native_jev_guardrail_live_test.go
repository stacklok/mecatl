package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

// TestNativeJevLiveDriver is a manually invoked, billable smoke test. Ordinary
// go test skips without the explicit token-file path; no key enters a log or fixture.
func TestNativeJevLiveDriver(t *testing.T) {
	path := os.Getenv("MECATL_JEV_TEST_KEY_FILE")
	if path == "" {
		t.Skip("live TypeSafe test requires MECATL_JEV_TEST_KEY_FILE")
	}
	key, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(key)) == "" {
		t.Fatal("live TypeSafe token file is unavailable")
	}
	t.Setenv("MECATL_SANDBOX", "1")
	cfg := Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), NoSoul: true, StoreDir: t.TempDir(), MemoryDir: t.TempDir(),
		Model: "test-model", Shell: "/bin/sh", Posture: PostureAuto, UseMock: true,
		GuardrailsBackend: "jev", TypesafeAPIKey: strings.TrimSpace(string(key)),
		GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre", "post"}, Mode: "block"}},
		MockProvider: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("c1", "Shell", json.RawMessage(`{"command":"printf synthetic-smoke-result"}`))),
			mockllm.TextTurn("done"),
		),
	}
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := built.Service.ListGuardrailCoverage(context.Background(), sess.ID)
	if err != nil || !coverage.Enabled || coverage.CheckerProviderID != "jev" {
		t.Fatalf("native checker not composed: enabled=%t provider=%q err=%v", coverage.Enabled, coverage.CheckerProviderID, err)
	}
	ui := httptest.NewServer(serveradapter.NewHTTPHandler(built.Service))
	defer ui.Close()
	evs := driveGuardrailPrompt(t, ui.URL, string(sess.ID), "Print the harmless synthetic phrase for a smoke test.", nil)
	var resultSeen bool
	for _, ev := range evs {
		if ev.Type == "tool.result" {
			resultSeen = true
			if ev.ToolResult.IsError {
				t.Fatalf("live Jev check did not clear the synthetic tool result: %s", ev.ToolResult.Content)
			}
		}
	}
	if !resultSeen {
		t.Fatal("no reviewed tool result delivered")
	}
}
