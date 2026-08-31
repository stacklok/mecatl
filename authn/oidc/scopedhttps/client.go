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
	clientTimeout       = 15 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
	maxIdleConnections  = 8
	maxIdlePerHost      = 2
	idleConnTimeout     = 30 * time.Second
)

type approvedEndpoint struct {
	host string
	port string
	ips  map[string]struct{}
}

type policy struct {
	endpoints map[string]approvedEndpoint
	// authorityRoots holds each approved endpoint's OWN trusted CA pool, keyed
	// by the SAME canonical host:port authority as endpoints -- never by
	// hostname alone, which would merge two different issuers that happen to
	// share a hostname on different ports. Isolated from every other
	// endpoint's pool -- see verifyPeerChain.
	authorityRoots map[string]*x509.CertPool
	lookup         func(context.Context, string) ([]net.IP, error)
}

// NewClient constructs an HTTPS-only client restricted to endpoints, one
// scoped connection-pool lifecycle for every endpoint in endpointCAs. Each
// endpoint must resolve to a private address at construction and at every
// dial. endpointCAs maps each approved HTTPS endpoint URL to the exact CA PEM
// bundle trusted for THAT endpoint's own host:port authority -- never a
// shared/unioned pool, and never merged with a DIFFERENT authority that
// happens to share a hostname on another port. Two endpoints on different
// authorities, each supplying its own CA, are kept isolated: a certificate
// presented for authority B is verified ONLY against B's own pool, never
// against A's, so a compromised or overly permissive CA configured for one
// endpoint can never authenticate a connection to another.
func NewClient(ctx context.Context, endpointCAs map[string][]byte) (*http.Client, error) {
	policy, err := newPolicy(ctx, endpointCAs, defaultLookup)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		// DialTLSContext (not DialContext + TLSClientConfig): the standard
		// library has no client-side per-ServerName root-pool hook (that
		// exists only server-side, via GetConfigForClient), and TLS SNI is
		// never sent for an IP-literal dial target, so per-endpoint root
		// isolation requires performing the handshake here, per dial, against
		// exactly the dialed endpoint's own pool with an explicit ServerName.
		DialTLSContext: policy.dialTLS,
		// Keep-alive is intentional: dialTLS already re-validates the dial
		// target against the approved IP set on every new connection, so an
		// established connection is at least as trustworthy as a fresh one and
		// reusing it avoids paying a resolver round trip per request (a
		// "cluster.local"-suffixed host costs a deterministic ~5s stall per
		// lookup on a macOS client, since that TLD forces mDNS resolution).
		MaxIdleConns:          maxIdleConnections,
		MaxIdleConnsPerHost:   maxIdlePerHost,
		IdleConnTimeout:       idleConnTimeout,
		ResponseHeaderTimeout: clientTimeout,
	}
	return &http.Client{
		Transport: scopedTransport{policy: policy, next: transport},
		Timeout:   clientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("HTTPS redirect refused")
		},
	}, nil
}

// NewSingleIssuerClient is NewClient's convenience form for the common case
// of one issuer's own endpoints (e.g. its discovery document and JWKS URI)
// all trusting the SAME CA bundle.
func NewSingleIssuerClient(ctx context.Context, endpoints []string, trustedCAPEM []byte) (*http.Client, error) {
	endpointCAs := make(map[string][]byte, len(endpoints))
	for _, endpoint := range endpoints {
		endpointCAs[endpoint] = trustedCAPEM
	}
	return NewClient(ctx, endpointCAs)
}

// verifyPeerChain verifies a peer's certificate chain against the given
// pool, independent of TLS's own automatic verification path.
func verifyPeerChain(certs []*x509.Certificate, roots *x509.CertPool, dnsName string) error {
	if len(certs) == 0 {
		return errors.New("no peer certificates presented")
	}
	intermediates := x509.NewCertPool()
	for _, cert := range certs[1:] {
		intermediates.AddCert(cert)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{DNSName: dnsName, Roots: roots, Intermediates: intermediates})
	return err
}

func rootsFromPEM(pemBytes []byte) (*x509.CertPool, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("trusted CA bundle contains no certificates")
	}
	return roots, nil
}

func newPolicy(ctx context.Context, endpointCAs map[string][]byte, lookup func(context.Context, string) ([]net.IP, error)) (policy, error) {
	endpoints := make(map[string]approvedEndpoint)
	authorityRoots := make(map[string]*x509.CertPool)
	for raw, ca := range endpointCAs {
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
		roots, err := rootsFromPEM(ca)
		if err != nil {
			return policy{}, fmt.Errorf("HTTPS endpoint %q: %w", raw, err)
		}
		// Two endpoints sharing one host:port authority (e.g. an issuer's
		// discovery doc and its JWKS URI on the same host and port)
		// legitimately share one pool; a distinct authority -- including the
		// SAME hostname on a DIFFERENT port, a different issuer entirely --
		// always gets its own. Roots are never merged across authorities.
		key := net.JoinHostPort(host, port)
		if existing, ok := authorityRoots[key]; ok {
			roots = existing
			if !roots.AppendCertsFromPEM(ca) {
				return policy{}, fmt.Errorf("HTTPS endpoint %q: trusted CA bundle contains no certificates", raw)
			}
		}
		authorityRoots[key] = roots
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
	return policy{endpoints: endpoints, authorityRoots: authorityRoots, lookup: lookup}, nil
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

func (p policy) dialTLS(ctx context.Context, network, address string) (net.Conn, error) {
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
	rawConn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(candidates[0], port))
	if err != nil {
		return nil, err
	}
	// ServerName is set explicitly to the approved hostname rather than left
	// to derive from the dial address (an IP literal, for which TLS never
	// sends SNI) -- required both for correct hostname verification and so a
	// server presenting SNI-routed certificates sees the right name. RootCAs
	// is ONLY this endpoint's own authority pool (host:port, not host alone):
	// a handshake against this endpoint is never checked against a
	// DIFFERENT approved endpoint's roots, including one on the SAME
	// hostname but a different port, however many other approved endpoints
	// this policy also covers.
	authority := net.JoinHostPort(endpoint.host, endpoint.port)
	tlsConn := tls.Client(rawConn, &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         endpoint.host,
		InsecureSkipVerify: true, // #nosec G402 -- verifyPeerChain below performs the full manual chain+hostname verification this normally does, against this endpoint's own pool
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyPeerChain(cs.PeerCertificates, p.authorityRoots[authority], endpoint.host)
		},
	})
	handshakeCtx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	return tlsConn, nil
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
