package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/proto"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

func customProviderConfig() Config {
	return Config{
		ProviderDefinitions: permconfig.ProviderDefinitions{
			"gateway-responses": {
				ID:           "gateway-responses",
				BaseURL:      "https://gateway.example/v1",
				DefaultModel: "gateway-default",
				APIFlavor:    "openai-responses",
				Auth:         permconfig.ProviderAuth{Method: "api_key"},
			},
		},
		CustomProviderAPIKeys: map[string]string{"gateway-responses": "gateway-key"},
	}
}

func assertProtoHides(t *testing.T, message proto.Message, sensitive ...string) {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal client-visible protobuf: %v", err)
	}
	for _, value := range sensitive {
		if bytes.Contains(payload, []byte(value)) {
			t.Errorf("client-visible protobuf leaked %q", value)
		}
	}
}

func sendProviderRequest(t *testing.T, provider port.LLMProvider, model string) {
	t.Helper()
	stream, err := provider.Stream(context.Background(), port.LLMRequest{Model: model})
	if err != nil {
		return // the test server deliberately sends a terminal rejection.
	}
	for _, err := range stream {
		if err != nil {
			return // the test server deliberately sends a terminal rejection.
		}
	}
}

func TestADR_0238_CustomProviderInferenceRefusesRedirects(t *testing.T) {
	for _, tc := range []struct {
		name, flavor, suffix string
	}{
		{name: "responses", flavor: "openai-responses", suffix: "/v1"},
		{name: "chat", flavor: "openai-chat-completions", suffix: "/v1"},
		{name: "anthropic", flavor: "anthropic-messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attackerHits atomic.Int32
			var attackerAuthorization, attackerBody string
			attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attackerHits.Add(1)
				attackerAuthorization = r.Header.Get("Authorization")
				body, _ := io.ReadAll(r.Body)
				attackerBody = string(body)
				http.Error(w, "unexpected redirect target", http.StatusBadRequest)
			}))
			defer attacker.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, attacker.URL, http.StatusTemporaryRedirect)
			}))
			defer proxy.Close()

			cfg := Config{
				LLMMaxAttempts: 1,
				ProviderDefinitions: permconfig.ProviderDefinitions{"gateway": {
					ID: "gateway", BaseURL: proxy.URL + tc.suffix, DefaultModel: "model", APIFlavor: tc.flavor,
					Auth: permconfig.ProviderAuth{Method: "api_key"},
				}},
				CustomProviderAPIKeys: map[string]string{"gateway": "gateway-secret"},
			}
			reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}
			entry, ok := reg.Lookup("gateway")
			if !ok {
				t.Fatal("custom provider was not registered")
			}
			sendProviderRequest(t, entry.provider, "model")
			reg.remintEntry("gateway", "live-model")
			entry, ok = reg.Lookup("gateway")
			if !ok {
				t.Fatal("custom provider disappeared after live-model remint")
			}
			sendProviderRequest(t, entry.provider, "live-model")
			if got := attackerHits.Load(); got != 0 {
				t.Fatalf("redirect target received %d request(s), authorization=%q body=%q; want no credential or request body delivery", got, attackerAuthorization, attackerBody)
			}
		})
	}
}

func TestADR_0238_CustomProviderNoneDoesNotUseAmbientOpenAIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-secret")
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		http.Error(w, "stop", http.StatusBadRequest)
	}))
	defer server.Close()

	cfg := Config{
		LLMMaxAttempts: 1,
		ProviderDefinitions: permconfig.ProviderDefinitions{"anonymous": {
			ID: "anonymous", BaseURL: server.URL + "/v1", DefaultModel: "model", APIFlavor: "openai-responses",
			Auth: permconfig.ProviderAuth{Method: "none"},
		}},
	}
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup("anonymous")
	if !ok {
		t.Fatal("anonymous custom provider was not registered")
	}
	sendProviderRequest(t, entry.provider, "model")
	if authorization != "" {
		t.Fatalf("anonymous custom provider sent ambient authorization %q", authorization)
	}
}

