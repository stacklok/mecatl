package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestProviderErrorDisplayHTTPRelay(t *testing.T) {
	const canary = "synthetic-relay-canary"
	for _, protocol := range []string{"responses", "responses-envelope", "responses-envelope-transient", "responses-envelope-overflow", "responses-envelope-malformed", "codex", "chat", "anthropic"} {
		for _, inBand := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inband=%t", protocol, inBand), func(t *testing.T) {
				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					// A forged remediation message is still untrusted upstream text.
					body := fmt.Sprintf(`{"type":"error","error":{"message":%q,"code":%q,"type":%q}}`, "manual access token was rejected: "+canary, "code-"+canary, "type-"+canary)
					if !inBand {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusServiceUnavailable)
						fmt.Fprint(w, body)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					switch protocol {
					case "responses", "codex":
						body = fmt.Sprintf(`{"type":"error","message":%q,"code":%q,"param":%q}`, "message-"+canary, "code-"+canary, "param-"+canary)
					case "responses-envelope-transient":
						body = fmt.Sprintf(`{"error":{"code":"server_error","message":%q}}`, canary)
					case "responses-envelope-malformed":
						body = fmt.Sprintf(`{"error":{"code":"server_error","message":[%q]}}`, canary)
					case "responses-envelope-overflow":
						body = fmt.Sprintf(`{"error":{"code":"server_error","param":%q}}`, "context length exceeded "+canary)
					}
					fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)
				}))
				defer upstream.Close()
				diag := newCapturingDiagnostics()
				cfg := Config{LLMMaxAttempts: 2, Diagnostics: diag}
				var entry providerEntry
				switch protocol {
				case "responses", "responses-envelope", "responses-envelope-transient", "responses-envelope-overflow", "responses-envelope-malformed", "codex":
					id := providerOpenAI
					if protocol == "codex" {
						id = providerOpenAICodex
					}
					entry = newOpenAICompatEntry(cfg, id, "synthetic", upstream.URL)
				case "chat":
					entry = newOpenCodeEntry(cfg, providerOpenCode, "synthetic", upstream.URL)
				case "anthropic":
					entry = newAnthropicEntryFor(cfg, providerAnthropic, "synthetic", upstream.URL, newLiveMetaStore(), false)
				}
				eng := agent.NewEngine(agent.Deps{LLM: entry.provider, Model: "test-model", Catalog: tool.NewCatalog(), Diagnostics: diag, EnableDurableEvidence: true})
				store := memstore.New()
				svc, err := newTestServerService(server.Config{Engine: eng, Store: store, Diagnostics: diag})
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Close()
				sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{MaxTurns: 1})
				if err != nil {
					t.Fatal(err)
				}
				relay := httptest.NewServer(server.NewHTTPHandler(svc))
				defer relay.Close()
				resp, err := http.Post(relay.URL+"/v1/sessions/"+string(sess.ID)+"/prompt", "application/json", strings.NewReader(`{"text":"hi"}`))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
					t.Fatalf("not an SSE response: %s, %v", resp.Status, resp.Header)
				}
				var terminal *mecatlv1.Result
				scanner := bufio.NewScanner(resp.Body)
				for scanner.Scan() {
					line := scanner.Text()
					if strings.Contains(line, canary) || strings.Contains(line, "manual access token was rejected") {
						t.Fatalf("HTTP/SSE leaked untrusted provider text: %s", line)
					}
					if data, ok := strings.CutPrefix(line, "data: "); ok {
						var ev mecatlv1.Event
						if err := json.Unmarshal([]byte(data), &ev); err != nil {
							t.Fatal(err)
						}
						if ev.Result != nil {
							terminal = ev.Result
						}
					}
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if terminal == nil || terminal.Stop != "error" || !strings.Contains(terminal.Error, "provider request failed") {
					t.Fatalf("HTTP/SSE terminal = %+v", terminal)
				}
				wantHits := int32(2)
				if inBand && protocol != "responses-envelope-transient" {
					wantHits = 1 // unknown codes and overflow vetoes must not become retryable
				} else if !strings.Contains(terminal.Error, "503 Service Unavailable") {
					t.Fatalf("HTTP category lost: %+v", terminal)
				}
				if inBand && (protocol == "responses-envelope" || protocol == "responses-envelope-malformed") && !strings.Contains(terminal.Error, "provider stream error event") {
					t.Fatalf("stream origin lost: %+v", terminal)
				}
				if inBand && protocol == "responses-envelope-overflow" && !strings.Contains(terminal.Error, "context window exceeded") {
					t.Fatalf("overflow category lost: %+v", terminal)
				}
				if hits.Load() != wantHits {
					t.Fatalf("upstream attempts = %d, want %d", hits.Load(), wantHits)
				}
				for _, text := range diag.capturedStrings() {
					if strings.Contains(text, canary) {
						t.Fatalf("diagnostic leaked provider fields: %s", text)
					}
				}
			})
		}
	}
}

