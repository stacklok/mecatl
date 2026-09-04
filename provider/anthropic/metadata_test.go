package anthropic

import (
	"context"
	"encoding/json"
	"errors"
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

type providerMetadataCarrier interface {
	error
	ProviderHTTPStatus() int
	ProviderInBandStatus() int
	ProviderErrorCode() string
	ProviderErrorCorrelationKind() string
	ProviderErrorCorrelationID() string
}

type providerMetadata struct {
	httpStatus, inBandStatus             int
	code, correlationKind, correlationID string
}

func metadataOf(t *testing.T, err error) providerMetadata {
	t.Helper()
	var carrier providerMetadataCarrier
	if !errors.As(err, &carrier) {
		t.Fatalf("error %T does not expose provider metadata", err)
	}
	return providerMetadata{
		httpStatus: carrier.ProviderHTTPStatus(), inBandStatus: carrier.ProviderInBandStatus(),
		code: carrier.ProviderErrorCode(), correlationKind: carrier.ProviderErrorCorrelationKind(),
		correlationID: carrier.ProviderErrorCorrelationID(),
	}
}

func mustStreamEvent(t *testing.T, raw string) sdk.MessageStreamEventUnion {
	t.Helper()
	var event sdk.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("unmarshal stream event: %v", err)
	}
	return event
}

func TestProviderErrorMetadataSSERequestIDPrecedesMessageID(t *testing.T) {
	var st streamState
	start := mustStreamEvent(t, `{"type":"message_start","message":{"id":"msg_fallback","usage":{}}}`)
	if _, err := translate(start, &st); err != nil {
		t.Fatalf("translate message_start: %v", err)
	}
	event := mustStreamEvent(t, `{"type":"error","request_id":"req_409","error":{"type":"overloaded_error","message":"body must-not-leak"},"extra":"must-not-leak"}`)
	_, err := translate(event, &st)
	if err == nil {
		t.Fatal("expected stream error")
	}

	got := metadataOf(t, err)
	want := providerMetadata{
		inBandStatus:    http.StatusServiceUnavailable,
		code:            "overloaded_error",
		correlationKind: "request",
		correlationID:   "req_409",
	}
	if got != want {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
	if strings.Contains(got.correlationID, "must-not-leak") ||
		strings.Contains(got.code, "must-not-leak") {
		t.Fatal("metadata leaked event message/body")
	}
}

func TestProviderErrorMetadataSSEUsesMessageFallback(t *testing.T) {
	var st streamState
	start := mustStreamEvent(t, `{"type":"message_start","message":{"id":"msg_409","usage":{}}}`)
	if _, err := translate(start, &st); err != nil {
		t.Fatalf("translate message_start: %v", err)
	}
	event := mustStreamEvent(t, `{"type":"error","error":{"type":"rate_limit_error","message":"limited"}}`)
	_, err := translate(event, &st)
	if err == nil {
		t.Fatal("expected stream error")
	}
	got := metadataOf(t, err)
	want := providerMetadata{
		inBandStatus:    http.StatusTooManyRequests,
		code:            "rate_limit_error",
		correlationKind: "message",
		correlationID:   "msg_409",
	}
	if got != want {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
	type statusCoder interface{ StatusCode() int }
	var status statusCoder
	if !errors.As(err, &status) || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %v, want %d", status, http.StatusTooManyRequests)
	}
}

func TestProviderErrorMetadataHTTPPreservesSDKError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Request-ID", "req_http_409")
		w.Header().Set("X-Secret", "must-not-leak")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"credentials are invalid"},"raw_secret":"must-not-leak"}`)
	}))
	defer srv.Close()

	provider := New(
		WithAPIKey("test-key"),
		WithBaseURL(srv.URL),
		WithRequestOption(option.WithMaxRetries(0)),
	)
	seq, err := provider.Stream(context.Background(), port.LLMRequest{
		Model: "test-model", Messages: []session.Message{session.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var streamErr error
	for _, err := range seq {
		if err != nil {
			streamErr = err
			break
		}
	}
	if streamErr == nil {
		t.Fatal("expected SDK HTTP error")
	}

	var preserved *sdk.Error
	if !errors.As(streamErr, &preserved) {
		t.Fatal("errors.As did not preserve the original SDK error")
	}
	if got, want := streamErr.Error(), "authentication_error: credentials are invalid"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if strings.Contains(streamErr.Error(), "req_http_409") || strings.Contains(streamErr.Error(), "must-not-leak") {
		t.Fatalf("display error leaked request metadata or raw body: %q", streamErr)
	}
	got := metadataOf(t, streamErr)
	want := providerMetadata{
		httpStatus:      http.StatusUnauthorized,
		code:            "authentication_error",
		correlationKind: "request",
		correlationID:   "req_http_409",
	}
	if got != want {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
	if strings.Contains(got.code, "must-not-leak") {
		t.Fatal("metadata leaked SDK error body")
	}
}
