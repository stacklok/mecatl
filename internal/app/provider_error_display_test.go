package app

import (
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

	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openaioption "github.com/openai/openai-go/v3/option"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
	anthropicprovider "github.com/stacklok/mecatl/provider/anthropic"
	openairesponse "github.com/stacklok/mecatl/provider/openai"
	openaichatprovider "github.com/stacklok/mecatl/provider/openaichat"
)

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
			// Disable SDK retries so request counts measure outer attempts only.
			switch protocol {
			case "responses":
				entry = newOpenAICompatEntry(cfg, providerOpenAI, "synthetic", srv.URL, openairesponse.WithMaxRetries(0))
			case "chat":
				entry = newOpenCodeEntry(cfg, providerOpenCode, "synthetic", srv.URL, openaichatprovider.WithRequestOption(openaioption.WithMaxRetries(0)))
			case "anthropic":
				entry = newAnthropicEntryFor(cfg, providerAnthropic, "synthetic", srv.URL, newLiveMetaStore(), false, anthropicprovider.WithRequestOption(anthropicoption.WithMaxRetries(0)))
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
