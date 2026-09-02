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
	for _, required := range []string{"vmcp-check:", "vmcp-setup:", "vmcp-status:", "toolhive-operator-crds", "--version=0.45.0", "vmcp-redis-auth", "vmcp-signing-key", "vmcp-hmac", "wait --for=condition=Ready virtualmcpserver/vmcp", "rollout status deployment/vmcp", "kind get kubeconfig --name={{.CLUSTER}} > {{.KUBECONFIG}}"} {
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
	kc := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")
	for _, required := range []string{"0.45.0", "cc922a8b47652988ae4d057a957942385fc59270", "1.1.1", "e5b8908ed6f53c1171ac805d82cf858d2982fa19e"} {
		if !strings.Contains(versions, required) {
			t.Errorf("versions missing %q", required)
		}
	}
	for _, required := range []string{"kind: MCPServer", "metadata:\n  name: yardstick", "groupRef: {name: vmcp}", "ghcr.io/stackloklabs/yardstick/yardstick-server:1.1.1", "transport: streamable-http", "kind: VirtualMCPServer\nmetadata:\n  name: vmcp", "disableUpstreamTokenInjection: true", "kind: MCPOIDCConfig", "caBundleRef:", "name: fixture-ca", "kind: MCPGroup", "\"mcp:read\""} {
		if !strings.Contains(manifest, required) {
			t.Errorf("vMCP manifest missing %q", required)
		}
	}
	for _, required := range []string{"\"clientId\": \"vmcp-browser\"", "\"publicClient\": true", "https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl/protocol/openid-connect/auth", "https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl/protocol/openid-connect/token", "http://127.0.0.1:18080/oauth/callback"} {
		if !strings.Contains(kc, required) && !strings.Contains(manifest, required) {
			t.Errorf("Keycloak fixture missing vMCP upstream client setting %q", required)
		}
	}
	if strings.Contains(manifest, "authorizationEndpoint: http://") {
		t.Error("vMCP browser authorization endpoint must use the shared HTTPS Keycloak issuer")
	}
	for _, required := range []string{"not in the outbound vMCP path", "mecak8s outbound brokerage", "only in memory", "Secret123", "not Kubernetes Secret values", "provider credentials", "reusable user"} {
		if !strings.Contains(readme, required) {
			t.Errorf("README missing scope or custody statement %q", required)
		}
	}
}

func TestMecak8sVMCPFixtureDisablesKeycloakServiceAccountToken(t *testing.T) {
	for _, path := range []string{"deploy/mecak8s-kind/keycloak.yaml", "deploy/mecak8s-vmcp/keycloak.yaml"} {
		body := readRepoFile(t, path)
		if !strings.Contains(body, "    spec:\n      automountServiceAccountToken: false\n      securityContext:") {
			t.Errorf("%s must disable the Keycloak service-account token at PodSpec level", path)
		}
	}
}

func TestMecak8sVMCPREADMERemoteLoginContract(t *testing.T) {
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")
	for _, required := range []string{
		"mecatui-kind --audience http://127.0.0.1:18080/mcp",
		"Authorization Code + PKCE, not device flow",
		"ssh -N -L 18473:127.0.0.1:18473 user@login-host",
		"fixed callback",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("vMCP README missing remote-login contract %q", required)
		}
	}
	if strings.Contains(readme, "--audience mecatui-kind") {
		t.Error("vMCP README must not use the client ID as the resource audience")
	}
}

// TestMecak8sVMCPFixtureOfflineAccess keeps the generated Keycloak realm and
// the remote-login walkthrough aligned: requesting this optional scope is what
// earns a refresh token for the fixture users.
func TestMecak8sVMCPFixtureOfflineAccess(t *testing.T) {
	generator := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak-realm-generate.sh")
	realm := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	for _, required := range []string{
		`KEEP_SCOPES='["basic","profile","email","offline_access","mcp:read"]'`,
		`optional-client-scopes/$OFFLINE_SCOPE_ID`,
		`{roles: {realm: [.roles.realm[] | select(.name == "offline_access")]}}`,
		`"realmRoles":["offline_access"]`,
	} {
		if !strings.Contains(generator, required) {
			t.Errorf("realm generator missing offline-access configuration %q", required)
		}
	}
	for _, required := range []string{
		`"name": "offline_access"`,
		`"description": "OpenID Connect built-in scope: offline_access"`,
		`"clientId": "mecatui-kind"`,
		"\"optionalClientScopes\": [\n            \"mcp:read\",\n            \"offline_access\"",
		`"roles": {`,
		`"realmRoles": [`,
	} {
		if !strings.Contains(realm, required) {
			t.Errorf("generated Keycloak realm missing offline-access configuration %q", required)
		}
	}
	for _, user := range []string{"alice", "bob"} {
		userStart := strings.Index(realm, `"username": "`+user+`"`)
		if userStart < 0 {
			t.Errorf("generated Keycloak realm is missing fixture user %s", user)
			continue
		}
		userEnd := strings.Index(realm[userStart:], "\n        }")
		if userEnd < 0 || !strings.Contains(realm[userStart:userStart+userEnd], `"offline_access"`) {
			t.Errorf("generated Keycloak realm does not assign offline_access to %s", user)
		}
	}
	for _, required := range []string{
		"--scopes openid,profile,mcp:read,offline_access",
		"receives a refresh token",
		"refresh the access token",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("vMCP README missing offline-access walkthrough contract %q", required)
		}
	}
}

