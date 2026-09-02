package clientauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/authn/oidc/scopedhttps"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	issuerClientTimeout     = 15 * time.Second
	issuerTLSHandshake      = 10 * time.Second
	issuerMaxIdleConns      = 8
	issuerMaxIdlePerHost    = 2
	issuerIdleConnTimeout   = 30 * time.Second
	issuerResponseHeaderMax = 15 * time.Second
)

// IssuerHTTPClient builds the HTTP client used for one saved connection's OIDC
// issuer endpoints -- discovery, token exchange, JWKS, refresh, and revocation.
// It is the SINGLE mapping from a persisted IssuerAddressPolicy to a transport,
// shared by login/refresh (oidcClient) and logout, so the two cannot drift.
//
// The two policies are genuinely different problems, not two settings of one
// (ADR 0279):
//
//   - private: the operator supplies an internal CA that legitimately signs MANY
//     internal services, so TLS alone cannot say WHICH one answered. The scoped
//     transport adds the missing control -- an approved-authority allowlist with
//     per-authority root pools and pinned DNS answers. The CA is mandatory.
//
//   - public: verification is ordinary WebPKI. That is already the control, so
//     the scoped machinery is omitted; see newPublicIssuerClient.
func IssuerHTTPClient(ctx context.Context, policy IssuerAddressPolicy, issuer string, trustedCAPEM []byte) (*http.Client, error) {
	if policy == IssuerAddressPolicyPrivate {
		if len(trustedCAPEM) == 0 {
			return nil, errors.New("private HTTPS issuer requires a CA bundle")
		}
		return scopedhttps.NewSingleIssuerClient(ctx, []string{issuer}, trustedCAPEM)
	}
	return newPublicIssuerClient(trustedCAPEM)
}

// newPublicIssuerClient returns the transport for a PUBLIC OIDC issuer.
//
// A public issuer is verified the ordinary way: system roots and the standard
// TLS hostname check, performed by crypto/tls itself. That IS the control here.
// An attacker who can answer DNS for the issuer hostname still cannot present a
// chain valid for it, and the response never reaches the attacker, so the
// private-mode machinery buys nothing on this path while costing real
// behaviour: an issuer-only authority allowlist breaks any provider whose token
// or JWKS endpoint lives on another host (Google), and construction-time DNS
// pinning breaks a CDN-fronted issuer whose answer set rotates mid-session.
//
// Deliberately NOT reimplemented here: InsecureSkipVerify plus a hand-rolled
// chain verification. Selecting a per-authority root pool at dial time is the
// only reason that existed, and public mode has one authority and no custom
// pool. Hand-rolled verification is a downgrade from the stdlib's.
//
// An optional CA bundle REPLACES system roots, matching the flag's documented
// behaviour for an operator pinning a public issuer to a known root.
//
// The dialer still refuses non-public addresses. That is defence in depth
// against a blind internal port probe -- not the primary control -- and it
// delegates to the engine's single dial-layer predicate rather than keeping a
// local range list, so a gap is fixed once for every caller.
func newPublicIssuerClient(trustedCAPEM []byte) (*http.Client, error) {
	// RootCAs nil means system roots, which is the default for a public issuer.
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(trustedCAPEM) > 0 {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(trustedCAPEM) {
			return nil, errors.New("trusted CA bundle contains no certificates")
		}
		tlsCfg.RootCAs = roots
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Control: refusePrivateAddress}).DialContext,
		TLSHandshakeTimeout:   issuerTLSHandshake,
		ResponseHeaderTimeout: issuerResponseHeaderMax,
		MaxIdleConns:          issuerMaxIdleConns,
		MaxIdleConnsPerHost:   issuerMaxIdlePerHost,
		IdleConnTimeout:       issuerIdleConnTimeout,
		TLSClientConfig:       tlsCfg,
	}
	return &http.Client{
		Transport: publicIssuerTransport{next: transport},
		Timeout:   issuerClientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("HTTPS redirect refused")
		},
	}, nil
}

// publicIssuerTransport refuses a non-HTTPS or userinfo-bearing request before
// it is dialed. Discovery names the token and JWKS URLs, so this is the one
// place a downgraded scheme from a issuer-supplied document is caught.
type publicIssuerTransport struct{ next http.RoundTripper }

func (t publicIssuerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, errors.New("issuer request must use HTTPS without userinfo")
	}
	return t.next.RoundTrip(req)
}

func (t publicIssuerTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// refusePrivateAddress runs AFTER DNS resolution on the address actually being
// dialed, so it re-checks on every new connection and on every redirect hop
// rather than pinning one answer set at construction.
func refusePrivateAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("dial address is not an IP literal")
	}
	return session.ValidateResolvedIP(ip)
}
