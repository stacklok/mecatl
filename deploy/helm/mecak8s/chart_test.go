package mecak8s_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func chartDir(t *testing.T) string {
	t.Helper()
	return "."
}

func helm(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart render tests")
	}
	cmd := exec.Command("helm", args...)
	cmd.Dir = chartDir(t)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func productionArgs() []string {
	return []string{"template", "production", ".", "--set", "image.tag=v0.0.0", "--set", "redis.endpoint=redis.example.internal:6380", "--set", "redis.credentialsSecret=redis-credentials"}
}

func TestMecak8sHelmChart_Scenario1_ProductionValuesRequireExternalRedis(t *testing.T) {
	if _, err := helm(t, "template", "production", "."); err == nil {
		t.Fatal("production defaults rendered without external Redis inputs")
	}
	args := productionArgs()
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	if strings.Contains(rendered, "kind: StatefulSet") || strings.Contains(rendered, "name: redis\n") {
		t.Fatal("production render created local Redis")
	}
	if !strings.Contains(rendered, "secretName: redis-credentials") || !strings.Contains(rendered, "redis.example.internal:6380") {
		t.Fatal("production render did not reference external Redis credentials and endpoint")
	}
	args = append(productionArgs(), "--set", "image.digest=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "--set", "image.tag=")
	rendered, err = helm(t, args...)
	if err != nil {
		t.Fatalf("render production digest image: %v", err)
	}
	if !strings.Contains(rendered, "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("production render did not use the configured digest image")
	}
	args = append(productionArgs(), "--set", "image.digest=,image.tag=")
	if _, err := helm(t, args...); err == nil {
		t.Fatal("production render accepted an image without a tag or digest")
	}
	args = append(productionArgs(), "--set", "image.digest=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if _, err := helm(t, args...); err == nil {
		t.Fatal("production render accepted both an image tag and digest")
	}
}

func TestMecak8sHelmChart_SecureRedisModes(t *testing.T) {
	base := productionArgs()
	for _, tc := range []struct {
		name      string
		set       []string
		want      []string
		forbidden []string
	}{
		{"private CA only", nil, []string{"--redis-tls-ca=/var/run/secrets/redis/ca.pem"}, []string{"--redis-username-file", "--redis-password-file", "--redis-tls\n"}},
		{"password default ACL", []string{"redis.passwordKey=password"}, []string{"--redis-password-file=/var/run/secrets/redis/password", "--redis-tls-ca=/var/run/secrets/redis/ca.pem"}, []string{"--redis-username-file"}},
		{"username and password ACL", []string{"redis.usernameKey=username", "redis.passwordKey=password"}, []string{"--redis-username-file=/var/run/secrets/redis/username", "--redis-password-file=/var/run/secrets/redis/password"}, nil},
		// An empty caKey selects system-trust TLS: no CA path, no mounted Secret.
		{"system trust, no ACL", []string{"redis.caKey="}, []string{"--redis-tls"}, []string{"--redis-tls-ca", "redis-credentials", "/var/run/secrets/redis"}},
		{"system trust with ACL", []string{"redis.caKey=", "redis.passwordKey=password"}, []string{"--redis-tls", "--redis-password-file=/var/run/secrets/redis/password", "secretName: redis-credentials"}, []string{"--redis-tls-ca"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{}, base...)
			if len(tc.set) > 0 {
				args = append(args, "--set", strings.Join(tc.set, ","))
			}
			rendered, err := helm(t, args...)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(rendered, want) {
					t.Fatalf("render missing %q", want)
				}
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(rendered, forbidden) {
					t.Fatalf("render unexpectedly contains %q", forbidden)
				}
			}
			if strings.Contains(rendered, "password: ") {
				t.Fatal("rendered Secret content")
			}
		})
	}

	if _, err := helm(t, append(base, "--set", "redis.clientCertKey=client.pem")...); err == nil {
		t.Fatal("render accepted a removed mTLS value (ADR 0233 dropped client-certificate support)")
	}
	if _, err := helm(t, append(base, "--set", "redis.usernameKey=username")...); err == nil {
		t.Fatal("render accepted a username without a password key")
	}
	// A configured Secret key with no Secret to read it from must fail closed.
	if _, err := helm(t, append(base, "--set", "redis.credentialsSecret=")...); err == nil {
		t.Fatal("render accepted a CA key with no credentialsSecret")
	}
	for _, missing := range []string{"redis.endpoint="} {
		args := append(append([]string{}, base...), "--set", missing)
		if _, err := helm(t, args...); err == nil {
			t.Fatalf("render accepted missing production field %q", missing)
		}
	}

	// System-trust TLS without ACL credentials needs no Secret at all.
	publicTLS := []string{"template", "production", ".", "--set", "image.tag=v0.0.0", "--set", "redis.endpoint=redis.example.internal:6380", "--set", "redis.caKey="}
	rendered, err := helm(t, publicTLS...)
	if err != nil {
		t.Fatalf("render system-trust TLS without Secret: %v", err)
	}
	for _, forbidden := range []string{"redis-credentials", "/var/run/secrets/redis", "secretName:"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("system-trust TLS without ACL unexpectedly references %q", forbidden)
		}
	}
}

