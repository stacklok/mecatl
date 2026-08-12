package openaicodex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func modelsTestCredential(t *testing.T) Credential {
	t.Helper()
	credential, err := NewCredential(validJWT("acct-models", testNow.Add(time.Hour), true), "", "", testNow)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	return credential
}

func modelsJSONResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestCodexModelsRequest(t *testing.T) {
	var got *http.Request
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		got.Header = req.Header.Clone()
		return modelsJSONResponse(req, http.StatusOK, `{"models":[]}`), nil
	})
	policy, err := NewRequestPolicy(modelsTestCredential(t), func() time.Time { return testNow }, transport)
	if err != nil {
		t.Fatal(err)
	}
	lister := NewLister(policy)
	if _, err := lister.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got == nil {
		t.Fatal("models request was not sent")
	}
	wantURL := BaseURL + "/models?client_version=1.0.0"
	if got.Method != http.MethodGet || got.URL.String() != wantURL {
		t.Fatalf("request = %s %s, want GET %s", got.Method, got.URL, wantURL)
	}
	for key, want := range map[string]string{
		"Accept":                  "application/json",
		"Authorization":           "Bearer " + policy.credential.accessToken,
		"ChatGPT-Account-ID":      policy.credential.accountID,
		"originator":              "mecatl",
		"User-Agent":              UserAgent,
		"X-Stainless-Retry-Count": "0",
		"X-OpenAI-Fedramp":        "true",
	} {
		if value := got.Header.Get(key); value != want {
			t.Errorf("%s = %q, want %q", key, value, want)
		}
	}
}

func TestCodexModelsBoundsAndCancellation(t *testing.T) {
	t.Run("bounded default client", func(t *testing.T) {
		policy, err := NewRequestPolicy(modelsTestCredential(t), func() time.Time { return testNow }, roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return modelsJSONResponse(req, http.StatusOK, `{"models":[]}`), nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got := NewLister(policy).client.Timeout; got != 5*time.Second {
			t.Fatalf("models client timeout = %v, want 5s", got)
		}
	})

	t.Run("body cap", func(t *testing.T) {
		policy, err := NewRequestPolicy(modelsTestCredential(t), func() time.Time { return testNow }, roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxModelsResponseBytes+1))), Request: req}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewLister(policy).ListModels(context.Background())
		if err == nil || !strings.Contains(err.Error(), "cap") {
			t.Fatalf("oversized response error = %v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		policy, err := NewRequestPolicy(modelsTestCredential(t), func() time.Time { return testNow }, roundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = NewLister(policy).ListModels(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want context.Canceled", err)
		}
	})

	t.Run("redirect refused", func(t *testing.T) {
		policy, err := NewRequestPolicy(modelsTestCredential(t), func() time.Time { return testNow }, roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://attacker.invalid/steal"}},
				Body:       io.NopCloser(strings.NewReader("redirect body")),
				Request:    req,
			}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewLister(policy).ListModels(context.Background())
		if err == nil || !strings.Contains(err.Error(), "redirect refused") {
			t.Fatalf("redirect error = %v", err)
		}
	})
}

func TestCodexModelsEntitlementProjection(t *testing.T) {
	const validOpaqueSlug = "vendor/model:v1"
	overlong := strings.Repeat("x", maxModelIDRunes+1)
	body := fmt.Sprintf(`{"models":[
		{"slug":%q,"display_name":"Image","visibility":"list","supported_in_api":false,"context_window":200000,"input_modalities":["text","image"],"supported_reasoning_levels":[{"effort":"low"}]},
		{"slug":"text-only","display_name":"Text","visibility":"list","supported_in_api":true,"context_window":0,"max_context_window":180000,"input_modalities":["text"]},
		{"slug":"hidden","display_name":"Hidden","visibility":"hide"},
		{"slug":"","display_name":"Empty","visibility":"list"},
		{"slug":" leading","display_name":"Leading whitespace","visibility":"list"},
		{"slug":"trailing ","display_name":"Trailing whitespace","visibility":"list"},
		{"slug":"control\u001b","display_name":"Control","visibility":"list"},
		{"slug":"bidi\u202e","display_name":"Bidi","visibility":"list"},
		{"slug":%q,"display_name":"Overlong","visibility":"list"}
	]}`, validOpaqueSlug, overlong)
	policy, err := NewRequestPolicy(modelsTestCredential(t), func() time.Time { return testNow }, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return modelsJSONResponse(req, http.StatusOK, body), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewLister(policy).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []Model{
		{ID: validOpaqueSlug, DisplayName: "Image", ContextLimit: 200000, InputModalities: []string{"text", "image"}, InputModalitiesKnown: true, Reasoning: true, ReasoningKnown: true, ToolCall: true},
		{ID: "text-only", DisplayName: "Text", ContextLimit: 180000, InputModalities: []string{"text"}, InputModalitiesKnown: true, ToolCall: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
	if got[0].ID != validOpaqueSlug {
		t.Fatalf("opaque slug = %q, want byte-identical %q", got[0].ID, validOpaqueSlug)
	}
}
