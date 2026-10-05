package openaichat

import (
	"context"
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

func TestProviderErrorDisplayIsClosed(t *testing.T) {
	const canary = "Bearer synthetic-display-canary"
	for _, inBand := range []bool{false, true} {
		t.Run(fmt.Sprint(inBand), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := fmt.Sprintf(`{"error":{"message":%q,"code":%q,"type":%q}}`, canary, canary, canary)
				if inBand {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\n", body)
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, body)
				}
			}))
			defer srv.Close()
			err := displayStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)))
			for _, text := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", fmt.Errorf("terminal: %w", err))} {
				if strings.Contains(text, canary) {
					t.Errorf("unsafe display: %s", text)
				}
			}
			if inBand {
				// The Chat SDK returns an untyped error for SSE error envelopes.
				cause := errors.Unwrap(err)
				if cause == nil || !strings.Contains(cause.Error(), canary) || !errors.Is(err, cause) {
					t.Fatal("SDK stream cause lost")
				}
			} else {
				var sdkErr *oai.Error
				if !errors.As(err, &sdkErr) || sdkErr.Message != canary || sdkErr.StatusCode != 503 || !errors.Is(err, sdkErr) {
					t.Fatal("SDK HTTP cause lost")
				}
			}
		})
	}
}

func displayStreamError(t *testing.T, p *Provider) error {
	t.Helper()
	seq, err := p.Stream(context.Background(), port.LLMRequest{Model: "test", Messages: []session.Message{session.NewUserMessage("hi")}})
	if err != nil {
		return err
	}
	for _, err := range seq {
		if err != nil {
			return err
		}
	}
	t.Fatal("expected stream failure")
	return nil
}

func TestHTTPErrorDisplayPreservesRawClassification(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    session.RetryDisposition
	}{
		{"Bearer synthetic-classification-canary", session.RetryDispositionRetryable},
		{"input exceeds the context length: Bearer synthetic-classification-canary", session.RetryDispositionPermanent},
	} {
		t.Run(fmt.Sprint(tc.want), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintf(w, `{"error":{"code":"server_error","message":%q}}`, tc.message)
			}))
			defer srv.Close()
			err := displayStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)))
			var classified *openaichatStreamError
			if !errors.As(err, &classified) || classified.RetryDisposition() != tc.want || classified.StatusCode() != 503 {
				t.Fatalf("classification lost: %v", err)
			}
			if strings.Contains(err.Error(), "synthetic-classification-canary") {
				t.Fatalf("raw classification leaked: %v", err)
			}
		})
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
	err := displayStreamError(t, p)
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
