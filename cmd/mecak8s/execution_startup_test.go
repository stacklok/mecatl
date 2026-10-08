package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestDisabledExecutionStartupDoesNotContactExecutionOrKubernetes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer endpoint.Close()
	kubeconfig := filepath.Join(home, "kubeconfig")
	contents := "apiVersion: v1\nkind: Config\nclusters:\n- name: poison\n  cluster:\n    server: " + endpoint.URL + "\ncontexts:\n- name: poison\n  context:\n    cluster: poison\n    user: offline\ncurrent-context: poison\nusers:\n- name: offline\n  user: {}\n"
	if err := os.WriteFile(kubeconfig, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	oldArgs, oldLogger := os.Args, slog.Default()
	t.Cleanup(func() { os.Args = oldArgs; slog.SetDefault(oldLogger) })
	// Exercise run(), including actual app.Build, stopping at the first listener.
	// Poison TLS paths would fail before serve if the disabled client were built.
	os.Args = []string{"mecak8s", "--mock", "--posture=strict", "--no-soul", "--no-user-model", "--no-scheduler", "--session-lease-k8s-namespace=", "--grpc-addr=invalid-address", "--execution-enabled=false", "--execution-endpoint=" + strings.TrimPrefix(endpoint.URL, "http://"), "--execution-template-id=unused", "--execution-template-revision=v1-" + strings.Repeat("a", 64), "--execution-tls-ca=" + filepath.Join(home, "absent-ca"), "--execution-tls-cert=" + filepath.Join(home, "absent-cert"), "--execution-tls-key=" + filepath.Join(home, "absent-key")}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "invalid-address") {
		t.Fatalf("did not reach listener after composition: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("disabled startup made %d API requests", requests.Load())
	}
}

func TestExecutionPreflightRejectsIncompleteAndIncompatibleCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	required := []string{"--execution-endpoint=127.0.0.1:1", "--execution-template-id=go", "--execution-template-revision=v1-" + strings.Repeat("a", 64), "--execution-allowed-subjects=alice", "--execution-tls-ca=unused", "--execution-tls-cert=unused", "--execution-tls-key=unused"}
	base := []string{"--mock", "--execution-enabled", "--oidc-issuer=https://issuer.example", "--oidc-audience=mecatl"}
	if _, err := parseFlags(append(append([]string{}, base...), required...)); err != nil {
		t.Fatalf("complete configuration rejected: %v", err)
	}
	for missing := range required {
		args := append([]string{}, base...)
		for i, flag := range required {
			if i != missing {
				args = append(args, flag)
			}
		}
		if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "--execution-enabled requires") {
			t.Fatalf("missing %s: %v", required[missing], err)
		}
	}
	for _, extra := range []string{"--workspace=unused", "--redis-filesystem", "--enable-parallel", "--enable-teams"} {
		args := append(append(append([]string{}, base...), required...), extra)
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("incompatible %s accepted", extra)
		}
	}
}

func TestExecutionHostOwnerPolicyIsExplicit(t *testing.T) {
	cfg, err := parseFlags([]string{"--mock", "--execution-enabled", "--execution-endpoint=127.0.0.1:1", "--execution-template-id=go", "--execution-template-revision=v1-" + strings.Repeat("a", 64), "--execution-allowed-subjects=alice", "--execution-tls-ca=unused", "--execution-tls-cert=unused", "--execution-tls-key=unused", "--oidc-issuer=https://issuer.example", "--oidc-audience=mecatl"})
	if err != nil {
		t.Fatal(err)
	}
	allowed := appConfig(cfg, nil, observability{}).ExecutionTemplateAllowed
	for _, subject := range []string{"bob", "alice-admin", ""} {
		if allowed(&session.Principal{Issuer: "https://issuer.example", Subject: subject, GrantType: session.GrantTypeUser}, "go", cfg.executionTemplateRevision) {
			t.Fatalf("unauthorized subject %q can bind", subject)
		}
	}
	if !allowed(&session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}, "go", cfg.executionTemplateRevision) {
		t.Fatal("authorized subject cannot bind")
	}
}
