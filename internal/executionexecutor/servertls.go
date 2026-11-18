package executionexecutor

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/mecatl/internal/executionenv"
)

const maxTrustFileBytes = 1 << 20

var errNotProvider = errors.New("client certificate does not name the provider URI")

// ServerTLSConfig returns a mutual TLS configuration presenting the key pair in
// certDir and admitting only clients that chain to the CA bundle in trustDir and
// carry its client URI. The trust directory is reread for every handshake.
func ServerTLSConfig(certDir, trustDir string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certDir, "tls.crt"),
		filepath.Join(certDir, "tls.key"),
	)
	if err != nil {
		return nil, fmt.Errorf("load executor server certificate: %w", err)
	}
	if _, err := loadProviderTrust(trustDir); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			trust, err := loadProviderTrust(trustDir)
			if err != nil {
				return nil, err
			}
			return trust.config(cert), nil
		},
	}, nil
}

type providerTrust struct {
	roots *x509.CertPool
	uri   string
}

func (p providerTrust) config(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{cert},
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              p.roots,
		SessionTicketsDisabled: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 {
				return errNotProvider
			}
			id, err := executionenv.ClientIdentity(state.VerifiedChains[0][0])
			if err != nil || id != p.uri {
				return errNotProvider
			}
			return nil
		},
	}
}

func loadProviderTrust(dir string) (providerTrust, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return providerTrust{}, fmt.Errorf("open provider trust: %w", err)
	}
	defer func() { _ = root.Close() }()
	for range 3 {
		before, _ := root.Readlink("..data")
		trust, loadErr := readProviderTrust(root)
		after, _ := root.Readlink("..data")
		if before == after {
			return trust, loadErr
		}
	}
	return providerTrust{}, errors.New("provider trust changed during load")
}

func readProviderTrust(root *os.Root) (providerTrust, error) {
	caPEM, err := readTrustFile(root, executionenv.ExecutorTrustCAFile)
	if err != nil {
		return providerTrust{}, err
	}
	rawURI, err := readTrustFile(root, executionenv.ExecutorTrustURIFile)
	if err != nil {
		return providerTrust{}, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return providerTrust{}, errors.New("provider trust has no CA certificate")
	}
	uri := strings.TrimSpace(string(rawURI))
	parsed, err := url.Parse(uri)
	if err != nil {
		return providerTrust{}, errors.New("provider client URI is invalid")
	}
	canonical, err := executionenv.ClientIdentity(&x509.Certificate{URIs: []*url.URL{parsed}})
	if err != nil || canonical != uri {
		return providerTrust{}, errors.New("provider client URI is not canonical")
	}
	return providerTrust{roots: roots, uri: uri}, nil
}

func readTrustFile(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("read provider trust: %w", err)
	}
	b, readErr := io.ReadAll(io.LimitReader(f, maxTrustFileBytes))
	return b, errors.Join(readErr, f.Close())
}
