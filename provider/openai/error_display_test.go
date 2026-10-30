package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// All canaries are synthetic; no recorded operator requests or credentials.
func TestSDKStreamErrorProjection(t *testing.T) {
	const canary = "Bearer synthetic-envelope-canary"
	for _, tc := range []struct {
		name, payload, code, display string
		status                       int
		disposition                  session.RetryDisposition
	}{
		{"transient", `{"error":{"code":"server_error","message":"` + canary + `"}}`, "server_error", "503 Service Unavailable", 503, session.RetryDispositionRetryable},
		{"rate limit", `{"error":{"code":"rate_limit_exceeded"}}`, "rate_limit_exceeded", "429 Too Many Requests", 429, session.RetryDispositionRetryable},
		{"overflow message", `{"error":{"code":"server_error","message":"context length exceeded ` + canary + `"}}`, "server_error", "context window exceeded", 0, session.RetryDispositionPermanent},
		{"overflow param", `{"error":{"code":"server_error","param":"context length exceeded ` + canary + `"}}`, "server_error", "context window exceeded", 503, session.RetryDispositionPermanent},
		{"overflow code", `{"error":{"code":"context length exceeded ` + canary + `"}}`, "context length exceeded " + canary, "context window exceeded", 0, session.RetryDispositionPermanent},
		{"invalid request overflow message", `{"error":{"code":"invalid_request_error","message":"context length exceeded ` + canary + `"}}`, "invalid_request_error", "context window exceeded", 0, session.RetryDispositionPermanent},
		{"invalid request overflow param", `{"error":{"code":"invalid_request_error","param":"context length exceeded ` + canary + `"}}`, "invalid_request_error", "context window exceeded", 0, session.RetryDispositionPermanent},
		{"encrypted", `{"error":{"code":"invalid_encrypted_content"}}`, "invalid_encrypted_content", "invalid encrypted content", 0, session.RetryDispositionUnknown},
		{"invalid request", `{"error":{"code":"invalid_request_error"}}`, "invalid_request_error", "invalid request", 0, session.RetryDispositionUnknown},
		{"model", `{"error":{"code":"model_not_found"}}`, "model_not_found", "model not found", 0, session.RetryDispositionUnknown},
		{"quota", `{"error":{"code":"insufficient_quota"}}`, "insufficient_quota", "insufficient quota", 0, session.RetryDispositionUnknown},
		{"access denied", `{"error":{"code":"access_denied","message":"` + canary + `"}}`, "access_denied", "access denied", 0, session.RetryDispositionUnknown},
		{"unknown", `{"error":{"code":"` + canary + `","message":"` + canary + `"}}`, canary, "provider stream error event", 0, session.RetryDispositionUnknown},
		{"misleading text", `{"error":{"code":"opaque","message":"server_error timeout access_denied ` + canary + `"}}`, "opaque", "provider stream error event", 0, session.RetryDispositionUnknown},
		{"no code", `{"error":{"message":"` + canary + `"}}`, "", "provider stream error event", 0, session.RetryDispositionUnknown},
		{"string", `{"error":"` + canary + `"}`, "", "provider stream error event", 0, session.RetryDispositionUnknown},
		{"empty", `{"error":{}}`, "", "provider stream error event", 0, session.RetryDispositionUnknown},
		{"schema", `{"error":{"code":"server_error","message":[]}}`, "", "provider stream error event", 0, session.RetryDispositionUnknown},
		{"malformed", `{"error":{"code":"server_error"},`, "", "provider stream error event", 0, session.RetryDispositionUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\"}}\n\n")
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", canary, tc.payload)
			}))
			defer srv.Close()
			err := collectStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)), port.LLMRequest{Model: "test", Messages: []port.LLMMessage{{Role: session.RoleUser, Text: "hi"}}})
			var sdkErr *ssestream.StreamError
			var classified *responseStreamError
			if !errors.As(err, &sdkErr) || !errors.Is(err, sdkErr) {
				t.Fatalf("SDK event cause lost: %v", err)
			}
			if strings.TrimSpace(string(sdkErr.Event.Data)) != tc.payload {
				t.Fatalf("SDK event data = %q, want %q", sdkErr.Event.Data, tc.payload)
			}
			status := tc.status
			if tc.disposition == session.RetryDispositionPermanent {
				status = 0
			}
			if !errors.As(err, &classified) || classified.ProviderHTTPStatus() != 0 || classified.ProviderInBandStatus() != tc.status || classified.StatusCode() != status || classified.ProviderErrorCode() != tc.code || classified.RetryDisposition() != tc.disposition {
				t.Fatalf("classification lost: %v", err)
			}
			if classified.ProviderErrorCorrelationKind() != "response" || classified.ProviderErrorCorrelationID() != "resp_fixture" {
				t.Fatal("typed response correlation lost")
			}
			if !strings.Contains(err.Error(), tc.display) || strings.Contains(fmt.Sprintf("%+v", fmt.Errorf("terminal: %w", err)), canary) {
				t.Fatalf("unsafe or unhelpful display: %v", err)
			}
			if isInvalidEncryptedContent(err) {
				t.Fatal("HTTP 200 in-band error must not unlock encrypted repair")
			}
		})
	}
}

