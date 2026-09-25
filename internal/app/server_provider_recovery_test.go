package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openaichatoption "github.com/openai/openai-go/v3/option"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/provider/anthropic"
	"github.com/stacklok/mecatl/provider/openai"
	"github.com/stacklok/mecatl/provider/openaichat"
)

// TestServerProviderRecovery_Scenario2_RetryAfterParsingAndSecrecy proves the
// provider adapters expose only normalized retry timing from a single HTTP
// Retry-After value, without projecting the raw header into the terminal error.
func TestServerProviderRecovery_Scenario2_RetryAfterParsingAndSecrecy(t *testing.T) {
	horizon := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	cases := []struct {
		name    string
		values  []string
		valid   bool
		horizon bool
	}{
		{name: "delta", values: []string{"7"}, valid: true},
		{name: "http date", values: []string{time.Now().Add(7 * time.Second).UTC().Format(http.TimeFormat)}, valid: true},
		{name: "past date", values: []string{time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)}, valid: true},
		{name: "horizon", values: []string{"9223372037"}, valid: true, horizon: true},
		{name: "malformed", values: []string{"retry-after-secret"}},
		{name: "negative", values: []string{"-1"}},
		{name: "multiple", values: []string{"retry-after-first", "retry-after-second"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				for _, value := range tc.values {
					w.Header().Add("Retry-After", value)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"limited"}}`)
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
					if tc.valid && !tc.horizon && (at.Before(before) || at.After(time.Now().Add(8*time.Second))) {
						t.Fatalf("RetryNotBefore() = %v, want a normalized near-term time", at)
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

// TestServerProviderRecovery_Scenario2_OuterAttemptsAndProviderRepairAccounting
// proves composition gives each inference constructor to the outer resilience
// wrapper as exactly one physical SDK request per outer attempt.
func TestServerProviderRecovery_Scenario2_OuterAttemptsAndProviderRepairAccounting(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"unavailable"}}`)
	}))
	defer srv.Close()

	cfg := Config{LLMMaxAttempts: 1}
	entries := []struct {
		name  string
		entry providerEntry
	}{
		{"responses", newOpenAICompatEntry(cfg, providerOpenAI, "test", srv.URL+"/v1")},
		{"chat", newOpenCodeEntry(cfg, providerOpenCode, "test", srv.URL+"/v1")},
		{"anthropic", newAnthropicEntryFor(cfg, providerAnthropic, "test", srv.URL+"/v1", &liveMetaStore{}, false)},
	}
	for _, tc := range entries {
		t.Run(tc.name, func(t *testing.T) {
			before := hits.Load()
			_ = providerStreamError(t, tc.entry.provider)
			if got := hits.Load() - before; got != 1 {
				t.Fatalf("physical request count = %d, want 1 for one outer attempt", got)
			}
		})
	}
}

func providerStreamError(t *testing.T, provider port.LLMProvider) error {
	t.Helper()
	seq, err := provider.Stream(context.Background(), port.LLMRequest{
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
