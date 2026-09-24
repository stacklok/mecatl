package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
)

func TestReportStartupErrorIncludesStageAndCause(t *testing.T) {
	var output bytes.Buffer
	reportStartupError(&output, "configuration", errors.New("--config is required"))
	if got, want := output.String(), "mecabroker: configuration failed: --config is required\n"; got != want {
		t.Fatalf("startup error = %q, want %q", got, want)
	}
}

func TestReportStartupErrorDoesNothingForNil(t *testing.T) {
	var output bytes.Buffer
	reportStartupError(&output, "startup", nil)
	if output.Len() != 0 {
		t.Fatalf("nil startup error wrote %q", output.String())
	}
}

type fakeBrokerLifecycle struct{ starts, closes int }

func (l *fakeBrokerLifecycle) Start() <-chan error {
	l.starts++
	errs := make(chan error, 1)
	errs <- http.ErrServerClosed
	return errs
}
func (l *fakeBrokerLifecycle) Close(context.Context) error { l.closes++; return nil }

func TestRunTranslatesStrictFileAndOwnsProductionLifecycle(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeTestIdentity(t, dir)
	cfg := writeConfig(t, dir, certFile, keyFile)
	lifecycle := &fakeBrokerLifecycle{}
	var received mcpbrokerserver.ProductionConfig
	previous := newProduction
	newProduction = func(_ context.Context, got mcpbrokerserver.ProductionConfig) (brokerLifecycle, error) {
		received = got
		return lifecycle, nil
	}
	t.Cleanup(func() { newProduction = previous })
	if err := run(t.Context(), cfg, nil); err == nil {
		t.Fatal("run accepted an unexpected listener stop")
	}
	if lifecycle.starts != 1 || lifecycle.closes != 1 {
		t.Fatalf("lifecycle start/close = %d/%d, want 1/1", lifecycle.starts, lifecycle.closes)
	}
	if received.AdminAddress != defaultAdminAddress || received.RuntimeLimits != (mcpbroker.Limits{MaxLogicalSessions: 7, LogicalRetention: 9 * time.Minute, SweepInterval: 11 * time.Second, MaxPendingStates: 13}) {
		t.Fatalf("production runtime configuration = admin %q limits %+v", received.AdminAddress, received.RuntimeLimits)
	}
	if received.PublicAddress != ":9443" || received.Transport.MaxActiveExecutes != 17 || received.DrainTimeout != time.Minute || received.ShutdownTimeout != 3*time.Second {
		t.Fatalf("production configuration = %+v", received)
	}
	if len(received.ToolHive.Profiles) != 1 || received.ToolHive.Profiles[0].Name != "public" || len(received.ToolHive.Profiles[0].Static) != 0 {
		t.Fatalf("strict file translation = %+v", received.ToolHive)
	}
}

func TestReadConfigStrictValidation(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeTestIdentity(t, dir)
	base := configJSON(cert, key)
	for name, mutate := range map[string]func(string) string{
		"missing API version": func(s string) string { return strings.Replace(s, `"api_version":"`+brokerAPIVersion+`",`, "", 1) },
		"unknown field":       func(s string) string { return strings.TrimSuffix(s, "}") + `,"unknown":true}` },
		"trailing data":       func(s string) string { return s + " {}" },
		"incomplete listener": func(s string) string { return strings.Replace(s, `"tls_key_file":`+quote(key), `"tls_key_file":""`, 1) },
		"invalid HTTPS URL": func(s string) string {
			return strings.Replace(s, `"callback_url":"https://broker.example/callback"`, `"callback_url":"http://broker.example/callback"`, 1)
		},
		"invalid duration": func(s string) string { return strings.Replace(s, `"rpc_deadline":"10s"`, `"rpc_deadline":"0s"`, 1) },
		"invalid capacity": func(s string) string { return strings.Replace(s, `"max_handles":1`, `"max_handles":0`, 1) },
		"invalid static schema": func(s string) string {
			return strings.Replace(s, `"profiles":[{"name":"public","url":"https://mcp.example/api","auth":"none"}]`, `"profiles":[{"name":"private","url":"https://mcp.example/api","auth":"oauth","oauth":{"issuer":"https://issuer.example","client_id":"client","client_secret_file":"/secret"},"tools":[{"name":"bad","schema":"not-json"}]}]`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(mutate(base)), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readConfig(path); err == nil {
				t.Fatal("readConfig accepted invalid document")
			}
		})
	}
}

func TestDrainRequestTimeoutUsesConfiguredBudgets(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeTestIdentity(t, dir)
	cfg := writeConfig(t, dir, cert, key)
	if got, want := cfg.drainRequestTimeout(), 65*time.Second; got != want {
		t.Fatalf("drain timeout = %s, want %s", got, want)
	}
}

func writeConfig(t *testing.T, dir, cert, key string) fileConfig {
	t.Helper()
	path := filepath.Join(dir, "broker.json")
	if err := os.WriteFile(path, []byte(configJSON(cert, key)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
func quote(s string) string { raw, _ := json.Marshal(s); return string(raw) }
func configJSON(cert, key string) string {
	return `{"api_version":"` + brokerAPIVersion + `","listener":{"public_address":":9443","tls_cert_file":` + quote(cert) + `,"tls_key_file":` + quote(key) + `},"workload_jwt":{"issuer":"https://issuer.example","jwks_uri":"https://issuer.example/jwks","audience":"broker","subject":"agent","trust_bundle_file":` + quote(cert) + `,"max_jwks_staleness":"1m"},"callback_url":"https://broker.example/callback","profiles":[{"name":"public","url":"https://mcp.example/api","auth":"none"}],"drain":{"propagation_delay":"2s","timeout":"1m","listener_shutdown_timeout":"3s"},"transport":{"rpc_deadline":"10s","execute_deadline":"20s","handle_idle_timeout":"30s","sweep_interval":"11s","cleanup_timeout":"5s","max_handles":1,"max_owners":2,"max_receipts":3,"max_receipt_bytes":4,"max_pending_controls":5,"max_active_executes":17},"runtime":{"max_logical_sessions":7,"logical_retention":"9m","max_pending_auth_states":13}}`
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

func TestServingFlagsContainOnlyConfig(t *testing.T) {
	flags := flag.NewFlagSet("mecabroker", flag.ContinueOnError)
	originalArgs := os.Args
	os.Args = []string{"mecabroker"}
	t.Cleanup(func() { os.Args = originalArgs })
	if _, err := parseConfigFlag(flags); err == nil {
		t.Fatal("missing config was accepted")
	}
	var names []string
	flags.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	if got := strings.Join(names, ","); got != "config" {
		t.Fatalf("serving flags = %q, want config only", got)
	}
}
