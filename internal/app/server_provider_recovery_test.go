package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	oai "github.com/openai/openai-go/v3"
	openaichatoption "github.com/openai/openai-go/v3/option"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
	"github.com/stacklok/mecatl/provider/anthropic"
	"github.com/stacklok/mecatl/provider/openai"
	"github.com/stacklok/mecatl/provider/openaichat"
)

// TestServerProviderRecovery_Scenario2_RetryAfterParsingAndSecrecy proves the
// provider adapters expose only normalized retry timing from a single HTTP
// Retry-After value, without projecting the raw header into the terminal error.
func TestServerProviderRecovery_Scenario2_RetryAfterParsingAndSecrecy(t *testing.T) {
	horizon := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	cases := []struct {
		name    string
		values  []string
		valid   bool
		horizon bool
		delta   time.Duration
		date    time.Time
	}{
		{name: "delta", values: []string{"7"}, valid: true, delta: 7 * time.Second},
		{name: "http date with comma", values: []string{future.Format(http.TimeFormat)}, valid: true, date: future},
		{name: "past date", values: []string{time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)}, valid: true},
		{name: "horizon", values: []string{"9223372037"}, valid: true, horizon: true},
		{name: "100 digit integer", values: []string{strings.Repeat("9", 100)}, valid: true, horizon: true},
		{name: "huge leading zeros overflow", values: []string{strings.Repeat("0", 100) + strings.Repeat("9", 100)}, valid: true, horizon: true},
		{name: "huge leading zeros small", values: []string{strings.Repeat("0", 100) + "7"}, valid: true, delta: 7 * time.Second},
		{name: "huge leading zeros zero", values: []string{strings.Repeat("0", 100)}, valid: true},
		{name: "multiple numeric", values: []string{"7", "8"}},
		{name: "joined numeric", values: []string{"7, 8"}},
		{name: "joined dates", values: []string{"Wed, 01 Jan 2031 00:00:00 GMT, Wed, 01 Jan 2031 01:00:00 GMT"}},
		{name: "malformed", values: []string{"retry-after-secret"}},
		{name: "overflow with invalid suffix", values: []string{strings.Repeat("9", 100) + "x"}},
		{name: "empty", values: []string{""}},
		{name: "absent"},
		{name: "negative", values: []string{"-1"}},
		{name: "multiple", values: []string{"retry-after-first", "retry-after-second"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				for _, value := range tc.values {
					w.Header().Add("Retry-After", value)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"limited","raw_secret":"body-secret"}}`)
			}))
			defer srv.Close()
			providers := []struct {
				name string
				llm  port.LLMProvider
			}{
				{"responses", openai.New(openai.WithAPIKey("test"), openai.WithBaseURL(srv.URL+"/v1"), openai.WithMaxRetries(0))},
				{"chat", openaichat.New(openaichat.WithAPIKey("test"), openaichat.WithBaseURL(srv.URL+"/v1"), openaichat.WithRequestOption(openaichatoption.WithMaxRetries(0)))},
				{"anthropic", anthropic.New(anthropic.WithAPIKey("test"), anthropic.WithBaseURL(srv.URL+"/v1"), anthropic.WithRequestOption(anthropicoption.WithMaxRetries(0)))},
			}
			for _, provider := range providers {
				t.Run(provider.name, func(t *testing.T) {
					before := time.Now()
					err := providerStreamError(t, provider.llm)
					after := time.Now()
					header := recoverySDKHeader(t, provider.name, err, http.StatusTooManyRequests)
					var metadata interface{ ProviderHTTPStatus() int }
					if !errors.As(err, &metadata) || metadata.ProviderHTTPStatus() != http.StatusTooManyRequests {
						t.Fatal("provider HTTP status metadata not preserved")
					}
					var rawBody interface{ RawJSON() string }
					if !errors.As(err, &rawBody) || !strings.Contains(rawBody.RawJSON(), "body-secret") {
						t.Fatal("SDK cause did not retain the original response body")
					}
					if strings.Contains(err.Error(), "body-secret") {
						t.Fatal("raw response body projected into terminal error")
					}
					var hint interface{ RetryNotBefore() (time.Time, bool) }
					if !errors.As(err, &hint) {
						t.Fatalf("error %T does not expose retry timing", err)
					}
					at, ok := hint.RetryNotBefore()
					if ok != tc.valid {
						t.Fatalf("RetryNotBefore() validity = %v, want %v", ok, tc.valid)
					}
					if tc.horizon && at != horizon {
						t.Fatalf("RetryNotBefore() = %v, want horizon %v", at, horizon)
					}
					if !tc.date.IsZero() && !at.Equal(tc.date) {
						t.Fatalf("HTTP-date = %v, want %v", at, tc.date)
					}
					if tc.valid && !tc.horizon && tc.date.IsZero() && (at.Before(before.Add(tc.delta)) || at.After(after.Add(tc.delta))) {
						t.Fatalf("RetryNotBefore() = %v, want receipt + %v", at, tc.delta)
					}
					if !tc.valid && !at.IsZero() {
						t.Fatalf("invalid hint retained timing: %v", at)
					}
					// Even the unwrap-visible SDK response must not be a live source
					// for normalized metadata after the provider has received it.
					header.Set("Retry-After", "12345")
					for range 3 {
						if again, valid := hint.RetryNotBefore(); again != at || valid != ok {
							t.Fatal("retry hint changed after receipt")
						}
					}
					if tc.horizon {
						beforeHits := hits.Load()
						wrapped := llmresilience.Wrap(provider.llm, llmresilience.Config{
							MaxAttempts: 3, RecoveryBudget: time.Duration(math.MaxInt64),
							Clock: func() time.Time { return time.Date(9900, 1, 1, 0, 0, 0, 0, time.UTC) },
						})
						terminal := providerStreamError(t, wrapped)
						var disposition interface {
							RetryDisposition() session.RetryDisposition
						}
						if !errors.As(terminal, &disposition) || disposition.RetryDisposition() != session.RetryDispositionRetryable {
							t.Fatalf("horizon terminal is not retryable: %v", terminal)
						}
						if got := hits.Load() - beforeHits; got != 1 {
							t.Fatalf("unschedulable hint issued %d calls, want 1", got)
						}
					}
					for _, raw := range tc.values {
						if (tc.name == "malformed" || tc.name == "multiple") && raw != "" && strings.Contains(err.Error(), raw) {
							t.Fatalf("terminal error retained raw Retry-After value: %q", raw)
						}
					}
				})
			}
		})
	}
}

// Each constructor and remint uses outer attempts, while standalone adapters
// retain SDK retries and Responses can still perform its bounded semantic repair.
func TestServerProviderRecovery_Scenario2_OuterAttemptsAndProviderRepairAccounting(t *testing.T) {
	for _, attempts := range []int{1, 2} {
		t.Run(fmt.Sprintf("outer_attempts_%d", attempts), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"code":"server_error","type":"api_error","message":"unavailable"}}`)
			}))
			defer srv.Close()

			cfg := isolateConfig(t, Config{LLMMaxAttempts: attempts})
			entries := []struct {
				name  string
				entry providerEntry
			}{
				{"responses", newOpenAICompatEntry(cfg, providerOpenAI, "test", srv.URL+"/v1", openai.WithMaxRetries(2))},
				{"chat", newOpenCodeEntry(cfg, providerOpenCode, "test", srv.URL+"/v1", openaichat.WithRequestOption(openaichatoption.WithMaxRetries(2)))},
				{"anthropic", newAnthropicEntryFor(cfg, providerAnthropic, "test", srv.URL, &liveMetaStore{}, false, anthropic.WithRequestOption(anthropicoption.WithMaxRetries(2)))},
			}
			for _, tc := range entries {
				t.Run(tc.name, func(t *testing.T) {
					for _, mint := range []struct {
						name string
						llm  port.LLMProvider
					}{
						{"default", tc.entry.provider},
						{"effort", tc.entry.remint("high", tc.entry.provider.Capabilities())},
						{"capabilities", tc.entry.remint("", port.ProviderCapabilities{})},
						{"effort_and_capabilities", tc.entry.remint("high", port.ProviderCapabilities{})},
					} {
						t.Run(mint.name, func(t *testing.T) {
							before := hits.Load()
							err := providerStreamError(t, mint.llm)
							recoverySDKHeader(t, tc.name, err, http.StatusServiceUnavailable)
							if got := hits.Load() - before; got != int32(attempts) {
								t.Fatalf("physical requests = %d, want %d outer calls (SDK retries disabled even after extra options)", got, attempts)
							}
						})
					}
				})
			}
		})
	}
	t.Run("standalone_defaults", testRecoveryStandaloneDefaults)
	t.Run("semantic_repair", testRecoverySemanticRepair)
}

