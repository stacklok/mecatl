package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
)

type fakeBrokerLifecycle struct {
	starts int
	closes int
}

func (l *fakeBrokerLifecycle) Start() <-chan error {
	l.starts++
	errs := make(chan error, 1)
	errs <- http.ErrServerClosed
	return errs
}

func (l *fakeBrokerLifecycle) Close(context.Context) error {
	l.closes++
	return nil
}

func TestRunTranslatesStrictFileAndOwnsProductionLifecycle(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeTestIdentity(t, dir)
	configFile := filepath.Join(dir, "broker.json")
	if err := os.WriteFile(configFile, []byte(`{"callback_url":"https://broker.example/callback","profiles":[{"name":"public","url":"https://mcp.example/api","auth":"none","tools":[{"name":"lookup","schema":{"type":"object"},"read_only":true}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	lifecycle := &fakeBrokerLifecycle{}
	var received mcpbrokerserver.ProductionConfig
	previous := newProduction
	newProduction = func(_ context.Context, cfg mcpbrokerserver.ProductionConfig) (brokerLifecycle, error) {
		received = cfg
		return lifecycle, nil
	}
	t.Cleanup(func() { newProduction = previous })

	limits := mcpbroker.Limits{MaxLogicalSessions: 7, LogicalRetention: 9 * time.Minute, SweepInterval: 11 * time.Second, MaxPendingStates: 13}
	cfg := config{
		publicAddress: ":9443", adminAddress: defaultAdminAddress,
		tlsCertFile: certFile, tlsKeyFile: keyFile,
		oidcIssuer: "https://issuer.example", oidcJWKSURI: "https://issuer.example/jwks", oidcAudience: "broker", oidcSubject: "agent", oidcCAFile: certFile,
		maxJWKSStaleness: time.Minute, brokerConfigFile: configFile,
		propagationWait: time.Second, drainTimeout: time.Minute,
		transport: mcpbrokergrpc.DefaultConfig(), runtimeLimits: limits,
	}
	if err := run(t.Context(), cfg, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if lifecycle.starts != 1 || lifecycle.closes != 1 {
		t.Fatalf("lifecycle start/close = %d/%d, want 1/1", lifecycle.starts, lifecycle.closes)
	}
	if received.AdminAddress != defaultAdminAddress || received.RuntimeLimits != limits {
		t.Fatalf("production runtime configuration = admin %q limits %+v", received.AdminAddress, received.RuntimeLimits)
	}
	if len(received.ToolHive.Profiles) != 1 || received.ToolHive.Profiles[0].Name != "public" || len(received.ToolHive.Profiles[0].Static) != 1 || received.ToolHive.Profiles[0].Static[0].Name != "lookup" {
		t.Fatalf("strict file translation = %+v", received.ToolHive)
	}
}

func TestRunRejectsNonPositiveRuntimeLimitsBeforeConstruction(t *testing.T) {
	cfg := config{brokerConfigFile: "unused", oidcCAFile: "unused", propagationWait: time.Second, drainTimeout: time.Second}
	for _, limits := range []mcpbroker.Limits{
		{LogicalRetention: time.Second, MaxPendingStates: 1},
		{MaxLogicalSessions: 1, MaxPendingStates: 1},
		{MaxLogicalSessions: 1, LogicalRetention: time.Second},
	} {
		cfg.runtimeLimits = limits
		if err := run(t.Context(), cfg, nil); err == nil {
			t.Fatalf("accepted non-positive runtime limits: %+v", limits)
		}
	}
}

func writeTestIdentity(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mecabroker-test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
