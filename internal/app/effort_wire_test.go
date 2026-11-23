package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type wireEffort struct {
	levels []string
	known  bool
}

// grpcEffortLevels lists the gateway's models over gRPC, keyed by model ID.
func grpcEffortLevels(t *testing.T, built *Built) map[string]wireEffort {
	t.Helper()
	resp, err := server.NewHarnessServer(built.Service).ListModels(context.Background(), &mecatlv1.ListModelsRequest{})
	if err != nil {
		t.Fatalf("gRPC ListModels: %v", err)
	}
	out := map[string]wireEffort{}
	for _, m := range resp.GetModels() {
		if m.GetProviderId() == "gateway" {
			out[m.GetId()] = wireEffort{levels: m.GetEffortLevels(), known: m.GetEffortLevelsKnown()}
		}
	}
	return out
}

// httpEffortLevels lists the gateway's models over HTTP, keyed by model ID.
func httpEffortLevels(t *testing.T, built *Built) map[string]wireEffort {
	t.Helper()
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/models status = %d", resp.StatusCode)
	}
	var body struct {
		Models []struct {
			ID                string   `json:"id"`
			ProviderID        string   `json:"provider_id"`
			EffortLevels      []string `json:"effort_levels"`
			EffortLevelsKnown bool     `json:"effort_levels_known"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /v1/models: %v", err)
	}
	out := map[string]wireEffort{}
	for _, m := range body.Models {
		if m.ProviderID == "gateway" {
			out[m.ID] = wireEffort{levels: m.EffortLevels, known: m.EffortLevelsKnown}
		}
	}
	return out
}

// TestModelInfoEffortKnownStates: ListModels reports listed levels, known none,
// and unknown distinctly, the same over gRPC and HTTP.
func TestModelInfoEffortKnownStates(t *testing.T) {
	built, _ := effortGatewayBuild(t, "openai-responses", "gw-reasoner", publishingGatewayListing, nil)
	grpc, httpLevels := grpcEffortLevels(t, built), httpEffortLevels(t, built)
	for _, tc := range []struct {
		model string
		want  wireEffort
	}{
		{"gw-reasoner", wireEffort{levels: []string{"low", "medium", "high", "xhigh"}, known: true}},
		{"gw-plain", wireEffort{known: true}},
	} {
		for transport, got := range map[string]wireEffort{"gRPC": grpc[tc.model], "HTTP": httpLevels[tc.model]} {
			if got.known != tc.want.known || !slices.Equal(got.levels, tc.want.levels) {
				t.Errorf("%s %s: levels=%v known=%v, want %v known=%v", transport, tc.model, got.levels, got.known, tc.want.levels, tc.want.known)
			}
		}
	}

	unknown, _ := effortGatewayBuild(t, "openai-responses", "gw-model", `{"object":"list","data":[{"id":"gw-model"}]}`, nil)
	for transport, got := range map[string]wireEffort{"gRPC": grpcEffortLevels(t, unknown)["gw-model"], "HTTP": httpEffortLevels(t, unknown)["gw-model"]} {
		if got.known || len(got.levels) != 0 {
			t.Errorf("%s gw-model: levels=%v known=%v, want unknown", transport, got.levels, got.known)
		}
	}

	chat, _ := effortGatewayBuild(t, "openai-chat-completions", "chat-model", `{"object":"list","data":[{"id":"chat-model"}]}`, nil)
	if got := grpcEffortLevels(t, chat)["chat-model"]; !got.known || len(got.levels) != 0 {
		t.Errorf("unconfigured Chat Completions model: levels=%v known=%v, want known none", got.levels, got.known)
	}
}

// TestResolvedModelRequestedEffort: resolved_model reports the requested level
// beside the applied one, over gRPC and HTTP.
func TestResolvedModelRequestedEffort(t *testing.T) {
	built, _ := effortGatewayBuild(t, "openai-responses", "gw-reasoner", publishingGatewayListing, nil)
	ctx := context.Background()
	sess, err := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "gateway", ModelID: "gw-reasoner", ReasoningEffort: "max"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	resp, err := server.NewHarnessServer(built.Service).GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rm := resp.GetSession().GetResolvedModel(); rm.GetReasoningEffort() != "" || rm.GetRequestedReasoningEffort() != "max" {
		t.Fatalf("gRPC resolved_model applied=%q requested=%q, want none/max", rm.GetReasoningEffort(), rm.GetRequestedReasoningEffort())
	}

	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()
	httpResp, err := srv.Client().Get(srv.URL + "/v1/sessions/" + string(sess.ID))
	if err != nil {
		t.Fatalf("GET session: %v", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	var body struct {
		ResolvedModel struct {
			ReasoningEffort          string `json:"reasoning_effort"`
			RequestedReasoningEffort string `json:"requested_reasoning_effort"`
		} `json:"resolved_model"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if body.ResolvedModel.ReasoningEffort != "" || body.ResolvedModel.RequestedReasoningEffort != "max" {
		t.Fatalf("HTTP resolved_model = %+v, want none/max", body.ResolvedModel)
	}
}

func TestCompatibilityAdvertisesModelEffortLevels(t *testing.T) {
	built, _ := effortGatewayBuild(t, "openai-responses", "gw-reasoner", publishingGatewayListing, nil)
	resp, err := server.NewHarnessServer(built.Service).GetCompatibilityInfo(context.Background(), &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo: %v", err)
	}
	if !slices.Contains(resp.GetFeatures(), server.FeatureModelEffortLevels) {
		t.Fatalf("features = %v, want %s", resp.GetFeatures(), server.FeatureModelEffortLevels)
	}
}

// TestPublishedEffortLevelsMatchTheRequestPath: ListModels reports the levels
// requests are decided against, so what a client offers and what the server
// sends agree, the mock and an unconfigured Chat Completions model included.
func TestPublishedEffortLevelsMatchTheRequestPath(t *testing.T) {
	reg, _ := twoProviderEffortRegistry("")
	reg.entries[providerMock] = providerEntry{id: providerMock, available: true, provider: mockllm.New()}
	reg.entries["chat"] = providerEntry{id: "chat", available: true, protocol: protocolOpenAIChatCompletions,
		remint: func(string, port.ProviderCapabilities) port.LLMProvider { return mockllm.New() }}
	for _, tc := range []struct {
		pid, model string
		known      bool
		levels     []string
	}{
		{providerMock, "mock-model", true, nil},
		{"chat", "chat-model", true, nil},
		{providerOpenAI, "gpt-5", true, []string{effortLow, effortMedium, effortHigh}},
		{providerAnthropic, "unknown-model", false, nil},
	} {
		published := publishedEffortLevels(reg, Config{}, reg.meta.current(), tc.pid, tc.model)
		if published.known != tc.known || !slices.Equal(published.levels, tc.levels) {
			t.Errorf("%s/%s: published %v known=%v, want %v known=%v", tc.pid, tc.model, published.levels, published.known, tc.levels, tc.known)
		}
		for _, level := range effortVocabulary {
			if got, want := reg.appliedEffortFor(tc.pid, tc.model, level), appliedEffort(level, published); got != want {
				t.Errorf("%s/%s at %s: the request path sends %q, the listing implies %q", tc.pid, tc.model, level, got, want)
			}
		}
	}
}