func testRecoveryStandaloneDefaults(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"server_error","type":"api_error","message":"unavailable"}}`)
	}))
	defer srv.Close()
	for _, tc := range []struct {
		name string
		llm  port.LLMProvider
	}{
		{"responses", openai.New(openai.WithAPIKey("test"), openai.WithBaseURL(srv.URL+"/v1"))},
		{"chat", openaichat.New(openaichat.WithAPIKey("test"), openaichat.WithBaseURL(srv.URL+"/v1"))},
		{"anthropic", anthropic.New(anthropic.WithAPIKey("test"), anthropic.WithBaseURL(srv.URL))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := hits.Load()
			err := providerStreamError(t, tc.llm)
			recoverySDKHeader(t, tc.name, err, http.StatusServiceUnavailable)
			if got := hits.Load() - before; got != 3 {
				t.Fatalf("standalone physical requests = %d, want SDK default 3", got)
			}
		})
	}
}

func testRecoverySemanticRepair(t *testing.T) {
	for _, tc := range []struct {
		name                string
		replay, repairFails bool
		wantCalls           int32
	}{
		{"successful_repair", true, false, 2},
		{"failed_repair_vetoes_outer_retry", true, true, 2},
		{"no_replay_no_repair", false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := hits.Add(1)
				var body struct {
					Input []struct {
						Type string `json:"type"`
					} `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				reasoning := 0
				for _, item := range body.Input {
					if item.Type == "reasoning" {
						reasoning++
					}
				}
				wantReasoning := 0
				if call == 1 && tc.replay {
					wantReasoning = 1
				}
				if reasoning != wantReasoning {
					t.Errorf("request %d reasoning items = %d, want %d", call, reasoning, wantReasoning)
				}
				if call == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified or decrypted"}}`)
					return
				}
				if tc.repairFails {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"unavailable"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: response.output_text.delta\n"+
					`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"recovered"}`+"\n\n"+
					"event: response.completed\n"+
					`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
			}))
			defer srv.Close()
			entry := newOpenAICompatEntry(isolateConfig(t, Config{LLMMaxAttempts: 3}), providerOpenAI, "test", srv.URL+"/v1")
			req := port.LLMRequest{Model: "test-model", Messages: []session.Message{session.NewUserMessage("hi")}}
			const replay = `{"v":1,"items":[{"i":"rs_bad","e":"opaque-blob"}]}`
			if tc.replay {
				req.Messages = append(req.Messages, session.NewAssistantMessage("visible history", replay, nil))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			seq, terminal := entry.provider.Stream(ctx, req)
			var text string
			var done bool
			if terminal == nil {
				for chunk, err := range seq {
					if err != nil {
						terminal = err
						break
					}
					if chunk.Kind == port.ChunkText {
						text += chunk.Text
					}
					if chunk.Kind == port.ChunkDone {
						done = true
					}
				}
			}
			if tc.repairFails {
				recoverySDKHeader(t, "responses", terminal, http.StatusServiceUnavailable)
				var veto interface{ Retryable() bool }
				if !errors.As(terminal, &veto) || veto.Retryable() {
					t.Fatalf("failed repair lost explicit retry veto: %v", terminal)
				}
				var hint interface{ RetryNotBefore() (time.Time, bool) }
				if !errors.As(terminal, &hint) {
					t.Fatal("failed repair lost retry hint")
				}
				if _, valid := hint.RetryNotBefore(); !valid {
					t.Fatal("failed repair lost valid hint")
				}
			} else if !tc.replay {
				recoverySDKHeader(t, "responses", terminal, http.StatusBadRequest)
			} else if terminal != nil || text != "recovered" || !done {
				t.Fatalf("repair result = %q, done=%v, error=%v", text, done, terminal)
			}
			if got := hits.Load(); got != tc.wantCalls {
				t.Fatalf("physical requests = %d, want %d", got, tc.wantCalls)
			}
			if tc.replay && req.Messages[1].Reasoning != replay {
				t.Fatal("repair mutated caller replay history")
			}
		})
	}
}

func recoverySDKHeader(t *testing.T, provider string, err error, status int) http.Header {
	t.Helper()
	if provider == "anthropic" {
		var sdkErr *anthropicsdk.Error
		if !errors.As(err, &sdkErr) || sdkErr.StatusCode != status || sdkErr.Response == nil {
			t.Fatalf("Anthropic SDK cause/status not preserved: %v", err)
		}
		return sdkErr.Response.Header
	}
	var sdkErr *oai.Error
	if !errors.As(err, &sdkErr) || sdkErr.StatusCode != status || sdkErr.Response == nil {
		t.Fatalf("OpenAI SDK cause/status not preserved: %v", err)
	}
	return sdkErr.Response.Header
}

func providerStreamError(t *testing.T, provider port.LLMProvider) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	seq, err := provider.Stream(ctx, port.LLMRequest{
		Model: "test-model", Messages: []session.Message{session.NewUserMessage("hi")},
	})
	if err != nil {
		return err
	}
	for _, err := range seq {
		if err != nil {
			return err
		}
	}
	t.Fatal("Stream completed without an error")
	return nil
}
