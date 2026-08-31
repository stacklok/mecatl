package identityissuer

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIdentityIssuerSubstrate_Scenario1_DisabledHasNoSideEffects(t *testing.T) {
	t.Parallel()

	loads := 0
	host, err := NewHost(HostConfig{
		Issuer: Config{Enabled: false},
		LoadKey: func(string) ([]byte, error) {
			loads++
			return nil, errors.New("must not load")
		},
	})
	if err != nil {
		t.Fatalf("new disabled host: %v", err)
	}
	if loads != 0 {
		t.Fatalf("disabled host opened key material %d times", loads)
	}
	if got := host.Status(); got.Enabled || got.Ready || got.Algorithm != "" {
		t.Fatalf("disabled status = %+v, want no issuer generation", got)
	}
	for _, path := range []string{"/livez", "/readyz", "/bundle", "/status"} {
		recorder := httptest.NewRecorder()
		host.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if path == "/livez" && recorder.Code != http.StatusOK {
			t.Errorf("%s status = %d, want %d", path, recorder.Code, http.StatusOK)
		}
		if path != "/livez" && recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want %d", path, recorder.Code, http.StatusServiceUnavailable)
		}
	}
}

func TestIdentityIssuerSubstrate_Scenario4_IssuerOnlyHostSurface(t *testing.T) {
	t.Parallel()

	host := testHost(t)
	for _, path := range []string{"/livez", "/readyz", "/bundle", "/status"} {
		recorder := httptest.NewRecorder()
		host.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("%s status = %d, want %d", path, recorder.Code, http.StatusOK)
		}
	}
	for _, path := range []string{"/issue", "/mint", "/token", "/vmcp", "/toolhive", "/redis", "/v1/converse"} {
		recorder := httptest.NewRecorder()
		host.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("unsafe surface %s status = %d, want %d", path, recorder.Code, http.StatusNotFound)
		}
	}
}

func TestInvariant_identity_signer_outside_execution_process(t *testing.T) {
	t.Parallel()

	loads := 0
	_, err := NewHost(HostConfig{
		Issuer:           testConfig(),
		Manifest:         testManifest("active", true),
		ExecutionSurface: true,
		LoadKey: func(string) ([]byte, error) {
			loads++
			return testPKCS8(t), nil
		},
	})
	if err == nil {
		t.Fatal("unsafe signer-plus-execution composition was accepted")
	}
	if loads != 0 {
		t.Fatalf("unsafe composition opened key material %d times", loads)
	}

	for _, file := range []string{"host.go", "host_http.go"} {
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(".", file), nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", file, parseErr)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range []string{"engine/agent", "provider/", "engine/tool", "os/exec", "fstools", "command"} {
				if strings.Contains(path, forbidden) {
					t.Errorf("issuer host imports forbidden execution dependency %q in %s", path, file)
				}
			}
		}
	}
}

func TestIdentityIssuerSubstrate_Scenario4_SecretContainment(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		filepath.Join("..", "..", "deploy", "helm", "mecak8s", "templates", "deployment.yaml"),
		filepath.Join("..", "..", "deploy", "helm", "mecak8s", "templates", "rbac.yaml"),
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read agent workload projection %s: %v", path, err)
		}
		if bytes.Contains(body, []byte("identity-issuer")) || bytes.Contains(body, []byte("resources: [\"secrets\"]")) {
			t.Fatalf("agent workload projection %s grants issuer Secret custody", path)
		}
	}

	host := testHost(t)
	if status := host.Status(); !status.Ready || status.Algorithm != "ES256" {
		t.Fatalf("issuer host positive-control status = %+v", status)
	}
}

func TestADR_0251_SecretCanariesNeverLeak(t *testing.T) {
	t.Parallel()

	privateCanary := "PRIVATE-KEY-CANARY-4f64d99b"
	bearerCanary := "BEARER-CANARY-53a8bbd1"
	host, err := NewHost(HostConfig{
		Issuer:      testConfig(),
		Manifest:    testManifest("active", true),
		RefreshHint: time.Minute,
		LoadKey: func(string) ([]byte, error) {
			return nil, errors.New(privateCanary + " " + bearerCanary)
		},
	})
	if host != nil {
		t.Fatal("host published after key loading failed")
	}
	if err == nil {
		t.Fatal("key loading failure was accepted")
	}

	projections := []string{err.Error()}
	for _, path := range []string{"/bundle", "/status", "/readyz", "/livez"} {
		recorder := httptest.NewRecorder()
		testHost(t).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		projections = append(projections, recorder.Body.String())
	}
	ready := httptest.NewRecorder()
	NewDisabledHost().Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	projections = append(projections, ready.Body.String())
	for _, projection := range projections {
		for _, canary := range []string{privateCanary, bearerCanary} {
			if strings.Contains(projection, canary) {
				t.Fatalf("secret canary leaked into observable projection: %q", projection)
			}
		}
	}
}

func testHost(t *testing.T) *Host {
	t.Helper()
	der := testPKCS8(t)
	host, err := NewHost(HostConfig{
		Issuer:      testConfig(),
		Manifest:    testManifest("active", true),
		RefreshHint: time.Minute,
		LoadKey: func(name string) ([]byte, error) {
			if name != "active" {
				return nil, errors.New("unexpected key name")
			}
			return der, nil
		},
	})
	if err != nil {
		t.Fatalf("new issuer host: %v", err)
	}
	return host
}
