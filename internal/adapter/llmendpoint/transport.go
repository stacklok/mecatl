package llmendpoint

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// BearerSource supplies deployment-scoped gateway credentials. Refresh receives
// the rejected token so concurrent 401s can reuse a token another caller already
// refreshed instead of rotating it again.
type BearerSource interface {
	Token(context.Context) (string, error)
	Refresh(context.Context, string) (string, error)
}

// NewGatewayHTTPClient returns a redirect-refusing client whose transport
// validates the configured gateway origin and base-path boundary before asking
// for a bearer. A 401 is retried once, before the response is returned to the
// streaming decoder.
func NewGatewayHTTPClient(canonicalBase string, source BearerSource, base http.RoundTripper) (*http.Client, error) {
	validated, err := CanonicalGatewayURL(canonicalBase)
	if err != nil || validated != canonicalBase || source == nil {
		return nil, errInvalidGatewayURL
	}
	u, err := url.Parse(validated)
	if err != nil {
		return nil, errInvalidGatewayURL
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Transport:     &gatewayBearerTransport{base: base, gateway: u, source: source},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

type gatewayBearerTransport struct {
	base    http.RoundTripper
	gateway *url.URL
	source  BearerSource
}

func (t *gatewayBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.validate(req); err != nil {
		return nil, err
	}
	token, err := t.source.Token(req.Context())
	if err != nil || token == "" {
		return nil, errors.Join(ErrNotEnrolled, err)
	}
	first, err := requestWithBearer(req, token, false)
	if err != nil {
		return nil, err
	}
	resp, err := t.base.RoundTrip(first)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	_ = resp.Body.Close()
	refreshed, err := t.source.Refresh(req.Context(), token)
	if err != nil || refreshed == "" {
		return nil, errors.Join(ErrNotEnrolled, err)
	}
	retry, err := requestWithBearer(req, refreshed, true)
	if err != nil {
		return nil, err
	}
	return t.base.RoundTrip(retry)
}

func (t *gatewayBearerTransport) validate(req *http.Request) error {
	if req == nil || req.URL == nil || req.URL.User != nil || req.URL.Scheme != t.gateway.Scheme || !strings.EqualFold(req.URL.Host, t.gateway.Host) {
		return errInvalidGatewayURL
	}
	escaped, _, err := canonicalSegments(req.URL.EscapedPath(), false)
	if err != nil || escaped != req.URL.EscapedPath() {
		return errInvalidGatewayURL
	}
	base := t.gateway.EscapedPath()
	if base != "/" && escaped != base && !strings.HasPrefix(escaped, base+"/") {
		return errInvalidGatewayURL
	}
	return nil
}

func requestWithBearer(req *http.Request, token string, replay bool) (*http.Request, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if replay && req.Body != nil {
		if req.GetBody == nil {
			return nil, fmt.Errorf("gateway request body cannot be retried")
		}
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		clone.Body = body
	}
	clone.Header.Set("Authorization", "Bearer "+token)
	return clone, nil
}
