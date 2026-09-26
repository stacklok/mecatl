package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

func TestNativeJevOverLimitWarnStillWithholds(t *testing.T) {
	t.Setenv("MECATL_SANDBOX", "1")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"contextual-guardrail":{"type":"choice","choice":"clean","probabilities":{"clean":0.97,"action_redirection":0.01,"inbound_redirection":0.01,"unresolved":0.01},"confidence":0.96}},"usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()
	cfg := Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), NoSoul: true, StoreDir: t.TempDir(), MemoryDir: t.TempDir(),
		Model: "test-model", Shell: "/bin/sh", Posture: PostureAuto, UseMock: true,
		GuardrailsBackend: "jev", GuardrailsJevBaseURL: srv.URL, TypesafeAPIKey: "synthetic-test-key",
		GuardrailsOnCheckerDown: "warn",
		GuardrailsRules:         []GuardrailRule{{Match: "Shell", Phases: []string{"pre", "post"}, Mode: "block"}},
		MockProvider: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("c1", "Shell", json.RawMessage(`{"command":"printf '%017000d' 0"}`))),
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
	ui := httptest.NewServer(serveradapter.NewHTTPHandler(built.Service))
	defer ui.Close()
	evs := driveGuardrailPrompt(t, ui.URL, string(sess.ID), "Print digits for the test.", nil)
	var withheld bool
	for _, ev := range evs {
		if ev.Type == "tool.result" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "guardrail") {
			withheld = true
		}
	}
	if !withheld || calls.Load() != 1 {
		t.Fatalf("oversized inbound result bypassed enforcing review: withheld=%v Jev calls=%d", withheld, calls.Load())
	}
}