func TestVMCPFixtureDoesNotCommitCredentialValues(t *testing.T) {
	for _, path := range []string{"deploy/mecak8s-vmcp/Taskfile.yml", "deploy/mecak8s-vmcp/README.md", "deploy/mecak8s-vmcp/versions.yaml", "deploy/mecak8s-vmcp/toolhive-redis.yaml", "deploy/mecak8s-vmcp/vmcp.yaml"} {
		body := readRepoFile(t, path)
		for _, forbidden := range []string{"stringData:", "Authorization: Bearer", "\"access_token\":\"", "\"refresh_token\":\"", "eyJ", "\"secret\":"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains prohibited credential-shaped literal %q", path, forbidden)
			}
		}
	}
}

func TestMecak8sVMCPPOC_Scenario1_NetworkPolicyScope(t *testing.T) {
	kc := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	if strings.Contains(kc, "kind: NetworkPolicy") {
		t.Error("local fixture must not install a partial NetworkPolicy that blocks DNS or Redis")
	}
	for _, required := range []string{"deliberately installs no `NetworkPolicy`", "blocked DNS and Redis", "out of scope"} {
		if !strings.Contains(readme, required) {
			t.Errorf("README missing NetworkPolicy scope statement %q", required)
		}
	}
}

func TestMecak8sVMCPPOC_Scenario2_IdPTLSFixture(t *testing.T) {
	taskfile := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	kc := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak.yaml")
	tls := readRepoFile(t, "deploy/mecak8s-vmcp/fixture-tls.yaml")
	versions := readRepoFile(t, "deploy/mecak8s-vmcp/versions.yaml")

	for _, required := range []string{"cert-manager-install:", "jetstack/charts/cert-manager", "--version=v1.17.2", "--set crds.enabled=true", "certificate-apply:", "wait --for=condition=Ready certificate/keycloak-tls"} {
		if !strings.Contains(taskfile, required) {
			t.Errorf("Taskfile missing cert-manager lifecycle step %q", required)
		}
	}
	if strings.Index(taskfile, "cert-manager-install") >= strings.Index(taskfile, "certificate-apply") ||
		strings.Index(taskfile, "certificate-apply") >= strings.Index(taskfile, "keycloak-apply") {
		t.Error("Taskfile must install cert-manager and issue Keycloak TLS before starting Keycloak")
	}
	for _, required := range []string{"certManager:\n  chartVersion: \"v1.17.2\"", "kind: Issuer", "name: fixture-ca", "kind: Certificate", "name: keycloak-tls", "secretName: keycloak-tls", "--https-port=8443", "tls.crt", "tls.key", "name: https", "targetPort: https"} {
		if !strings.Contains(kc, required) && !strings.Contains(tls, required) && !strings.Contains(versions, required) {
			t.Errorf("Keycloak TLS fixture missing %q", required)
		}
	}
	for _, forbidden := range []string{"kind: Secret", "stringData:", "-----BEGIN"} {
		if strings.Contains(tls, forbidden) {
			t.Errorf("Keycloak TLS fixture commits credential material through %q", forbidden)
		}
	}
}