func TestOperatorDefinedLLMProviders_Scenario4_LiveListing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("models path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gateway-key" {
			t.Errorf("Authorization = %q, want resolved API key", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gateway-live"}]}`))
	}))
	defer server.Close()

	cfg := customProviderConfig()
	definition := cfg.ProviderDefinitions["gateway-responses"]
	definition.BaseURL = server.URL + "/v1"
	cfg.ProviderDefinitions[definition.ID] = definition
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}

	models := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, definition.ID)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if !slices.Equal(ids, []string{"gateway-default", "gateway-live"}) {
		t.Errorf("custom inventory = %v, want configured floor plus live listing", ids)
	}

	for _, tc := range []struct {
		name, flavor, defaultModel, body, baseSuffix string
		key                                          string
	}{
		{"chat", "openai-chat-completions", "chat-default", `{"data":[{"id":"chat-live"}]}`, "/v1", ""},
		{"anthropic", "anthropic-messages", "anthropic-default", `{"data":[{"id":"anthropic-live","type":"model","display_name":"Anthropic live","created_at":"2026-01-01T00:00:00Z","max_input_tokens":1,"max_tokens":1,"capabilities":{}}],"has_more":false,"first_id":"anthropic-live","last_id":"anthropic-live"}`, "", "anthropic-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					t.Errorf("models path = %q, want /v1/models", r.URL.Path)
				}
				if tc.flavor == "anthropic-messages" && r.Header.Get("X-Api-Key") != tc.key {
					t.Errorf("Anthropic API key was not resolved")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			cfg := Config{ProviderDefinitions: permconfig.ProviderDefinitions{"gateway": {
				ID: "gateway", BaseURL: server.URL + tc.baseSuffix, DefaultModel: tc.defaultModel, APIFlavor: tc.flavor,
				Auth: permconfig.ProviderAuth{Method: map[bool]string{true: "api_key", false: "none"}[tc.key != ""]},
			}}, CustomProviderAPIKeys: map[string]string{"gateway": tc.key}}
			reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}
			models := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, "gateway")
			if len(models) != 2 || models[0].ID != tc.defaultModel || models[1].ID == "" {
				t.Errorf("inventory = %v, want default floor plus live model", models)
			}
		})
	}
}

func TestInvariant_custom_provider_default_model_inventory_floor(t *testing.T) {
	cfg := customProviderConfig()
	cfg.DefaultProvider = "gateway-responses"
	cfg.DefaultModel = "gateway-default"
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if err := validateDefaultModel(cfg, reg); err != nil {
		t.Fatalf("validateDefaultModel rejected custom inventory floor: %v", err)
	}
	models := modelSnapshot(reg)
	if len(models) != 1 || models[0].GetId() != "gateway-default" || models[0].GetProviderId() != "gateway-responses" {
		t.Errorf("seed inventory = %v, want custom default model", models)
	}
	if got := reg.DefaultModelFor("gateway-responses"); got != "gateway-default" {
		t.Errorf("child/provider remint default = %q, want configured floor", got)
	}
}

