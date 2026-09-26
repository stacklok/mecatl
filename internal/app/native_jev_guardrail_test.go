package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

func TestNativeJevOperatorYAMLBuildAndDisabled(t *testing.T) {
	t.Setenv("MECATL_SANDBOX", "1")
	for _, disabled := range []bool{false, true} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"contextual-guardrail":{"type":"choice","choice":"clean","probabilities":{"clean":0.9,"action_redirection":0.03,"inbound_redirection":0.03,"unresolved":0.04},"confidence":0.9}},"usage":{"input_tokens":3,"output_tokens":1}}`))
		}))
		yaml := "guardrails:\n  backend: jev\n  jev:\n    model: jev-1.13.0\n  rules:\n    - match: Shell\n      phases: [pre, post]\n      mode: block\n"
		if disabled {
			yaml += "  disabled: true\n"
		}
		path := filepath.Join(t.TempDir(), "settings.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), NoSoul: true, StoreDir: t.TempDir(), MemoryDir: t.TempDir(), Model: "test-model", UseMock: true, PermissionConfigs: []string{path}}
		cfg.Shell, cfg.Posture, cfg.TypesafeAPIKey, cfg.GuardrailsJevBaseURL = "/bin/sh", PostureAuto, "synthetic-test-key", srv.URL
		cfg.MockProvider = mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("c1", "Shell", json.RawMessage(`{"command":"printf safe-guardrail-result"}`))), mockllm.TextTurn("done"))
		built, err := buildIsolated(t, t.Context(), cfg)
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
		if err != nil {
			built.Close()
			srv.Close()
			t.Fatal(err)
		}
		coverage, err := built.Service.ListGuardrailCoverage(t.Context(), sess.ID)
		if err != nil {
			built.Close()
			srv.Close()
			t.Fatal(err)
		}
		if coverage.Enabled == disabled || !disabled && (coverage.CheckerProviderID != "jev" || coverage.CheckerModelID != "jev-1.13.0") {
			built.Close()
			srv.Close()
			t.Fatalf("incorrect Jev coverage: %+v", coverage)
		}
		ui := httptest.NewServer(serveradapter.NewHTTPHandler(built.Service))
		_ = driveGuardrailPrompt(t, ui.URL, string(sess.ID), "print safe text", nil)
		ui.Close()
		built.Close()
		srv.Close()
		want := int32(2)
		if disabled {
			want = 0
		}
		if calls.Load() != want {
			t.Fatalf("disabled=%v Jev calls=%d, want %d", disabled, calls.Load(), want)
		}
	}
}

func TestNativeJevGuardrailBuildRun(t *testing.T) {
	t.Setenv("MECATL_SANDBOX", "1")
	for _, tc := range []struct {
		name, action, inbound string
		status                int
		blocked               bool
	}{
		{"clean", "clean", "clean", 200, false},
		{"action block", "action_redirection", "clean", 200, true},
		{"inbound hold", "clean", "inbound_redirection", 200, true},
		{"checker down", "clean", "clean", 503, true},
		{"malformed choice", "invalid", "clean", 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var jobs []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/systemone" {
					t.Errorf("jev path %q", r.URL.Path)
				}
				var request struct {
					State string `json:"state"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("request: %v", err)
					return
				}
				job := "action"
				choice := tc.action
				if strings.Contains(request.State, `"job":"inbound"`) {
					job = "inbound"
					choice = tc.inbound
				}
				var trusted struct {
					Facts      []agent.ReviewPrincipalFact `json:"trusted_facts"`
					Caller     agent.ReviewCaller          `json:"caller"`
					EventInput json.RawMessage             `json:"event_input"`
				}
				if err := json.Unmarshal([]byte(request.State), &trusted); err != nil || len(trusted.Facts) == 0 || trusted.Caller.Role == "" {
					t.Errorf("missing trusted facts or caller: count=%d role=%q err=%v", len(trusted.Facts), trusted.Caller.Role, err)
				}
				if len(trusted.EventInput) == 0 || job == "inbound" && !strings.Contains(string(trusted.EventInput), "safe-guardrail-result") {
					t.Errorf("missing complete %s event input", job)
				}
				if !strings.Contains(request.State, `"effective_call"`) || !strings.Contains(request.State, "safe-guardrail-result") && job == "inbound" {
					t.Errorf("missing full %s payload", job)
				}
				mu.Lock()
				jobs = append(jobs, job)
				mu.Unlock()
				if tc.status != 200 {
					w.WriteHeader(tc.status)
					return
				}
				probabilities := map[string]float64{"clean": 0.01, "action_redirection": 0.01, "inbound_redirection": 0.01, "unresolved": 0.01}
				probabilities[choice] = 0.97
				encoded, err := json.Marshal(probabilities)
				if err != nil {
					t.Errorf("encode probabilities: %v", err)
					return
				}
				_, _ = fmt.Fprintf(w, `{"model":"jev-1.13.0","answers":{"contextual-guardrail":{"type":"choice","choice":%q,"probabilities":%s,"confidence":0.96}},"usage":{"input_tokens":3,"output_tokens":1}}`, choice, encoded)
			}))
			defer srv.Close()
			cfg := Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), NoSoul: true, StoreDir: t.TempDir(), MemoryDir: t.TempDir(), Model: "test-model", Shell: "/bin/sh", Posture: PostureAuto,
				GuardrailsBackend: "jev", GuardrailsJevBaseURL: srv.URL, TypesafeAPIKey: "synthetic-test-key",
				GuardrailsRules: []GuardrailRule{{Match: "Shell", Phases: []string{"pre", "post"}, Mode: "block"}},
				envDetector:     fakeEnv(map[string]string{"OPENAI_API_KEY": "synthetic-provider-key"}), liveModelHTTPClient: offlineHTTPClient(),
				providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
					return mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("c1", "Shell", json.RawMessage(`{"command":"printf safe-guardrail-result"}`))), mockllm.TextTurn("done"))
				},
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
			evs := driveGuardrailPrompt(t, ui.URL, string(sess.ID), "print the benign phrase", nil)
			var blocked bool
			for _, ev := range evs {
				if ev.Type == "permission.ask" {
					t.Fatal("headless ask")
				}
				if ev.Type == "tool.result" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "guardrail") {
					blocked = true
				}
			}
			mu.Lock()
			got := append([]string(nil), jobs...)
			mu.Unlock()
			if len(got) == 0 || got[0] != "action" {
				t.Fatalf("Jev action calls: %v", got)
			}
			if tc.action == "clean" && tc.status == 200 && (len(got) < 2 || got[1] != "inbound") {
				t.Fatalf("Jev inbound calls: %v", got)
			}
			if blocked != tc.blocked {
				t.Fatalf("blocked=%v want=%v jobs=%v events=%v", blocked, tc.blocked, got, evs)
			}
		})
	}
}