func TestSDKJSONFailureDisplay(t *testing.T) {
	for _, tc := range []struct {
		name, payload, display string
	}{
		{"syntax", `{"type": "synthetic-json-canary",`, "invalid JSON response"},
		{"schema", `[]`, "unexpected JSON response schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", tc.payload)
			}))
			defer srv.Close()
			err := collectStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)), port.LLMRequest{Model: "test", Messages: []port.LLMMessage{{Role: session.RoleUser, Text: "hi"}}})
			if !strings.Contains(err.Error(), tc.display) || strings.Contains(err.Error(), "synthetic-json-canary") {
				t.Fatalf("decode display = %v", err)
			}
			var syntaxErr *json.SyntaxError
			var schemaErr *json.UnmarshalTypeError
			if !errors.As(err, &syntaxErr) && !errors.As(err, &schemaErr) {
				t.Fatalf("decode cause lost: %v", err)
			}
		})
	}
}

func TestTypedFailureDisplay(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{io.EOF, "response ended unexpectedly"},
		{io.ErrUnexpectedEOF, "response ended unexpectedly"},
		{context.DeadlineExceeded, "deadline exceeded"},
		{context.Canceled, "request canceled"},
		{&net.DNSError{Err: "synthetic-network-canary", Name: "synthetic-network-canary", IsTimeout: true}, "network timeout"},
		{&net.DNSError{Err: "synthetic-network-canary", Name: "synthetic-network-canary"}, "network error"},
		{&json.UnmarshalTypeError{Value: "synthetic-schema-canary", Field: "synthetic-schema-canary"}, "unexpected JSON response schema"},
		{&displayVetoError{}, "provider request failed"},
		{errors.New("server_error timeout access_denied context length exceeded synthetic-opaque-canary"), "provider request failed"},
	} {
		err := withHTTPErrorMetadata(fmt.Errorf("synthetic-wrapper-canary: %w", tc.cause), "")
		if !errors.Is(err, tc.cause) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "canary") {
			t.Fatalf("typed failure display = %v", err)
		}
		if tc.want == "provider request failed" && err.Error() != tc.want {
			t.Fatalf("opaque cause assigned a guessed category: %v", err)
		}
	}
}

func TestProviderErrorDisplayIsClosed(t *testing.T) {
	const canary = "Bearer synthetic-display-canary"
	for _, mode := range []string{"http", "error", "response.failed", "response.incomplete"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if mode == "http" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprintf(w, `{"error":{"message":%q,"code":%q,"type":%q}}`, canary, canary, canary)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch mode {
				case "error":
					fmt.Fprintf(w, "data: {\"type\":\"error\",\"message\":%q,\"code\":%q,\"param\":%q}\n\n", canary, canary, canary)
				case "response.failed":
					fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":%q,\"code\":%q}}}\n\n", canary, canary)
				default:
					fmt.Fprintf(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":%q}}}\n\n", canary)
				}
			}))
			defer srv.Close()
			err := collectStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)), port.LLMRequest{Model: "test", Messages: []port.LLMMessage{userMessage("hi")}})
			for _, text := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", fmt.Errorf("terminal: %w", err))} {
				if strings.Contains(text, canary) {
					t.Errorf("unsafe display: %s", text)
				}
			}
			if (mode == "error" || mode == "response.failed") && err.Error() != "provider request failed: provider stream error event" {
				t.Fatalf("stream origin lost: %v", err)
			}
			if mode == "http" {
				var sdkErr *oai.Error
				if !errors.As(err, &sdkErr) || sdkErr.StatusCode != 503 || sdkErr.Message != canary || !errors.Is(err, sdkErr) {
					t.Fatal("SDK cause lost")
				}
			}
		})
	}
}

func TestResponseStreamErrorKnownCategories(t *testing.T) {
	for code, want := range map[string]string{
		"content_filter":            "content filter blocked the response",
		"content_policy_violation":  "content filter blocked the response",
		"context_length_exceeded":   "context window exceeded",
		"invalid_request_error":     "invalid request",
		"invalid_prompt":            "invalid request",
		"invalid_encrypted_content": "invalid encrypted content",
	} {
		t.Run(code, func(t *testing.T) {
			err := &responseStreamError{msg: "Bearer synthetic-category-canary", metadata: providerErrorMetadata{providerCode: code}}
			if err.Error() != "provider request failed: "+want {
				t.Fatalf("category = %q", err.Error())
			}
			if err.RetryDisposition() != session.RetryDispositionUnknown {
				t.Fatal("display text changed classification")
			}
		})
	}
}

