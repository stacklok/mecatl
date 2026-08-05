// The package tests are intentionally in the normal Go package graph.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeClientRefusesRedirectAndDoesNotForwardSecrets(t *testing.T) {
	t.Parallel()
	var calls int
	client := newProbeClient()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatalf("redirect target reached with auth=%t account=%t",
				req.Header.Get("Authorization") != "", req.Header.Get("ChatGPT-Account-ID") != "")
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": []string{"https://redirect.invalid/steal"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})

	var auth authFile
	auth.Tokens.AccessToken = "secret-access-sentinel"
	auth.Tokens.AccountID = "secret-account-sentinel"
	_, _, err := probeModels(context.Background(), client, "https://origin.invalid", auth)
	if errCategory(err) != "models_http_3xx" {
		t.Fatalf("category = %q, want models_http_3xx", errCategory(err))
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want 1", calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDecodeLimitedRejectsOversizeAndTrailingJSON(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"oversize": `{}` + strings.Repeat(" ", maxBody),
		"trailing": `{} {}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			var dst map[string]any
			if err := decodeLimited(strings.NewReader(input), &dst); err == nil {
				t.Fatal("decode succeeded")
			}
		})
	}
}

func TestConsumeSSERejectsCompletionBeforeOverflow(t *testing.T) {
	t.Parallel()
	completed := "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"input_tokens_details\":{},\"output_tokens\":1,\"output_tokens_details\":{},\"total_tokens\":2}}}\n\n"
	input := completed + strings.Repeat("x", maxBody)
	var got wireResult
	if err := consumeSSE(strings.NewReader(input), &got); errCategory(err) != "responses_body_too_large" {
		t.Fatalf("category = %q, want responses_body_too_large", errCategory(err))
	}
}

func TestStatusFailureCategories(t *testing.T) {
	t.Parallel()
	tests := []struct {
		endpoint string
		status   int
		want     string
	}{
		{"models", 401, "models_authentication_or_originator_rejected"},
		{"models", 403, "models_authentication_or_originator_rejected"},
		{"models", 429, "models_quota_or_rate_limited"},
		{"responses", 401, "responses_authentication_or_originator_rejected"},
		{"responses", 403, "responses_authentication_or_originator_rejected"},
		{"responses", 429, "responses_quota_or_rate_limited"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%d", tt.endpoint, tt.status), func(t *testing.T) {
			if got := statusFailure(tt.endpoint, tt.status); got != tt.want {
				t.Fatalf("statusFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConsumeSSERejectsUnknownVocabulary(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"event": `{"type":"provider.secret_event"}`,
		"item":  `{"type":"response.output_item.done","item":{"type":"provider_item"}}`,
		"usage": `{"type":"response.completed","response":{"usage":{"provider_tokens":1}}}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			var got wireResult
			err := consumeSSE(strings.NewReader("data: "+data+"\n\n"), &got)
			if err == nil {
				t.Fatal("unknown vocabulary accepted")
			}
		})
	}
}

func TestFunctionCallValidationRequiresExactlyOneExpectedStrictCall(t *testing.T) {
	t.Parallel()
	valid := functionCall{ItemID: "item", CallID: "call", Name: "compatibility_probe", Arguments: `{"value":"OK"}`}
	tests := map[string][]functionCall{
		"none":       nil,
		"two":        {valid, valid},
		"name":       {{ItemID: "item", CallID: "call", Name: "other", Arguments: `{"value":"OK"}`}},
		"arguments":  {{ItemID: "item", CallID: "call", Name: "compatibility_probe", Arguments: `{"value":"NO"}`}},
		"extra_arg":  {{ItemID: "item", CallID: "call", Name: "compatibility_probe", Arguments: `{"value":"OK","extra":true}`}},
		"missing_id": {{Name: "compatibility_probe", Arguments: `{"value":"OK"}`}},
	}
	if err := validateFunctionCalls([]functionCall{valid}); err != nil {
		t.Fatalf("valid call rejected: %v", err)
	}
	for name, calls := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateFunctionCalls(calls); err == nil {
				t.Fatal("invalid call accepted")
			}
		})
	}
}

func TestDecodeLimitedRequiresExactlyOneStrictJSONValue(t *testing.T) {
	t.Parallel()
	type strict struct {
		Allowed bool `json:"allowed"`
	}
	var dst strict
	if err := decodeStrictLimited(strings.NewReader(`{"allowed":true,"provider_field":"secret"}`), &dst); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := decodeStrictLimited(strings.NewReader(`{"allowed":true} {}`), &dst); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if err := decodeStrictLimited(io.LimitReader(strings.NewReader(`{"allowed":true}`), maxBody), &dst); err != nil {
		t.Fatalf("valid strict JSON rejected: %v", err)
	}
}

func TestSanitizedFixturesPreserveRequiredRoles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		path  string
		check func(*testing.T, wireResult)
	}{
		{
			name: "subscription_compatibility_text.sse",
			path: fixtureInventory[0],
			check: func(t *testing.T, got wireResult) {
				if !got.textSeen || !got.contract.HasPhase || !got.contract.HasReplayItemID {
					t.Fatal("text fixture lacks output, phase, or replay item identifier")
				}
				if len(got.functionCalls) != 0 {
					t.Fatal("text fixture unexpectedly contains a function call")
				}
			},
		},
		{
			name: "subscription_compatibility_tool_call.sse",
			path: fixtureInventory[1],
			check: func(t *testing.T, got wireResult) {
				if err := validateFunctionCalls(got.functionCalls); err != nil {
					t.Fatalf("tool-call fixture: %v", err)
				}
				call := got.functionCalls[0]
				if call.ItemID != "function_synthetic" || call.CallID != "call_synthetic" ||
					call.Name != "compatibility_probe" || call.Arguments != `{"value":"OK"}` {
					t.Fatal("tool-call fixture does not use the exact synthetic call topology")
				}
			},
		},
		{
			name: "subscription_compatibility_tool_continuation.sse",
			path: fixtureInventory[2],
			check: func(t *testing.T, got wireResult) {
				if !got.textSeen || !got.contract.HasPhase || !got.contract.HasReplayItemID {
					t.Fatal("continuation fixture lacks text, phase, or replay item identifier")
				}
				if len(got.functionCalls) != 0 {
					t.Fatal("continuation fixture unexpectedly contains a new function call")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", filepath.FromSlash(tt.path)))
			if err != nil {
				t.Fatal(err)
			}
			var got wireResult
			if err := consumeSSE(strings.NewReader(string(b)), &got); err != nil {
				t.Fatalf("consume fixture: %v", err)
			}
			tt.check(t, got)
		})
	}
}
