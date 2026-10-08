package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestProviderErrorDisplayIsClosed(t *testing.T) {
	const canary = "Bearer synthetic-display-canary"
	for _, inBand := range []bool{false, true} {
		t.Run(fmt.Sprint(inBand), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := fmt.Sprintf(`{"type":"error","error":{"message":%q,"code":%q,"type":%q}}`, canary, canary, canary)
				if inBand {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)
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
			var sdkErr *sdk.Error
			wantStatus := http.StatusServiceUnavailable
			if inBand {
				wantStatus = http.StatusOK
			}
			if !errors.As(err, &sdkErr) || sdkErr.StatusCode != wantStatus || !strings.Contains(sdkErr.RawJSON(), canary) || !errors.Is(err, sdkErr) {
				t.Fatal("SDK cause lost")
			}
		})
	}
}

func TestSDKInBandErrorRetainsSafeCategory(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{"overloaded_error", "provider request failed (503 Service Unavailable)"},
		{"Bearer synthetic-kind-canary", "provider request failed"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":%q,\"message\":\"Bearer synthetic-message-canary\"}}\n\n", tc.kind)
			}))
			defer srv.Close()
			err := displayStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)))
			if got, want := err.Error(), tc.want+" (target: "+srv.URL+"/v1/messages)"; got != want {
				t.Fatalf("Error() = %q, want %q", got, want)
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
		kind    string
		want    session.RetryDisposition
	}{
		{"Bearer synthetic-classification-canary", "api_error", session.RetryDispositionRetryable},
		{"input exceeds the context length: Bearer synthetic-classification-canary", "api_error", session.RetryDispositionPermanent},
		{"Bearer synthetic-classification-canary", "context length exceeded: synthetic-classification-canary", session.RetryDispositionPermanent},
	} {
		t.Run(fmt.Sprint(tc.want), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`, tc.kind, tc.message)
			}))
			defer srv.Close()
			err := displayStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)))
			var classified *anthropicStreamError
			if !errors.As(err, &classified) || classified.RetryDisposition() != tc.want || classified.StatusCode() != 503 {
				t.Fatalf("classification lost: %v", err)
			}
			if strings.Contains(err.Error(), "synthetic-classification-canary") {
				t.Fatalf("raw classification leaked: %v", err)
			}
		})
	}
}

func TestResponseBodyErrorDisplayPreservesCause(t *testing.T) {
	cause := &displayVetoError{}
	p := New(WithAPIKey("synthetic"), WithBaseURL("https://fixture.invalid"), WithRequestOption(option.WithHTTPClient(&http.Client{Transport: displayBodyTransport{cause}})))
	seq, err := p.Stream(context.Background(), port.LLMRequest{Model: "test", Messages: []session.Message{session.NewUserMessage("hi")}})
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
	const event = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Request: req,
		Body: io.NopCloser(io.MultiReader(strings.NewReader(event), r.cause))}, nil
}

type displayVetoError struct{}

func (e *displayVetoError) Read([]byte) (int, error) { return 0, e }

func (*displayVetoError) Error() string   { return "Bearer synthetic-transport-canary" }
func (*displayVetoError) Retryable() bool { return false }

type displayTransport struct{ err error }

func (r displayTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }
func TestListerTransportErrorDisplayPreservesCause(t *testing.T) {
	cause := &displayVetoError{}
	_, err := NewLister("synthetic", "https://fixture.invalid", &http.Client{Transport: displayTransport{cause}}).ListModels(context.Background())
	if !errors.Is(err, cause) {
		t.Fatal("transport cause lost")
	}
	if strings.Contains(fmt.Sprintf("%+v", err), "synthetic-transport-canary") {
		t.Fatalf("unsafe lister display: %v", err)
	}
}

func TestTransportErrorDisplayPreservesCause(t *testing.T) {
	cause := &displayVetoError{}
	p := New(WithAPIKey("synthetic"), WithBaseURL("https://fixture.invalid"), WithRequestOption(option.WithHTTPClient(&http.Client{Transport: displayTransport{cause}})))
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
