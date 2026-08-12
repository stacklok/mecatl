package openaicodex

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var testNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func jwt(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("synthetic-signature"))
}

func validJWT(account string, expiry time.Time, fedRAMP bool) string {
	return jwt(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_account_is_fedramp":%t}}`,
		expiry.Unix(), account, fedRAMP))
}

func TestCredentialFormattingAndSlogAreRedacted(t *testing.T) {
	secretToken := validJWT("secret-account", testNow.Add(time.Hour), true)
	credential, err := NewCredential(secretToken, "", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		got := fmt.Sprintf(format, credential)
		if strings.Contains(got, secretToken) || strings.Contains(got, "secret-account") {
			t.Fatalf("format %s leaked credential material", format)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("format %s lacks redaction marker: %s", format, got)
		}
	}
	for _, test := range []struct {
		name string
		json bool
	}{{name: "text"}, {name: "json", json: true}} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var handler slog.Handler = slog.NewTextHandler(&output, nil)
			if test.json {
				handler = slog.NewJSONHandler(&output, nil)
			}
			slog.New(handler).Info("credential", "value", credential)
			got := output.String()
			if strings.Contains(got, secretToken) || strings.Contains(got, "secret-account") {
				t.Fatal("structured log leaked credential material")
			}
			if !strings.Contains(got, "REDACTED") {
				t.Errorf("structured log lacks redaction marker: %s", got)
			}
		})
	}
}

func TestCredentialValidation(t *testing.T) {
	secret := "secret-token-fragment"
	tests := []struct {
		name      string
		token     string
		accountID string
		expiresAt string
	}{
		{name: "empty token"},
		{name: "not JWT", token: secret},
		{name: "invalid base64url", token: "e30.***.sig"},
		{name: "invalid header", token: "***." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1893456000}`)) + ".c2ln"},
		{name: "invalid signature", token: "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1893456000}`)) + ".***"},
		{name: "padded header", token: "e30=." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1893456000}`)) + ".c2ln"},
		{name: "padded payload", token: "e30." + base64.URLEncoding.EncodeToString([]byte(`{"exp":1893456000}`)) + ".c2ln"},
		{name: "padded signature", token: "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1893456000}`)) + ".c2ln="},
		{name: "noncanonical signature", token: "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1893456000}`)) + ".Zh"},
		{name: "missing segment", token: "e30.e30"},
		{name: "extra segment", token: "e30.e30.c2ln.extra"},
		{name: "empty header", token: ".e30.c2ln"},
		{name: "empty payload", token: "e30..c2ln"},
		{name: "empty signature", token: "e30.e30."},
		{name: "oversized header", token: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("h", maxJWTHeaderBytes+1))) + ".e30.c2ln"},
		{name: "oversized signature", token: "e30.e30." + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", maxJWTSignatureBytes+1)))},
		{name: "trailing JSON", token: jwt(`{"exp":1893456000} {}`)},
		{name: "non integer expiry", token: jwt(`{"exp":1.5,"https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`)},
		{name: "string expiry", token: jwt(`{"exp":"1893456000","https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`)},
		{name: "exponent expiry", token: jwt(`{"exp":1e10,"https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`)},
		{name: "missing account", token: jwt(`{"exp":1893456000}`)},
		{name: "non object auth", token: jwt(`{"exp":1893456000,"https://api.openai.com/auth":[]}`)},
		{name: "non boolean fedramp", token: jwt(`{"exp":1893456000,"https://api.openai.com/auth":{"chatgpt_account_id":"acct","chatgpt_account_is_fedramp":"true"}}`)},
		{name: "mismatched account", token: validJWT("jwt-account", testNow.Add(time.Hour), false), accountID: "explicit-account"},
		{name: "invalid explicit expiry", token: validJWT("acct", testNow.Add(time.Hour), false), expiresAt: "tomorrow"},
		{name: "expired", token: validJWT("acct", testNow, false)},
		{name: "oversized", token: "e30." + strings.Repeat("A", maxJWTPayloadBytes*2) + ".sig"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCredential(tc.token, tc.accountID, tc.expiresAt, testNow)
			if err == nil {
				t.Fatal("NewCredential() error = nil")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "jwt-account") || strings.Contains(err.Error(), "explicit-account") {
				t.Fatalf("error leaks credential material: %q", err)
			}
		})
	}

	cred, err := NewCredential(validJWT("acct", testNow.Add(time.Hour), true), "acct", "", testNow)
	if err != nil {
		t.Fatalf("NewCredential(valid): %v", err)
	}
	if cred.accountID != "acct" || !cred.fedRAMP {
		t.Fatalf("routing metadata = (%q, %t)", cred.accountID, cred.fedRAMP)
	}
	if err := cred.Validate(testNow.Add(30 * time.Minute)); err != nil {
		t.Fatalf("Validate(valid): %v", err)
	}
	if err := cred.Validate(time.Time{}); err == nil {
		t.Fatal("Validate(zero time) error = nil")
	}
}