func TestMecak8sHelmChart_Scenario1_KindProfileIsExplicitlyLocal(t *testing.T) {
	rendered, err := helm(t, "template", "kind", ".", "-f", "values-kind.yaml")
	if err != nil {
		t.Fatalf("render Kind profile: %v", err)
	}
	for _, want := range []string{"kind: StatefulSet", "name: redis", "image: ko.local/mecak8s:dev", "--redis-url=redis:6379"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Kind render missing %q", want)
		}
	}
	if strings.Contains(rendered, "toolhive.stacklok.dev") || strings.Contains(rendered, "VirtualMCPServer") {
		t.Fatal("Kind render includes ToolHive resources")
	}
	for _, forbidden := range []string{"redis-credentials", "--redis-password-file", "--redis-tls-ca", "--redis-username-file", "--redis-tls\n"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("Kind render includes secure Redis material %q", forbidden)
		}
	}
}

func TestMecak8sHelmChart_ExternalRedisProjectsExactSecretKeys(t *testing.T) {
	args := productionArgs()
	args = append(args, "--set", "redis.usernameKey=user,redis.passwordKey=pass")
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	if !strings.Contains(rendered, "defaultMode: 0440") {
		t.Fatal("external Redis Secret volume is not mode 0440")
	}
	for _, item := range []string{
		`- {key: "ca.pem", path: "ca.pem"}`,
		`- {key: "user", path: "user"}`,
		`- {key: "pass", path: "pass"}`,
	} {
		if !strings.Contains(rendered, item) {
			t.Fatalf("external Redis Secret projection missing %q", item)
		}
	}
	if strings.Contains(rendered, `key: "unused"`) {
		t.Fatal("external Redis Secret projection contains an unconfigured key")
	}
}

func TestMecak8sHelmChart_LocalPlaintextIsExplicitAndExternalIsTLSOnly(t *testing.T) {
	local, err := helm(t, "template", "kind", ".", "-f", "values-kind.yaml")
	if err != nil {
		t.Fatalf("render Kind profile: %v", err)
	}
	if !strings.Contains(local, "--redis-allow-plaintext") {
		t.Fatal("local Kind profile does not explicitly opt in to plaintext Redis")
	}
	for _, forbidden := range []string{"--redis-tls-ca", "redis-credentials", "defaultMode: 0440"} {
		if strings.Contains(local, forbidden) {
			t.Fatalf("local Kind profile contains external Redis material %q", forbidden)
		}
	}

	external, err := helm(t, productionArgs()...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	if strings.Contains(external, "--redis-allow-plaintext") {
		t.Fatal("external Redis profile enables plaintext")
	}
}

// The endpoint must carry an explicit numeric port: it becomes --redis-url, and
// the TLS ServerName is derived from that host:port (ADR 0233).
func TestMecak8sHelmChart_ExternalEndpointRequiresNumericPort(t *testing.T) {
	for _, endpoint := range []string{"redis.example.internal", "redis.example.internal:tls"} {
		t.Run(endpoint, func(t *testing.T) {
			args := append([]string{}, productionArgs()...)
			args = append(args, "--set", "redis.endpoint="+endpoint)
			if _, err := helm(t, args...); err == nil {
				t.Fatalf("render accepted invalid endpoint %q", endpoint)
			}
		})
	}
}

func TestInvariant_mecak8s_storage_free_restricted_workload(t *testing.T) {
	args := productionArgs()
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	for _, want := range []string{
		"runAsNonRoot: true", "readOnlyRootFilesystem: true", "drop: [\"ALL\"]", "type: RuntimeDefault",
		"requests:", "limits:", "readinessProbe:", "livenessProbe:", "path: /readyz", "path: /healthz",
		"emptyDir:", "kind: PodDisruptionBudget", "maxUnavailable: 0",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("agent workload missing %q", want)
		}
	}
	if strings.Contains(rendered, "kind: PersistentVolumeClaim") || strings.Contains(rendered, "volumeClaimTemplates:") || strings.Contains(rendered, "--store-dir") {
		t.Fatal("agent render contains durable local state")
	}
}