func TestMecak8sVMCPPOC_Scenario2_SharedIdPIssuer(t *testing.T) {
	kc := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	const issuer = "https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl"
	for _, required := range []string{"--hostname=https://keycloak.mecatl-vmcp.svc.cluster.local:8443", "name: keycloak", "port: 8443", "nodePort: 30843", "127.0.0.1 keycloak.mecatl-vmcp.svc.cluster.local", issuer + "/.well-known/openid-configuration", issuer + "/protocol/openid-connect/certs"} {
		if !strings.Contains(kc, required) && !strings.Contains(readme, required) {
			t.Errorf("shared Keycloak issuer contract missing %q", required)
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
		"verified TLS\nusing the supplied custom fixture CA",
		"plaintext, unauthenticated,\nor an untrusted CA must fail during transport/authentication setup",
		"live client qualification remains confirmation-gated",
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
	kc := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak.yaml")

	for _, required := range []string{
		"enabled: true",
		"issuer: https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl",
		"audience: http://127.0.0.1:18080/mcp",
		"allowPrivateHTTPSIssuer: true",
		"caSecret: fixture-ca",
		"caKey: tls.crt",
	} {
		if !strings.Contains(values, required) {
			t.Errorf("Kind OIDC configuration missing %q", required)
		}
	}
	for _, user := range []string{"\"username\": \"alice\"", "\"username\": \"bob\""} {
		if !strings.Contains(kc, user) {
			t.Errorf("Keycloak fixture missing valid caller %q", user)
		}
	}
	setupEnd := strings.Index(taskfile, "  kind-status:")
	if setupEnd < 0 {
		t.Fatal("Taskfile has no kind-status boundary")
	}
	setup := taskfile[:setupEnd]
	if strings.Index(setup, "task: certificate-apply") >= strings.Index(setup, "task: keycloak-apply") ||
		strings.Index(setup, "task: keycloak-apply") >= strings.Index(setup, "task: chart-apply") {
		t.Error("Kind setup must issue and start HTTPS Keycloak before mecak8s starts OIDC discovery")
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

func TestMecak8sVMCPPOC_Scenario3_SeparateIdPClients(t *testing.T) {
	kc := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	for _, required := range []string{
		"\"clientId\": \"vmcp-browser\"",
		"\"clientId\": \"mecatui-kind\"",
		"\"publicClient\": true",
		"http://127.0.0.1:18080/oauth/callback",
		"http://127.0.0.1:18473/oauth/callback",
	} {
		if !strings.Contains(kc, required) {
			t.Errorf("Keycloak public-client registration missing %q", required)
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
	if strings.Contains(kc, "\"secret\":") {
		t.Error("Keycloak fixture commits a client secret")
	}
}

func TestMecak8sVMCPPOC_Scenario3_NoScopeEnforcement(t *testing.T) {
	values := readRepoFile(t, "deploy/helm/mecak8s/values-kind.yaml") + readRepoFile(t, "deploy/helm/mecak8s/values-kind-vmcp.yaml")
	deployment := readRepoFile(t, "deploy/helm/mecak8s/templates/deployment.yaml")

	if strings.Contains(values, "scope") {
		t.Errorf("mecak8s VMCP fixture must not configure scope authority")
	}
	for _, forbidden := range []string{"--auth-token", "--oidc-insecure-allow-private-issuer"} {
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

func TestMecak8sVMCPPOC_Scenario4_KindLoopbackNodePorts(t *testing.T) {
	taskfile := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	kindConfig := readRepoFile(t, "deploy/mecak8s-vmcp/kind-config.yaml")
	kc := readRepoFile(t, "deploy/mecak8s-vmcp/keycloak.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	for _, required := range []string{
		"--config=deploy/mecak8s-vmcp/kind-config.yaml",
		"containerPort: 30556", "hostPort: 5556", "listenAddress: 127.0.0.1",
		"containerPort: 30081", "hostPort: 18081",
		"type: NodePort", "nodePort: 30843",
		"kind-hosts-add:", "kind-hosts-remove:",
	} {
		if !strings.Contains(taskfile, required) && !strings.Contains(kindConfig, required) && !strings.Contains(kc, required) {
			t.Errorf("Kind local journey missing %q", required)
		}
	}
	if !strings.Contains(readme, "127.0.0.1 keycloak.mecatl-vmcp.svc.cluster.local") ||
		!strings.Contains(readme, "127.0.0.1 mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local") {
		t.Error("README must document the exact temporary host aliases")
	}
	if strings.Contains(kindConfig, "listenAddress: 0.0.0.0") {
		t.Error("Kind port mappings must not bind non-loopback addresses")
	}
}

func TestMecak8sVMCPPOC_Scenario2_IdPCertificateContract(t *testing.T) {
	tls := readRepoFile(t, "deploy/mecak8s-vmcp/fixture-tls.yaml")
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")

	for _, required := range []string{"dnsNames:", "keycloak.mecatl-vmcp.svc.cluster.local", "localhost", "ipAddresses:", "127.0.0.1", "issuerRef:", "name: fixture-ca", "ca:\n    secretName: fixture-ca", "secretName: fixture-ca"} {
		if !strings.Contains(tls, required) {
			t.Errorf("Keycloak certificate contract missing %q", required)
		}
	}
	for _, required := range []string{"fixture-ca", "tls.crt", "without OIDC insecure relaxation"} {
		if !strings.Contains(readme, required) {
			t.Errorf("README missing CA trust guidance %q", required)
		}
	}
}
