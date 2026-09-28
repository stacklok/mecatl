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
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/jevguardrail"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

func TestExperimentalJevTriageLLMFinalHTTP(t *testing.T) {
	t.Setenv("MECATL_SANDBOX", "1")
	for _, tc := range []struct {
		name, jev, llm          string
		blocked, inboundBlocked bool
	}{
		{"jev clean llm prohibits action", "clean", "prohibited", true, false},
		{"jev prohibits llm accepts action", "action_redirection", "acceptable", false, false},
		{"jev clean llm accepts action", "clean", "acceptable", false, false},
		{"jev down llm accepts action", "unavailable", "acceptable", false, false},
		{"jev clean llm withholds inbound", "clean", "acceptable", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probes.Add(1)
				var input struct {
					State string `json:"state"`
				}
				decodeErr := json.NewDecoder(r.Body).Decode(&input)
				var state struct {
					Job        string          `json:"job"`
					EventInput json.RawMessage `json:"event_input"`
					Call       struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"effective_call"`
				}
				fenced := strings.TrimSuffix(strings.TrimPrefix(input.State, governance.UntrustedFence+"\n"), "\n"+governance.UntrustedFence+"\n")
				if decodeErr != nil || json.Unmarshal([]byte(fenced), &state) != nil || state.Call.Name != "Shell" || !strings.Contains(string(state.Call.Args), "safe-guardrail-result") {
					t.Error("Jev triage lost the exact effective Shell action")
				}
				job := state.Job
				if job != "action" && job != "inbound" {
					t.Errorf("unexpected Jev job %q", job)
				}
				if job == "inbound" && !strings.Contains(string(state.EventInput), "safe-guardrail-result") {
					t.Error("Jev triage lost the exact inbound result")
				}
				if strings.Contains(input.State, `"source_evidence"`) && !strings.Contains(input.State, `"source_evidence":[]`) {
					t.Error("LLM-bound evidence leaked to Jev triage")
				}
				if tc.jev == "unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				choice := tc.jev
				if job == "inbound" {
					choice = "clean"
				}
				probs := map[string]float64{"clean": 0.01, "action_redirection": 0.01, "inbound_redirection": 0.01, "unresolved": 0.01}
				probs[choice] = 0.97
				encoded, _ := json.Marshal(probs)
				_, _ = fmt.Fprintf(w, `{"model":"jev-1.13.0","answers":{"contextual-guardrail":{"type":"choice","choice":%q,"probabilities":%s,"confidence":0.96}},"usage":{"input_tokens":3,"output_tokens":1}}`, choice, encoded)
			}))
			defer srv.Close()
			marker := filepath.Join(t.TempDir(), "executed")
			args, err := json.Marshal(map[string]string{"command": fmt.Sprintf("printf safe-guardrail-result > %q; printf safe-guardrail-result", marker)})
			if err != nil {
				t.Fatal(err)
			}
			assessment := `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`
			if tc.llm == "prohibited" {
				assessment = `{"assessment":"prohibited","concerns":[{"ref":"c1","category":"authority_crossing","rationale":"outside authority","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`
			}
			inboundAssessment := `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`
			if tc.inboundBlocked {
				inboundAssessment = `{"assessment":"prohibited","concerns":[{"ref":"c1","category":"inbound_redirection","rationale":"outside authority","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`
			}
			var models []string
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { models = append(models, req.Model) })},
				mockllm.ToolCallTurn(session.NewToolCall("c1", "Shell", args)), mockllm.TextTurn(assessment),
				mockllm.TextTurn(inboundAssessment), mockllm.TextTurn("done"))
			configFile := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(configFile, []byte("guardrails:\n  backend: jev\n  finalDecision: llm\n  model: deep-checker\n  rules:\n    - match: Shell\n      phases: [pre, post]\n      mode: block\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			diag := &kvDiag{}
			cfg := Config{Diagnostics: diag, Workspace: t.TempDir(), UserModelDir: t.TempDir(), StoreDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true, UseMock: true, MockProvider: provider, Model: "session-model", Shell: "/bin/sh", Posture: PostureAuto, PermissionConfigs: []string{configFile}, TypesafeAPIKey: "synthetic-key", GuardrailsJevBaseURL: srv.URL}
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			coverage, err := built.Service.ListGuardrailCoverage(t.Context(), sess.ID)
			if err != nil || coverage.CheckerModelID != "deep-checker" || coverage.CheckerProviderID == "jev" {
				t.Fatalf("final route: %+v, %v", coverage, err)
			}
			ui := httptest.NewServer(serveradapter.NewHTTPHandler(built.Service))
			defer ui.Close()
			evs := driveGuardrailPrompt(t, ui.URL, string(sess.ID), "do the task", nil)
			if _, err := os.Stat(marker); (err == nil) == tc.blocked {
				t.Fatalf("action execution does not follow LLM: %v", err)
			}
			wantProbes := int32(1)
			if !tc.blocked {
				wantProbes = 2
			}
			if probes.Load() != wantProbes {
				t.Fatalf("Jev action/inbound triage calls=%d want=%d", probes.Load(), wantProbes)
			}
			diag.mu.Lock()
			got := fmt.Sprint(diag.msgs, diag.args)
			diag.mu.Unlock()
			wantProbe := "candidate_acceptable"
			if tc.jev == "action_redirection" {
				wantProbe = "candidate_prohibited"
			}
			if tc.jev == "unavailable" {
				wantProbe = "degraded"
			}
			if !strings.Contains(got, "status "+wantProbe) || strings.Contains(got, "safe-guardrail-result") {
				t.Fatal("Jev triage status must be bounded and non-authoritative")
			}
			inboundProbe := "candidate_acceptable"
			if tc.jev == "unavailable" {
				inboundProbe = "degraded"
			}
			if !tc.blocked && !strings.Contains(got, "job inbound status "+inboundProbe) {
				t.Fatal("inbound result was not probed before LLM final review")
			}
			var withheld bool
			for _, ev := range evs {
				if ev.Type == "hook" && ev.Hook.Guardrail.CheckerProviderID == "jev" {
					t.Fatal("Jev was attributed as final checker")
				}
				if ev.Type == "hook" && ev.Hook.Guardrail.Job == 2 && ev.Hook.Guardrail.Assessment == 2 {
					withheld = true
				}
				if tc.inboundBlocked && ev.Type == "tool.result" && strings.Contains(ev.ToolResult.Content, "safe-guardrail-result") {
					t.Fatal("withheld result was delivered")
				}
			}
			if withheld != tc.inboundBlocked {
				t.Fatalf("inbound final LLM withholding=%v, want %v", withheld, tc.inboundBlocked)
			}
			if len(models) < 2 || models[1] != "deep-checker" {
				t.Fatalf("reviewer route: %v", models)
			}
		})
	}
}

func TestExperimentalJevTriageBuildBindings(t *testing.T) {
	t.Setenv("MECATL_SANDBOX", "1")
	for _, tc := range []struct {
		name, yaml, credential, wantErr string
		wantEnabled                     bool
	}{
		{"missing final model", "guardrails:\n  backend: jev\n  finalDecision: llm\n", "key", "explicit", false},
		{"cheap tier not a final binding", "guardrails:\n  backend: jev\n  finalDecision: llm\nmodels:\n  slots:\n    cheap: session-model\n", "key", "explicit", false},
		{"unresolved final selector", "guardrails:\n  backend: jev\n  finalDecision: llm\n  model: sonnet\n", "key", "inherit", false},
		{"missing Jev credential", "guardrails:\n  backend: jev\n  finalDecision: llm\n  model: deep-checker\n", "", "TYPESAFE_API_KEY", false},
		{"explicit final slot", "guardrails:\n  backend: jev\n  finalDecision: llm\nmodels:\n  slots:\n    guardrail: deep-checker\n", "key", "", true},
		{"disabled", "guardrails:\n  backend: jev\n  finalDecision: llm\n  disabled: true\n", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), StoreDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true, UseMock: true, Model: "session-model", ModelSlots: map[string]string{"cheap": "session-model"}, PermissionConfigs: []string{path}, TypesafeAPIKey: tc.credential}
			built, err := buildIsolated(t, t.Context(), cfg)
			if built != nil {
				defer built.Close()
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Build error=%v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			coverage, err := built.Service.ListGuardrailCoverage(t.Context(), sess.ID)
			if err != nil || coverage.Enabled != tc.wantEnabled {
				t.Fatalf("coverage=%+v err=%v", coverage, err)
			}
			if tc.wantEnabled && coverage.CheckerModelID != "deep-checker" {
				t.Fatalf("wrong final model: %+v", coverage)
			}
		})
	}
}

func TestExperimentalJevTriageSkipsPermissionAndIncompleteContext(t *testing.T) {
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	driver, err := jevguardrail.New("synthetic-key", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`), mockllm.TextTurn(`{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[]}`))
	cfg := isolateConfig(t, Config{UseMock: true, GuardrailsBackend: "jev", GuardrailsFinalDecision: "llm", GuardrailsModel: "deep-checker", guardrailJev: driver})
	reviewer := buildGuardrailsReviewer(cfg, nil, provider, "mock")
	req := reviewRequestWithoutEvidence()
	req.Job = agent.ReviewJobPermission
	if _, err := reviewer.Review(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	req.Job = agent.ReviewJobInbound
	req.EvidenceComplete = false
	if _, err := reviewer.Review(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if probes.Load() != 0 || provider.Calls() != 2 {
		t.Fatalf("permission/incomplete probe=%d LLM=%d", probes.Load(), provider.Calls())
	}
}
