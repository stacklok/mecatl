package openaichat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

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

func TestProviderErrorMetadataHTTPPreservesSDKError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "req_409")
		w.Header().Set("X-Secret", "must-not-leak")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_request_error","message":"invalid input","raw_secret":"must-not-leak"}}`)
	}))
	defer srv.Close()

	provider := New(
		WithAPIKey("test-key"),
		WithBaseURL(srv.URL+"/v1"),
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

	var preserved *oai.Error
	if !errors.As(streamErr, &preserved) {
		t.Fatal("errors.As did not preserve the original SDK error")
	}
	if got, want := streamErr.Error(), "invalid_request_error: invalid input"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if strings.Contains(streamErr.Error(), "req_409") || strings.Contains(streamErr.Error(), "must-not-leak") {
		t.Fatalf("display error leaked request metadata or raw body: %q", streamErr)
	}
	got := metadataOf(t, streamErr)
	want := providerMetadata{
		httpStatus:      http.StatusBadRequest,
		code:            "invalid_request_error",
		correlationKind: "request",
		correlationID:   "req_409",
	}
	if got != want {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
	if strings.Contains(got.correlationID, "must-not-leak") {
		t.Fatal("metadata leaked an arbitrary response header")
	}
}

func TestProviderErrorMetadataPrefersRequestID(t *testing.T) {
	apiErr := &oai.Error{
		Code: "rate_limit_exceeded",
		Response: &http.Response{Header: http.Header{
			"X-Request-Id": {"req_preferred"},
		}},
	}
	err := openaichatStreamErr(apiErr, "original stream error", "chatcmpl_fallback")
	metadata := metadataOf(t, err)
	if metadata.correlationKind != "request" || metadata.correlationID != "req_preferred" {
		t.Fatalf("correlation = %q/%q, want request/req_preferred", metadata.correlationKind, metadata.correlationID)
	}
}

func TestProviderErrorMetadataInBandUsesCompletionFallback(t *testing.T) {
	var st streamState
	if _, err := translate(oai.ChatCompletionChunk{ID: "chatcmpl_409"}, &st); err != nil {
		t.Fatalf("translate completion ID: %v", err)
	}
	apiErr := &oai.Error{Code: "rate_limit_exceeded", Message: "body must-not-leak"}
	err := openaichatStreamErr(apiErr, "original stream error", st.completionID)

	want := providerMetadata{
		inBandStatus:    http.StatusTooManyRequests,
		code:            "rate_limit_exceeded",
		correlationKind: "completion",
		correlationID:   "chatcmpl_409",
	}
	got := metadataOf(t, err)
	if got != want {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want %d", got, http.StatusTooManyRequests)
	}
	if strings.Contains(got.correlationID, "must-not-leak") ||
		strings.Contains(got.code, "must-not-leak") {
		t.Fatal("metadata leaked provider error message/body")
	}
}
