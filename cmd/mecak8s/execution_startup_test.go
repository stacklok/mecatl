package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	if cfg.noScheduler || !appConfig(cfg, nil, observability{}).SchedulerEnabled {
		t.Fatal("remote execution silently disabled scheduled-task fires")
	}
	optOut, err := parseFlags([]string{"--mock", "--execution-enabled", "--execution-endpoint=127.0.0.1:1", "--execution-template-id=go", "--execution-template-revision=v1-" + strings.Repeat("a", 64), "--execution-allowed-subjects=alice", "--execution-tls-ca=unused", "--execution-tls-cert=unused", "--execution-tls-key=unused", "--oidc-issuer=https://issuer.example", "--oidc-audience=mecatl", "--no-scheduler"})
	if err != nil || !optOut.noScheduler || appConfig(optOut, nil, observability{}).SchedulerEnabled {
		t.Fatalf("explicit remote scheduler opt-out not honored: %v", err)
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

// executionArgs returns a complete execution configuration with the given
// allowlist and OIDC flags.
func executionArgs(extra ...string) []string {
	return append([]string{"--mock", "--execution-enabled", "--execution-endpoint=127.0.0.1:1", "--execution-template-id=go", "--execution-template-revision=v1-" + strings.Repeat("a", 64), "--execution-tls-ca=unused", "--execution-tls-cert=unused", "--execution-tls-key=unused", "--oidc-issuer=https://as1.example", "--oidc-audience=mecatl"}, extra...)
}

const subIDFlag = "--oidc-owner-identity=sub_id"

func TestExecutionAllowlistMatchesIssuerAndSubject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	entries := []string{"--execution-allowed-principal=https://idp.example|alice", "--execution-allowed-principal=https://idp.example|auth0|bob", "--execution-allowed-principal=http://127.0.0.1:5556/dex|team carol"}
	cases := map[string][]string{
		"principal, owner identity off": entries,
		"principal, owner identity on":  append(slices.Clone(entries), subIDFlag, "--oidc-accepted-client-ids=mecatui"),
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseFlags(executionArgs(extra...))
			if err != nil {
				t.Fatal(err)
			}
			built := appConfig(cfg, nil, observability{})
			for _, p := range []session.Principal{{Issuer: "https://idp.example", Subject: "alice"}, {Issuer: "https://idp.example", Subject: "auth0|bob"}, {Issuer: "http://127.0.0.1:5556/dex", Subject: "team carol"}} {
				p.GrantType = session.GrantTypeUser
				if !built.ExecutionTemplateAllowed(&p, "go", cfg.executionTemplateRevision) || !built.ExecutionOwnerAllowed(&p) {
					t.Fatalf("allowlisted %+v refused", p)
				}
			}
			for _, p := range []session.Principal{
				{Issuer: "https://other.example", Subject: "alice"},
				{Issuer: "https://as1.example", Subject: "alice"},
				{Issuer: "https://idp.example/", Subject: "alice"},
				{Issuer: "https://idp.example", Subject: "auth0"},
				{Issuer: "https://idp.example|auth0", Subject: "bob"},
			} {
				p.GrantType = session.GrantTypeUser
				if built.ExecutionTemplateAllowed(&p, "go", cfg.executionTemplateRevision) || built.ExecutionOwnerAllowed(&p) {
					t.Fatalf("unlisted %+v allowed", p)
				}
			}
		})
	}
}

func TestExecutionAllowedSubjectsMeansOIDCIssuer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := parseFlags(executionArgs("--execution-allowed-subjects=alice,bob"))
	if err != nil {
		t.Fatal(err)
	}
	built := appConfig(cfg, nil, observability{})
	for _, tc := range []struct {
		issuer, subject string
		want            bool
	}{
		{"https://as1.example", "alice", true},
		{"https://as1.example", "bob", true},
		{"https://other.example", "alice", false},
		{"https://as1.example", "carol", false},
	} {
		p := &session.Principal{Issuer: tc.issuer, Subject: tc.subject, GrantType: session.GrantTypeUser}
		if built.ExecutionTemplateAllowed(p, "go", cfg.executionTemplateRevision) != tc.want || built.ExecutionOwnerAllowed(p) != tc.want {
			t.Fatalf("%s|%s: want allowed=%v", tc.issuer, tc.subject, tc.want)
		}
	}
}

func TestExecutionAllowlistRejectsUnsafeConfiguration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for name, tc := range map[string]struct {
		extra []string
		want  string
	}{
		"subjects with owner identity sub_id": {[]string{"--execution-allowed-subjects=alice", subIDFlag, "--oidc-accepted-client-ids=mecatui"}, "--execution-allowed-principal=ISSUER|SUBJECT"},
		"both forms":                          {[]string{"--execution-allowed-subjects=alice", "--execution-allowed-principal=https://idp.example|alice"}, "mutually exclusive"},
		"no separator":                        {[]string{"--execution-allowed-principal=alice"}, "want ISSUER|SUBJECT"},
		"empty issuer":                        {[]string{"--execution-allowed-principal=|alice"}, "nonempty"},
		"empty subject":                       {[]string{"--execution-allowed-principal=https://idp.example|"}, "nonempty"},
		"padded subject":                      {[]string{"--execution-allowed-principal=https://idp.example| alice"}, "unpadded"},
		"padded issuer":                       {[]string{"--execution-allowed-principal=https://idp.example |alice"}, "unpadded"},
		"control character":                   {[]string{"--execution-allowed-principal=https://idp.example|al\x00ice"}, "control characters"},
		"duplicate":                           {[]string{"--execution-allowed-principal=https://idp.example|alice", "--execution-allowed-principal=https://idp.example|alice"}, "duplicate"},
		"padded legacy subject":               {[]string{"--execution-allowed-subjects=alice, bob"}, "unpadded"},
		"issuer without scheme":               {[]string{"--execution-allowed-principal=idp.example|alice"}, "must be an https:// or http:// URL"},
		"issuer with other scheme":            {[]string{"--execution-allowed-principal=ftp://idp.example|alice"}, "must be an https:// or http:// URL"},
		"issuer without host":                 {[]string{"--execution-allowed-principal=https:///tenant|alice"}, "must be an https:// or http:// URL"},
		"issuer with credentials":             {[]string{"--execution-allowed-principal=https://user@idp.example|alice"}, "must be an https:// or http:// URL"},
		"issuer with query":                   {[]string{"--execution-allowed-principal=https://idp.example?x=1|alice"}, "must be an https:// or http:// URL"},
		"issuer with fragment":                {[]string{"--execution-allowed-principal=https://idp.example#x|alice"}, "must be an https:// or http:// URL"},
		"issuer with space":                   {[]string{"--execution-allowed-principal=https://idp.example/a b|alice"}, "must be an https:// or http:// URL"},
		"issuer with malformed escape":        {[]string{"--execution-allowed-principal=https://idp.example/%zz|alice"}, "must be an https:// or http:// URL"},
		"issuer with brace in host":           {[]string{"--execution-allowed-principal=https://idp{x}.example|alice"}, "must be an https:// or http:// URL"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFlags(executionArgs(tc.extra...)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseFlags error = %v, want %q", err, tc.want)
			}
		})
	}
}
