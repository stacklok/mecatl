package mecak8s_kind

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const fixtureTask = "Taskfile.yml"

// TestMecak8sKindFixture_Scenario1_ToolHiveFreeSetup pins the standalone
// fixture boundary: its base lifecycle contains only Kind, the local chart,
// and local Redis, not the optional identity or vMCP stacks.
func TestMecak8sKindFixture_Scenario1_ToolHiveFreeSetup(t *testing.T) {
	text := fixtureTaskClosure(t, "kind-setup")
	for _, want := range []string{
		"reset-state:", "cluster-create:", "namespace-apply:", "image-build-load:", "chart-apply:",
		"--values=deploy/helm/mecak8s/values-kind.yaml", "--values=deploy/mecak8s-kind/kind-nodeports.yaml", "statefulset/redis",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Kind base setup missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"cert-manager", "certificate-apply", "keycloak", "toolhive", "yardstick", "vmcp",
		"values-kind-vmcp.yaml", "oci://", "curl ", "api.github.com",
	} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("Kind base setup transitively depends on forbidden %q", forbidden)
		}
	}
}

// TestMecak8sKindFixture_Scenario1_DedicatedKubeconfig pins the fixture's
// explicit context boundary, including loopback-only host access and cleanup.
func TestMecak8sKindFixture_Scenario1_DedicatedKubeconfig(t *testing.T) {
	body, err := os.ReadFile(fixtureTask)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body) + "\n" + fixtureTaskClosure(t, "kind-setup", "kind-status", "kind-keycloak-demo", "kind-destroy")
	for _, want := range []string{
		"KUBECONFIG: deploy/mecak8s-kind/kconfig.yaml", "CONTEXT: kind-mecatl-dev",
		"--kubeconfig={{.KUBECONFIG}}", "--context={{.CONTEXT}}", "--kube-context={{.CONTEXT}}",
		"kind delete cluster --name={{.CLUSTER}}", "rm -rf {{.STATE}}", "rm -f {{.KUBECONFIG}} {{.SETUP_LOCK}}",
		"chmod 0700 {{.STATE}}", "chmod 0600 \"$kubeconfig\"", "mv -f \"$kubeconfig\" {{.KUBECONFIG}}",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dedicated fixture lifecycle missing %q", want)
		}
	}
}

// TestMecak8sKindFixture_Scenario1_DocumentationBoundaries pins that the
// local operator fixture is neither the production chart nor e2e/k8s, and
// makes no production isolation claim.
func TestMecak8sKindFixture_Scenario1_DocumentationBoundaries(t *testing.T) {
	body, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	baseDocs, _, _ := strings.Cut(text, "\n## Optional Keycloak login journey")
	for _, want := range []string{
		"operator-run", "deploy/helm/mecak8s/", "e2e/k8s/", "no general NetworkPolicy",
		"127.0.0.1", "NodePort", "extraPortMappings",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("fixture documentation missing boundary %q", want)
		}
	}
	for _, forbidden := range []string{"production network isolation", "ToolHive", "vMCP"} {
		if strings.Contains(baseDocs, forbidden) {
			t.Fatalf("ToolHive-free fixture documentation contains %q", forbidden)
		}
	}
}

// TestMecak8sKindFixture_Scenario2_MockDefault pins the cost-free fixture
// default: without an operator credential setup selects the canned provider and
// never contacts a provider.
func TestMecak8sKindFixture_Scenario2_MockDefault(t *testing.T) {
	text := fixtureTaskClosure(t, "kind-setup")
	for _, want := range []string{
		`if [ -n "${OPENROUTER_API_KEY:-}" ]; then`,
		"--values=deploy/helm/mecak8s/values-kind.yaml",
		"kind-provider-real.yaml",
		"kind-provider-mock.yaml",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Kind setup missing provider-mode control %q", want)
		}
	}
	for _, forbidden := range []string{"curl ", "openrouter.ai", "api.openai.com", "provider smoke"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("mock-default setup performs provider work %q", forbidden)
		}
	}

	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required to render the mock fixture")
	}
	cmd := exec.Command("helm", "template", "kind", ".", "-f", "values-kind.yaml", "-f", "../../mecak8s-kind/kind-provider-mock.yaml")
	cmd.Dir = "../helm/mecak8s"
	rendered, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render mock fixture: %v\n%s", err, rendered)
	}
	if !strings.Contains(string(rendered), "- --mock") {
		t.Fatal("mock fixture render omits the canned mock provider")
	}
	for _, forbidden := range []string{"OPENROUTER_API_KEY", "mecak8s-openrouter"} {
		if strings.Contains(string(rendered), forbidden) {
			t.Fatalf("mock fixture render retains provider Secret projection %q", forbidden)
		}
	}
}

