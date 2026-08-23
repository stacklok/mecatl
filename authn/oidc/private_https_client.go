package oidc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const privateHTTPSClientTimeout = 15 * time.Second

type approvedEndpoint struct {
	host string
	port string
	ips  map[string]struct{}
}

type privateHTTPSPolicy struct {
	endpoints map[string]approvedEndpoint
}

func newPrivateHTTPSClient(ctx context.Context, cfg Config) (*http.Client, error) {
	policy, err := newPrivateHTTPSPolicy(ctx, cfg.Issuer, cfg.JWKSURI)
	if err != nil {
		return nil, err
	}
	roots, err := rootsFromPEM(cfg.TrustedCAPEM)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DialContext:           policy.dialContext,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: privateHTTPSClientTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
	}
	return &http.Client{
		Transport: scopedTransport{policy: policy, next: transport},
		Timeout:   privateHTTPSClientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("OIDC redirect refused")
		},
	}, nil
}

func rootsFromPEM(pemBytes []byte) (*x509.CertPool, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("trusted CA bundle contains no certificates")
	}
	return roots, nil
}

func newPrivateHTTPSPolicy(ctx context.Context, issuer, jwksURI string) (privateHTTPSPolicy, error) {
	endpoints := make(map[string]approvedEndpoint)
	for _, raw := range []string{issuer, jwksURI} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return privateHTTPSPolicy{}, fmt.Errorf("invalid private HTTPS endpoint %q", raw)
		}
		host := strings.ToLower(u.Hostname())
		port := u.Port()
		if port == "" {
			port = "443"
		}
		key := net.JoinHostPort(host, port)
		if _, ok := endpoints[key]; ok {
			continue
		}
		ips, err := resolvePrivateIPs(ctx, host)
		if err != nil {
			return privateHTTPSPolicy{}, fmt.Errorf("resolve private HTTPS endpoint %q: %w", raw, err)
		}
		endpoints[key] = approvedEndpoint{host: host, port: port, ips: ips}
	}
	return privateHTTPSPolicy{endpoints: endpoints}, nil
}

func resolvePrivateIPs(ctx context.Context, host string) (map[string]struct{}, error) {
	var resolved []string
	if ip := net.ParseIP(host); ip != nil {
		resolved = []string{ip.String()}
	} else {
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			resolved = append(resolved, addr.String())
		}
	}
	ips := make(map[string]struct{})
	for _, raw := range resolved {
		ip := net.ParseIP(raw)
		if ip != nil && isPrivateAddress(ip) {
			ips[ip.String()] = struct{}{}
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("host has no private address")
	}
	return ips, nil
}

func isPrivateAddress(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

func (p privateHTTPSPolicy) endpoint(host, port string) (approvedEndpoint, bool) {
	endpoint, ok := p.endpoints[net.JoinHostPort(strings.ToLower(host), port)]
	return endpoint, ok
}

func (p privateHTTPSPolicy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid OIDC dial address %q: %w", address, err)
	}
	endpoint, ok := p.endpoint(host, port)
	if !ok {
		return nil, fmt.Errorf("OIDC target %q is not an approved issuer or JWKS host", address)
	}
	resolved, err := resolvePrivateIPs(ctx, endpoint.host)
	if err != nil {
		return nil, fmt.Errorf("re-resolve OIDC target %q: %w", address, err)
	}
	var candidates []string
	for ip := range resolved {
		if _, ok := endpoint.ips[ip]; ok {
			candidates = append(candidates, ip)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("OIDC target %q no longer resolves to an approved private address", address)
	}
	sort.Strings(candidates)
	return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(candidates[0], port))
}

type scopedTransport struct {
	policy privateHTTPSPolicy
	next   http.RoundTripper
}

func (t scopedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, errors.New("OIDC request must use HTTPS without userinfo")
	}
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	if _, ok := t.policy.endpoint(req.URL.Hostname(), port); !ok {
		return nil, fmt.Errorf("OIDC target %q is not an approved issuer or JWKS host", req.URL.Host)
	}
	return t.next.RoundTrip(req)
}
