package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	anthropicprovider "github.com/stacklok/mecatl/provider/anthropic"
	openairesponse "github.com/stacklok/mecatl/provider/openai"
	openaichatprovider "github.com/stacklok/mecatl/provider/openaichat"
)

func TestProductionProviderEntriesShareNetworkAttemptObservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"bad_request","message":"rejected"}}`))
	}))
	defer server.Close()

	cfg := Config{LLMMaxAttempts: 1}
	tests := []struct {
		name  string
		model string
		entry func() providerEntry
	}{
		{name: providerOpenAI, model: "gpt-5", entry: func() providerEntry {
			return newOpenAICompatEntry(cfg, providerOpenAI, "test", server.URL, openairesponse.WithHTTPClient(server.Client()))
		}},
		{name: providerOpenRouter, model: "openai/gpt-5", entry: func() providerEntry {
			return newOpenAICompatEntry(cfg, providerOpenRouter, "test", server.URL, openairesponse.WithHTTPClient(server.Client()))
		}},
		{name: providerToolhive, model: "toolhive-model", entry: func() providerEntry {
			return newOpenAICompatEntry(cfg, providerToolhive, "test", server.URL, openairesponse.WithHTTPClient(server.Client()))
		}},
		{name: providerOpenCode, model: "opencode-model", entry: func() providerEntry {
			return newOpenCodeEntry(cfg, providerOpenCode, "test", server.URL, openaichatprovider.WithHTTPClient(server.Client()))
		}},
		{name: providerAnthropic, model: "claude-sonnet-4-5", entry: func() providerEntry {
			return newAnthropicEntryFor(cfg, providerAnthropic, "test", server.URL, newLiveMetaStore(), false,
				anthropicprovider.WithRequestOption(anthropicoption.WithHTTPClient(server.Client())))
		}},
		{name: providerToolhiveAnthropic, model: "claude-sonnet-4-6", entry: func() providerEntry {
			return newAnthropicEntryFor(cfg, providerToolhiveAnthropic, "test", server.URL, newLiveMetaStore(), false,
				anthropicprovider.WithRequestOption(anthropicoption.WithHTTPClient(server.Client())))
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var observations []session.NetworkAttemptPayload
			ctx := port.WithAttemptObserver(
				port.WithTurnIndex(port.WithRunSerial(port.WithSessionID(context.Background(), "provider-coverage"), 9), 2),
				func(observation session.NetworkAttemptPayload) { observations = append(observations, observation) },
			)
			seq, err := test.entry().provider.Stream(ctx, port.LLMRequest{
				Model: test.model, Messages: []session.Message{session.NewUserMessage("hello")},
			})
			if err == nil && seq != nil {
				for _, streamErr := range seq {
					if streamErr != nil {
						err = streamErr
						break
					}
				}
			}
			if err == nil {
				t.Fatal("provider error = nil, test did not exercise the failed-attempt path")
			}
			if len(observations) != 1 {
				t.Fatalf("network observations = %d, want 1", len(observations))
			}
			got := observations[0]
			if got.SessionID != "provider-coverage" || got.RunSerial != 9 || got.Turn != 2 ||
				got.Attempt != 1 || got.MaxAttempts != 1 || got.Decision != "terminal" ||
				got.SuppressionReason != "permanent" || got.HTTPStatus != http.StatusBadRequest {
				t.Fatalf("network observation = %+v", got)
			}
		})
	}
}