func TestOperatorDefinedLLMProviders_Scenario4_ListingFallback(t *testing.T) {
	for _, tc := range []struct {
		name, flavor, wantState string
		code                    int
	}{
		{name: "responses/unreachable", flavor: "openai-responses", code: http.StatusBadGateway, wantState: statusUnreachable},
		{name: "responses/unauthorized", flavor: "openai-responses", code: http.StatusUnauthorized, wantState: statusUnauthorized},
		{name: "chat/unreachable", flavor: "openai-chat-completions", code: http.StatusBadGateway, wantState: statusUnreachable},
		{name: "chat/unauthorized", flavor: "openai-chat-completions", code: http.StatusUnauthorized, wantState: statusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sensitiveBody = "listing body must not reach the picker"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, sensitiveBody, tc.code)
			}))
			defer server.Close()

			cfg := customProviderConfig()
			definition := cfg.ProviderDefinitions["gateway-responses"]
			definition.APIFlavor = tc.flavor
			definition.BaseURL = server.URL + "/v1"
			cfg.ProviderDefinitions[definition.ID] = definition
			cfg.DefaultProvider = definition.ID
			cfg.DefaultModel = definition.DefaultModel
			reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}
			models := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, definition.ID)
			if len(models) != 1 || models[0].ID != definition.DefaultModel {
				t.Errorf("fallback inventory = %v, want selectable configured default model %q", models, definition.DefaultModel)
			}
			if err := validateDefaultModel(cfg, reg); err != nil {
				t.Fatalf("validateDefaultModel rejected configured default after listing failure: %v", err)
			}

			rows := providerStatusProto(reg)
			if len(rows) != 1 {
				t.Fatalf("provider_status = %+v, want one custom-provider row", rows)
			}
			row := rows[0]
			if row.GetProviderId() != definition.ID || row.GetState() != tc.wantState {
				t.Errorf("provider_status row = %+v, want provider=%q state=%q", row, definition.ID, tc.wantState)
			}
			if row.GetHint() != "" || row.GetAvailableNotDefault() || row.GetDefaultModelAutoSelected() || row.GetModelCount() != 0 {
				t.Errorf("custom provider status = %+v, want only its safe failure state", row)
			}
			assertProtoHides(t, row, server.URL, sensitiveBody, cfg.CustomProviderAPIKeys[definition.ID])
		})
	}
}

func TestOperatorDefinedLLMProviders_EmptyListingPreservesFloorAndStatus(t *testing.T) {
	const sensitiveBody = "custom listing response body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"private":"custom listing response body"}`))
	}))
	defer server.Close()

	cfg := customProviderConfig()
	definition := cfg.ProviderDefinitions["gateway-responses"]
	definition.BaseURL = server.URL + "/v1"
	cfg.ProviderDefinitions[definition.ID] = definition
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	models := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, definition.ID)
	if len(models) != 1 || models[0].ID != definition.DefaultModel {
		t.Fatalf("empty listing inventory = %v, want configured floor %q", models, definition.DefaultModel)
	}
	rows := providerStatusProto(reg)
	if len(rows) != 1 || rows[0].GetProviderId() != definition.ID || rows[0].GetState() != statusEmpty || rows[0].GetHint() != "" {
		t.Fatalf("provider_status = %+v, want one safe custom empty row", rows)
	}
	assertProtoHides(t, rows[0], server.URL, sensitiveBody, cfg.CustomProviderAPIKeys[definition.ID])
}

func TestOperatorDefinedLLMProviders_AnthropicListingFailureStatus(t *testing.T) {
	for _, tc := range []struct {
		name, wantState string
		code            int
	}{
		{name: "unauthorized", code: http.StatusUnauthorized, wantState: statusUnauthorized},
		{name: "unreachable", code: http.StatusBadGateway, wantState: statusUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sensitiveBody = "anthropic custom listing response body"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, sensitiveBody, tc.code)
			}))
			defer server.Close()

			cfg := Config{
				ProviderDefinitions: permconfig.ProviderDefinitions{"gateway-anthropic": {
					ID: "gateway-anthropic", BaseURL: server.URL, DefaultModel: "gateway-default", APIFlavor: "anthropic-messages",
					Auth: permconfig.ProviderAuth{Method: "api_key"},
				}},
				CustomProviderAPIKeys: map[string]string{"gateway-anthropic": "anthropic-gateway-secret"},
				liveModelHTTPClient:   server.Client(),
			}
			reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}
			models := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, "gateway-anthropic")
			if len(models) != 1 || models[0].ID != "gateway-default" {
				t.Fatalf("fallback inventory = %v, want custom default floor", models)
			}
			rows := providerStatusProto(reg)
			if len(rows) != 1 || rows[0].GetState() != tc.wantState {
				t.Fatalf("provider_status = %+v, want custom Anthropic %s", rows, tc.wantState)
			}
			assertProtoHides(t, rows[0], server.URL, sensitiveBody, "anthropic-gateway-secret")
		})
	}
}

