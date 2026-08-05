package anthropic

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/stacklok/mecatl/engine/port"
)

// recordingRoundTripper captures the first outgoing HTTP request and then aborts
// (returns an error) so no real network call is made. The captured request lets a
// test assert the host, headers, and credentials the adapter actually issues.
type recordingRoundTripper struct {
	req *http.Request
}

var errAbort = errors.New("aborted by recorder")

func (r *recordingRoundTripper) Do(req *http.Request) (*http.Response, error) {
	r.req = req
	return nil, errAbort
}

// TestNewSuppressesAmbientEnv proves the adapter passes
// option.WithoutEnvironmentDefaults: with ANTHROPIC_BASE_URL and
// ANTHROPIC_AUTH_TOKEN set in the process env, a request issues to
// api.anthropic.com (NOT the env base URL) with x-api-key and NO ambient
// auth-token header — the harness's single-knob custody is not bypassed.
func TestNewSuppressesAmbientEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://attacker.example")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-token-should-not-be-sent")

	rec := &recordingRoundTripper{}
	p := New(WithAPIKey("k"), WithRequestOption(option.WithHTTPClient(rec)))

	stream, err := p.Stream(context.Background(), port.LLMRequest{Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// Drain so the request is issued (the recorder aborts before any network I/O).
	for range stream {
	}

	if rec.req == nil {
		t.Fatal("no request was issued to the recorder")
	}
	if got := rec.req.URL.Host; got != "api.anthropic.com" {
		t.Fatalf("request host = %q, want api.anthropic.com (ambient ANTHROPIC_BASE_URL must be ignored)", got)
	}
	if got := rec.req.Header.Get("x-api-key"); got != "k" {
		t.Fatalf("x-api-key = %q, want the explicit harness key 'k'", got)
	}
	// The ambient auth token must NOT ride as an authorization/x-api-key header.
	if auth := rec.req.Header.Get("Authorization"); auth != "" {
		t.Fatalf("Authorization header present (%q); ambient ANTHROPIC_AUTH_TOKEN leaked", auth)
	}
	if xk := rec.req.Header.Get("x-api-key"); xk == "ambient-token-should-not-be-sent" {
		t.Fatal("ambient ANTHROPIC_AUTH_TOKEN was used as the key")
	}
}

// TestWithBaseURLOverride proves the EXPLICIT flag base URL (the single knob) IS
// honoured — the suppression is of AMBIENT env, not of the operator's flag.
func TestWithBaseURLOverride(t *testing.T) {
	rec := &recordingRoundTripper{}
	p := New(WithAPIKey("k"), WithBaseURL("https://gateway.internal/anthropic/"),
		WithRequestOption(option.WithHTTPClient(rec)))

	stream, err := p.Stream(context.Background(), port.LLMRequest{Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream {
	}
	if rec.req == nil {
		t.Fatal("no request issued")
	}
	if got := rec.req.URL.Host; got != "gateway.internal" {
		t.Fatalf("request host = %q, want gateway.internal (explicit --anthropic-base-url honoured)", got)
	}
}
