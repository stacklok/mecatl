// Package modelhttp contains the bounded, protocol-neutral HTTP GET mechanics
// shared by live model-catalog adapters. JSON decoding, endpoint policy,
// headers, redirects, and field sanitization remain owned by each adapter.
package modelhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultTimeout bounds live model-catalog HTTP requests when an adapter does
// not inject its own client.
const DefaultTimeout = 5 * time.Second

// StatusError reports a non-2xx response without retaining or rendering its
// potentially sensitive body.
type StatusError struct {
	Source string
	Code   int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: unexpected status %d", e.Source, e.Code)
}

// StatusCode is the narrow cross-adapter classification seam used by
// composition. It avoids teaching the shared live-listing pipeline every
// adapter's concrete error type.
func (e *StatusError) StatusCode() int { return e.Code }

// DefaultClient returns client unchanged when supplied. Otherwise it creates a
// client with the shared catalog timeout and the adapter-owned redirect policy.
func DefaultClient(client *http.Client, redirect func(*http.Request, []*http.Request) error) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: DefaultTimeout, CheckRedirect: redirect}
}

// Get performs one context-aware GET and returns a bounded successful body.
// prepare owns protocol-specific headers and is invoked before the request is
// sent. The response body is always closed.
func Get(
	ctx context.Context,
	client *http.Client,
	rawURL string,
	source string,
	maxBytes int64,
	prepare func(*http.Request),
) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("%s: nil HTTP client", source)
	}
	if maxBytes < 1 {
		return nil, fmt.Errorf("%s: invalid response cap", source)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", source, err)
	}
	if prepare != nil {
		prepare(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: fetch models: %w", source, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &StatusError{Source: source, Code: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", source, err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%s: response exceeds %d-byte cap", source, maxBytes)
	}
	return body, nil
}