func TestOperatorDefinedLLMProviders_ListingFailureProjectsOnListModelsWire(t *testing.T) {
	for _, tc := range []struct {
		name, wantState string
		code            int
	}{
		{name: "unreachable", code: http.StatusBadGateway, wantState: statusUnreachable},
		{name: "unauthorized", code: http.StatusUnauthorized, wantState: statusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sensitiveBody = "custom listing response body"
			modelServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, sensitiveBody, tc.code)
			}))
			defer modelServer.Close()

			workspace := t.TempDir()
			operator := writeOperatorSettingsFile(t, "providers:\n  gateway:\n    base_url: "+modelServer.URL+"/v1\n    default_model: gateway-default\n    api_flavor: openai-responses\n    auth:\n      method: api_key\n")
			built, err := Build(context.Background(), Config{
				Workspace:               workspace,
				NoSoul:                  true,
				PermissionsConventional: true,
				permConfigEnv:           isolatedPermConfigEnv(t),
				PermissionConfigs:       []string{operator},
				DefaultProvider:         "gateway",
				DefaultModel:            "gateway-default",
				CustomProviderAPIKeys:   map[string]string{"gateway": "gateway-secret"},
				liveModelHTTPClient:     modelServer.Client(),
				liveModelRefreshSync:    true,
				providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
					return mockllm.New(mockllm.TextTurn("ok"))
				},
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()

			resp, err := serveradapter.NewHarnessServer(built.Service).ListModels(context.Background(), &mecatlv1.ListModelsRequest{})
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if models := resp.GetModels(); len(models) != 1 || models[0].GetProviderId() != "gateway" || models[0].GetId() != "gateway-default" {
				t.Fatalf("models = %+v, want selectable gateway-default floor", models)
			}
			rows := resp.GetProviderStatus()
			if len(rows) != 1 || rows[0].GetProviderId() != "gateway" || rows[0].GetState() != tc.wantState {
				t.Fatalf("provider_status = %+v, want one safe gateway %s row", rows, tc.wantState)
			}
			assertProtoHides(t, resp, modelServer.URL, sensitiveBody, "gateway-secret")
		})
	}
}

func TestInvariant_custom_provider_omitted_live_modalities_use_adapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gateway-default"}]}`))
	}))
	defer server.Close()

	cfg := customProviderConfig()
	definition := cfg.ProviderDefinitions["gateway-responses"]
	definition.BaseURL = server.URL + "/v1"
	cfg.ProviderDefinitions[definition.ID] = definition
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	fresh := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, definition.ID)
	reg.meta.mergeSwap(map[string][]modelEntry{definition.ID: fresh})
	if caps := modelCapability(reg, definition.ID, definition.DefaultModel); !caps.Image || caps.Audio {
		t.Errorf("unknown custom live metadata capabilities = %+v, want adapter image capability", caps)
	}
	if info := projectModelEntry(reg, definition.ID, fresh[0]); info.GetContextLimit() != defaultContextWindowTokens {
		t.Errorf("unknown custom live metadata context limit = %d, want conservative floor %d", info.GetContextLimit(), defaultContextWindowTokens)
	}
}

func TestInvariant_custom_provider_matching_live_model_replaces_floor_metadata(t *testing.T) {
	const (
		model         = "gpt-5.6-terra"
		contextWindow = 1_050_000
	)
	modelServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("models path = %q, want /v1/models", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-terra","display_name":"GPT-5.6 Terra","context_window":1050000}]}`))
	}))
	defer modelServer.Close()

	workspace := t.TempDir()
	operator := writeOperatorSettingsFile(t, "providers:\n  gateway:\n    base_url: "+modelServer.URL+"/v1\n    default_model: "+model+"\n    api_flavor: openai-responses\n    auth:\n      method: api_key\n")
	built, err := Build(context.Background(), Config{
		Workspace:               workspace,
		NoSoul:                  true,
		PermissionsConventional: true,
		permConfigEnv:           isolatedPermConfigEnv(t),
		PermissionConfigs:       []string{operator},
		DefaultProvider:         "gateway",
		DefaultModel:            model,
		CustomProviderAPIKeys:   map[string]string{"gateway": "gateway-key"},
		liveModelHTTPClient:     modelServer.Client(),
		liveModelRefreshSync:    true,
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("ok"))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	models := built.Service.ListModels(context.Background())
	if len(models) != 1 || models[0].GetProviderId() != "gateway" || models[0].GetId() != model || models[0].GetContextLimit() != contextWindow {
		t.Fatalf("listed models = %v, want one gateway/%s row with context limit %d", models, model, contextWindow)
	}

	selected, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, defaultLimits(),
		serveradapter.ProviderSelector{ProviderID: "gateway", ModelID: model})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if got := built.Service.ResolvedModel(selected.ID).ContextWindow; got != contextWindow {
		t.Errorf("resolved context window = %d, want %d", got, contextWindow)
	}
}

