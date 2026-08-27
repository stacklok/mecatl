package deploycheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func TestVMCPFixturePinsAndTasks(t *testing.T) {
	taskfile := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	gitignore := readRepoFile(t, ".gitignore")
	for _, required := range []string{"vmcp-check:", "vmcp-setup:", "vmcp-status:", "toolhive-operator-crds", "--version=0.44.0", "vmcp-redis-auth", "vmcp-signing-key", "vmcp-hmac", "wait --for=condition=Ready virtualmcpserver/vmcp", "rollout status deployment/vmcp", "kind get kubeconfig --name={{.CLUSTER}} > {{.KUBECONFIG}}"} {
		if !strings.Contains(taskfile, required) {
			t.Errorf("fixture Taskfile missing %q", required)
		}
	}
	if !strings.Contains(taskfile, "KUBECONFIG: deploy/mecak8s-vmcp/kconfig.yaml") {
		t.Error("fixture Taskfile must use the discoverable fixture-local kubeconfig")
	}
	if !strings.Contains(gitignore, "/deploy/mecak8s-vmcp/kconfig.yaml") {
		t.Error("fixture-local kubeconfig must be gitignored")
	}
	for _, forbidden := range []string{"set -x", "kubectl get secret -o yaml", "kubectl get secret -o json"} {
		if strings.Contains(taskfile, forbidden) {
			t.Errorf("fixture Taskfile contains stale or unsafe reference %q", forbidden)
		}
	}
}

func TestVMCPFixturePinsBackendAndBoundary(t *testing.T) {
	versions := readRepoFile(t, "deploy/mecak8s-vmcp/versions.yaml")
	manifest := readRepoFile(t, "deploy/mecak8s-vmcp/vmcp.yaml")
	dex := readRepoFile(t, "deploy/mecak8s-vmcp/dex.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")
	for _, required := range []string{"0.44.0", "b3df9689bdb7d55d0765565890ba9dc0c076dec0", "1.1.1", "e5b8908ed6f53c1171ac805d82cf858d2982fa19e"} {
		if !strings.Contains(versions, required) {
			t.Errorf("versions missing %q", required)
		}
	}
	for _, required := range []string{"kind: MCPServer", "metadata:\n  name: yardstick", "groupRef: {name: vmcp}", "ghcr.io/stackloklabs/yardstick/yardstick-server:1.1.1", "transport: streamable-http", "kind: VirtualMCPServer\nmetadata:\n  name: vmcp", "disableUpstreamTokenInjection: true", "kind: MCPOIDCConfig", "caBundleRef:", "name: dex-fixture-ca", "kind: MCPGroup", "scopes: [openid, profile, email, offline_access]"} {
		if !strings.Contains(manifest, required) {
			t.Errorf("vMCP manifest missing %q", required)
		}
	}
	for _, required := range []string{"id: vmcp-browser", "public: true", "https://dex.mecatl-vmcp.svc.cluster.local:5556/auth", "https://dex.mecatl-vmcp.svc.cluster.local:5556/token", "http://127.0.0.1:18080/oauth/callback"} {
		if !strings.Contains(dex, required) && !strings.Contains(manifest, required) {
			t.Errorf("Dex fixture missing vMCP upstream client setting %q", required)
		}
	}
	if strings.Contains(manifest, "authorizationEndpoint: http://") {
		t.Error("vMCP browser authorization endpoint must use the shared HTTPS Dex issuer")
	}
	for _, required := range []string{"not in the outbound vMCP path", "mecak8s outbound brokerage", "only in memory", "Secret123", "not Kubernetes Secret values", "provider credentials", "reusable user"} {
		if !strings.Contains(readme, required) {
			t.Errorf("README missing scope or custody statement %q", required)
		}
	}
}

