package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestVisibleReasoning_Scenario1_SparseListingPreservesOtherFields(t *testing.T) {
	const model = "claude-opus-5-5"
	const key = "fixture-key"
	wire := make(chan map[string]any, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != key {
			t.Error("credential missing from authenticated provider request")
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"`+model+`","type":"model","display_name":"Fixture","created_at":"2026-01-01T00:00:00Z","max_input_tokens":1000000,"max_tokens":64000}],"has_more":false}`)
		case "/v1/messages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			select {
			case wire <- body:
			default: // Async title requests must not block the provider.
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`+"\n\n"+
				"event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`+"\n\n"+
				"event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"signed thought"}}`+"\n\n"+
				"event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG-sparse=="}}`+"\n\n"+
				"event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n"+
				"event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
				"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	cfg := Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), MemoryDir: t.TempDir(), UserModelDir: t.TempDir(), NoSoul: true,
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
	found := false
	for _, m := range built.Service.ListModels(context.Background()) {
		if m.GetProviderId() == "gateway" && m.GetId() == model {
			found = true
			if m.GetReasoning() {
				t.Error("sparse public inventory advertised known reasoning")
			}
		}
	}
	if !found {
		t.Fatal("sparse model missing from inventory")
	}
	sess, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: "gateway", ModelID: model, ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	resolved := built.Service.ResolvedModel(sess.ID)
	if resolved.ModelID != model || resolved.ContextWindow != 1000000 {
		t.Errorf("resolved identity/window = %+v", resolved)
	}
	relay := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer relay.Close()
	for i := range 2 {
		seen := false
		promptOverHTTP(t, relay.URL, string(sess.ID), "hello", func(ev sseEvent) {
			if ev.Type == "reasoning.delta" && ev.Text == "signed thought" {
				seen = true
			}
		})
		if !seen {
			t.Errorf("turn %d missing reasoning.delta", i)
		}
	}
	first, second := <-wire, <-wire
	for i, request := range []map[string]any{first, second} {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), key) {
			t.Errorf("credential leaked into request body on turn %d", i)
		}
		if request["model"] != model {
			t.Errorf("turn %d model = %v", i, request["model"])
		}
		if got := request["max_tokens"]; got != float64(64000) {
			t.Errorf("turn %d max_tokens = %v", i, got)
		}
		if thinking, _ := request["thinking"].(map[string]any); thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
			t.Errorf("turn %d thinking = %v", i, thinking)
		}
	}
	messages, _ := json.Marshal(second["messages"])
	if !strings.Contains(string(messages), `"signature":"SIG-sparse=="`) || !strings.Contains(string(messages), `"thinking":"signed thought"`) {
		t.Errorf("signed reasoning missing from next-request replay: %s", messages)
	}
	if strings.Contains(string(messages), `"text":"signed thought"`) {
		t.Errorf("display summary became replay text block: %s", messages)
	}
}
