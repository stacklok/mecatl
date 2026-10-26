// Package authserverclient is the identity broker's client of the authorization
// servers AS-1 and AS-2: it calls their token and revocation endpoints,
// authenticating every request as the OAuth client
// identitybroker.BrokerClientID with private_key_jwt (RFC 7523), a fresh
// client assertion each time, and never with a client secret. It turns HTTP
// outcomes into the core's identitybroker.ServerRefusal and
// identitybroker.ErrNoResponse, which identitybroker.IsConfigurationFault and
// identitybroker.IsUnavailable classify; the package itself is only
// transport.
//
// The package never puts a token, an assertion, or a form value in an error.
package authserverclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	assertionTypeJWTBearer = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" // #nosec G101 -- RFC 7523 assertion type URN, not a credential.
	maxResponseBytes       = 1 << 20
)

// ErrResponseTooLarge reports a response body over 1 MiB.
var ErrResponseTooLarge = errors.New("authserverclient: the response is too large")

// Assertions mints client assertions; *identitybroker.ClientAssertionSigner
// is the implementation.
type Assertions interface {
	ClientAssertion(audience string, now time.Time) (identitybroker.Token, error)
}

// Config configures a Client for one authorization server.
type Config struct {
	// Issuer is the server's issuer identifier, which every assertion names
	// as its only audience. Required.
	Issuer string
	// TokenEndpoint is the server's HTTPS token endpoint. Required.
	TokenEndpoint string
	// RevocationEndpoint is the server's HTTPS RFC 7009 revocation endpoint.
	// Empty when the broker does not revoke at this server.
	RevocationEndpoint string
	// Assertions mints the client assertions. Required.
	Assertions Assertions
	// HTTPClient sends the requests and owns the transport policy (TLS,
	// proxy, timeouts). Required. The Client uses a copy that refuses every
	// redirect.
	HTTPClient *http.Client
	// Now is the clock for assertions. Nil selects time.Now.
	Now func() time.Time
}

// Client sends authenticated requests to one authorization server. It is
// safe for concurrent use.
type Client struct {
	issuer, token, revocation string
	assertions                Assertions
	http                      *http.Client
	now                       func() time.Time
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("authserverclient: an issuer is required")
	}
	if cfg.Assertions == nil {
		return nil, errors.New("authserverclient: an assertion signer is required")
	}
	if cfg.HTTPClient == nil {
		return nil, errors.New("authserverclient: an HTTP client is required")
	}
	if err := checkEndpoint("token", cfg.TokenEndpoint); err != nil {
		return nil, err
	}
	if cfg.RevocationEndpoint != "" {
		if err := checkEndpoint("revocation", cfg.RevocationEndpoint); err != nil {
			return nil, err
		}
	}
	hc := *cfg.HTTPClient
	// A redirect would resend the request, assertion and all, elsewhere; the
	// 3xx is returned as a refusal instead.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		issuer: cfg.Issuer, token: cfg.TokenEndpoint, revocation: cfg.RevocationEndpoint,
		assertions: cfg.Assertions, http: &hc, now: now,
	}, nil
}

func checkEndpoint(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("authserverclient: the %s endpoint must be an https URL", name)
	}
	return nil
}

// Token posts form to the token endpoint and decodes the JSON success
// response into out. A refusal returns an *identitybroker.ServerRefusal; no
// response returns an error wrapping identitybroker.ErrNoResponse.
func (c *Client) Token(ctx context.Context, form url.Values, out any) error {
	return c.post(ctx, c.token, form, out)
}

// Revoke posts form to the revocation endpoint, with Token's errors.
func (c *Client) Revoke(ctx context.Context, form url.Values) error {
	if c.revocation == "" {
		return errors.New("authserverclient: no revocation endpoint is configured")
	}
	return c.post(ctx, c.revocation, form, nil)
}

func (c *Client) post(ctx context.Context, endpoint string, form url.Values, out any) error {
	for _, name := range []string{"client_id", "client_secret", "client_assertion", "client_assertion_type"} {
		if form.Has(name) {
			return fmt.Errorf("authserverclient: the form must not carry %s; the client authenticates itself", name)
		}
	}
	assertion, err := c.assertions.ClientAssertion(c.issuer, c.now())
	if err != nil {
		return fmt.Errorf("authserverclient: mint client assertion: %w", err)
	}
	body := url.Values{}
	maps.Copy(body, form)
	body.Set("client_assertion_type", assertionTypeJWTBearer)
	body.Set("client_assertion", assertion.Value())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return errors.New("authserverclient: build request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("authserverclient: %w", ctxErr)
		}
		return fmt.Errorf("authserverclient: %w: %s", identitybroker.ErrNoResponse, transportFailure(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("authserverclient: %w: reading the response failed", identitybroker.ErrNoResponse)
	}
	if len(raw) > maxResponseBytes {
		return ErrResponseTooLarge
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var oauthErr struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &oauthErr) // a non-JSON body leaves both empty
		return identitybroker.NewServerRefusal(resp.StatusCode, oauthErr.Error, oauthErr.Description)
	}
	if out == nil {
		return nil
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("authserverclient: the response is not valid JSON")
	}
	return nil
}

// transportFailure returns a fixed, coarse name for a failed request. The
// error's own text is never used: a custom transport or a net/http protocol
// error can carry request or server-echoed content.
func transportFailure(err error) string {
	var (
		netErr     net.Error
		dnsErr     *net.DNSError
		certErr    *tls.CertificateVerificationError
		recordErr  tls.RecordHeaderError
		alertErr   tls.AlertError
		unknownCA  x509.UnknownAuthorityError
		hostErr    x509.HostnameError
		invalidErr x509.CertificateInvalidError
	)
	switch {
	case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.As(err, &certErr), errors.As(err, &recordErr), errors.As(err, &alertErr),
		errors.As(err, &unknownCA), errors.As(err, &hostErr), errors.As(err, &invalidErr):
		return "tls"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return "connection closed"
	default:
		return "other"
	}
}
