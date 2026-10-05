package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestVisibleReasoning_Scenario2_ListingFailureAndModelMiss(t *testing.T) {
	const model = "claude-opus-5-5"
	for _, tc := range []struct {
		name, body string
		failure    error
	}{
		{"failure", "", errors.New("offline fixture failure")},
		{"timeout", "", context.DeadlineExceeded},
		{"model miss", `{"data":[{"id":"claude-opus-4-5","type":"model","display_name":"Claude Opus 4.5","created_at":"2026-01-01T00:00:00Z"}],"has_more":false}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("x-api-key") != "test-key" {
					t.Errorf("listing did not use configured credential")
				}
				if tc.failure != nil {
					return nil, tc.failure
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			})}
			wire := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					t.Errorf("unexpected inference path: %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				wire <- body
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := io.WriteString(w, toolhiveCompletedMessageSSE); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			cfg := Config{
				ProviderDefinitions: permconfig.ProviderDefinitions{"gateway": {
					ID: "gateway", BaseURL: srv.URL, DefaultModel: model, APIFlavor: "anthropic-messages", Auth: permconfig.ProviderAuth{Method: "api_key"},
				}},
				LLMMaxAttempts:        1,
				CustomProviderAPIKeys: map[string]string{"gateway": "test-key"}, liveModelHTTPClient: client,
			}
			reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
			if err != nil {
				t.Fatal(err)
			}
			models := discoverAllModels(t, reg)
			a, e, known := reg.meta.thinkingFor("gateway", model)
			if a || e || known {
				t.Errorf("missing listing thinking = %v %v %v, want unknown", a, e, known)
			}
			stream, err := reg.entries["gateway"].provider.Stream(context.Background(), port.LLMRequest{Model: model})
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range stream {
				if err != nil {
					t.Fatal(err)
				}
			}
			select {
			case body := <-wire:
				if body["model"] != model {
					t.Errorf("inference model = %v, want configured default", body["model"])
				}
				thinking, _ := body["thinking"].(map[string]any)
				if thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
					t.Errorf("fallback inference thinking = %v", thinking)
				}
				if _, ok := thinking["budget_tokens"]; ok {
					t.Errorf("adaptive fallback sent budget_tokens: %v", thinking)
				}
			default:
				t.Fatal("no inference request reached fixture")
			}
			if reg.entries["gateway"].defaultModel != model {
				t.Errorf("custom default lost: %q", reg.entries["gateway"].defaultModel)
			}
			found := false
			for _, m := range models {
				if m.ProviderId == "gateway" && m.Id == model {
					found = true
				}
			}
			if !found {
				t.Errorf("custom default model %q absent from picker", model)
			}
		})
	}
	// The built-in Anthropic embedded catalog remains a resolver floor on both failed and sparse refreshes.
	for _, tc := range []struct {
		name, body string
		failure    error
	}{
		{"failed refresh", "", errors.New("offline")},
		{"sparse refresh", `{"data":[{"id":"claude-opus-4-5","type":"model","display_name":"Claude Opus 4.5","created_at":"2026-01-01T00:00:00Z"}],"has_more":false}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := regWithAnthropicLister(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if tc.failure != nil {
					return nil, tc.failure
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			})})
			before := reg.meta.outputLimitFor(providerAnthropic, catAnthropicModel)
			if before != catAnthropicOutput {
				t.Fatalf("pre-refresh catalog ceiling = %d, want %d", before, catAnthropicOutput)
			}
			discoverAllModels(t, reg)
			if after := reg.meta.outputLimitFor(providerAnthropic, catAnthropicModel); after != before {
				t.Errorf("catalog ceiling lost after refresh: %d, was %d", after, before)
			}
		})
	}
}