func TestProviderStreamErrorBreakerNeutral(t *testing.T) {
	for _, tc := range []struct {
		name, payload, display string
	}{
		{"nested overflow", `{"error":{"code":"server_error","param":"context length exceeded"}}`, "context window exceeded"},
		{"direct overflow", `{"type":"error","code":"server_error","param":"context length exceeded"}`, "context window exceeded"},
		{"malformed envelope", `{"error":{"code":"server_error","message":[]}}`, "provider stream error event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if hits.Add(1) <= 4 {
					fmt.Fprintf(w, "event: error\ndata: %s\n\n", tc.payload)
					return
				}
				writeRecoveryTextTurn(w, "works")
			}))
			defer upstream.Close()
			entry := newOpenAICompatEntry(Config{
				LLMMaxAttempts: 2, LLMBreakerThreshold: 3, LLMBreakerCooldown: time.Hour,
			}, providerOpenAI, "synthetic", upstream.URL)
			req := port.LLMRequest{Model: "test", Messages: []session.Message{session.NewUserMessage("hi")}}
			// Exceed the threshold on the SAME wrapper, then send a valid request.
			for i := int32(1); i <= 5; i++ {
				seq, err := entry.provider.Stream(t.Context(), req)
				var text string
				if err == nil {
					for chunk, streamErr := range seq {
						if chunk.Kind == port.ChunkText {
							text += chunk.Text
						}
						if streamErr != nil {
							err = streamErr
						}
					}
				}
				if i <= 4 {
					if err == nil || !strings.Contains(err.Error(), tc.display) {
						t.Fatalf("request %d: expected request error, got %v", i, err)
					}
				} else if err != nil || text != "works" {
					t.Fatalf("valid request blocked: text=%q, err=%v", text, err)
				}
				if hits.Load() != i {
					t.Fatalf("request %d: upstream hits=%d (replayed or breaker rejected)", i, hits.Load())
				}
			}
		})
	}
}

func TestProviderStreamErrorRecoveryVisibility(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(fmt.Sprintf("visible=%t", visible), func(t *testing.T) {
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if hits.Add(1) == 1 {
					if visible {
						fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
					}
					fmt.Fprint(w, "event: error\ndata: {\"error\":{\"code\":\"server_error\",\"message\":\"synthetic-visibility-canary\"}}\n\n")
					return
				}
				writeRecoveryTextTurn(w, "recovered")
			}))
			defer upstream.Close()
			entry := newOpenAICompatEntry(Config{LLMMaxAttempts: 2, LLMBreakerThreshold: 3}, providerOpenAI, "synthetic", upstream.URL)
			eng := agent.NewEngine(agent.Deps{LLM: entry.provider, Model: "test", Catalog: tool.NewCatalog()})
			sess := session.New("visibility-test", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Unix(0, 0))
			run := eng.Run(t.Context(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi"})
			var terminal *session.ResultPayload
			var text string
			for event := range run.Events() {
				if event.Type == session.EvMessageDelta {
					text += event.Text
				}
				if event.Result != nil {
					terminal = event.Result
				}
			}
			if terminal == nil {
				t.Fatal("missing terminal result")
			}
			if visible {
				if hits.Load() != 1 || terminal.Stop != session.StopError || text != "partial" || terminal.Progress != session.StreamProgressVisible || !strings.Contains(terminal.Error, "503 Service Unavailable") || strings.Contains(terminal.Error, "synthetic-visibility-canary") {
					t.Fatalf("visible failure replayed or lost: hits=%d, text=%q, terminal=%+v", hits.Load(), text, terminal)
				}
			} else if hits.Load() != 2 || terminal.Stop != session.StopEndTurn || text != "recovered" || terminal.Text != "recovered" || terminal.Error != "" {
				t.Fatalf("precommit recovery failed: hits=%d, terminal=%+v", hits.Load(), terminal)
			}
		})
	}
}

