package webfetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

const (
	maxRedirects = 5
	maxURLBytes  = 8 << 10
)

var unsafePrefixes = []netip.Prefix{
	// Documentation, benchmarking, transition, and reserved ranges that Go still
	// classifies as global unicast.
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	// RFC 8215 local-use NAT64 and Azure's platform virtual IP.
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("168.63.129.16/32"),
}

type lookupNetIPFunc func(context.Context, string, string) ([]netip.Addr, error)
type dialContextFunc func(context.Context, string, string) (net.Conn, error)

type fetchTransport struct {
	lookup    lookupNetIPFunc
	dial      dialContextFunc
	roundTrip func(context.Context, *url.URL, []netip.Addr) (*http.Response, error)
}

func newFetchTransport() *fetchTransport {
	resolver := &net.Resolver{}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	f := &fetchTransport{
		lookup: resolver.LookupNetIP,
		dial:   dialer.DialContext,
	}
	f.roundTrip = f.oneHop
	return f
}

func parseTarget(raw string) (*url.URL, error) {
	if len(raw) > maxURLBytes {
		return nil, errors.New("URL exceeds the 8 KiB size limit")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	if !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("URL must be absolute and use http or https")
	}
	if u.User != nil {
		return nil, errors.New("URL userinfo is not allowed")
	}
	if u.Hostname() == "" {
		return nil, errors.New("URL host is required")
	}
	if strings.HasSuffix(u.Host, ":") && u.Port() == "" {
		return nil, errors.New("URL port is invalid")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, errors.New("URL port is invalid")
		}
	}
	return u, nil
}

func (f *fetchTransport) resolveSafe(ctx context.Context, u *url.URL) ([]netip.Addr, error) {
	addrs, err := f.lookup(ctx, "ip", u.Hostname())
	if err != nil {
		return nil, fmt.Errorf("resolve host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("host resolved to no addresses")
	}
	for _, addr := range addrs {
		if unsafeAddress(addr) {
			return nil, fmt.Errorf("host resolves to unsafe address %s", addr)
		}
	}
	return addrs, nil
}

func unsafeAddress(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return true
	}
	addr = addr.Unmap()
	// The shared predicate covers loopback, RFC 1918, link-local, multicast,
	// CGNAT 100.64.0.0/10, and well-known-prefix NAT64 embeddings.
	if session.ValidateResolvedIP(net.IP(addr.AsSlice())) != nil {
		return true
	}
	for _, prefix := range unsafePrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (f *fetchTransport) oneHop(ctx context.Context, u *url.URL, addrs []netip.Addr) (*http.Response, error) {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, addr := range addrs {
				conn, err := f.dial(ctx, network, net.JoinHostPort(addr.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return transport.RoundTrip(req)
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func (f *fetchTransport) get(ctx context.Context, raw string) (*http.Response, error) {
	target, err := parseTarget(raw)
	if err != nil {
		return nil, err
	}

	for redirects := 0; ; redirects++ {
		addrs, err := f.resolveSafe(ctx, target)
		if err != nil {
			return nil, err
		}
		resp, err := f.roundTrip(ctx, target, addrs)
		if err != nil {
			return nil, err
		}
		if !isRedirectStatus(resp.StatusCode) {
			if resp.Request == nil {
				resp.Request = &http.Request{URL: target}
			}
			return resp, nil
		}
		if redirects == maxRedirects {
			_ = resp.Body.Close()
			return nil, errors.New("too many redirects")
		}
		location := resp.Header.Get("Location")
		_ = resp.Body.Close()
		if location == "" {
			return nil, errors.New("redirect response has no Location")
		}
		next, err := target.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("parse redirect URL: %w", err)
		}
		target, err = parseTarget(next.String())
		if err != nil {
			return nil, fmt.Errorf("invalid redirect target: %w", err)
		}
	}
}