// TestMecak8sKindFixture_Scenario2_KeycloakRetainsProviderOverlay pins that
// Keycloak's Helm layer cannot reset a real-provider setup to the mock overlay.
func TestMecak8sKindFixture_Scenario2_KeycloakRetainsProviderOverlay(t *testing.T) {
	text := fixtureTaskClosure(t, "chart-keycloak-apply")
	for _, want := range []string{
		`if [ -n "${OPENROUTER_API_KEY:-}" ]; then`,
		"provider_values=deploy/mecak8s-kind/kind-provider-real.yaml",
		"provider_values=deploy/mecak8s-kind/kind-provider-mock.yaml",
		"--values=deploy/helm/mecak8s/values-kind-keycloak.yaml",
		`--values="$provider_values"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Keycloak chart layer missing provider overlay control %q", want)
		}
	}
}

// TestInvariant_credential_not_process_argument pins that the fixture's
// operator credential crosses only kubectl's standard input as a file payload.
func TestInvariant_credential_not_process_argument(t *testing.T) {
	body, err := os.ReadFile(fixtureTask)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		`printf '%s' "$OPENROUTER_API_KEY" | kubectl`,
		"--from-file=OPENROUTER_API_KEY=/dev/stdin",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("credential handoff missing %q", want)
		}
	}
	for _, forbidden := range []string{"--from-literal=OPENROUTER_API_KEY", "echo $OPENROUTER_API_KEY", "echo \"$OPENROUTER_API_KEY\""} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("credential is exposed by fixture command %q", forbidden)
		}
	}
}

// TestMecak8sKindFixture_Scenario2_ResetToMock pins that a later mock setup
// deletes the fixture-owned provider Secret before applying the mock overlay.
func TestMecak8sKindFixture_Scenario2_ResetToMock(t *testing.T) {
	body, err := os.ReadFile(fixtureTask)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"PROVIDER_SECRET: mecak8s-openrouter",
		"delete secret {{.PROVIDER_SECRET}} --namespace={{.NAMESPACE}} --ignore-not-found",
		"provider_values=deploy/mecak8s-kind/kind-provider-mock.yaml",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("mock reset missing %q", want)
		}
	}
	mockValues, err := os.ReadFile("kind-provider-mock.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mockValues), "OPENROUTER_API_KEY") {
		t.Fatal("mock overlay retains an OpenRouter credential projection")
	}
}

// TestMecak8sKindFixture_Scenario2_LiveSmokeIsExplicit pins that billing is an
// operator decision, documented outside setup and default tests.
func TestMecak8sKindFixture_Scenario2_LiveSmokeIsExplicit(t *testing.T) {
	body, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"OPENROUTER_API_KEY", "billable", "A real-provider smoke call", "operator action"} {
		if !strings.Contains(text, want) {
			t.Fatalf("fixture instructions missing live-provider boundary %q", want)
		}
	}
	if strings.Contains(fixtureTaskClosure(t, "kind-setup"), "live-smoke") {
		t.Fatal("setup must not invoke the live-provider smoke action")
	}
}

func TestMecak8sKindFixture_LearningDriverManifest(t *testing.T) {
	body, err := os.ReadFile("learning-driver.yaml")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(body), "\n---\n")
	if len(parts) != 2 {
		t.Fatalf("learning driver manifest has %d documents, want Service and Deployment", len(parts))
	}
	var service, deployment map[string]any
	if err = yaml.Unmarshal([]byte(parts[0]), &service); err != nil {
		t.Fatalf("decode learning driver Service: %v", err)
	}
	if err = yaml.Unmarshal([]byte(parts[1]), &deployment); err != nil {
		t.Fatalf("decode learning driver Deployment: %v", err)
	}
	if service["kind"] != "Service" || deployment["kind"] != "Deployment" {
		t.Fatalf("manifest kinds = %v, %v; want Service, Deployment", service["kind"], deployment["kind"])
	}
	text := string(body)
	for _, want := range []string{
		"type: ClusterIP", "replicas: 1", "type: Recreate", "automountServiceAccountToken: false",
		"runAsNonRoot: true", "runAsUser: 65532", "allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true", `capabilities: {drop: ["ALL"]}`, "seccompProfile: {type: RuntimeDefault}",
		"emptyDir: {}", "secretName: learning-driver-tls", "--data-dir=/data", "--tls-cert=", "--tls-key=",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("learning driver manifest missing %q", want)
		}
	}
	for _, forbidden := range []string{"kind: StatefulSet", "replicas: 2", "type: LoadBalancer", "type: NodePort"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("learning driver fixture contains unsupported topology %q", forbidden)
		}
	}
}

var fixtureTaskBlockRe = regexp.MustCompile(`(?m)^  ([a-zA-Z][a-zA-Z0-9_-]*):\s*$`)

func fixtureTaskClosure(t *testing.T, roots ...string) string {
	t.Helper()
	body, err := os.ReadFile(fixtureTask)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	tasksIdx := strings.Index(text, "\ntasks:\n")
	if tasksIdx < 0 {
		t.Fatal("Taskfile has no tasks section")
	}
	taskBody := text[tasksIdx:]
	headers := fixtureTaskBlockRe.FindAllStringSubmatchIndex(taskBody, -1)
	blocks := make(map[string]string, len(headers))
	for i, header := range headers {
		end := len(taskBody)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		blocks[taskBody[header[2]:header[3]]] = taskBody[header[0]:end]
	}

	refs := regexp.MustCompile(`task:\s*([a-zA-Z][a-zA-Z0-9_-]*)`)
	visited := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		block, ok := blocks[name]
		if !ok {
			t.Fatalf("Taskfile references unknown task %q", name)
		}
		visited[name] = true
		for _, match := range refs.FindAllStringSubmatch(block, -1) {
			visit(match[1])
		}
	}
	for _, root := range roots {
		visit(root)
	}

	var out strings.Builder
	for _, root := range roots {
		out.WriteString(blocks[root])
	}
	for name, block := range blocks {
		if visited[name] && !slices.Contains(roots, name) {
			out.WriteString(block)
		}
	}
	return out.String()
}

func TestMecak8sKindFixture_Scenario3_LoopbackReachability(t *testing.T) {
	task := fixtureTaskClosure(t, "kind-keycloak-demo")
	for _, want := range []string{"nc -z 127.0.0.1", "wait_port Keycloak 8443", "wait_port mecak8s-gRPC 18080", "wait_port mecak8s-HTTPS 18081"} {
		if !strings.Contains(task, want) {
			t.Fatalf("direct loopback readiness missing %q", want)
		}
	}
	for _, forbidden := range []string{"port-forward", "kill ", "kind-port-forward", "kind-keycloak-port-forward"} {
		if strings.Contains(task, forbidden) {
			t.Fatalf("direct mapping task retains forwarding lifecycle %q", forbidden)
		}
	}
	taskfile, err := os.ReadFile(fixtureTask)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"kind-port-forward:", "kind-keycloak-port-forward:"} {
		if strings.Contains(string(taskfile), forbidden) {
			t.Fatalf("Taskfile retains obsolete task %q", forbidden)
		}
	}
	if regexp.MustCompile(`kubectl[^\n]*port-forward`).Match(taskfile) {
		t.Fatal("Taskfile retains a kubectl port-forward command")
	}

	config, err := os.ReadFile("kind-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var kindConfig struct {
		Nodes []struct {
			Role              string `yaml:"role"`
			ExtraPortMappings []struct {
				ContainerPort int    `yaml:"containerPort"`
				HostPort      int    `yaml:"hostPort"`
				ListenAddress string `yaml:"listenAddress"`
				Protocol      string `yaml:"protocol"`
			} `yaml:"extraPortMappings"`
		} `yaml:"nodes"`
	}
	if err := yaml.Unmarshal(config, &kindConfig); err != nil {
		t.Fatalf("parse Kind config: %v", err)
	}
	var mappings []struct {
		ContainerPort int    `yaml:"containerPort"`
		HostPort      int    `yaml:"hostPort"`
		ListenAddress string `yaml:"listenAddress"`
		Protocol      string `yaml:"protocol"`
	}
	for _, node := range kindConfig.Nodes {
		if node.Role == "control-plane" {
			mappings = append(mappings, node.ExtraPortMappings...)
		}
	}
	wantMappings := map[int]int{30080: 18080, 30081: 18081, 30443: 8443}
	if len(mappings) != len(wantMappings) {
		t.Fatalf("control-plane extraPortMappings = %#v, want exactly %#v", mappings, wantMappings)
	}
	for _, mapping := range mappings {
		if wantHostPort, ok := wantMappings[mapping.ContainerPort]; !ok || mapping.HostPort != wantHostPort || mapping.Protocol != "TCP" || mapping.ListenAddress != "127.0.0.1" {
			t.Fatalf("unexpected control-plane extraPortMapping: %#v", mapping)
		}
		delete(wantMappings, mapping.ContainerPort)
	}
	if len(wantMappings) != 0 {
		t.Fatalf("missing control-plane extraPortMappings: %#v", wantMappings)
	}
	keycloak, err := os.ReadFile("keycloak.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type: NodePort", "port: 8443", "targetPort: https", "nodePort: 30443"} {
		if !strings.Contains(string(keycloak), want) {
			t.Fatalf("Keycloak Service missing %q", want)
		}
	}
	// The fixture's only host path is the loopback-bound Kind mapping in
	// front of that NodePort; nothing here may reach further.
	for _, forbidden := range []string{"LoadBalancer", "Ingress", "0.0.0.0", "*"} {
		if strings.Contains(string(keycloak), forbidden) {
			t.Fatalf("Keycloak Service exposes the fixture externally with %q", forbidden)
		}
	}
	overlay, err := os.ReadFile("kind-nodeports.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type: NodePort", "grpc: 30080", "http: 30081"} {
		if !strings.Contains(string(overlay), want) {
			t.Fatalf("NodePort overlay missing %q", want)
		}
	}
	for _, forbidden := range []string{"LoadBalancer", "Ingress", "0.0.0.0", "*"} {
		if strings.Contains(string(overlay), forbidden) {
			t.Fatalf("NodePort overlay exposes the fixture externally with %q", forbidden)
		}
	}
	// NodePort plumbing lives in kind-nodeports.yaml alone: the Keycloak
	// overlay stays a pure OIDC/TLS layer with no Service exposure of its own.
	keycloakOverlay, err := os.ReadFile("../helm/mecak8s/values-kind-keycloak.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"NodePort", "nodePort", "LoadBalancer", "Ingress", "0.0.0.0", "*"} {
		if strings.Contains(string(keycloakOverlay), forbidden) {
			t.Fatalf("Keycloak overlay exposes mecak8s externally with %q", forbidden)
		}
	}
	base, err := os.ReadFile("../helm/mecak8s/values-kind.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(base), "endpoint: redis:6379") || strings.Contains(string(base), "NodePort") {
		t.Fatal("shared Kind values must remain bare ClusterIP profile")
	}
	chartDefaults, err := os.ReadFile("../helm/mecak8s/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chartDefaults), "type: ClusterIP") {
		t.Fatal("chart default Service must be ClusterIP")
	}
	certs, err := os.ReadFile("fixture-tls.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- localhost", "- 127.0.0.1"} {
		if !strings.Contains(string(certs), want) {
			t.Fatalf("fixture certificate misses loopback client identity %q", want)
		}
	}
	hostsTasks := fixtureTaskClosure(t, "kind-hosts-show", "kind-hosts-add", "kind-hosts-remove")
	for _, want := range []string{"127.0.0.1 keycloak.mecatl.svc.cluster.local", "grep -Fqx", "sudo sh -c", "sudo sed -i.bak"} {
		if !strings.Contains(hostsTasks, want) {
			t.Fatalf("Keycloak hosts lifecycle missing %q", want)
		}
	}
}

func TestMecak8sKindFixture_Scenario3_LoginDocumentation(t *testing.T) {
	for _, path := range []string{
		"README.md", "../mecak8s-vmcp/README.md", "../README.md",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(body)
		for _, want := range []string{"Authorization Code + PKCE", "password grant", "test helper"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s does not document Keycloak login boundary %q", path, want)
			}
		}
		if path == "README.md" && !strings.Contains(text, "offline_access") {
			t.Fatalf("%s does not document the optional offline_access scope", path)
		}
		if strings.Index(text, "Authorization Code + PKCE") > strings.Index(text, "password grant") {
			t.Fatalf("%s presents password grant before the normal PKCE journey", path)
		}
	}
	deploymentGuide, err := os.ReadFile("../../user-docs/building/deployment/mecak8s.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/building/getting-started/kubernetes.md",
		"deploy/mecak8s-kind/README.md",
	} {
		if !strings.Contains(string(deploymentGuide), want) {
			t.Fatalf("production deployment guide does not route local fixture readers to %q", want)
		}
	}
}

// TestMecak8sKindFixture_Scenario3_KeycloakIsOptIn pins the identity layer's
// independent lifecycle: the base cannot transitively install identity assets,
// while the opt-in setup applies them only after the base is ready.
func TestMecak8sKindFixture_Scenario3_KeycloakIsOptIn(t *testing.T) {
	base := fixtureTaskClosure(t, "kind-setup")
	for _, forbidden := range []string{"cert-manager", "certificate-apply", "keycloak", "oidc", "tls", "values-kind-keycloak.yaml"} {
		if strings.Contains(strings.ToLower(base), forbidden) {
			t.Fatalf("base setup transitively depends on optional identity asset %q", forbidden)
		}
	}

	identity := fixtureTaskClosure(t, "kind-keycloak-apply", "kind-keycloak-setup")
	for _, want := range []string{
		"kind-keycloak-apply:", "kind-keycloak-setup:", "cert-manager-install:",
		"certificate-apply:", "keycloak-apply:", "chart-keycloak-apply:",
		"deploy/mecak8s-kind/fixture-tls.yaml", "deploy/mecak8s-kind/keycloak.yaml",
		"values-kind-keycloak.yaml", "helm upgrade --install cert-manager", "apply -f",
		"rollout status deployment/keycloak",
		"rollout status deployment/{{.RELEASE}}-mecak8s",
	} {
		if !strings.Contains(identity, want) {
			t.Fatalf("optional Keycloak lifecycle missing %q", want)
		}
	}
	if strings.Index(identity, "task: kind-setup") > strings.Index(identity, "task: kind-keycloak-apply") {
		t.Fatal("kind-keycloak-setup must establish the base before applying identity")
	}
}

func TestMecak8sKindFixture_Scenario3_KeycloakDemoQuickstart(t *testing.T) {
	text := fixtureTaskClosure(t, "kind-keycloak-demo")
	for _, want := range []string{
		"kind-keycloak-demo:", "task: cluster-ready", "nc -z 127.0.0.1", "trap 'rm -f",
		"get secret fixture-ca", "fixture-ca.crt", "base64 -D <",
		"wait_port Keycloak 8443", "wait_port mecak8s-gRPC 18080", "wait_port mecak8s-HTTPS 18081",
		"mecatui login mecak8s-mecak8s.mecatl.svc.cluster.local:18080", "--client-id mecatui-kind", "--audience mecak8s",
		"--scopes openid,profile,mecak8s:access,offline_access", "--private-issuer", "mecatui connect mecak8s-mecak8s.mecatl.svc.cluster.local:18080 --tls",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Keycloak demo quickstart missing %q", want)
		}
	}
	for _, forbidden := range []string{"task: kind-keycloak-setup", "task: kind-hosts-add", "sudo", "port-forward", "--address=0.0.0.0"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Keycloak demo quickstart violates fixture boundary with %q", forbidden)
		}
	}
}

// TestMecak8sKindFixture_Scenario3_KeycloakOIDCOverlay pins the disposable
// private-HTTPS OIDC shape. The CA is narrowly mounted for the validator and
// no process-wide or deprecated insecure escape hatch is admitted.
func TestMecak8sKindFixture_Scenario3_KeycloakOIDCOverlay(t *testing.T) {
	overlay, err := os.ReadFile("../helm/mecak8s/values-kind-keycloak.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"issuer: https://keycloak.mecatl.svc.cluster.local:8443/realms/mecatl",
		"audience: mecak8s", "allowPrivateHTTPSIssuer: true", "caSecret: fixture-ca",
		"caKey: tls.crt", "secretName: mecak8s-tls", "certKey: tls.crt", "keyKey: tls.key",
	} {
		if !strings.Contains(string(overlay), want) {
			t.Fatalf("Keycloak overlay missing %q", want)
		}
	}

	task := fixtureTaskClosure(t, "kind-keycloak-apply")
	for _, want := range []string{"--values=deploy/helm/mecak8s/values-kind.yaml", "deploy/mecak8s-kind/kind-nodeports.yaml", "--values=deploy/helm/mecak8s/values-kind-keycloak.yaml"} {
		if !strings.Contains(task, want) {
			t.Fatalf("Keycloak chart apply missing %q", want)
		}
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required to render the Keycloak overlay")
	}
	cmd := exec.Command("helm", "template", "kind", ".", "-f", "values-kind.yaml", "-f", "../../mecak8s-kind/kind-nodeports.yaml", "-f", "values-kind-keycloak.yaml")
	cmd.Dir = "../helm/mecak8s"
	rendered, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render Keycloak OIDC overlay: %v\n%s", err, rendered)
	}
	for _, want := range []string{
		"type: NodePort", "name: grpc", "nodePort: 30080", "name: http", "nodePort: 30081",
		"--oidc-issuer=https://keycloak.mecatl.svc.cluster.local:8443/realms/mecatl",
		"--oidc-audience=mecak8s", "--oidc-ca-cert-file=/var/run/secrets/oidc-ca/tls.crt",
		"--oidc-allow-private-https-issuer", "secretName: fixture-ca",
		`- {key: "tls.crt", path: "tls.crt"}`, "--tls-cert=/var/run/secrets/tls/tls.crt",
		"--tls-key=/var/run/secrets/tls/tls.key", "secretName: mecak8s-tls",
	} {
		if !strings.Contains(string(rendered), want) {
			t.Fatalf("rendered Keycloak OIDC overlay missing %q", want)
		}
	}
	for _, forbidden := range []string{"SSL_CERT_FILE", "--oidc-insecure-allow-private-issuer"} {
		if strings.Contains(string(rendered), forbidden) {
			t.Fatalf("rendered Keycloak OIDC overlay contains forbidden transport setting %q", forbidden)
		}
	}
}

// TestMecak8sKindFixture_Scenario3_OptionalClientScopes pins the public desktop
// client's deliberately requested resource audience and offline-access scopes.
func TestMecak8sKindFixture_Scenario3_OptionalClientScopes(t *testing.T) {
	manifest, err := os.ReadFile("keycloak.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	_, realmJSON, found := strings.Cut(text, "realm.json: |\n")
	if !found {
		t.Fatal("Keycloak manifest does not contain realm.json")
	}
	realmJSON, _, found = strings.Cut(realmJSON, "\n---\n")
	if !found || !json.Valid([]byte(realmJSON)) {
		t.Fatal("Keycloak realm.json is not valid JSON")
	}
	for _, want := range []string{
		`"name": "mecak8s:access"`, `"included.custom.audience": "mecak8s"`,
		`"name": "offline_access"`, `"description": "OpenID Connect built-in scope: offline_access"`,
		`"access.token.claim": "true"`, `"id.token.claim": "false"`,
		`"clientId": "mecatui-kind"`, `"pkce.code.challenge.method": "S256"`,
		`"publicClient": true`, `"standardFlowEnabled": true`,
		"\"optionalClientScopes\": [\n            \"mecak8s:access\",\n            \"offline_access\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Keycloak optional client-scope configuration missing %q", want)
		}
	}
	client := text[strings.Index(text, `"clientId": "mecatui-kind"`):]
	for _, forbidden := range []string{
		`"clientAuthenticatorType": "client-secret"`, `"directAccessGrantsEnabled": true`,
		`"implicitFlowEnabled": true`, `"serviceAccountsEnabled": true`,
		"\"defaultClientScopes\": [\n            \"mecak8s:access\"",
		"\"defaultClientScopes\": [\n            \"offline_access\"",
	} {
		if strings.Contains(client, forbidden) {
			t.Fatalf("public mecatui client contains forbidden configuration %q", forbidden)
		}
	}
}