func TestInBandErrorDisplayPreservesRawClassification(t *testing.T) {
	for _, mode := range []string{"error", "response.failed"} {
		for _, tc := range []struct {
			message string
			code    string
			param   string
			want    session.RetryDisposition
			status  int
		}{
			{"Bearer synthetic-classification-canary", "server_error", "", session.RetryDispositionRetryable, 503},
			{"input exceeds the context length: Bearer synthetic-classification-canary", "server_error", "", session.RetryDispositionPermanent, 0},
			{"Bearer synthetic-classification-canary", "context length exceeded: synthetic-classification-canary", "", session.RetryDispositionPermanent, 0},
			{"Bearer synthetic-classification-canary", "server_error", "context length exceeded: synthetic-classification-canary", session.RetryDispositionPermanent, 0},
		} {
			if mode == "response.failed" && tc.param != "" {
				continue // only top-level errors carry param
			}
			t.Run(mode+fmt.Sprint(tc.want), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if mode == "error" {
						fmt.Fprintf(w, "data: {\"type\":\"error\",\"code\":%q,\"message\":%q,\"param\":%q}\n\n", tc.code, tc.message, tc.param)
					} else {
						fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":%q,\"message\":%q}}}\n\n", tc.code, tc.message)
					}
				}))
				defer srv.Close()
				err := collectStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)), port.LLMRequest{Model: "test", Messages: []port.LLMMessage{userMessage("hi")}})
				var classified *responseStreamError
				if !errors.As(err, &classified) || classified.RetryDisposition() != tc.want || classified.StatusCode() != tc.status || classified.ProviderHTTPStatus() != 0 || classified.ProviderInBandStatus() != providerErrorStatus(tc.code, tc.message) {
					t.Fatalf("classification lost: %v", err)
				}
				if strings.Contains(err.Error(), "synthetic-classification-canary") {
					t.Fatalf("raw classification leaked: %v", err)
				}
			})
		}
	}
}

func TestResponseBodyErrorDisplayPreservesCause(t *testing.T) {
	cause := &displayVetoError{}
	p := New(WithAPIKey("synthetic"), WithBaseURL("https://fixture.invalid"), WithHTTPClient(&http.Client{Transport: displayBodyTransport{cause}}))
	seq, err := p.Stream(context.Background(), port.LLMRequest{Model: "test", Messages: []port.LLMMessage{userMessage("hi")}})
	if err != nil {
		t.Fatalf("stream establishment failed: %v", err)
	}
	var text string
	for chunk, streamErr := range seq {
		if chunk.Kind == port.ChunkText {
			text += chunk.Text
		}
		if streamErr != nil {
			err = streamErr
		}
	}
	if text != "hello" || !errors.Is(err, cause) {
		t.Fatalf("body error did not follow valid SSE text: text=%q, err=%v", text, err)
	}
	var veto interface{ Retryable() bool }
	if !errors.As(err, &veto) || veto.Retryable() {
		t.Fatal("body read retry veto lost")
	}
	if strings.Contains(fmt.Sprintf("%+v", err), "synthetic-transport-canary") {
		t.Fatalf("unsafe body read display: %v", err)
	}
}

type displayBodyTransport struct{ cause *displayVetoError }

func (r displayBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	const event = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Request: req,
		Body: io.NopCloser(io.MultiReader(strings.NewReader(event), r.cause))}, nil
}

type displayVetoError struct{}

func (e *displayVetoError) Read([]byte) (int, error) { return 0, e }

func (*displayVetoError) Error() string   { return "Bearer synthetic-transport-canary" }
func (*displayVetoError) Retryable() bool { return false }

type displayTransport struct{ err error }

func (r displayTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }
func TestTransportErrorDisplayPreservesCause(t *testing.T) {
	cause := &displayVetoError{}
	p := New(WithAPIKey("synthetic"), WithBaseURL("https://fixture.invalid"), WithHTTPClient(&http.Client{Transport: displayTransport{cause}}))
	err := collectStreamError(t, p, port.LLMRequest{Model: "test", Messages: []port.LLMMessage{userMessage("hi")}})
	var veto interface{ Retryable() bool }
	if !errors.As(err, &veto) || veto.Retryable() {
		t.Fatal("explicit retry veto lost")
	}
	if !errors.Is(err, cause) {
		t.Fatal("transport cause lost")
	}
	if strings.Contains(fmt.Sprintf("%+v", err), "synthetic-transport-canary") {
		t.Fatalf("unsafe transport display: %v", err)
	}
}