func TestOperatorDefinedLLMProviders_Scenario3_ResponsesAdapter(t *testing.T) {
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("Responses path = %q, want /v1/responses", r.URL.Path)
		}
		var request struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Responses request: %v", err)
		}
		models = append(models, request.Model)
		http.Error(w, "stop", http.StatusBadRequest)
	}))
	defer server.Close()

	cfg := customProviderConfig()
	cfg.LLMMaxAttempts = 1
	definition := cfg.ProviderDefinitions["gateway-responses"]
	definition.BaseURL = server.URL + "/v1"
	cfg.ProviderDefinitions[definition.ID] = definition
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup("gateway-responses")
	if !ok || entry.provider == nil {
		t.Fatal("Responses custom provider was not registered")
	}
	sendProviderRequest(t, entry.provider, reg.DefaultModelFor("gateway-responses"))
	sendProviderRequest(t, entry.provider, "explicit-model")
	if len(models) != 2 || models[0] != "gateway-default" || models[1] != "explicit-model" {
		t.Errorf("Responses models = %v, want [gateway-default explicit-model]", models)
	}
}

func TestOperatorDefinedLLMProviders_Scenario3_DeclaredFlavorSelectsAdapter(t *testing.T) {
	paths := make(map[string]string)
	keys := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.Path] = r.URL.Path
		keys[r.URL.Path] = r.Header.Get("X-Api-Key")
		http.Error(w, "stop", http.StatusBadRequest)
	}))
	defer server.Close()

	cfg := customProviderConfig()
	cfg.LLMMaxAttempts = 1
	cfg.ProviderDefinitions["gateway-chat"] = permconfig.ProviderDefinition{
		ID: "gateway-chat", BaseURL: server.URL + "/v1", DefaultModel: "chat-default",
		APIFlavor: "openai-chat-completions", Auth: permconfig.ProviderAuth{Method: "none"},
	}
	cfg.ProviderDefinitions["gateway-anthropic"] = permconfig.ProviderDefinition{
		ID: "gateway-anthropic", BaseURL: server.URL, DefaultModel: "claude-gateway",
		APIFlavor: "anthropic-messages", Auth: permconfig.ProviderAuth{Method: "api_key"},
	}
	cfg.CustomProviderAPIKeys["gateway-anthropic"] = "anthropic-key"

	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	for _, id := range []string{"gateway-chat", "gateway-anthropic"} {
		entry, ok := reg.Lookup(id)
		if !ok || entry.provider == nil {
			t.Fatalf("declared custom flavor %q was not registered", id)
		}
		sendProviderRequest(t, entry.provider, reg.DefaultModelFor(id))
	}
	if paths["/v1/chat/completions"] == "" {
		t.Error("Chat Completions flavor did not use /v1/chat/completions")
	}
	if paths["/v1/messages"] == "" {
		t.Error("Anthropic Messages flavor did not use /v1/messages")
	}
	if keys["/v1/messages"] != "anthropic-key" {
		t.Errorf("Anthropic API key header = %q, want configured key", keys["/v1/messages"])
	}
}