func TestMecak8sHelmChart_Scenario1_LeastPrivilegeLease(t *testing.T) {
	rendered, err := helm(t, productionArgs()...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	for _, want := range []string{
		"kind: Role", "apiGroups: [\"coordination.k8s.io\"]", "resources: [\"leases\"]", "verbs: [\"get\", \"create\", \"update\", \"delete\"]",
		"kind: RoleBinding",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("least-privilege render missing %q", want)
		}
	}
	for _, forbidden := range []string{"\"list\"", "\"watch\"", "kind: ClusterRole"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("least-privilege render includes %q", forbidden)
		}
	}
}

func TestMecak8sHelmChart_Scenario1_KindLifecycleUsesNamedCluster(t *testing.T) {
	path := filepath.Join("..", "..", "mecak8s-vmcp", "Taskfile.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"reset-state:", "cluster-ready:", "--kubeconfig={{.KUBECONFIG}}", "--context={{.CONTEXT}}", "kind delete cluster --name={{.CLUSTER}}", "kind-destroy"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lifecycle guard missing %q", want)
		}
	}
	for _, forbidden := range []string{"release-resolve", "toolhive-install", "api.github.com", "curl "} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Phase-A lifecycle can contact ToolHive/GitHub through %q", forbidden)
		}
	}
}

func TestMecak8sHelmChart_OIDC_DisabledByDefault(t *testing.T) {
	rendered, err := helm(t, productionArgs()...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	for _, forbidden := range []string{"--oidc-issuer", "--oidc-audience", "--oidc-jwks-uri", "kind: NetworkPolicy", "raw-driver"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("default render (oidc disabled) unexpectedly contains %q", forbidden)
		}
	}
}

func TestMecak8sHelmChart_OIDC_EnabledRendersArgs(t *testing.T) {
	base := append(productionArgs(), "--set", "oidc.enabled=true", "--set", "oidc.issuer=https://idp.example.com", "--set", "oidc.audience=mecatl")

	rendered, err := helm(t, base...)
	if err != nil {
		t.Fatalf("render oidc-enabled values: %v", err)
	}
	for _, want := range []string{"--oidc-issuer=https://idp.example.com", "--oidc-audience=mecatl", "--oidc-max-jwks-staleness=1h"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("oidc-enabled render missing %q", want)
		}
	}
	if strings.Contains(rendered, "--oidc-jwks-uri") {
		t.Fatal("oidc-enabled render without jwksURI unexpectedly set --oidc-jwks-uri")
	}

	withJWKS := append(append([]string{}, base...), "--set", "oidc.jwksURI=https://idp.example.com/certs")
	rendered, err = helm(t, withJWKS...)
	if err != nil {
		t.Fatalf("render oidc-enabled values with jwksURI: %v", err)
	}
	if !strings.Contains(rendered, "--oidc-jwks-uri=https://idp.example.com/certs") {
		t.Fatal("oidc-enabled render with jwksURI missing --oidc-jwks-uri")
	}
}

func TestMecak8sHelmChart_OIDC_EnabledWithoutAudienceFails(t *testing.T) {
	args := append(productionArgs(), "--set", "oidc.enabled=true", "--set", "oidc.issuer=https://idp.example.com")
	if _, err := helm(t, args...); err == nil {
		t.Fatal("render accepted oidc.enabled=true with no oidc.audience")
	}
}

func TestMecak8sHelmChart_OIDC_EnabledWithoutIssuerFails(t *testing.T) {
	args := append(productionArgs(), "--set", "oidc.enabled=true", "--set", "oidc.audience=mecatl")
	if _, err := helm(t, args...); err == nil {
		t.Fatal("render accepted oidc.enabled=true with no oidc.issuer")
	}
}

func TestMecak8sHelmChart_OIDC_RawDriverNetworkPolicyShape(t *testing.T) {
	args := append(productionArgs(), "--set", "oidc.enabled=true", "--set", "oidc.issuer=https://idp.example.com", "--set", "oidc.audience=mecatl")
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render oidc-enabled values: %v", err)
	}
	if !strings.Contains(rendered, "name: production-mecak8s-raw-driver") {
		t.Fatal("oidc-enabled render missing the raw-driver NetworkPolicy")
	}
	for _, want := range []string{
		"app.kubernetes.io/component: raw-driver",
		"app.kubernetes.io/component: agent",
		"port: 9090",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("raw-driver NetworkPolicy render missing %q", want)
		}
	}
}