func TestCredentialUsesEarlierExpiry(t *testing.T) {
	for _, tc := range []struct {
		name           string
		jwtExpiry      time.Time
		explicitExpiry time.Time
		want           time.Time
	}{
		{name: "explicit earlier", jwtExpiry: testNow.Add(2 * time.Hour), explicitExpiry: testNow.Add(time.Hour), want: testNow.Add(time.Hour)},
		{name: "JWT earlier", jwtExpiry: testNow.Add(time.Hour), explicitExpiry: testNow.Add(2 * time.Hour), want: testNow.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cred, err := NewCredential(validJWT("acct", tc.jwtExpiry, false), "", tc.explicitExpiry.Format(time.RFC3339), testNow)
			if err != nil {
				t.Fatal(err)
			}
			if !cred.expiresAt.Equal(tc.want) {
				t.Fatalf("expiresAt = %v, want %v", cred.expiresAt, tc.want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func sdkClient(policy RequestPolicy) oai.Client {
	return oai.NewClient(
		option.WithAPIKey("policy-owned"),
		option.WithBaseURL(BaseURL),
		option.WithMaxRetries(0),
		option.WithHTTPClient(policy.HTTPClient()),
	)
}

func TestRequestPolicyRejectsLateExpiry(t *testing.T) {
	cred, err := NewCredential(validJWT("acct", testNow.Add(time.Minute), false), "", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	policy, err := NewRequestPolicy(cred, func() time.Time { return testNow.Add(2 * time.Minute) }, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonResponse(http.StatusOK), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	client := sdkClient(policy)
	err = client.Post(context.Background(), "responses", []byte(`{}`), nil)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Post() error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport calls = %d, want 0", calls.Load())
	}
}

func TestRequestPolicyClonesBeforeCredentialInjection(t *testing.T) {
	cred, err := NewCredential(validJWT("acct", testNow.Add(time.Hour), false), "", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	var captured *http.Request
	policy, err := NewRequestPolicy(cred, func() time.Time { return testNow }, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req.Clone(req.Context())
		captured.Header = req.Header.Clone()
		return jsonResponse(http.StatusOK), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, BaseURL+"/responses", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = ""
	req.Header.Set("Authorization", "Bearer attacker")
	req.Header.Set("X-Late-Header", "must-not-pass")
	if _, err := policy.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer attacker" || req.Header.Get("X-Late-Header") != "must-not-pass" {
		t.Fatalf("original request was mutated: %v", req.Header)
	}
	if captured == nil || captured.Header.Get("Authorization") != "Bearer "+cred.accessToken || captured.Header.Get("X-Late-Header") != "" {
		t.Fatalf("wire request did not carry the exact policy headers: %v", captured)
	}
}

func TestRequestPolicyOverridesAmbientOpenAIDefaults(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-api-key")
	t.Setenv("OPENAI_ORG_ID", "ambient-org")
	t.Setenv("OPENAI_PROJECT_ID", "ambient-project")
	t.Setenv("OPENAI_BASE_URL", "https://attacker.invalid/v1")
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Secret-Ambient: leak-me\nAuthorization: Bearer attacker\nUser-Agent: Codex CLI")
	cred, err := NewCredential(validJWT("acct", testNow.Add(time.Hour), true), "", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	var captured *http.Request
	policy, err := NewRequestPolicy(cred, func() time.Time { return testNow }, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req.Clone(req.Context())
		return jsonResponse(http.StatusOK), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	client := sdkClient(policy)
	poisoned := []option.RequestOption{
		option.WithHeader("Accept", "poison-accept"),
		option.WithHeader("Content-Type", "poison-content-type"),
		option.WithHeader("X-Stainless-Retry-Count", "99"),
		option.WithHeader("X-Stainless-Timeout", "secret-timeout"),
		option.WithHeader("OpenAI-Organization", "late-org"),
		option.WithHeader("OpenAI-Project", "late-project"),
		option.WithHeader("X-Secret-Ambient", "late-secret"),
		option.WithHeader("Authorization", "Bearer late-attacker"),
		option.WithHeader("ChatGPT-Account-ID", "late-account"),
		option.WithHeader("X-OpenAI-Fedramp", "false"),
		option.WithHeader("originator", "codex_cli_rs"),
		option.WithHeader("User-Agent", "Codex CLI"),
	}
	if err := client.Post(context.Background(), "responses", []byte(`{}`), nil, poisoned...); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("transport did not receive a request")
	}
	if got := captured.URL.String(); got != BaseURL+"/responses" {
		t.Fatalf("URL = %q", got)
	}
	responsesHeaders := canonicalHeaders(map[string]string{
		"Accept":                  "text/event-stream",
		"Authorization":           "Bearer " + cred.accessToken,
		"ChatGPT-Account-ID":      "acct",
		"Content-Type":            "application/json",
		"X-OpenAI-Fedramp":        "true",
		"originator":              "mecatl",
		"User-Agent":              UserAgent,
		"X-Stainless-Retry-Count": "0",
	})
	assertExactHeaders(t, captured.Header, responsesHeaders)

	// The same exact policy applies to the second permitted surface. Keep the
	// query (owned by the later models lister) while pinning the fixed origin.
	captured = nil
	if err := client.Get(context.Background(), "models?client_version=dev", nil, nil, poisoned...); err != nil {
		t.Fatal(err)
	}
	if captured == nil || captured.URL.String() != BaseURL+"/models?client_version=dev" {
		t.Fatalf("models URL = %v", captured)
	}
	modelsHeaders := canonicalHeaders(map[string]string{
		"Accept":                  "application/json",
		"Authorization":           "Bearer " + cred.accessToken,
		"ChatGPT-Account-ID":      "acct",
		"X-OpenAI-Fedramp":        "true",
		"originator":              "mecatl",
		"User-Agent":              UserAgent,
		"X-Stainless-Retry-Count": "0",
	})
	assertExactHeaders(t, captured.Header, modelsHeaders)

	t.Run("false FedRAMP removes ambient header", func(t *testing.T) {
		nonFed, credErr := NewCredential(validJWT("acct", testNow.Add(time.Hour), false), "", "", testNow)
		if credErr != nil {
			t.Fatal(credErr)
		}
		var got *http.Request
		isolated, policyErr := NewRequestPolicy(nonFed, func() time.Time { return testNow }, roundTripFunc(func(req *http.Request) (*http.Response, error) {
			got = req.Clone(req.Context())
			return jsonResponse(http.StatusOK), nil
		}))
		if policyErr != nil {
			t.Fatal(policyErr)
		}
		isolatedClient := sdkClient(isolated)
		if err := isolatedClient.Get(context.Background(), "models?client_version=dev", nil, nil,
			option.WithHeader("X-OpenAI-Fedramp", "true")); err != nil {
			t.Fatal(err)
		}
		want := canonicalHeaders(map[string]string{
			"Accept":                  "application/json",
			"Authorization":           "Bearer " + nonFed.accessToken,
			"ChatGPT-Account-ID":      "acct",
			"originator":              "mecatl",
			"User-Agent":              UserAgent,
			"X-Stainless-Retry-Count": "0",
		})
		assertExactHeaders(t, got.Header, want)
	})

	t.Run("late endpoint override is refused", func(t *testing.T) {
		var calls atomic.Int32
		isolated, policyErr := NewRequestPolicy(cred, func() time.Time { return testNow }, roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return jsonResponse(http.StatusOK), nil
		}))
		if policyErr != nil {
			t.Fatal(policyErr)
		}
		isolatedClient := sdkClient(isolated)
		err := isolatedClient.Post(context.Background(), "responses", []byte(`{}`), nil, option.WithBaseURL("https://attacker.invalid/v1"))
		if err == nil || !strings.Contains(err.Error(), "refused endpoint override") {
			t.Fatalf("Post() error = %v", err)
		}
		if calls.Load() != 0 {
			t.Fatalf("transport calls = %d, want 0", calls.Load())
		}
	})

	t.Run("request target allowlist rejects deviations", func(t *testing.T) {
		tests := []struct {
			name   string
			method string
			rawURL string
			mutate func(*http.Request)
		}{
			{name: "host override", method: http.MethodPost, rawURL: BaseURL + "/responses", mutate: func(req *http.Request) { req.Host = "attacker.invalid" }},
			{name: "raw path", method: http.MethodPost, rawURL: BaseURL + "/responses", mutate: func(req *http.Request) { req.URL.RawPath = "/backend-api/codex/%72esponses" }},
			{name: "HTTP scheme", method: http.MethodPost, rawURL: "http://chatgpt.com/backend-api/codex/responses"},
			{name: "host with port", method: http.MethodPost, rawURL: "https://chatgpt.com:443/backend-api/codex/responses"},
			{name: "userinfo", method: http.MethodPost, rawURL: "https://user@chatgpt.com/backend-api/codex/responses"},
			{name: "fragment", method: http.MethodPost, rawURL: BaseURL + "/responses#fragment"},
			{name: "opaque", method: http.MethodPost, rawURL: BaseURL + "/responses", mutate: func(req *http.Request) { req.URL.Opaque = "opaque" }},
			{name: "wrong response method", method: http.MethodGet, rawURL: BaseURL + "/responses"},
			{name: "wrong path", method: http.MethodPost, rawURL: BaseURL + "/other"},
			{name: "response query", method: http.MethodPost, rawURL: BaseURL + "/responses?client_version=dev"},
			{name: "forced empty query", method: http.MethodPost, rawURL: BaseURL + "/responses", mutate: func(req *http.Request) { req.URL.ForceQuery = true }},
			{name: "models missing query", method: http.MethodGet, rawURL: BaseURL + "/models"},
			{name: "models empty version", method: http.MethodGet, rawURL: BaseURL + "/models?client_version="},
			{name: "models extra query", method: http.MethodGet, rawURL: BaseURL + "/models?client_version=dev&extra=value"},
			{name: "models duplicate version", method: http.MethodGet, rawURL: BaseURL + "/models?client_version=dev&client_version=other"},
			{name: "models trailing separator", method: http.MethodGet, rawURL: BaseURL + "/models?client_version=dev&"},
			{name: "models noncanonical key encoding", method: http.MethodGet, rawURL: BaseURL + "/models?%63lient_version=dev"},
			{name: "wrong models method", method: http.MethodPost, rawURL: BaseURL + "/models?client_version=dev"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				var calls atomic.Int32
				isolated, policyErr := NewRequestPolicy(cred, func() time.Time { return testNow }, roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return jsonResponse(http.StatusOK), nil
				}))
				if policyErr != nil {
					t.Fatal(policyErr)
				}
				req, requestErr := http.NewRequestWithContext(context.Background(), tc.method, tc.rawURL, nil)
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				if tc.mutate != nil {
					tc.mutate(req)
				}
				_, requestErr = isolated.RoundTrip(req)
				if requestErr == nil || !strings.Contains(requestErr.Error(), "refused endpoint override") {
					t.Fatalf("middleware() error = %v", requestErr)
				}
				if calls.Load() != 0 {
					t.Fatalf("transport calls = %d, want 0", calls.Load())
				}
			})
		}
	})

	t.Run("redirect is not followed", func(t *testing.T) {
		var calls atomic.Int32
		body := &trackingBody{Reader: strings.NewReader("redirect")}
		isolated, policyErr := NewRequestPolicy(cred, func() time.Time { return testNow }, roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": {"https://attacker.invalid/capture"}},
				Body:       body,
			}, nil
		}))
		if policyErr != nil {
			t.Fatal(policyErr)
		}
		isolatedClient := sdkClient(isolated)
		if err := isolatedClient.Post(context.Background(), "responses", []byte(`{}`), nil); err == nil {
			t.Fatal("Post() error = nil")
		}
		if calls.Load() != 1 {
			t.Fatalf("transport calls = %d, want 1", calls.Load())
		}
		if !body.closed.Load() {
			t.Fatal("redirect response body was not closed")
		}
	})
}

