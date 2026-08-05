package openaicodex

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/openai/openai-go/v3/option"
)

const (
	// BaseURL is the one endpoint permitted by the manual-token provider.
	BaseURL = "https://chatgpt.com/backend-api/codex"
	// UserAgent identifies mecatl honestly; it deliberately does not imitate the
	// Codex CLI.
	UserAgent = "mecatl"
)

var errInvalidRequestPolicy = errors.New("openai-codex: invalid request policy")

// RequestPolicy owns the exact endpoint, headers, request-time expiry check,
// redirect refusal, and SDK retry setting shared by inference and model listing.
type RequestPolicy struct {
	credential Credential
	now        func() time.Time
	client     *http.Client
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
	return RequestPolicy{
		credential: credential,
		now:        now,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Options returns the OpenAI SDK construction options. These are intended to
// ride provider/openai.WithRequestOption so the existing Responses
// implementation remains the sole LLM adapter.
func (p RequestPolicy) Options() []option.RequestOption {
	return []option.RequestOption{
		option.WithAPIKey(p.credential.accessToken),
		option.WithBaseURL(BaseURL),
		option.WithHTTPClient(p.client),
		option.WithMaxRetries(0),
		option.WithMiddleware(p.middleware),
	}
}

func (p RequestPolicy) middleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	if err := p.credential.Validate(p.now()); err != nil {
		return nil, err
	}
	if !allowedRequest(req) {
		return nil, errors.New("openai-codex: refused endpoint override")
	}

	// Rebuild the endpoint's complete header set from owned constants. Copying
	// even apparently harmless SDK headers would let OPENAI_CUSTOM_HEADERS or a
	// late request option smuggle arbitrary values through the policy.
	clean := make(http.Header)
	if req.URL.Path == "/backend-api/codex/responses" {
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
	req.Header = clean

	resp, err := next(req)
	if contextErr := req.Context().Err(); contextErr != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, contextErr
	}
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, &StatusError{status: resp.StatusCode}
	}
	if resp != nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, errors.New("openai-codex: redirect refused")
	}
	return resp, nil
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
