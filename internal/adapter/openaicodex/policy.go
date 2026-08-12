package openaicodex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	// BaseURL is the one endpoint permitted by the manual-token provider.
	BaseURL = "https://chatgpt.com/backend-api/codex"
	// UserAgent identifies mecatl honestly; it deliberately does not imitate the
	// Codex CLI.
	UserAgent = "mecatl"
)

var errInvalidRequestPolicy = errors.New("openai-codex: invalid request policy")

// RequestPolicy is the final HTTP transport shared by inference and model
// listing. It owns the exact endpoint, headers, request-time expiry check,
// redirect refusal, and bounded authentication errors. SDK retry configuration
// remains at the provider construction boundary because a transport cannot
// disable an SDK retry loop.
type RequestPolicy struct {
	credential Credential
	now        func() time.Time
	base       http.RoundTripper
}

// NewRequestPolicy creates an immutable policy. transport is an offline-test
// seam; nil uses http.DefaultTransport. The returned client never follows a
// redirect and deliberately has no blanket timeout because inference streams.
func NewRequestPolicy(credential Credential, now func() time.Time, transport http.RoundTripper) (RequestPolicy, error) {
	if now == nil {
		return RequestPolicy{}, errInvalidRequestPolicy
	}
	if credential.accessToken == "" || !validAccountID(credential.accountID) || credential.expiresAt.IsZero() {
		return RequestPolicy{}, errInvalidRequestPolicy
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	return RequestPolicy{credential: credential, now: now, base: transport}, nil
}

// HTTPClient returns an inference-safe client over this policy. It deliberately
// has no blanket timeout because a streaming response may run for minutes. The
// lister builds its own bounded client over the same immutable transport.
func (p RequestPolicy) HTTPClient() *http.Client {
	return &http.Client{
		Transport: p,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// RoundTrip validates and clones req before placing credential material on the
// clone at the final network boundary. The caller's request remains untouched.
func (p RequestPolicy) RoundTrip(req *http.Request) (*http.Response, error) {
	clone, err := p.authorizedRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := p.base.RoundTrip(clone)
	return normalizePolicyResponse(clone.Context(), resp, err)
}

func (p RequestPolicy) authorizedRequest(req *http.Request) (*http.Request, error) {
	if req == nil {
		return nil, errors.New("openai-codex: refused endpoint override")
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if !allowedRequest(req) {
		return nil, errors.New("openai-codex: refused endpoint override")
	}
	if err := p.credential.Validate(p.now()); err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())

	// Rebuild the endpoint's complete header set from owned constants. Copying
	// even apparently harmless SDK headers would let OPENAI_CUSTOM_HEADERS or a
	// late request option smuggle arbitrary values through the policy.
	clean := make(http.Header)
	if clone.URL.Path == "/backend-api/codex/responses" {
		clean.Set("Accept", "text/event-stream")
		clean.Set("Content-Type", "application/json")
	} else {
		clean.Set("Accept", "application/json")
	}
	clean.Set("X-Stainless-Retry-Count", "0")
	clean.Set("Authorization", "Bearer "+p.credential.accessToken)
	clean.Set("ChatGPT-Account-ID", p.credential.accountID)
	if p.credential.fedRAMP {
		clean.Set("X-OpenAI-Fedramp", "true")
	}
	clean.Set("originator", "mecatl")
	clean.Set("User-Agent", UserAgent)
	clone.Header = clean
	return clone, nil
}

func normalizePolicyResponse(ctx context.Context, resp *http.Response, err error) (*http.Response, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		closePolicyResponse(resp)
		return nil, contextErr
	}
	if err != nil {
		closePolicyResponse(resp)
		return nil, err
	}
	if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		closePolicyResponse(resp)
		return nil, &StatusError{status: resp.StatusCode}
	}
	if resp != nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		closePolicyResponse(resp)
		return nil, errors.New("openai-codex: redirect refused")
	}
	if resp == nil {
		return nil, errors.New("openai-codex: empty transport response")
	}
	return resp, nil
}

func closePolicyResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func allowedRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	target := req.URL
	if target == nil || req.Host != "" || target.Scheme != "https" || target.Host != "chatgpt.com" || target.User != nil ||
		target.Fragment != "" || target.Opaque != "" || target.RawPath != "" || target.ForceQuery {
		return false
	}
	if req.Method == http.MethodPost && target.Path == "/backend-api/codex/responses" {
		return target.RawQuery == ""
	}
	if req.Method != http.MethodGet || target.Path != "/backend-api/codex/models" {
		return false
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil || len(query) != 1 {
		return false
	}
	versions, ok := query["client_version"]
	return ok && len(versions) == 1 && versions[0] != "" && query.Encode() == target.RawQuery
}

// StatusError is the bounded manual-token authentication error. Its status is
// retained for the shared resilience classifier while provider body content,
// request metadata, and credential values are intentionally omitted.
type StatusError struct{ status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("openai-codex: manual access token was rejected (HTTP %d); replace it in auth.yaml and restart mecatl", e.status)
}

// StatusCode preserves the generic classifier seam used by llmresilience.
func (e *StatusError) StatusCode() int { return e.status }