func TestRequestPolicyStatusMapping(t *testing.T) {
	cred, err := NewCredential(validJWT("acct", testNow.Add(time.Hour), false), "", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := &trackingBody{Reader: strings.NewReader(`{"error":{"message":"provider detail"}}`)}
			policy, policyErr := NewRequestPolicy(cred, func() time.Time { return testNow }, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
			}))
			if policyErr != nil {
				t.Fatal(policyErr)
			}
			client := sdkClient(policy)
			err := client.Post(context.Background(), "responses", []byte(`{}`), nil)
			if err == nil {
				t.Fatal("Post() error = nil")
			}
			if got := errorStatusCode(err); got != status {
				t.Fatalf("error %T status = %d, want %d", err, got, status)
			}
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				if !strings.Contains(err.Error(), "auth.yaml") || !strings.Contains(err.Error(), "restart") || strings.Contains(err.Error(), "provider detail") {
					t.Fatalf("manual-token error is not bounded/actionable: %q", err)
				}
			}
			if !body.closed.Load() {
				t.Fatal("response body was not closed")
			}
		})
	}

	t.Run("cancellation propagates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls atomic.Int32
		body := &trackingBody{Reader: strings.NewReader(`{}`)}
		policy, policyErr := NewRequestPolicy(cred, func() time.Time { return testNow }, roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			cancel()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
		}))
		if policyErr != nil {
			t.Fatal(policyErr)
		}
		client := sdkClient(policy)
		err := client.Post(ctx, "responses", []byte(`{}`), nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Post() error = %v, want context.Canceled", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("transport calls = %d, want 1", calls.Load())
		}
		if !body.closed.Load() {
			t.Fatal("response body was not closed after cancellation")
		}
	})
}

