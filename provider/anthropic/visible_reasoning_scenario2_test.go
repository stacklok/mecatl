package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/stacklok/mecatl/engine/port"
)

func TestVisibleReasoning_Scenario2_Claude5AdaptiveFallback(t *testing.T) {
	for _, model := range []string{
		"claude-opus-5", "claude-opus-5-5", "claude-opus-5-1-20261001",
		"claude-sonnet-5", "claude-sonnet-5-5-20261001", "claude-sonnet-5-1",
		"claude-fable-5", "claude-fable-5-1-20261001", "claude-mythos-5", "claude-mythos-5-5-20261001",
	} {
		t.Run(model, func(t *testing.T) {
			params, err := testProvider().buildParams(port.LLMRequest{Model: model})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(params.Thinking)
			if err != nil {
				t.Fatal(err)
			}
			var thinking map[string]any
			if err := json.Unmarshal(raw, &thinking); err != nil {
				t.Fatal(err)
			}
			if thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
				t.Errorf("thinking = %s, want adaptive summarized", raw)
			}
			if _, ok := thinking["budget_tokens"]; ok {
				t.Errorf("adaptive thinking has budget_tokens: %s", raw)
			}
		})
	}
	for _, model := range []string{"claude-opus-4-5", "claude-sonnet-4-5", "claude-haiku-4-5"} {
		t.Run(model, func(t *testing.T) {
			cfg, err := testProvider().buildParams(port.LLMRequest{Model: model})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Thinking.OfEnabled == nil || cfg.Thinking.OfAdaptive != nil || cfg.Thinking.OfEnabled.BudgetTokens != 4096 || cfg.Thinking.OfEnabled.Display != "summarized" {
				t.Errorf("manual thinking = %+v", cfg.Thinking)
			}
		})
	}
	cfg, err := testProvider().buildParams(port.LLMRequest{Model: "claude-3-5-sonnet-20241022"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Thinking.OfEnabled != nil || cfg.Thinking.OfAdaptive != nil {
		t.Errorf("3.5 must omit thinking: %+v", cfg.Thinking)
	}
}

func TestVisibleReasoning_Scenario2_LivePrecedenceAndFallback(t *testing.T) {
	const model = "claude-opus-5-5"
	for _, tc := range []struct {
		name, capabilities, want string
	}{
		{"missing capabilities", "", "adaptive"},
		{"missing thinking", `,"capabilities":{}`, "adaptive"},
		{"manual live", `,"capabilities":{"thinking":{"supported":true,"types":{"enabled":{"supported":true}}}}`, "enabled"},
		{"unsupported live", `,"capabilities":{"thinking":{"supported":false}}`, ""},
		{"adaptive live", `,"capabilities":{"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}}}`, "adaptive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"data":[{"id":"` + model + `","type":"model","display_name":"Claude Opus 5.5","created_at":"2026-01-01T00:00:00Z"` + tc.capabilities + `}],"has_more":false}`
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("x-api-key") != "fixture-key" {
					t.Error("listing omitted configured credential")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			})}
			models, err := NewLister("fixture-key", "", client).ListModels(context.Background())
			if err != nil || len(models) != 1 {
				t.Fatalf("listing: %v, models: %v", err, models)
			}
			resolve := func(requested string) (bool, bool, bool) {
				if requested != models[0].ID {
					return false, false, false
				}
				th := models[0].Thinking
				return th.Adaptive, th.Enabled, th.Known
			}
			p := New(WithAPIKey("test"), WithThinkingResolver(resolve))
			params, err := p.buildParams(port.LLMRequest{Model: model})
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if params.Thinking.OfAdaptive != nil {
				got = "adaptive"
			}
			if params.Thinking.OfEnabled != nil {
				got = "enabled"
			}
			if got != tc.want {
				t.Errorf("thinking mode = %q, want %q", got, tc.want)
			}
			if got == "adaptive" && params.Thinking.OfAdaptive.Display != "summarized" {
				t.Error("adaptive display not summarized")
			}
		})
	}
}

func TestVisibleReasoning_Scenario2_NamespacedAnthropicFallback(t *testing.T) {
	for _, tc := range []struct{ model, mode string }{
		{"anthropic/claude-opus-5-5", "adaptive"},
		{"anthropic/claude-opus-4-5", "enabled"},
		{"anthropic/claude-3-5-sonnet-20241022", ""},
		{"vendor/claude-opus-5-5", ""},
		{"notanthropic/claude-opus-5-5", ""},
		{"anthropic/vendor/claude-opus-5-5", ""},
	} {
		t.Run(tc.model, func(t *testing.T) {
			rec := &recordingRoundTripper{}
			p := New(WithAPIKey("test"), WithRequestOption(option.WithHTTPClient(rec), option.WithMaxRetries(0)))
			stream, err := p.Stream(context.Background(), port.LLMRequest{Model: tc.model})
			if err != nil {
				t.Fatal(err)
			}
			for range stream {
			}
			if rec.req == nil {
				t.Fatal("no wire request")
			}
			var body struct {
				Model    string         `json:"model"`
				Thinking map[string]any `json:"thinking"`
			}
			if err := json.NewDecoder(rec.req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Model != tc.model {
				t.Errorf("wire model = %q, want unchanged %q", body.Model, tc.model)
			}
			if tc.mode == "" {
				if body.Thinking != nil {
					t.Errorf("thinking must be omitted: %v", body.Thinking)
				}
				return
			}
			if body.Thinking["type"] != tc.mode || body.Thinking["display"] != "summarized" {
				t.Errorf("thinking = %v, want %s summarized", body.Thinking, tc.mode)
			}
			if tc.mode == "adaptive" {
				if _, ok := body.Thinking["budget_tokens"]; ok {
					t.Errorf("adaptive budget on wire: %v", body.Thinking)
				}
			}
		})
	}
}
