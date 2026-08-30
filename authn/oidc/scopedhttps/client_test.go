package scopedhttps

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScopedTransportForwardsCloseIdleConnections(t *testing.T) {
	closed := false
	transport := scopedTransport{next: roundTripperWithClose{close: func() { closed = true }}}
	transport.CloseIdleConnections()
	if !closed {
		t.Fatal("CloseIdleConnections was not forwarded")
	}
}

type roundTripperWithClose struct {
	close func()
}

func (roundTripperWithClose) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (r roundTripperWithClose) CloseIdleConnections() { r.close() }

func TestNewClientBoundsIdleConnections(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	client := newTestClient(t, srv)
	transport, ok := client.Transport.(scopedTransport)
	if !ok {
		t.Fatalf("Transport type = %T, want scopedTransport", client.Transport)
	}
	next, ok := transport.next.(*http.Transport)
	if !ok {
		t.Fatalf("wrapped transport type = %T, want *http.Transport", transport.next)
	}
	if next.MaxIdleConns != maxIdleConnections || next.MaxIdleConnsPerHost != maxIdlePerHost || next.IdleConnTimeout != idleConnTimeout {
		t.Fatalf("idle bounds = (%d, %d, %s), want (%d, %d, %s)", next.MaxIdleConns, next.MaxIdleConnsPerHost, next.IdleConnTimeout, maxIdleConnections, maxIdlePerHost, idleConnTimeout)
	}
}
func TestNewClientRejectsInvalidEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1",
		"https://user@127.0.0.1",
		"https://",
	} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := newPolicy(context.Background(), []string{endpoint}, defaultLookup)
			if err == nil || !strings.Contains(err.Error(), "invalid HTTPS endpoint") {
				t.Fatalf("newPolicy(%q) error = %v, want invalid endpoint", endpoint, err)
			}
		})
	}
}

func TestPrivateAddressAllowlist(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "private IPv4", addr: "10.0.0.1", want: true},
		{name: "private IPv6", addr: "fd00::1", want: true},
		{name: "loopback IPv4", addr: "127.0.0.1", want: true},
		{name: "loopback IPv6", addr: "::1", want: true},
		{name: "link-local unicast IPv4", addr: "169.254.1.1", want: true},
		{name: "link-local unicast IPv6", addr: "fe80::1", want: true},
		{name: "link-local multicast IPv4", addr: "224.0.0.1", want: true},
		{name: "link-local multicast IPv6", addr: "ff02::1", want: true},
		{name: "unspecified IPv4", addr: "0.0.0.0"},
		{name: "unspecified IPv6", addr: "::"},
		{name: "public IPv4", addr: "8.8.8.8"},
		{name: "public IPv6", addr: "2001:4860:4860::8888"},
		{name: "non-link-local multicast IPv4", addr: "224.0.1.1"},
		{name: "non-link-local multicast IPv6", addr: "ff05::1"},
		{name: "documentation IPv4", addr: "192.0.2.1"},
		{name: "documentation IPv6", addr: "2001:db8::1"},
		{name: "reserved IPv4", addr: "240.0.0.1"},
		{name: "reserved IPv6", addr: "100::1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPrivateAddress(net.ParseIP(test.addr)); got != test.want {
				t.Errorf("isPrivateAddress(%q) = %t, want %t", test.addr, got, test.want)
			}
		})
	}
}

func TestClientRejectsDifferentHostOrPort(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	client := newTestClient(t, srv)

	for _, target := range []string{
		"https://localhost" + strings.TrimPrefix(srv.URL, "https://127.0.0.1"),
		"https://127.0.0.1:1",
	} {
		t.Run(target, func(t *testing.T) {
			_, err := client.Get(target)
			if err == nil || !strings.Contains(err.Error(), "not an approved endpoint") {
				t.Fatalf("GET %q error = %v, want rejected endpoint", target, err)
			}
		})
	}
}

func TestPolicyRejectsDNSAddressDrift(t *testing.T) {
	lookups := 0
	lookup := func(context.Context, string) ([]net.IP, error) {
		lookups++
		if lookups == 1 {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("127.0.0.2")}, nil
	}
	policy, err := newPolicy(context.Background(), []string{"https://issuer.test"}, lookup)
	if err != nil {
		t.Fatalf("newPolicy: %v", err)
	}
	if _, err := policy.dialContext(context.Background(), "tcp", "issuer.test:443"); err == nil || !strings.Contains(err.Error(), "no longer resolves to an approved private address") {
		t.Fatalf("DNS-pinned dial accepted changed address: %v", err)
	}
}

func TestClientRejectsRedirect(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/other", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	client := newTestClient(t, srv)

	_, err := client.Get(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "HTTPS redirect refused") {
		t.Fatalf("redirect error = %v, want redirect refusal", err)
	}
}

func TestClientRejectsWrongCA(t *testing.T) {
	target := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(target.Close)

	client, err := NewClient(context.Background(), []string{target.URL}, unrelatedCertPEM(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Get(target.URL)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("wrong CA error = %v, want certificate verification failure", err)
	}
}

func newTestClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	client, err := NewClient(context.Background(), []string{srv.URL}, certPEM(srv.Certificate()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func unrelatedCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "unrelated test CA"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func certPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}
