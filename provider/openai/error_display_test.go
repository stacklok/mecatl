package openai

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oai "github.com/openai/openai-go/v3"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// All canaries are synthetic; no recorded operator requests or credentials.
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
			err := collectStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)), port.LLMRequest{Model: "test", Messages: []session.Message{session.NewUserMessage("hi")}})
			for _, text := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", fmt.Errorf("terminal: %w", err))} {
				if strings.Contains(text, canary) {
					t.Errorf("unsafe display: %s", text)
				}
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
			want    session.RetryDisposition
			status  int
		}{
			{"Bearer synthetic-classification-canary", session.RetryDispositionRetryable, 503},
			{"input exceeds the context length: Bearer synthetic-classification-canary", session.RetryDispositionPermanent, 0},
		} {
			t.Run(mode+fmt.Sprint(tc.want), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if mode == "error" {
						fmt.Fprintf(w, "data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":%q}\n\n", tc.message)
					} else {
						fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":%q}}}\n\n", tc.message)
					}
				}))
				defer srv.Close()
				err := collectStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)), port.LLMRequest{Model: "test", Messages: []session.Message{session.NewUserMessage("hi")}})
				var classified *responseStreamError
				if !errors.As(err, &classified) || classified.RetryDisposition() != tc.want || classified.StatusCode() != tc.status {
					t.Fatalf("classification lost: %v", err)
				}
				if strings.Contains(err.Error(), "synthetic-classification-canary") {
					t.Fatalf("raw classification leaked: %v", err)
				}
			})
		}
	}
}

type displayVetoError struct{}

func (*displayVetoError) Error() string   { return "Bearer synthetic-transport-canary" }
func (*displayVetoError) Retryable() bool { return false }

type displayTransport struct{ err error }

func (r displayTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }
func TestTransportErrorDisplayPreservesCause(t *testing.T) {
	cause := &displayVetoError{}
	p := New(WithAPIKey("synthetic"), WithBaseURL("https://fixture.invalid"), WithHTTPClient(&http.Client{Transport: displayTransport{cause}}))
	err := collectStreamError(t, p, port.LLMRequest{Model: "test", Messages: []session.Message{session.NewUserMessage("hi")}})
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