func TestVMCPFixtureDoesNotCommitCredentialValues(t *testing.T) {
	for _, path := range []string{"deploy/mecak8s-vmcp/Taskfile.yml", "deploy/mecak8s-vmcp/README.md", "deploy/mecak8s-vmcp/versions.yaml", "deploy/mecak8s-vmcp/toolhive-redis.yaml", "deploy/mecak8s-vmcp/vmcp.yaml"} {
		body := readRepoFile(t, path)
		for _, forbidden := range []string{"stringData:", "Authorization: Bearer", "refresh_token", "access_token", "clientSecret:"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains prohibited credential-shaped literal %q", path, forbidden)
			}
		}
	}
}

func TestMecak8sVMCPPOC_Scenario1_NetworkPolicyScope(t *testing.T) {
	dex := readRepoFile(t, "deploy/mecak8s-vmcp/dex.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	if strings.Contains(dex, "kind: NetworkPolicy") {
		t.Error("local fixture must not install a partial NetworkPolicy that blocks DNS or Redis")
	}
	for _, required := range []string{"deliberately installs no `NetworkPolicy`", "blocked DNS and Redis", "out of scope"} {
		if !strings.Contains(readme, required) {
			t.Errorf("README missing NetworkPolicy scope statement %q", required)
		}
	}
}

func TestMecak8sVMCPPOC_Scenario2_DexTLSFixture(t *testing.T) {
	taskfile := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	dex := readRepoFile(t, "deploy/mecak8s-vmcp/dex.yaml")
	tls := readRepoFile(t, "deploy/mecak8s-vmcp/dex-tls.yaml")
	versions := readRepoFile(t, "deploy/mecak8s-vmcp/versions.yaml")

	for _, required := range []string{"cert-manager-install:", "jetstack/charts/cert-manager", "--version=v1.17.2", "--set crds.enabled=true", "dex-certificate-apply:", "wait --for=condition=Ready certificate/dex-tls"} {
		if !strings.Contains(taskfile, required) {
			t.Errorf("Taskfile missing cert-manager lifecycle step %q", required)
		}
	}
	if strings.Index(taskfile, "cert-manager-install") >= strings.Index(taskfile, "dex-certificate-apply") ||
		strings.Index(taskfile, "dex-certificate-apply") >= strings.Index(taskfile, "dex-apply") {
		t.Error("Taskfile must install cert-manager and issue Dex TLS before starting Dex")
	}
	for _, required := range []string{"certManager:\n  chartVersion: \"v1.17.2\"", "kind: Issuer", "name: dex-fixture-ca", "kind: Certificate", "name: dex-tls", "secretName: dex-tls", "https: 0.0.0.0:5556", "tls.crt", "tls.key", "name: https", "targetPort: https"} {
		if !strings.Contains(dex, required) && !strings.Contains(tls, required) && !strings.Contains(versions, required) {
			t.Errorf("Dex TLS fixture missing %q", required)
		}
	}
	for _, forbidden := range []string{"kind: Secret", "stringData:", "-----BEGIN"} {
		if strings.Contains(tls, forbidden) {
			t.Errorf("Dex TLS fixture commits credential material through %q", forbidden)
		}
	}
}

func TestMecak8sVMCPPOC_Scenario2_SharedDexIssuer(t *testing.T) {
	dex := readRepoFile(t, "deploy/mecak8s-vmcp/dex.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	const issuer = "https://dex.mecatl-vmcp.svc.cluster.local:5556"
	for _, required := range []string{"issuer: " + issuer, "name: dex", "port: 5556", "nodePort: 30556", "127.0.0.1 dex.mecatl-vmcp.svc.cluster.local", issuer + "/.well-known/openid-configuration", issuer + "/keys"} {
		if !strings.Contains(dex, required) && !strings.Contains(readme, required) {
			t.Errorf("shared Dex issuer contract missing %q", required)
		}
	}
	if !strings.Contains(readme, "same HTTPS issuer URL") {
		t.Error("README must state that pod and host use the same HTTPS issuer URL")
	}
}

func TestMecak8sVMCPPOC_Scenario3_Mecak8sTLSConnection(t *testing.T) {
	taskfile := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	for _, required := range []string{
		"kind-hosts-add",
		"kind-hosts-remove",
		"127.0.0.1 mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local",
		"18081",

		"base64 --decode > .scratch/mecak8s-vmcp-ca.crt",
		"plaintext and an untrusted CA",
		"live client connection demonstration\nis deferred",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("mecak8s TLS connection contract missing %q", required)
		}
	}
	if !strings.Contains(taskfile, "--values=deploy/helm/mecak8s/values-kind.yaml") {
		t.Error("Kind setup does not install the TLS-enabled Kind values")
	}
	if !strings.Contains(taskfile, "--values=deploy/helm/mecak8s/values-kind-vmcp.yaml") {
		t.Error("Kind setup does not layer the vMCP OIDC/TLS overlay (values-kind.yaml alone must stay secret-free for e2e/k8s)")
	}
}