func TestInvariant_custom_provider_has_no_builtin_private_options(t *testing.T) {
	var metadataHeader string
	var providerField json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadataHeader = r.Header.Get("X-OpenRouter-Metadata")
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode custom request: %v", err)
		}
		providerField = request["provider"]
		http.Error(w, "stop", http.StatusBadRequest)
	}))
	defer server.Close()

	cfg := customProviderConfig()
	cfg.LLMMaxAttempts = 1
	definition := cfg.ProviderDefinitions["gateway-responses"]
	definition.BaseURL = server.URL + "/v1"
	cfg.ProviderDefinitions[definition.ID] = definition
	cfg.OpenRouterKey = "openrouter-key"
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	custom, ok := reg.Lookup("gateway-responses")
	if !ok {
		t.Fatal("custom provider was not registered")
	}
	if custom.lister == nil {
		t.Error("custom provider did not configure its flavor-appropriate live lister")
	}
	sendProviderRequest(t, custom.provider, "gateway-default")
	if metadataHeader != "" {
		t.Errorf("custom request sent OpenRouter metadata header %q", metadataHeader)
	}
	if providerField != nil {
		t.Errorf("custom request sent OpenRouter provider preferences %s", providerField)
	}
}

func TestOperatorDefinedLLMProviders_Scenario5_DefaultProvider(t *testing.T) {
	cfg := customProviderConfig()
	cfg.DefaultProvider = "gateway-responses"
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if got := reg.Default(); got != "gateway-responses" {
		t.Errorf("default provider = %q, want gateway-responses", got)
	}
	if got := reg.ResolvedDefaultModel(); got != "gateway-default" {
		t.Errorf("default model = %q, want gateway-default", got)
	}
}

func TestOperatorDefinedLLMProviders_Scenario5_OverridePrecedence(t *testing.T) {
	cfg := customProviderConfig()
	settings := permconfig.ProviderOverrides{
		"openai": {BaseURL: "https://settings.example/v1"},
	}
	command := permconfig.ProviderOverrides{
		"openai": {BaseURL: "https://flag.example/v1"},
	}
	cfg.ProviderOverrides = mergeProviderOverrides(settings, command)
	cfg.OpenAIKey = "openai-key"
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	openai, ok := reg.Lookup(providerOpenAI)
	if !ok {
		t.Fatal("openai provider was not registered")
	}
	if got := openai.baseURL; got != "https://flag.example/v1" {
		t.Errorf("CLI base URL = %q, want https://flag.example/v1", got)
	}
	custom, _ := reg.Lookup("gateway-responses")
	if got := custom.baseURL; got != "https://gateway.example/v1" {
		t.Errorf("custom provider base URL = %q, want configured URL", got)
	}
}

func TestOperatorDefinedLLMProviders_Scenario7_EndpointOverridePrecedence(t *testing.T) {
	settings := permconfig.ProviderOverrides{
		"openai":     {BaseURL: "https://settings.example/v1"},
		"anthropic":  {BaseURL: "https://anthropic-settings.example/v1"},
		"openrouter": {BaseURL: "https://openrouter-settings.example/v1"},
	}
	command := permconfig.ProviderOverrides{
		"openai": {BaseURL: "https://cli.example/v1"},
	}
	merged := mergeProviderOverrides(settings, command)
	if got := merged["openai"].BaseURL; got != "https://cli.example/v1" {
		t.Errorf("CLI override = %q, want https://cli.example/v1", got)
	}
	if got := merged["anthropic"].BaseURL; got != "https://anthropic-settings.example/v1" {
		t.Errorf("settings-only override = %q, want settings value", got)
	}
	if got := builtinBaseURL(Config{}, providerOpenAI); got != "" {
		t.Errorf("OpenAI default endpoint override = %q, want SDK default empty", got)
	}
	if got := builtinBaseURL(Config{}, providerAnthropic); got != "" {
		t.Errorf("Anthropic default endpoint override = %q, want SDK default empty", got)
	}
}

func TestOperatorDefinedLLMProviders_Scenario5_PersistedSelector(t *testing.T) {
	cfg := customProviderConfig()
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup("gateway-responses")
	if !ok || entry.provider == nil {
		t.Fatal("persisted custom provider selector cannot rehydrate")
	}
	if _, ok := reg.Lookup("removed-gateway"); ok {
		t.Error("removed custom provider selector unexpectedly resolved")
	}
}
