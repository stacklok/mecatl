package executionenv

import (
	"crypto/x509"
	"errors"
	"strings"
)

// Mounts and files through which an executor Pod receives its server
// certificate and the provider's client trust.
const (
	ExecutorServerTLSDir     = "/etc/mecatl-executor/tls"
	ExecutorProviderTrustDir = "/etc/mecatl-executor/provider"
	ExecutorTrustCAFile      = "ca.crt"
	ExecutorTrustURIFile     = "client-uri"
)

// ExecutorServerPort is the TCP port of the executor gRPC server in its Pod.
const ExecutorServerPort = 7443

// ClientIdentity returns the single URI SAN of c with its scheme and host lowercased.
func ClientIdentity(c *x509.Certificate) (string, error) {
	if len(c.URIs) != 1 {
		return "", errors.New("certificate must have exactly one URI SAN")
	}
	u := c.URIs[0]
	if u == nil || u.Scheme == "" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("certificate must have exactly one canonical URI SAN")
	}
	v := *u
	v.Scheme, v.Host = strings.ToLower(v.Scheme), strings.ToLower(v.Host)
	return v.String(), nil
}
