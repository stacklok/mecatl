package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

func TestBuildTLSConfigWiresReloadLifecycleAndStaticClientCA(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	certPEM, keyPEM := makeCompositionKeyPair(t)
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, lifecycle, err := buildTLSConfig(config{
		tlsCert:     certFile,
		tlsKey:      keyFile,
		clientCA:    certFile,
		diagnostics: port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetCertificate == nil || len(cfg.Certificates) != 0 {
		t.Fatal("TLS config does not use the reload adapter callback exclusively")
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert || cfg.ClientCAs == nil {
		t.Fatal("static client CA was not wired for both listeners' shared TLS config")
	}
	if cert, getErr := cfg.GetCertificate(nil); getErr != nil || cert == nil || cert.Leaf == nil {
		t.Fatalf("GetCertificate returned cert=%v err=%v", cert, getErr)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildTLSConfigInitialErrorsDoNotExposeCredentialPaths(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "mounted-credential-marker")
	if err := os.Mkdir(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(marker, "tls.crt")
	certPEM, _ := makeCompositionKeyPair(t)
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []config{
		{tlsCert: filepath.Join(marker, "missing.crt"), tlsKey: filepath.Join(marker, "missing.key")},
		{tlsCert: certFile, tlsKey: filepath.Join(marker, "missing.key")},
	} {
		_, lifecycle, err := buildTLSConfig(cfg)
		if lifecycle != nil {
			t.Fatal("failed TLS construction returned a lifecycle")
		}
		if err == nil || err.Error() != "TLS keypair load failed" || strings.Contains(err.Error(), marker) {
			t.Fatalf("initial TLS error = %v", err)
		}
	}
}

func TestBuildTLSConfigStaticClientCAReadsRegularAndProjectedFiles(t *testing.T) {
	certPEM, keyPEM := makeCompositionKeyPair(t)
	for _, tc := range []struct {
		name   string
		caFile func(*testing.T, string, []byte) string
	}{
		{name: "regular", caFile: func(t *testing.T, dir string, caPEM []byte) string {
			t.Helper()
			path := filepath.Join(dir, "client-ca.crt")
			if err := os.WriteFile(path, caPEM, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "projected", caFile: func(t *testing.T, dir string, caPEM []byte) string {
			t.Helper()
			version := filepath.Join(dir, "..v1")
			if err := os.Mkdir(version, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(version, "client-ca.crt"), caPEM, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("..v1", filepath.Join(dir, "..data")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "client-ca.crt")
			if err := os.Symlink(filepath.Join("..data", "client-ca.crt"), path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			certFile := filepath.Join(dir, "tls.crt")
			keyFile := filepath.Join(dir, "tls.key")
			if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, lifecycle, err := buildTLSConfig(config{tlsCert: certFile, tlsKey: keyFile, clientCA: tc.caFile(t, dir, certPEM)})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ClientCAs == nil || cfg.ClientAuth != tls.RequireAndVerifyClientCert {
				t.Fatal("client CA was not configured")
			}
			if err := lifecycle.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBuildTLSConfigRejectsUnsafeStaticClientCAWithoutPathDisclosure(t *testing.T) {
	certPEM, keyPEM := makeCompositionKeyPair(t)
	marker := filepath.Join(t.TempDir(), "mounted-credential-marker")
	if err := os.Mkdir(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(marker, "tls.crt")
	keyFile := filepath.Join(marker, "tls.key")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path func(*testing.T) string
	}{
		{name: "missing", path: func(*testing.T) string { return filepath.Join(marker, "missing-ca.crt") }},
		{name: "directory", path: func(*testing.T) string { return marker }},
		{name: "fifo", path: func(t *testing.T) string {
			path := filepath.Join(marker, "client-ca.fifo")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "oversized", path: func(t *testing.T) string {
			path := filepath.Join(marker, "client-ca.crt")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(1<<20 + 1); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, lifecycle, err := buildTLSConfig(config{tlsCert: certFile, tlsKey: keyFile, clientCA: tc.path(t)})
			if lifecycle != nil {
				t.Fatal("failed TLS construction returned a lifecycle")
			}
			if err == nil || err.Error() != "--client-ca load failed" || strings.Contains(err.Error(), marker) {
				t.Fatalf("client CA error = %v", err)
			}
		})
	}
}

func TestBrokerControlsRequireVerifiedCallerIdentity(t *testing.T) {
	handlers := mcpbroker.HandlerBundle{Callback: http.NotFoundHandler()}
	for _, tc := range []struct {
		name    string
		cfg     config
		tlsCfg  *tls.Config
		wantErr bool
	}{
		{name: "server TLS only", tlsCfg: &tls.Config{}, wantErr: true},
		{name: "auth token", cfg: config{authToken: "token"}},
		{name: "OIDC", cfg: config{oidc: cliconfig.OIDCConfig{Issuer: "https://issuer.example", Audience: "mecatl"}}},
		{name: "verified mTLS", tlsCfg: &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBrokerControlOwnership(
				"0.0.0.0:8081",
				brokerControlVerifiedIdentity(tc.cfg, tc.tlsCfg),
				false,
				handlers,
			)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateBrokerControlOwnership() error = %v, want error = %t", err, tc.wantErr)
			}
		})
	}
}

func makeCompositionKeyPair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