func TestMecak8sVMCPPOC_Scenario3_OIDCNegativeCases(t *testing.T) {
	taskfile := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	values := readRepoFile(t, "deploy/helm/mecak8s/values-kind.yaml") + readRepoFile(t, "deploy/helm/mecak8s/values-kind-vmcp.yaml")
	deployment := readRepoFile(t, "deploy/helm/mecak8s/templates/deployment.yaml")
	dex := readRepoFile(t, "deploy/mecak8s-vmcp/dex.yaml")

	for _, required := range []string{
		"enabled: true",
		"issuer: https://dex.mecatl-vmcp.svc.cluster.local:5556",
		"audience: mecatui-kind",
		"allowPrivateHTTPSIssuer: true",
		"caSecret: dex-fixture-ca",
		"caKey: tls.crt",
	} {
		if !strings.Contains(values, required) {
			t.Errorf("Kind OIDC configuration missing %q", required)
		}
	}
	for _, user := range []string{"username: alice", "username: bob"} {
		if !strings.Contains(dex, user) {
			t.Errorf("Dex fixture missing valid caller %q", user)
		}
	}
	setupEnd := strings.Index(taskfile, "  kind-status:")
	if setupEnd < 0 {
		t.Fatal("Taskfile has no kind-status boundary")
	}
	setup := taskfile[:setupEnd]
	if strings.Index(setup, "task: dex-certificate-apply") >= strings.Index(setup, "task: dex-apply") ||
		strings.Index(setup, "task: dex-apply") >= strings.Index(setup, "task: chart-apply") {
		t.Error("Kind setup must issue and start HTTPS Dex before mecak8s starts OIDC discovery")
	}
	for _, required := range []string{"--oidc-allow-private-https-issuer", "--oidc-ca-cert-file="} {
		if !strings.Contains(deployment, required) {
			t.Errorf("Kind OIDC fixture deployment wiring missing %q", required)
		}
	}
	for _, forbidden := range []string{"oidc-insecure-allow-private-issuer", "issuer: http://", "--auth-token"} {
		if strings.Contains(taskfile, forbidden) || strings.Contains(values, forbidden) {
			t.Errorf("Kind OIDC fixture weakens or replaces caller identity with %q", forbidden)
		}
	}
}

func TestMecak8sVMCPPOC_Scenario3_SeparateDexClients(t *testing.T) {
	dex := readRepoFile(t, "deploy/mecak8s-vmcp/dex.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	for _, required := range []string{
		"id: vmcp-browser",
		"id: mecatui-kind",
		"public: true",
		"http://127.0.0.1:18080/oauth/callback",
		"http://127.0.0.1:18473/oauth/callback",
	} {
		if !strings.Contains(dex, required) {
			t.Errorf("Dex public-client registration missing %q", required)
		}
	}
	for _, required := range []string{
		"`vmcp-browser`", "`mecatui-kind`",
		"http://127.0.0.1:18080/oauth/callback",
		"http://127.0.0.1:18473/oauth/callback",
		"Neither client has a secret",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("README public-client contract missing %q", required)
		}
	}
	if strings.Contains(dex, "clientSecret:") {
		t.Error("Dex fixture commits a client secret")
	}
}