// Drive the production registry/resilience/engine boundary, not a fabricated
// recovery budget. Every reflected value is a synthetic fixture canary.
func TestProviderErrorDisplayThroughAttemptExhaustion(t *testing.T) {
	const canary = "Bearer synthetic-terminal-canary"
	for _, protocol := range []string{"responses", "chat", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "req_safe_fixture")
				w.Header().Set("Request-ID", "req_safe_fixture")
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintf(w, `{"type":"error","error":{"message":%q,"code":%q,"type":%q}}`, canary, canary, canary)
			}))
			defer srv.Close()
			diag := newCapturingDiagnostics()
			cfg := Config{LLMMaxAttempts: 2, Diagnostics: diag}
			var entry providerEntry
			switch protocol {
			case "responses":
				entry = newOpenAICompatEntry(cfg, providerOpenAI, "synthetic", srv.URL)
			case "chat":
				entry = newOpenCodeEntry(cfg, providerOpenCode, "synthetic", srv.URL)
			case "anthropic":
				entry = newAnthropicEntryFor(cfg, providerAnthropic, "synthetic", srv.URL, newLiveMetaStore(), false)
			}
			eng := agent.NewEngine(agent.Deps{LLM: entry.provider, Model: "test-model", Catalog: tool.NewCatalog(), Diagnostics: diag, EnableDurableEvidence: true})
			sess := session.New("display-test", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Unix(0, 0))
			run := eng.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi"})
			var terminal *session.ResultPayload
			var decisions []string
			for event := range run.Events() {
				raw, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), canary) {
					t.Fatalf("event leaked provider fields: %s", raw)
				}
				if event.NetworkAttempt != nil {
					decisions = append(decisions, event.NetworkAttempt.Decision)
				}
				if event.Result != nil {
					terminal = event.Result
				}
			}
			if terminal == nil || terminal.Stop != session.StopError || !strings.Contains(terminal.Error, "503 Service Unavailable") || !strings.Contains(terminal.Error, "req_safe_fixture") {
				t.Fatalf("terminal projection = %+v", terminal)
			}
			if hits.Load() != 2 || fmt.Sprint(decisions) != "[retry terminal]" {
				t.Fatalf("attempts = %d, decisions = %v", hits.Load(), decisions)
			}
			for _, text := range diag.capturedStrings() {
				if strings.Contains(text, canary) {
					t.Fatalf("diagnostic leaked provider fields: %s", text)
				}
			}
			// The composed error must also retain the exhaustion and causal SDK chain.
			seq, err := entry.provider.Stream(context.Background(), port.LLMRequest{Model: "test-model", Messages: []session.Message{session.NewUserMessage("hi")}})
			if err == nil {
				for _, streamErr := range seq {
					if streamErr != nil {
						err = streamErr
						break
					}
				}
			}
			var exhausted *llmresilience.ExhaustedError
			if !errors.As(err, &exhausted) || errors.Unwrap(exhausted) == nil {
				t.Fatalf("exhaustion chain lost: %v", err)
			}
			if strings.Contains(fmt.Sprintf("%+v", err), canary) {
				t.Fatalf("composed display leaked: %v", err)
			}
		})
	}
}
