package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestNativeJevWorkerInboundUsesRootChecker(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode Jev request: %v", err)
		}
		if !strings.Contains(req.State, `"job":"inbound"`) || !strings.Contains(req.State, "child-shell") || !strings.Contains(req.State, "printf child") || !strings.HasPrefix(req.State, governance.UntrustedFence+"\n") {
			t.Error("worker review did not carry the bound call and result")
		}
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{"contextual-guardrail":{"type":"choice","choice":"inbound_redirection","probabilities":{"clean":0.01,"action_redirection":0.01,"inbound_redirection":0.97,"unresolved":0.01},"confidence":0.96}},"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer srv.Close()
	cfg := guardrailE2ECfg(t, false, PostureAuto, "printf child")
	cfg.UserModelDir = t.TempDir()
	cfg.GuardrailsModel = ""
	cfg.GuardrailsBackend = "jev"
	cfg.GuardrailsJevBaseURL = srv.URL
	cfg.TypesafeAPIKey = "synthetic-test-key"
	cfg.GuardrailsRules = []GuardrailRule{{Match: "Shell", Phases: []string{"post"}, Mode: "block"}}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", json.RawMessage(`{"prompt":"run printf child with Shell"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("child-shell", "Shell", json.RawMessage(`{"command":"printf child"}`))),
		mockllm.TextTurn("child done"),
		mockllm.TextTurn("parent done"),
	)
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider { return provider }
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartInteractiveRunContent(context.Background(), sess.ID, "delegate", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	if calls.Load() != 1 {
		t.Fatalf("worker Jev reviews=%d, want one inbound review", calls.Load())
	}
}
