// Package scopedhttps constructs HTTPS clients confined to explicitly approved
// private-network endpoints.
package scopedhttps

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

const (
	clientTimeout      = 15 * time.Second
	maxIdleConnections = 8
	maxIdlePerHost     = 2
	idleConnTimeout    = 30 * time.Second
)

type approvedEndpoint struct {
	host string
	port string
	ips  map[string]struct{}
}

type policy struct {
	endpoints map[string]approvedEndpoint
	lookup    func(context.Context, string) ([]net.IP, error)
}

// NewClient constructs an HTTPS-only client restricted to endpoints. Each
// endpoint must resolve to a private address at construction and at every dial.
// The supplied PEM bundle is the complete set of trusted TLS roots.
func NewClient(ctx context.Context, endpoints []string, trustedCAPEM []byte) (*http.Client, error) {
	roots, err := rootsFromPEM(trustedCAPEM)
	if err != nil {
		return nil, err
	}
	policy, err := newPolicy(ctx, endpoints, defaultLookup)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DialContext: policy.dialContext,
		// Keep-alive is intentional: dialContext already re-validates the dial
		// target against the approved IP set on every new connection, so an
		// established connection is at least as trustworthy as a fresh one and
		// reusing it avoids paying a resolver round trip per request (a
		// "cluster.local"-suffixed host costs a deterministic ~5s stall per
		// lookup on a macOS client, since that TLD forces mDNS resolution).
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          maxIdleConnections,
		MaxIdleConnsPerHost:   maxIdlePerHost,
		IdleConnTimeout:       idleConnTimeout,
		ResponseHeaderTimeout: clientTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
	}
	return &http.Client{
		Transport: scopedTransport{policy: policy, next: transport},
		Timeout:   clientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("HTTPS redirect refused")
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

func newPolicy(ctx context.Context, rawEndpoints []string, lookup func(context.Context, string) ([]net.IP, error)) (policy, error) {
	endpoints := make(map[string]approvedEndpoint)
	for _, raw := range rawEndpoints {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return policy{}, fmt.Errorf("invalid HTTPS endpoint %q", raw)
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
		ips, err := resolvePrivateIPs(ctx, host, lookup)
		if err != nil {
			return policy{}, fmt.Errorf("resolve HTTPS endpoint %q: %w", raw, err)
		}
		endpoints[key] = approvedEndpoint{host: host, port: port, ips: ips}
	}
	if len(endpoints) == 0 {
		return policy{}, errors.New("no HTTPS endpoints approved")
	}
	return policy{endpoints: endpoints, lookup: lookup}, nil
}

func defaultLookup(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, net.IP(addr.AsSlice()))
	}
	return ips, nil
}

func resolvePrivateIPs(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IP, error)) (map[string]struct{}, error) {
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make(map[string]struct{})
	for _, ip := range addrs {
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

func (p policy) endpoint(host, port string) (approvedEndpoint, bool) {
	endpoint, ok := p.endpoints[net.JoinHostPort(strings.ToLower(host), port)]
	return endpoint, ok
}

func (p policy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid HTTPS dial address %q: %w", address, err)
	}
	endpoint, ok := p.endpoint(host, port)
	if !ok {
		return nil, fmt.Errorf("HTTPS target %q is not an approved endpoint", address)
	}
	resolved, err := resolvePrivateIPs(ctx, endpoint.host, p.lookup)
	if err != nil {
		return nil, fmt.Errorf("re-resolve HTTPS target %q: %w", address, err)
	}
	var candidates []string
	for ip := range resolved {
		if _, ok := endpoint.ips[ip]; ok {
			candidates = append(candidates, ip)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("HTTPS target %q no longer resolves to an approved private address", address)
	}
	sort.Strings(candidates)
	return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(candidates[0], port))
}

type scopedTransport struct {
	policy policy
	next   http.RoundTripper
}

func (t scopedTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
func (t scopedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, errors.New("HTTPS request must use HTTPS without userinfo")
	}
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	if _, ok := t.policy.endpoint(req.URL.Hostname(), port); !ok {
		return nil, fmt.Errorf("HTTPS target %q is not an approved endpoint", req.URL.Host)
	}
	return t.next.RoundTrip(req)
}