func TestSDKRetriesDisabled(t *testing.T) {
	cred, err := NewCredential(validJWT("acct", testNow.Add(time.Hour), false), "", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	policy, err := NewRequestPolicy(cred, func() time.Time { return testNow }, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonResponse(http.StatusInternalServerError), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	client := sdkClient(policy)
	_ = client.Post(context.Background(), "responses", []byte(`{}`), nil)
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want exactly 1", calls.Load())
	}
}

func TestADR_0104_OpenAICodexIsAdjunctOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, forbidden := range []string{"engine/port", "responses.New", "NewStreaming", "ResponseInputItem"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s contains forbidden successful-Responses/port surface %q", file, forbidden)
			}
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), file, data, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.IMPORT {
				continue
			}
			for _, spec := range gen.Specs {
				path := strings.Trim(spec.(*ast.ImportSpec).Path.Value, `"`)
				if path == "github.com/openai/openai-go/v3/responses" {
					t.Errorf("%s imports successful Responses implementation package", file)
				}
			}
		}
	}
}

func jsonResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}
}

func canonicalHeaders(values map[string]string) http.Header {
	header := make(http.Header, len(values))
	for key, value := range values {
		header.Set(key, value)
	}
	return header
}

func assertExactHeaders(t *testing.T, got, want http.Header) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("header key count = %d, want %d; got %v", len(got), len(want), got)
	}
	for key, wantValues := range want {
		gotValues := got.Values(key)
		if len(gotValues) != len(wantValues) {
			t.Errorf("header %s values = %q, want %q", key, gotValues, wantValues)
			continue
		}
		for i := range wantValues {
			if gotValues[i] != wantValues[i] {
				t.Errorf("header %s values = %q, want %q", key, gotValues, wantValues)
				break
			}
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected header %s=%q", key, got.Values(key))
		}
	}
}

func errorStatusCode(err error) int {
	var status interface{ StatusCode() int }
	if errors.As(err, &status) {
		return status.StatusCode()
	}
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

type trackingBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackingBody) Close() error { b.closed.Store(true); return nil }