func TestMecak8sVMCPPOC_Scenario3_NoScopeEnforcement(t *testing.T) {
	values := readRepoFile(t, "deploy/helm/mecak8s/values-kind.yaml") + readRepoFile(t, "deploy/helm/mecak8s/values-kind-vmcp.yaml")
	deployment := readRepoFile(t, "deploy/helm/mecak8s/templates/deployment.yaml")

	for _, forbidden := range []string{"scope", "--auth-token", "--oidc-insecure-allow-private-issuer"} {
		if strings.Contains(values, forbidden) || strings.Contains(deployment, forbidden) {
			t.Errorf("mecak8s caller identity adds an out-of-scope authority mechanism %q", forbidden)
		}
	}
	for _, required := range []string{"--oidc-issuer=", "--oidc-audience="} {
		if !strings.Contains(deployment, required) {
			t.Errorf("mecak8s caller identity is missing %q", required)
		}
	}
}

func TestMecak8sVMCPLiveProviderUsesExplicitLocalUnsafeMode(t *testing.T) {
	taskfile := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	liveBranch := strings.Index(taskfile, `if [ -n "${OPENROUTER_API_KEY:-}" ]`)
	if liveBranch < 0 {
		t.Fatal("fixture Taskfile has no live-provider branch")
	}
	mockBranch := strings.Index(taskfile[liveBranch:], "else")
	if mockBranch < 0 {
		t.Fatal("fixture Taskfile has no live-provider branch")
	}
	branch := taskfile[liveBranch : liveBranch+mockBranch]
	for _, required := range []string{"mockProvider=false", "security.allowUnsafeRealProvider=true", "extraEnv[0].name=OPENROUTER_API_KEY", "valueFrom.secretKeyRef"} {
		if !strings.Contains(branch, required) {
			t.Errorf("live-provider branch missing %q", required)
		}
	}
}

func TestMecak8sVMCPPOC_Scenario4_KindLoopbackNodePorts(t *testing.T) {
	taskfile := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	kindConfig := readRepoFile(t, "deploy/mecak8s-vmcp/kind-config.yaml")
	dex := readRepoFile(t, "deploy/mecak8s-vmcp/dex.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	for _, required := range []string{
		"--config=deploy/mecak8s-vmcp/kind-config.yaml",
		"containerPort: 30556", "hostPort: 5556", "listenAddress: 127.0.0.1",
		"containerPort: 30081", "hostPort: 18081",
		"type: NodePort", "nodePort: 30556",
		"kind-hosts-add:", "kind-hosts-remove:",
	} {
		if !strings.Contains(taskfile, required) && !strings.Contains(kindConfig, required) && !strings.Contains(dex, required) {
			t.Errorf("Kind local journey missing %q", required)
		}
	}
	if !strings.Contains(readme, "127.0.0.1 dex.mecatl-vmcp.svc.cluster.local") ||
		!strings.Contains(readme, "127.0.0.1 mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local") {
		t.Error("README must document the exact temporary host aliases")
	}
	if strings.Contains(kindConfig, "listenAddress: 0.0.0.0") {
		t.Error("Kind port mappings must not bind non-loopback addresses")
	}
}

func TestMecak8sVMCPPOC_Scenario2_DexCertificateContract(t *testing.T) {
	tls := readRepoFile(t, "deploy/mecak8s-vmcp/dex-tls.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	for _, required := range []string{"dnsNames:", "dex.mecatl-vmcp.svc.cluster.local", "localhost", "ipAddresses:", "127.0.0.1", "issuerRef:", "name: dex-fixture-ca", "ca:\n    secretName: dex-fixture-ca", "secretName: dex-fixture-ca"} {
		if !strings.Contains(tls, required) {
			t.Errorf("Dex certificate contract missing %q", required)
		}
	}
	for _, required := range []string{"dex-fixture-ca", "tls.crt", "without OIDC insecure relaxation"} {
		if !strings.Contains(readme, required) {
			t.Errorf("README missing CA trust guidance %q", required)
		}
	}
}
