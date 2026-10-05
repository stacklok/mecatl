package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestVisibleReasoning_Scenario1_ExplicitCapabilitiesWin(t *testing.T) {
	for _, tc := range []struct{ name, model, capabilities, mode, effort string }{
		{"unsupported", "claude-opus-5-5", `,"capabilities":{"thinking":{"supported":false}}`, "", ""},
		{"unsupported overrides types", "claude-opus-5-5", `,"capabilities":{"thinking":{"supported":false,"types":{"adaptive":{"supported":true},"enabled":{"supported":true}}}}`, "", ""},
		{"adaptive", "claude-sonnet-4-5", `,"capabilities":{"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}}}`, "adaptive", "high"},
		{"manual", "claude-opus-5-5", `,"capabilities":{"thinking":{"supported":true,"types":{"enabled":{"supported":true}}}}`, "enabled", "high"},
		{"older manual", "claude-sonnet-4-5", `,"capabilities":{"thinking":{"supported":true,"types":{"enabled":{"supported":true}}}}`, "enabled", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exerciseAnthropicThinkingComposition(t, tc.model, tc.capabilities, tc.mode, tc.effort, true)
		})
	}
}

func TestVisibleReasoning_Scenario2_LivePrecedenceAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, model, capabilities, mode, effort string
		known                                   bool
	}{
		{"claude 5 sparse", "claude-opus-5-5", "", "adaptive", "high", false},
		{"claude 5 manual live", "claude-opus-5-5", `,"capabilities":{"thinking":{"supported":true,"types":{"enabled":{"supported":true}}}}`, "enabled", "high", true},
		{"claude 5 unsupported live", "claude-opus-5-5", `,"capabilities":{"thinking":{"supported":false,"types":{"adaptive":{"supported":true}}}}`, "", "", true},
		{"older manual live", "claude-sonnet-4-5", `,"capabilities":{"thinking":{"supported":true,"types":{"enabled":{"supported":true}}}}`, "enabled", "high", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exerciseAnthropicThinkingComposition(t, tc.model, tc.capabilities, tc.mode, tc.effort, tc.known)
		})
	}
}

func exerciseAnthropicThinkingComposition(t *testing.T, model, capabilities, wantMode, wantEffort string, wantKnown bool) {
	t.Helper()
	const key = "fixture-key"
	wire := make(chan map[string]any, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != key {
			t.Error("upstream did not receive the configured credential")
		}
		switch r.URL.Path {
		case "/v1/models":
			if r.Method != http.MethodGet {
				t.Errorf("listing method = %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"`+model+`","type":"model","display_name":"Fixture","created_at":"2026-01-01T00:00:00Z"`+capabilities+`}],"has_more":false}`)
		case "/v1/messages":
			if r.Method != http.MethodPost {
				t.Errorf("request method = %s", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			select {
			case wire <- body:
			default: // Later title requests must not block the stream.
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`+"\n\n"+
				"event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`+"\n\n"+
				"event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"visible summary"}}`+"\n\n"+
				"event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n"+
				"event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
				"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	cfg := Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true,
		DefaultProvider: "gateway", DefaultModel: model, ReasoningEffort: "high", LLMMaxAttempts: 1,
		ProviderDefinitions:   permconfig.ProviderDefinitions{"gateway": {ID: "gateway", BaseURL: upstream.URL, DefaultModel: model, APIFlavor: "anthropic-messages", Auth: permconfig.ProviderAuth{Method: "api_key"}}},
		CustomProviderAPIKeys: map[string]string{"gateway": key}, envDetector: fakeEnv(nil),
		liveModelHTTPClient: upstream.Client(), liveModelRefreshSync: true,
	}
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	models := built.Service.ListModels(context.Background())
	found := false
	for _, m := range models {
		if m.GetProviderId() == "gateway" && m.GetId() == model {
			found = true
			if m.GetReasoning() != (wantKnown && wantMode != "") {
				t.Errorf("inventory reasoning = %v, want %v", m.GetReasoning(), wantKnown && wantMode != "")
			}
		}
	}
	if !found {
		t.Fatal("authenticated listing did not reach model inventory")
	}
	sess, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: "gateway", ModelID: model, ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if got := built.Service.ResolvedModel(sess.ID).ReasoningEffort; got != wantEffort {
		t.Errorf("session effort echo = %q, want %q", got, wantEffort)
	}
	relay := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer relay.Close()
	seen := false
	promptOverHTTP(t, relay.URL, string(sess.ID), "hello", func(ev sseEvent) {
		if ev.Type == "reasoning.delta" {
			if ev.Text != "visible summary" {
				t.Errorf("reasoning delta text = %q", ev.Text)
			}
			seen = true
		}
	})
	if !seen {
		t.Error("stream did not project thinking delta as reasoning.delta")
	}
	var request map[string]any
	select {
	case request = <-wire:
	default:
		t.Fatal("no provider request")
	}
	if request["model"] != model {
		t.Errorf("request model = %v, want %s", request["model"], model)
	}
	thinking, _ := request["thinking"].(map[string]any)
	if wantMode == "" {
		if _, ok := request["thinking"]; ok {
			t.Errorf("unsupported thinking sent: %v", thinking)
		}
	} else if thinking["type"] != wantMode || thinking["display"] != "summarized" {
		t.Errorf("thinking = %v, want %s summarized", thinking, wantMode)
	}
	output, _ := request["output_config"].(map[string]any)
	if wantEffort == "" {
		if output != nil && output["effort"] != nil {
			t.Errorf("unsupported effort sent: %v", output)
		}
	} else if output["effort"] != wantEffort {
		t.Errorf("wire effort = %v, want %s", output, wantEffort)
	}
}
