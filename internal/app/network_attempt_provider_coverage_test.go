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

func TestADR_0347_Scenario1_SuccessfulProviderFixtures(t *testing.T) {
	tests := []struct {
		name, model, body string
		entry             func(Config, string, *http.Client) providerEntry
	}{
		{
			name: "responses", model: "gpt-5",
			body: "event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}` + "\n\n" +
				"event: response.completed\n" + `data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n",
			entry: func(cfg Config, url string, client *http.Client) providerEntry {
				return newOpenAICompatEntry(cfg, providerOpenAI, "test", url, openairesponse.WithHTTPClient(client))
			},
		},
		{
			name: "chat", model: "chat-model",
			body: `data: {"id":"chat-1","choices":[{"index":0,"finish_reason":null,"delta":{"content":"ok"}}]}` + "\n\n" +
				`data: {"id":"chat-1","choices":[{"index":0,"finish_reason":"stop","delta":{}}]}` + "\n\ndata: [DONE]\n\n",
			entry: func(cfg Config, url string, client *http.Client) providerEntry {
				return newOpenCodeEntry(cfg, providerOpenCode, "test", url, openaichatprovider.WithHTTPClient(client))
			},
		},
		{
			name: "anthropic", model: "claude-test",
			body: "event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
				"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
				"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
				"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n",
			entry: func(cfg Config, url string, client *http.Client) providerEntry {
				return newAnthropicEntryFor(cfg, providerAnthropic, "test", url, newLiveMetaStore(), false,
					anthropicprovider.WithRequestOption(anthropicoption.WithHTTPClient(client)))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			var observations []session.NetworkAttemptPayload
			ctx := port.WithAttemptObserver(context.Background(), func(row session.NetworkAttemptPayload) {
				observations = append(observations, row)
			})
			seq, err := test.entry(Config{LLMMaxAttempts: 1}, server.URL, server.Client()).provider.Stream(ctx, port.LLMRequest{
				Model: test.model, Messages: []session.Message{session.NewUserMessage("hello")},
			})
			if err != nil {
				t.Fatal(err)
			}
			var text string
			for chunk, streamErr := range seq {
				if streamErr != nil {
					t.Fatal(streamErr)
				}
				if chunk.Kind == port.ChunkText {
					text += chunk.Text
				}
			}
			if text != "ok" {
				t.Fatalf("visible text = %q", text)
			}
			if len(observations) != 1 || observations[0].StreamOutcome != "complete" || observations[0].ProviderTerminalObserved == nil || !*observations[0].ProviderTerminalObserved {
				t.Fatalf("structural observations = %+v", observations)
			}
		})
	}
}

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
