package mecak8s_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"sigs.k8s.io/yaml"

	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/cliconfig"
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

func serviceFromRender(t *testing.T, rendered string) *corev1.Service {
	t.Helper()
	for _, document := range strings.Split(rendered, "\n---") {
		var meta struct {
			Kind string `yaml:"kind"`
		}
		if err := yaml.Unmarshal([]byte(document), &meta); err != nil || meta.Kind != "Service" {
			continue
		}
		var service corev1.Service
		if err := yaml.Unmarshal([]byte(document), &service); err != nil {
			t.Fatal(err)
		}
		// The Keycloak fixture renders a Redis Service too; select the
		// chart's own Service by name rather than document order.
		if !strings.HasSuffix(service.Name, "-mecak8s") {
			continue
		}
		return &service
	}
	t.Fatal("rendered chart has no mecak8s Service")
	return nil
}

func assertFixtureServiceNodePorts(t *testing.T, rendered string) {
	t.Helper()
	service := serviceFromRender(t, rendered)
	if service.Spec.Type != corev1.ServiceTypeNodePort {
		t.Fatalf("Service type = %q, want NodePort", service.Spec.Type)
	}
	got := map[string]int32{}
	for _, port := range service.Spec.Ports {
		got[port.Name] = port.NodePort
	}
	want := map[string]int32{"grpc": 30080, "http": 30081}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Service NodePorts = %#v, want %#v", got, want)
	}
}

func deploymentFromRender(t *testing.T, rendered string) *appsv1.Deployment {
	t.Helper()
	for _, document := range strings.Split(rendered, "\n---") {
		var meta struct {
			Kind string `yaml:"kind"`
		}
		if err := yaml.Unmarshal([]byte(document), &meta); err != nil || meta.Kind != "Deployment" {
			continue
		}
		var deployment appsv1.Deployment
		if err := yaml.Unmarshal([]byte(document), &deployment); err != nil {
			t.Fatal(err)
		}
		return &deployment
	}
	t.Fatal("rendered chart has no Deployment")
	return nil
}

func pdbFromRender(t *testing.T, rendered string) *policyv1.PodDisruptionBudget {
	t.Helper()
	for _, document := range strings.Split(rendered, "\n---") {
		var meta struct {
			Kind string `yaml:"kind"`
		}
		if err := yaml.Unmarshal([]byte(document), &meta); err != nil || meta.Kind != "PodDisruptionBudget" {
			continue
		}
		var pdb policyv1.PodDisruptionBudget
		if err := yaml.Unmarshal([]byte(document), &pdb); err != nil {
			t.Fatal(err)
		}
		return &pdb
	}
	return nil
}

func TestADR_0294_TerminationGracePeriodIsConfigurableAndFitsDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int64
	}{
		{name: "default", args: productionArgs(), want: 60},
		{name: "override", args: append(productionArgs(), "--set", "terminationGracePeriodSeconds=75"), want: 75},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := helm(t, tc.args...)
			if err != nil {
				t.Fatal(err, rendered)
			}
			got := deploymentFromRender(t, rendered).Spec.Template.Spec.TerminationGracePeriodSeconds
			if got == nil || *got != tc.want {
				t.Fatalf("terminationGracePeriodSeconds = %v, want %d", got, tc.want)
			}
		})
	}
	// preStop 3s + drain 15s + gRPC 10s + HTTP 5s + close 5s + telemetry 5s.
	if budget := int64(3 + 15 + 10 + 5 + 5 + 5); budget >= 60 {
		t.Fatalf("documented default shutdown budget = %ds, want < 60s", budget)
	}
}

func productionArgs() []string {
	return []string{"template", "production", ".", "--set", "image.tag=v0.0.0", "--set", "redis.endpoint=redis.example.internal:6380", "--set", "redis.credentialsSecret=redis-credentials", "--set", "security.allowUnsafeRealProvider=true"}
}

func secureProductionArgs() []string {
	return []string{"template", "production", ".", "--set", "image.tag=v0.0.0", "--set", "redis.endpoint=redis.example.internal:6380", "--set", "redis.credentialsSecret=redis-credentials", "--set", "tls.enabled=true,tls.secretName=mecak8s-tls", "--set", "oidc.enabled=true,oidc.issuer=https://idp.example.com,oidc.audience=mecatl"}
}

func TestMecak8sHelmChart_RedisFilesystemFlagsAndWorkspaceExclusion(t *testing.T) {
	args := append(productionArgs(), "--set", "redis.filesystem.enabled=true,redis.readLedger.enabled=true")
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatal(err, rendered)
	}
	for _, want := range []string{"--redis-filesystem", "--redis-read-ledger"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Redis filesystem render missing %q", want)
		}
	}
	if strings.Contains(rendered, "--workspace=") {
		t.Fatal("Redis filesystem render unexpectedly contains --workspace")
	}

	args = append(args, "--set", "workspace=/workspace")
	if rendered, err = helm(t, args...); err == nil {
		t.Fatalf("Redis filesystem with mounted workspace rendered successfully:\n%s", rendered)
	}
}

// kindVMCPArgs renders the Kind profile with the mecak8s-vmcp fixture's own
// OIDC/TLS overlay layered on top — the shape deploy/mecak8s-vmcp/Taskfile.yml
// actually installs. Never pass values-kind-vmcp.yaml alone or without
// values-kind.yaml first: e2e/k8s's suite installs values-kind.yaml ALONE and
// must stay free of secrets that overlay assumes exist (fixture-ca,
// mecak8s-tls) — see values-kind.yaml's own comment.
func kindVMCPArgs() []string {
	return []string{"template", "kind", ".", "-f", "values-kind.yaml", "-f", "values-kind-vmcp.yaml"}
}

func kindFixtureArgs() []string {
	return []string{"template", "kind", ".", "-f", "values-kind.yaml", "-f", "../../mecak8s-kind/kind-nodeports.yaml"}
}

func kindKeycloakFixtureArgs() []string {
	return append(kindFixtureArgs(), "-f", "values-kind-keycloak.yaml")
}

// TestMecak8sHelmChart_KindProfileAloneHasNoSecretDependency pins the exact
// shape e2e/k8s's Ginkgo suite installs: `helm ... --values values-kind.yaml
// --wait`, with no other overrides and no Secrets/ConfigMaps created beyond
// the namespace. values-kind.yaml alone must render with OIDC/TLS off and no
// NodePort — any of those pull in a Secret (mecak8s-tls, fixture-ca) that
// only the mecak8s-vmcp fixture's own setup creates, and the e2e pod would
// hang mounting a missing volume until the install times out (the regression
// this test exists to catch).

func TestMecak8sHelmChart_KindProfileAloneHasNoSecretDependency(t *testing.T) {
	values, err := os.ReadFile("values-kind.yaml")
	if err != nil {
		t.Fatalf("read Kind values: %v", err)
	}
	for _, want := range []string{
		"mockProvider: true", "endpoint: redis:6379", "credentialsSecret: \"\"",
		"enabled: true", "workspace: /tmp",
	} {
		if !strings.Contains(string(values), want) {
			t.Fatalf("Kind values missing %q", want)
		}
	}

	e2eFiles, err := filepath.Glob(filepath.Join("..", "..", "..", "e2e", "k8s", "*.go"))
	if err != nil {
		t.Fatalf("list e2e files: %v", err)
	}
	e2eText := ""
	for _, path := range e2eFiles {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read e2e file %s: %v", path, readErr)
		}
		e2eText += string(body)
	}
	if !strings.Contains(e2eText, "values-kind.yaml") || strings.Contains(e2eText, "values-kind-vmcp.yaml") {
		t.Fatal("e2e/k8s must install values-kind.yaml directly, without an operator-fixture overlay")
	}

	rendered, err := helm(t, "template", "kind", ".", "-f", "values-kind.yaml")
	if err != nil {
		t.Fatalf("render Kind profile: %v", err)
	}
	for _, forbidden := range []string{"--oidc-issuer", "--tls-cert", "--tls-key", "mecak8s-tls", "fixture-ca", "secretName:", "type: NodePort", "nodePort:"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("bare Kind render (no fixture overlay) unexpectedly contains %q — e2e/k8s's suite creates no matching Secret and would hang", forbidden)
		}
	}
	for _, want := range []string{"replicas: 2", "- --mock", "--redis-url=redis:6379", "--workspace=/tmp", "type: ClusterIP"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("bare Kind render missing %q", want)
		}
	}
}

func TestADR_0290_HelmProtectedResourceProfile(t *testing.T) {
	rendered, err := helm(t, "template", "profile", ".", "--set", "mockProvider=true", "--set", "redis.local.enabled=true", "--set", "oidc.enabled=true", "--set", "oidc.issuer=https://idp.example.com", "--set", "oidc.audience=mecatl", "--set", "oidc.resource=https://api.example.com/mcp", "--set", "oidc.clientID=mecatui", "--set", "oidc.scopes[0]=openid", "--set", "oidc.scopes[1]=profile")
	if err != nil {
		t.Fatalf("render protected-resource profile: %v\n%s", err, rendered)
	}
	deployment := deploymentFromRender(t, rendered)
	args := deployment.Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{"--oidc-resource=https://api.example.com/mcp", "--oidc-client-id=mecatui", "--oidc-scopes=openid,profile"} {
		if !slices.Contains(args, want) {
			t.Fatalf("rendered args missing %q: %v", want, args)
		}
	}
	for name, values := range map[string]string{
		"partial":  "oidc.enabled=true,oidc.issuer=https://idp.example.com,oidc.audience=mecatl,oidc.resource=https://api.example.com/mcp",
		"disabled": "oidc.enabled=false,oidc.issuer=https://idp.example.com,oidc.audience=mecatl,oidc.resource=https://api.example.com/mcp,oidc.clientID=mecatui",
		"scope":    "oidc.enabled=true,oidc.issuer=https://idp.example.com,oidc.audience=mecatl,oidc.resource=https://api.example.com/mcp,oidc.clientID=mecatui,oidc.scopes={open%20id}",
	} {
		t.Run(name, func(t *testing.T) {
			if output, err := helm(t, "template", name, ".", "--set", "mockProvider=true", "--set", values); err == nil {
				t.Fatalf("malformed profile rendered successfully:\n%s", output)
			}
		})
	}
	for name, values := range map[string][]string{
		"userinfo":           {"oidc.resource=https://user@api.example.com/mcp"},
		"malformed-url":      {"oidc.resource=https://api.example.com/%zz"},
		"comma-resource":     {"oidc.resource=https://api.example.com/mcp,other"},
		"quoted-resource":    {`oidc.resource=https://api.example.com/mcp"`},
		"backslash-resource": {`oidc.resource=https://api.example.com/mcp\\\\`},
		"comma-scope":        {"oidc.scopes[0]=openid,profile"},
		"quoted-scope":       {`oidc.scopes[0]=openid"`},
		"backslash-scope":    {`oidc.scopes[0]=openid\\\\`},
	} {
		t.Run(name, func(t *testing.T) {
			args := []string{"template", name, ".", "--set", "mockProvider=true", "--set", "redis.local.enabled=true", "--set", "oidc.enabled=true", "--set", "oidc.issuer=https://idp.example.com", "--set", "oidc.audience=mecatl", "--set", "oidc.clientID=mecatui"}
			for _, value := range values {
				args = append(args, "--set-string", value)
			}
			if output, err := helm(t, args...); err == nil {
				t.Fatalf("unsafe profile rendered successfully:\n%s", output)
			}
		})
	}
}

func TestMecak8sHelmChart_EdgeTerminatedTLS(t *testing.T) {
	rendered, err := helm(t, "template", "production", ".", "-f", "ci/production-edge-tls-values.yaml")
	if err != nil {
		t.Fatalf("render edge TLS fixture: %v", err)
	}
	deployment := deploymentFromRender(t, rendered)
	container := deployment.Spec.Template.Spec.Containers[0]
	for _, want := range []string{"--oidc-issuer=https://idp.example.com", "--oidc-audience=mecatl", "--oidc-max-jwks-staleness=1h"} {
		if !slices.Contains(container.Args, want) {
			t.Fatalf("edge TLS args missing %q: %q", want, container.Args)
		}
	}
	for _, absent := range []string{"--tls-cert=", "--tls-key="} {
		if slices.ContainsFunc(container.Args, func(arg string) bool { return strings.HasPrefix(arg, absent) }) {
			t.Fatalf("edge TLS args unexpectedly contain %q: %q", absent, container.Args)
		}
	}
	if deployment.Spec.Template.Annotations["mecatl.stacklok.com/tls-terminated-upstream"] != "true" || len(deployment.Spec.Template.Annotations) != 1 {
		t.Fatalf("edge TLS annotations = %#v", deployment.Spec.Template.Annotations)
	}
	if _, ok := deployment.Spec.Template.Annotations["mecatl.stacklok.com/unsafe-real-provider"]; ok {
		t.Fatal("edge TLS render carries unsafe annotation")
	}
	if slices.ContainsFunc(deployment.Spec.Template.Spec.Volumes, func(volume corev1.Volume) bool { return volume.Name == "tls" }) {
		t.Fatal("edge TLS render includes a pod TLS Secret volume")
	}
	if slices.ContainsFunc(container.VolumeMounts, func(mount corev1.VolumeMount) bool { return mount.Name == "tls" }) {
		t.Fatal("edge TLS render includes a pod TLS Secret volume mount")
	}
	for _, probe := range []*corev1.Probe{container.StartupProbe, container.ReadinessProbe, container.LivenessProbe} {
		if probe == nil || probe.HTTPGet == nil || probe.HTTPGet.Scheme != corev1.URISchemeHTTP {
			t.Fatalf("edge TLS probe = %#v, want HTTP", probe)
		}
	}
	if container.Lifecycle == nil || container.Lifecycle.PreStop == nil || container.Lifecycle.PreStop.HTTPGet == nil || container.Lifecycle.PreStop.HTTPGet.Scheme != corev1.URISchemeHTTP {
		t.Fatalf("edge TLS preStop = %#v, want HTTP", container.Lifecycle)
	}
	service := serviceFromRender(t, rendered)
	if service.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatalf("edge TLS Service type = %q, want ClusterIP", service.Spec.Type)
	}
	for _, port := range service.Spec.Ports {
		if port.Name == "grpc" {
			if port.AppProtocol == nil || *port.AppProtocol != "kubernetes.io/h2c" {
				t.Fatalf("edge TLS gRPC Service port = %#v, want kubernetes.io/h2c", port)
			}
			continue
		}
		if port.AppProtocol != nil {
			t.Fatalf("edge TLS non-gRPC Service port %q has appProtocol %q", port.Name, *port.AppProtocol)
		}
	}
}

func TestMecak8sHelmChart_EdgeFixtureRendersNoExternalBoundaryResources(t *testing.T) {
	fixtures := []struct {
		name string
		args []string
	}{
		{"production edge TLS", []string{"template", "production", ".", "-f", "ci/production-edge-tls-values.yaml"}},
		{"production", []string{"template", "production", ".", "-f", "ci/production-values.yaml"}},
		{"production OIDC", []string{"template", "production", ".", "-f", "ci/production-oidc-values.yaml"}},
		{"production TLS", []string{"template", "production", ".", "-f", "ci/production-tls-values.yaml"}},
		{"config mount", []string{"template", "config-mount", ".", "-f", "ci/config-mount-values.yaml"}},
		{"Kind", []string{"template", "kind", ".", "-f", "values-kind.yaml"}},
		{"Kind NodePort", kindFixtureArgs()},
		{"Kind Keycloak", kindKeycloakFixtureArgs()},
		{"Kind vMCP", kindVMCPArgs()},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			rendered, err := helm(t, fixture.args...)
			if err != nil {
				t.Fatalf("render fixture: %v", err)
			}
			for _, document := range strings.Split(rendered, "\n---") {
				var meta struct {
					Kind     string `yaml:"kind"`
					Metadata struct {
						Name string `yaml:"name"`
					} `yaml:"metadata"`
				}
				if err := yaml.Unmarshal([]byte(document), &meta); err != nil {
					t.Fatal(err)
				}
				switch meta.Kind {
				case "Gateway", "HTTPRoute", "GRPCRoute", "TLSRoute", "Route", "Certificate", "BackendTrafficPolicy":
					t.Fatalf("fixture unexpectedly renders platform-owned %s", meta.Kind)
				case "NetworkPolicy":
					if !strings.HasSuffix(meta.Metadata.Name, "-raw-driver") {
						t.Fatalf("fixture unexpectedly renders general NetworkPolicy %q", meta.Metadata.Name)
					}
				}
			}
		})
	}
}

func TestADR_0294_HelmHasNoAffinityPolicySurface(t *testing.T) {
	schemaJSON, err := os.ReadFile("values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["affinity"]; !ok {
		t.Fatal("values schema removed the unrelated Kubernetes pod scheduling affinity")
	}
	for _, forbidden := range []string{"gateway", "sessionAffinity", "session_affinity"} {
		if _, ok := schema.Properties[forbidden]; ok {
			t.Fatalf("values schema exposes forbidden Gateway affinity property %q", forbidden)
		}
	}

	valuesYAML, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	valuesJSON, err := yaml.YAMLToJSON(valuesYAML)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(valuesJSON, &values); err != nil {
		t.Fatal(err)
	}
	if got, ok := values["affinity"]; !ok || string(got) != "{}" {
		t.Fatalf("default pod scheduling affinity = %s, present = %v; want empty object", got, ok)
	}

	rendered, err := helm(t, append(productionArgs(), "--set", "affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key=topology.kubernetes.io/zone", "--set", "affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator=Exists")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "affinity:\n        nodeAffinity:") || !strings.Contains(rendered, "key: topology.kubernetes.io/zone") {
		t.Fatal("deployment did not render the configured Kubernetes pod scheduling affinity")
	}
	for _, forbiddenKind := range []string{"Gateway", "HTTPRoute", "GRPCRoute", "BackendTrafficPolicy"} {
		if strings.Contains(rendered, "kind: "+forbiddenKind) {
			t.Fatalf("pod scheduling affinity rendered forbidden platform-owned %s", forbiddenKind)
		}
	}
}

func TestMecak8sHelmChart_ChartOwnedAnnotationsCannotBeOverridden(t *testing.T) {
	base := []string{"template", "production", ".", "--set", "image.tag=v0.0.0", "--set", "redis.endpoint=redis.example.internal:6380", "--set", "redis.credentialsSecret=redis-credentials"}
	for _, tc := range []struct {
		name string
		set  string
		want string
	}{
		{"edge", "security.tlsTerminatedUpstream=true,oidc.enabled=true,oidc.issuer=https://idp.example.com,oidc.audience=mecatl,podAnnotations.example\\.com/kept=value,podAnnotations.mecatl\\.stacklok\\.com/tls-terminated-upstream=false,podAnnotations.mecatl\\.stacklok\\.com/unsafe-real-provider=false", "mecatl.stacklok.com/tls-terminated-upstream"},
		{"edge re-encryption", "security.tlsTerminatedUpstream=true,tls.enabled=true,tls.secretName=mecak8s-tls,oidc.enabled=true,oidc.issuer=https://idp.example.com,oidc.audience=mecatl,podAnnotations.example\\.com/kept=value,podAnnotations.mecatl\\.stacklok\\.com/tls-terminated-upstream=false", "mecatl.stacklok.com/tls-terminated-upstream"},
		{"unsafe", "security.allowUnsafeRealProvider=true,podAnnotations.example\\.com/kept=value,podAnnotations.mecatl\\.stacklok\\.com/tls-terminated-upstream=true,podAnnotations.mecatl\\.stacklok\\.com/unsafe-real-provider=false", "mecatl.stacklok.com/unsafe-real-provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := helm(t, append(base, "--set", tc.set)...)
			if err != nil {
				t.Fatal(err)
			}
			annotations := deploymentFromRender(t, rendered).Spec.Template.Annotations
			if annotations[tc.want] != "true" || annotations["example.com/kept"] != "value" || len(annotations) != 2 {
				t.Fatalf("chart-owned annotations = %#v", annotations)
			}
		})
	}
}

func TestMecak8sHelmChart_RuntimeArgsAreOptIn(t *testing.T) {
	rendered, err := helm(t, productionArgs()...)
	if err != nil {
		t.Fatalf("render defaults: %v", err)
	}
	for _, absent := range []string{"--default-provider=", "--model=", "--max-run-tokens=", "--max-team-tokens="} {
		if strings.Contains(rendered, absent) {
			t.Fatalf("default render unexpectedly contains %q", absent)
		}
	}

	args := append(secureProductionArgs(), "--set", "defaultProvider=openrouter,model=anthropic/claude-sonnet-4-6,maxRunTokens=1000,maxTeamTokens=4000")
	rendered, err = helm(t, args...)
	if err != nil {
		t.Fatalf("render runtime selections: %v", err)
	}
	for _, want := range []string{"--default-provider=openrouter", "--model=anthropic/claude-sonnet-4-6", "--max-run-tokens=1000", "--max-team-tokens=4000"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("runtime render missing %q", want)
		}
	}
}

func TestMecak8sHelmChart_LearningStore(t *testing.T) {
	base := productionArgs()
	rendered, err := helm(t, base...)
	if err != nil {
		t.Fatalf("render defaults: %v", err)
	}
	deployment := deploymentFromRender(t, rendered)
	container := deployment.Spec.Template.Spec.Containers[0]
	for _, forbidden := range []string{"--learning-store-url=", "--driver-tls", "MECATL_DRIVER_AUTH_TOKEN", "learning-store-ca", "learning-store-mtls"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("default render unexpectedly contains %q", forbidden)
		}
	}
	if len(container.Env) != 0 {
		t.Fatalf("default learning store environment = %#v, want none", container.Env)
	}

	for _, tc := range []struct {
		name    string
		set     string
		wantTLS bool
	}{
		{
			name:    "secure in-cluster anonymous",
			set:     "learning.store.endpoint=learning-driver.default.svc.cluster.local:8443,learning.store.tls.enabled=true",
			wantTLS: true,
		},
		{
			name: "plaintext anonymous",
			set:  "learning.store.endpoint=learning-driver.default.svc.cluster.local:8443",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, base...), "--set", tc.set)
			rendered, err := helm(t, args...)
			if err != nil {
				t.Fatalf("render anonymous learning store: %v", err)
			}
			container := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0]
			if !slices.Contains(container.Args, "--learning-store-url=learning-driver.default.svc.cluster.local:8443") {
				t.Fatalf("anonymous learning store URL missing from args: %q", container.Args)
			}
			if got := slices.Contains(container.Args, "--driver-tls"); got != tc.wantTLS {
				t.Fatalf("anonymous learning store --driver-tls = %t, want %t: %q", got, tc.wantTLS, container.Args)
			}
			if len(container.Env) != 0 || strings.Contains(rendered, "MECATL_DRIVER_AUTH_TOKEN") {
				t.Fatalf("anonymous learning store environment = %#v, want no driver token", container.Env)
			}
		})
	}

	secure := append([]string{}, base...)
	secure = append(secure, "--set", strings.Join([]string{
		"learning.store.endpoint=driver.example.internal:8443",
		"learning.store.tokenSecret=learning-driver-credentials",
		"learning.store.tokenKey=bearer-token",
		"learning.store.tls.enabled=true",
		"learning.store.tls.caSecret=learning-driver-ca",
		"learning.store.tls.caKey=ca.pem",
		"learning.store.tls.mtlsSecret=learning-driver-client",
		"learning.store.tls.certKey=client.crt",
		"learning.store.tls.keyKey=client.key",
	}, ","))
	rendered, err = helm(t, secure...)
	if err != nil {
		t.Fatalf("render secure learning store: %v", err)
	}
	deployment = deploymentFromRender(t, rendered)
	container = deployment.Spec.Template.Spec.Containers[0]
	for _, want := range []string{
		"--learning-store-url=driver.example.internal:8443",
		"--driver-tls",
		"--driver-tls-ca=/var/run/secrets/learning-store-ca/ca.pem",
		"--driver-tls-cert=/var/run/secrets/learning-store-mtls/client.crt",
		"--driver-tls-key=/var/run/secrets/learning-store-mtls/client.key",
	} {
		if !slices.Contains(container.Args, want) {
			t.Fatalf("secure learning store args missing %q: %q", want, container.Args)
		}
	}
	if slices.ContainsFunc(container.Args, func(arg string) bool { return strings.HasPrefix(arg, "--driver-auth-token") }) {
		t.Fatalf("driver token was rendered as an argument: %q", container.Args)
	}
	if len(container.Env) != 1 || container.Env[0].Name != "MECATL_DRIVER_AUTH_TOKEN" || container.Env[0].Value != "" || container.Env[0].ValueFrom == nil || container.Env[0].ValueFrom.SecretKeyRef == nil || container.Env[0].ValueFrom.SecretKeyRef.Name != "learning-driver-credentials" || container.Env[0].ValueFrom.SecretKeyRef.Key != "bearer-token" {
		t.Fatalf("learning driver environment = %#v", container.Env)
	}
	for _, name := range []string{"learning-store-ca", "learning-store-mtls"} {
		if !slices.ContainsFunc(container.VolumeMounts, func(mount corev1.VolumeMount) bool { return mount.Name == name && mount.ReadOnly }) {
			t.Fatalf("learning TLS volume %q is not mounted read-only: %#v", name, container.VolumeMounts)
		}
	}
	for _, forbidden := range []string{"unrenderable-token-value", "value: bearer-token", "--driver-auth-token="} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("render disclosed a driver token value via %q", forbidden)
		}
	}

	invalid := [][]string{
		{"learning.store.endpoint=driver.example.internal:8443", "learning.store.tokenSecret=learning-driver-credentials"},
		{"learning.store.endpoint=driver.example.internal:8443", "learning.store.tokenKey=bearer-token"},
		{"learning.store.endpoint=driver.example.internal:8443", "learning.store.tokenSecret=learning-driver-credentials", "learning.store.tokenKey=bearer-token", "learning.store.tls.enabled=true", "learning.store.tls.caSecret=learning-driver-ca"},
		{"learning.store.endpoint=driver.example.internal:8443", "learning.store.tokenSecret=learning-driver-credentials", "learning.store.tokenKey=bearer-token", "learning.store.tls.caSecret=learning-driver-ca", "learning.store.tls.caKey=ca.pem"},
		{"learning.store.endpoint=driver.example.internal:8443", "learning.store.tokenSecret=learning-driver-credentials", "learning.store.tokenKey=bearer-token", "learning.store.tls.enabled=true", "learning.store.tls.mtlsSecret=learning-driver-client", "learning.store.tls.certKey=client.crt"},
		{"learning.store.endpoint=driver.example.internal:8443", "learning.store.tokenSecret=learning-driver-credentials", "learning.store.tokenKey=bearer-token"},
	}
	for _, set := range invalid {
		args := append(append([]string{}, base...), "--set", strings.Join(set, ","))
		if _, err := helm(t, args...); err == nil {
			t.Fatalf("schema accepted invalid learning store values %q", set)
		}
		args = append(append([]string{}, base...), "--skip-schema-validation", "--set", strings.Join(set, ","))
		if _, err := helm(t, args...); err == nil {
			t.Fatalf("helper accepted invalid learning store values %q", set)
		}
	}
	oidc := append(secureProductionArgs(), "--set", "learning.store.endpoint=driver.example.internal:8443,learning.store.tokenSecret=learning-driver-credentials,learning.store.tokenKey=bearer-token,learning.store.tls.enabled=true")
	if _, err := helm(t, oidc...); err == nil {
		t.Fatal("schema accepted OIDC with a learning store")
	}
	oidc = append(secureProductionArgs(), "--skip-schema-validation", "--set", "learning.store.endpoint=driver.example.internal:8443,learning.store.tokenSecret=learning-driver-credentials,learning.store.tokenKey=bearer-token,learning.store.tls.enabled=true")
	if _, err := helm(t, oidc...); err == nil {
		t.Fatal("helper accepted OIDC with a learning store")
	}
}

func TestMecak8sHelmChart_OpaqueModelArgumentIsYAMLSafe(t *testing.T) {
	model := "vendor/model: tier #stable"
	args := append(secureProductionArgs(), "--set", "defaultProvider=openrouter", "--set-string", "model="+model)
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render opaque model: %v", err)
	}
	deployment := deploymentFromRender(t, rendered)
	if got := deployment.Spec.Template.Spec.Containers[0].Args; !slices.Contains(got, "--model="+model) || !slices.Contains(got, "--default-provider=openrouter") {
		t.Fatalf("decoded args lost opaque values: %q", got)
	}
}

func TestMecak8sHelmChart_ValueDerivedArgumentsAreYAMLSafe(t *testing.T) {
	audience := "api:agents # primary"
	workspace := "/workspaces/team: alpha #1"
	args := append(secureProductionArgs(),
		"--set-string", "oidc.audience="+audience,
		"--set-string", "workspace="+workspace,
	)
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render opaque arguments: %v", err)
	}
	got := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{"--oidc-audience=" + audience, "--workspace=" + workspace} {
		if !slices.Contains(got, want) {
			t.Fatalf("decoded args lost %q: %q", want, got)
		}
	}
}

func TestMecak8sHelmChart_DeployCheckProductionFixtureRuntimeAndSpread(t *testing.T) {
	// Match task deploy:check exactly: no explicit release name is supplied.
	rendered, err := helm(t, "template", ".", "-f", "ci/production-values.yaml")
	if err != nil {
		t.Fatalf("render production fixture: %v", err)
	}
	deployment := deploymentFromRender(t, rendered)
	wantArgs := []string{
		"--grpc-addr=0.0.0.0:8080",
		"--http-addr=0.0.0.0:8081",
		"--drain-addr=0.0.0.0:8082",
		"--redis-url=redis.example.internal:6379",
		"--session-lease-k8s-namespace=default",
		"--headless=true",
		"--posture=auto",
		"--default-provider=openrouter",
		"--model=anthropic/claude-sonnet-4-6",
		"--max-run-tokens=200000",
		"--max-team-tokens=800000",
		"--redis-tls-ca=/var/run/secrets/redis/ca.pem",
		"--oidc-issuer=https://idp.example.com",
		"--oidc-audience=mecatl",
		"--oidc-max-jwks-staleness=1h",
		"--tls-cert=/var/run/secrets/tls/tls.crt",
		"--tls-key=/var/run/secrets/tls/tls.key",
	}
	if got := deployment.Spec.Template.Spec.Containers[0].Args; !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("production args = %#v, want %#v", got, wantArgs)
	}
	constraints := deployment.Spec.Template.Spec.TopologySpreadConstraints
	if len(constraints) != 1 {
		t.Fatalf("topology spread constraints = %d, want 1", len(constraints))
	}
	constraint := constraints[0]
	if constraint.MaxSkew != 1 || constraint.TopologyKey != "kubernetes.io/hostname" || constraint.WhenUnsatisfiable != "DoNotSchedule" {
		t.Fatalf("unexpected production topology spread: %+v", constraint)
	}
	wantLabels := map[string]string{
		"app.kubernetes.io/name":      "mecak8s",
		"app.kubernetes.io/instance":  "release-name",
		"app.kubernetes.io/component": "agent",
	}
	if constraint.LabelSelector == nil || !reflect.DeepEqual(constraint.LabelSelector.MatchLabels, wantLabels) {
		t.Fatalf("spread selector = %#v, want %#v", constraint.LabelSelector, wantLabels)
	}
	for key, value := range constraint.LabelSelector.MatchLabels {
		if deployment.Spec.Template.Labels[key] != value {
			t.Fatalf("spread selector %s=%s does not match pod labels %#v", key, value, deployment.Spec.Template.Labels)
		}
	}
}

func TestMecak8sHelmChart_ProductionFixturesReferenceProviderCredential(t *testing.T) {
	for _, fixture := range []string{"ci/production-values.yaml", "ci/production-oidc-values.yaml", "ci/production-edge-tls-values.yaml"} {
		t.Run(fixture, func(t *testing.T) {
			rendered, err := helm(t, "template", ".", "-f", fixture)
			if err != nil {
				t.Fatalf("render production fixture: %v", err)
			}
			container := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0]
			if !slices.Contains(container.Args, "--default-provider=openrouter") || !slices.Contains(container.Args, "--model=anthropic/claude-sonnet-4-6") {
				t.Fatalf("production provider selection = %q", container.Args)
			}
			if len(container.Env) != 1 {
				t.Fatalf("provider environment = %#v, want one SecretKeyRef", container.Env)
			}
			env := container.Env[0]
			if env.Name != "OPENROUTER_API_KEY" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil || env.ValueFrom.SecretKeyRef.Name != "provider-credentials" || env.ValueFrom.SecretKeyRef.Key != "openrouter-api-key" {
				t.Fatalf("provider environment = %#v", env)
			}
		})
	}
}

// providerSecurityCase is one point in the real-provider gate's input space. The gate is
// enforced twice and independently — values.schema.json and the
// mecak8s.validateProviderSecurity helper — so both enforcement tests iterate THIS table
// against THIS oracle. Keeping the rule in one place is the point: a divergence between
// the two enforcement paths must fail as a disagreement with the shared oracle, never be
// papered over by editing one test's private copy of the rule.
type providerSecurityCase struct {
	serviceType                   string
	mock, unsafe, tls, edge, oidc bool
}

func (c providerSecurityCase) name() string {
	return fmt.Sprintf("service=%s/mock=%t/unsafe=%t/tls=%t/edge=%t/oidc=%t", c.serviceType, c.mock, c.unsafe, c.tls, c.edge, c.oidc)
}

// wantAccepted is the ADR 0278 gate: a mock provider or the explicit unsafe bypass is
// always accepted; a secure real provider needs OIDC plus either in-pod TLS or an
// upstream-TLS attestation on a ClusterIP-only h2c backend. The upstream attestation is
// meaningless without a real provider, so mockProvider=true rejects it outright rather
// than accepting a value it would silently ignore.
func (c providerSecurityCase) wantAccepted() bool {
	if c.mock {
		return !c.edge
	}
	return c.unsafe || (c.oidc && (c.tls || (c.edge && c.serviceType == "ClusterIP")))
}

func providerSecurityCases() []providerSecurityCase {
	var cases []providerSecurityCase
	for _, serviceType := range []string{"ClusterIP", "NodePort", "LoadBalancer"} {
		for _, mock := range []bool{false, true} {
			for _, unsafe := range []bool{false, true} {
				for _, tls := range []bool{false, true} {
					for _, edge := range []bool{false, true} {
						for _, oidc := range []bool{false, true} {
							cases = append(cases, providerSecurityCase{serviceType, mock, unsafe, tls, edge, oidc})
						}
					}
				}
			}
		}
	}
	return cases
}

func TestMecak8sValuesSchemaIndependentlyEnforcesProviderSecurity(t *testing.T) {
	schemaJSON, err := os.ReadFile("values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	valuesYAML, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	valuesJSON, err := yaml.YAMLToJSON(valuesYAML)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range providerSecurityCases() {
		t.Run(tc.name(), func(t *testing.T) {
			var values map[string]any
			if err := json.Unmarshal(valuesJSON, &values); err != nil {
				t.Fatal(err)
			}
			values["mockProvider"] = tc.mock
			values["security"].(map[string]any)["allowUnsafeRealProvider"] = tc.unsafe
			values["security"].(map[string]any)["tlsTerminatedUpstream"] = tc.edge
			values["tls"].(map[string]any)["enabled"] = tc.tls
			values["oidc"].(map[string]any)["enabled"] = tc.oidc
			values["service"].(map[string]any)["type"] = tc.serviceType
			if err := resolved.Validate(values); (err == nil) != tc.wantAccepted() {
				t.Fatalf("schema acceptance = %t, want %t: %v", err == nil, tc.wantAccepted(), err)
			}
		})
	}

	var legacyPodTLS map[string]any
	if err := json.Unmarshal(valuesJSON, &legacyPodTLS); err != nil {
		t.Fatal(err)
	}
	legacyPodTLS["mockProvider"] = false
	delete(legacyPodTLS["security"].(map[string]any), "tlsTerminatedUpstream")
	legacyPodTLS["tls"].(map[string]any)["enabled"] = true
	legacyPodTLS["tls"].(map[string]any)["secretName"] = "mecak8s-tls"
	legacyPodTLS["oidc"].(map[string]any)["enabled"] = true
	if err := resolved.Validate(legacyPodTLS); err != nil {
		t.Fatalf("schema rejected chart-0.2-shaped pod TLS + OIDC values: %v", err)
	}
}

func TestMecak8sHelmHelperMatchesProviderSecuritySchema(t *testing.T) {
	chart := t.TempDir()
	if err := os.CopyFS(chart, os.DirFS(".")); err != nil {
		t.Fatalf("copy chart without schema: %v", err)
	}
	if err := os.Remove(filepath.Join(chart, "values.schema.json")); err != nil {
		t.Fatalf("remove chart schema: %v", err)
	}
	base := []string{"template", "production", chart, "--set", "image.tag=v0.0.0", "--set", "redis.endpoint=redis.example.internal:6380", "--set", "redis.credentialsSecret=redis-credentials"}
	for _, tc := range providerSecurityCases() {
		t.Run(tc.name(), func(t *testing.T) {
			args := append([]string{}, base...)
			args = append(args, "--set", fmt.Sprintf("mockProvider=%t,security.allowUnsafeRealProvider=%t,security.tlsTerminatedUpstream=%t,tls.enabled=%t,oidc.enabled=%t,service.type=%s", tc.mock, tc.unsafe, tc.edge, tc.tls, tc.oidc, tc.serviceType))
			if tc.tls {
				args = append(args, "--set", "tls.secretName=mecak8s-tls")
			}
			if tc.oidc {
				args = append(args, "--set", "oidc.issuer=https://idp.example.com,oidc.audience=mecatl")
			}
			_, err := helm(t, args...)
			if (err == nil) != tc.wantAccepted() {
				t.Fatalf("helper acceptance = %t, want %t: %v", err == nil, tc.wantAccepted(), err)
			}
		})
	}
}

func TestMecak8sHelmChart_KindFixtureNodePortsAndProductionClusterIP(t *testing.T) {
	fixture, err := helm(t, kindFixtureArgs()...)
	if err != nil {
		t.Fatalf("render Kind fixture overlay: %v", err)
	}
	assertFixtureServiceNodePorts(t, fixture)
	keycloakFixture, err := helm(t, kindKeycloakFixtureArgs()...)
	if err != nil {
		t.Fatalf("render Kind Keycloak fixture overlays in Taskfile order: %v", err)
	}
	assertFixtureServiceNodePorts(t, keycloakFixture)
	bare, err := helm(t, "template", "kind", ".", "-f", "values-kind.yaml")
	if err != nil {
		t.Fatalf("render bare Kind profile: %v", err)
	}
	if !strings.Contains(bare, "type: ClusterIP") || strings.Contains(bare, "nodePort:") {
		t.Fatal("shared Kind values must remain ClusterIP without NodePorts")
	}
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
	rendered, err = helm(t, args...)
	if err != nil {
		t.Fatalf("render chart-version image: %v", err)
	}
	chartYAML, err := os.ReadFile("Chart.yaml")
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	var chart struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(chartYAML, &chart); err != nil {
		t.Fatalf("parse Chart.yaml: %v", err)
	}
	wantImage := "ghcr.io/stacklok/mecatl/mecak8s:v" + chart.Version
	if !strings.Contains(rendered, wantImage) {
		t.Fatalf("production render did not default the image tag to %q from Chart.yaml", wantImage)
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
	publicTLS := []string{"template", "production", ".", "--set", "image.tag=v0.0.0", "--set", "redis.endpoint=redis.example.internal:6380", "--set", "redis.caKey=", "--set", "security.allowUnsafeRealProvider=true"}
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
	for _, forbidden := range []string{"--redis-tls-ca", "redis-credentials"} {
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

func TestMecak8sHelmChart_ReplicaCountControlsDisruptionBudget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		replicaCount string
		wantPDB      bool
		wantMinAvail int32
	}{
		{name: "single replica", replicaCount: "1", wantPDB: false},
		{name: "two replicas", replicaCount: "2", wantPDB: true, wantMinAvail: 1},
		{name: "three replicas", replicaCount: "3", wantPDB: true, wantMinAvail: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(productionArgs(), "--set", "replicaCount="+tc.replicaCount)
			rendered, err := helm(t, args...)
			if err != nil {
				t.Fatalf("render replica count %s: %v", tc.replicaCount, err)
			}
			deployment := deploymentFromRender(t, rendered)
			if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != int32(mustParseInt(t, tc.replicaCount)) {
				t.Fatalf("Deployment replicas = %v, want %s", deployment.Spec.Replicas, tc.replicaCount)
			}
			pdb := pdbFromRender(t, rendered)
			if (pdb != nil) != tc.wantPDB {
				t.Fatalf("PDB present = %t, want %t", pdb != nil, tc.wantPDB)
			}
			if pdb == nil {
				return
			}
			if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntVal != tc.wantMinAvail {
				t.Fatalf("PDB minAvailable = %v, want %d", pdb.Spec.MinAvailable, tc.wantMinAvail)
			}
			if !reflect.DeepEqual(pdb.Spec.Selector, deployment.Spec.Selector) {
				t.Fatalf("PDB selector = %#v, Deployment selector = %#v", pdb.Spec.Selector, deployment.Spec.Selector)
			}
		})
	}
}

func mustParseInt(t *testing.T, value string) int {
	t.Helper()
	var parsed int
	if _, err := fmt.Sscan(value, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed
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

// taskBlockRe matches a top-level Taskfile task header (2-space indent under
// `tasks:`, e.g. "  kind-setup:"). Not a full YAML parser — this Taskfile only
// nests task bodies one level deeper than their header.
var taskBlockRe = regexp.MustCompile(`(?m)^  ([a-zA-Z][a-zA-Z0-9_-]*):\s*$`)

// taskFileClosure splits a Taskfile's `tasks:` section into named blocks and
// returns the concatenated text of `roots` plus every task transitively
// reachable from them via `task: <name>` references.
func taskFileClosure(t *testing.T, text string, roots ...string) string {
	t.Helper()
	tasksIdx := strings.Index(text, "\ntasks:\n")
	if tasksIdx < 0 {
		t.Fatal("Taskfile has no tasks: section")
	}
	body := text[tasksIdx:]
	headers := taskBlockRe.FindAllStringSubmatchIndex(body, -1)
	blocks := make(map[string]string, len(headers))
	for i, h := range headers {
		name := body[h[2]:h[3]]
		end := len(body)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		blocks[name] = body[h[0]:end]
	}
	refRe := regexp.MustCompile(`task:\s*([a-zA-Z][a-zA-Z0-9_-]*)`)
	visited := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		block, ok := blocks[name]
		if !ok {
			t.Fatalf("Taskfile references unknown task %q", name)
		}
		visited[name] = true
		for _, m := range refRe.FindAllStringSubmatch(block, -1) {
			visit(m[1])
		}
	}
	for _, root := range roots {
		visit(root)
	}
	var out strings.Builder
	for _, name := range roots {
		out.WriteString(blocks[name])
	}
	for name, block := range blocks {
		if visited[name] && !slices.Contains(roots, name) {
			out.WriteString(block)
		}
	}
	return out.String()
}

func TestMecak8sHelmChart_Scenario1_KindLifecycleUsesNamedCluster(t *testing.T) {
	path := filepath.Join("..", "..", "mecak8s-kind", "Taskfile.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Scope the guard to the base disposable Kind lifecycle (bootstrap +
	// teardown) reachable from kind-setup/kind-destroy, not the whole file:
	// the vMCP integration phase (vmcp-setup -> toolhive-install) legitimately
	// installs ToolHive from an OCI registry, and lives in sibling tasks this
	// guard was never meant to cover.
	text := taskFileClosure(t, string(body), "kind-setup", "kind-destroy")
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
	for _, forbidden := range []string{"--oidc-issuer", "--oidc-audience", "--oidc-jwks-uri", "--oidc-ca-cert-file", "--oidc-allow-private-https-issuer", "kind: NetworkPolicy", "raw-driver"} {
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
func TestMecak8sHelmChart_OIDC_PrivateHTTPSIssuer(t *testing.T) {
	base := append(productionArgs(), "--set", "oidc.enabled=true", "--set", "oidc.issuer=https://idp.example.internal", "--set", "oidc.audience=mecatl", "--set", "oidc.allowPrivateHTTPSIssuer=true", "--set", "oidc.caSecret=issuer-ca", "--set", "oidc.caKey=ca.pem")
	rendered, err := helm(t, base...)
	if err != nil {
		t.Fatalf("render private HTTPS issuer: %v", err)
	}
	for _, want := range []string{"--oidc-allow-private-https-issuer", "--oidc-ca-cert-file=/var/run/secrets/oidc-ca/ca.pem", "secretName: issuer-ca"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("private HTTPS issuer render missing %q", want)
		}
	}
	for _, forbidden := range []string{"--oidc-insecure-allow-private-issuer", "SSL_CERT_FILE"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("private HTTPS issuer render contains %q", forbidden)
		}
	}
	for _, set := range []string{"oidc.issuer=http://idp.example.internal", "oidc.caSecret=", "oidc.caKey=", "oidc.enabled=false"} {
		args := append(append([]string{}, base...), "--set", set)
		if _, err := helm(t, args...); err == nil {
			t.Fatalf("private HTTPS issuer render accepted %q", set)
		}
	}
}

func TestMecak8sHelmChart_ServerTLS(t *testing.T) {
	base := productionArgs()
	defaultRender, err := helm(t, base...)
	if err != nil {
		t.Fatalf("render default production values: %v", err)
	}
	falseRender, err := helm(t, append(append([]string{}, base...), "--set", "tls.enabled=false")...)
	if err != nil {
		t.Fatalf("render tls.enabled=false production values: %v", err)
	}
	if defaultRender != falseRender {
		t.Fatal("tls.enabled=false changed the default production render")
	}
	for _, forbidden := range []string{"--tls-cert", "--tls-key", "/var/run/secrets/tls", "name: tls", "scheme: HTTPS"} {
		if strings.Contains(defaultRender, forbidden) {
			t.Fatalf("default production render unexpectedly contains %q", forbidden)
		}
	}
	for _, want := range []string{
		"startupProbe:\n            httpGet:\n              scheme: HTTP\n              path: /readyz\n              port: http",
		"readinessProbe:\n            httpGet:\n              scheme: HTTP\n              path: /readyz\n              port: http",
		"livenessProbe:\n            httpGet:\n              scheme: HTTP\n              path: /healthz\n              port: http",
		"preStop:\n              httpGet:\n                scheme: HTTP\n                path: /drain\n                port: drain",
	} {
		if !strings.Contains(defaultRender, want) {
			t.Fatalf("default production render missing HTTP endpoint block %q", want)
		}
	}
	deployment := deploymentFromRender(t, defaultRender)
	container := deployment.Spec.Template.Spec.Containers[0]
	if !slices.Contains(container.Args, "--drain-addr=0.0.0.0:8082") {
		t.Fatalf("drain listener arg missing: %q", container.Args)
	}
	if !slices.ContainsFunc(container.Ports, func(port corev1.ContainerPort) bool {
		return port.Name == "drain" && port.ContainerPort == 8082
	}) {
		t.Fatalf("drain container port missing: %#v", container.Ports)
	}
	defaultService := serviceFromRender(t, defaultRender)
	wantServicePorts := map[string]struct {
		port       int32
		targetPort string
	}{
		"grpc": {port: 8080, targetPort: "grpc"},
		"http": {port: 8081, targetPort: "http"},
	}
	if len(defaultService.Spec.Ports) != len(wantServicePorts) {
		t.Fatalf("Service ports = %#v, want exactly grpc and http", defaultService.Spec.Ports)
	}
	for _, port := range defaultService.Spec.Ports {
		want, ok := wantServicePorts[port.Name]
		if !ok || port.Port != want.port || port.TargetPort.String() != want.targetPort {
			t.Fatalf("Service port = %#v, want mappings %#v", port, wantServicePorts)
		}
	}

	fixtureArgs := []string{"template", "production", ".", "-f", "ci/production-tls-values.yaml"}
	rendered, err := helm(t, fixtureArgs...)
	if err != nil {
		t.Fatalf("render TLS production fixture: %v", err)
	}
	for _, want := range []string{
		"--tls-cert=/var/run/secrets/tls/tls.crt",
		"--tls-key=/var/run/secrets/tls/tls.key",
		"- {name: tls, mountPath: /var/run/secrets/tls, readOnly: true}",
		"- name: tls\n          secret:\n            secretName: mecak8s-tls\n            defaultMode: 0440\n            items:\n              - {key: \"tls.crt\", path: \"tls.crt\"}\n              - {key: \"tls.key\", path: \"tls.key\"}",
		"startupProbe:\n            httpGet:\n              scheme: HTTPS\n              path: /readyz\n              port: http",
		"readinessProbe:\n            httpGet:\n              scheme: HTTPS\n              path: /readyz\n              port: http",
		"livenessProbe:\n            httpGet:\n              scheme: HTTPS\n              path: /healthz\n              port: http",
		"preStop:\n              httpGet:\n                scheme: HTTP\n                path: /drain\n                port: drain",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("TLS production fixture missing %q", want)
		}
	}

	service := serviceFromRender(t, rendered)
	for _, port := range service.Spec.Ports {
		if port.AppProtocol != nil && *port.AppProtocol == "kubernetes.io/h2c" {
			t.Fatalf("pod TLS Service port %q has h2c appProtocol", port.Name)
		}
	}

	enabled := append(append([]string{}, base...), "--set", "tls.enabled=true,tls.secretName=mecak8s-tls")
	customKeys := append(append([]string{}, enabled...), "--set", "tls.certKey=server.crt,tls.keyKey=server.key")
	rendered, err = helm(t, customKeys...)
	if err != nil {
		t.Fatalf("render TLS production values with custom keys: %v", err)
	}
	for _, want := range []string{
		"--tls-cert=/var/run/secrets/tls/server.crt",
		"--tls-key=/var/run/secrets/tls/server.key",
		"- name: tls\n          secret:\n            secretName: mecak8s-tls\n            defaultMode: 0440\n            items:\n              - {key: \"server.crt\", path: \"server.crt\"}\n              - {key: \"server.key\", path: \"server.key\"}",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("TLS custom-key render missing %q", want)
		}
	}
	for _, forbidden := range []string{`- {key: "tls.crt", path: "tls.crt"}`, `- {key: "tls.key", path: "tls.key"}`} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("TLS custom-key render unexpectedly retained %q", forbidden)
		}
	}

	for _, set := range []string{"tls.secretName=", "tls.certKey=", "tls.keyKey=", "tls.clientCA=ca.pem"} {
		args := append(append([]string{}, enabled...), "--set", set)
		if _, err := helm(t, args...); err == nil {
			t.Fatalf("render accepted invalid TLS configuration %q", set)
		}
	}
}

func TestMecak8sVMCPPOC_Scenario3_ChartTLSContract(t *testing.T) {
	args := kindVMCPArgs()
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render Kind TLS profile: %v", err)
	}
	for _, want := range []string{
		"--tls-cert=/var/run/secrets/tls/tls.crt",
		"--tls-key=/var/run/secrets/tls/tls.key",
		"name: tls",
		"mountPath: /var/run/secrets/tls",
		"readOnly: true",
		"secretName: mecak8s-tls",
		`- {key: "tls.crt", path: "tls.crt"}`,
		`- {key: "tls.key", path: "tls.key"}`,
		"name: oidc-ca",
		"mountPath: /var/run/secrets/oidc-ca",
		"secretName: fixture-ca",
		`- {key: "tls.crt", path: "tls.crt"}`,
		"--oidc-ca-cert-file=/var/run/secrets/oidc-ca/tls.crt",
		"--oidc-allow-private-https-issuer",
		"scheme: HTTPS",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Kind TLS render missing %q", want)
		}
	}
	for _, missing := range []string{"tls.certKey=", "tls.keyKey=", "tls.secretName="} {
		if _, err := helm(t, append(append([]string{}, args...), "--set", missing)...); err == nil {
			t.Fatalf("render accepted incomplete TLS configuration %q", missing)
		}
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

func TestMecak8sHelmChart_ImagePullSecrets(t *testing.T) {
	rendered, err := helm(t, productionArgs()...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	if strings.Contains(rendered, "imagePullSecrets:") {
		t.Fatal("default render (imagePullSecrets unset) unexpectedly contains imagePullSecrets")
	}

	args := append(productionArgs(), "--set", "imagePullSecrets[0].name=ghcr-pull-secret")
	rendered, err = helm(t, args...)
	if err != nil {
		t.Fatalf("render with imagePullSecrets: %v", err)
	}
	if !strings.Contains(rendered, "imagePullSecrets:") || !strings.Contains(rendered, "- name: ghcr-pull-secret") {
		t.Fatal("render with imagePullSecrets set missing the projected pull secret")
	}
}

func TestMecak8sHelmChart_KindLiveProviderDisablesMock(t *testing.T) {
	args := append(kindVMCPArgs(),
		"--set", "mockProvider=false",
		"--set", "security.allowUnsafeRealProvider=true",
		"--set", "extraEnv[0].name=OPENROUTER_API_KEY",
		"--set", "extraEnv[0].valueFrom.secretKeyRef.name=mecak8s-live-provider",
		"--set", "extraEnv[0].valueFrom.secretKeyRef.key=OPENROUTER_API_KEY",
	)
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render Kind live-provider profile: %v", err)
	}
	container := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0]
	if slices.Contains(container.Args, "--mock") {
		t.Fatal("Kind live-provider profile retained --mock")
	}
	if len(container.Env) != 1 || container.Env[0].Name != "OPENROUTER_API_KEY" || container.Env[0].ValueFrom == nil || container.Env[0].ValueFrom.SecretKeyRef == nil || container.Env[0].ValueFrom.SecretKeyRef.Name != "mecak8s-live-provider" {
		t.Fatalf("Kind live-provider environment = %#v", container.Env)
	}
}

func TestMecak8sHelmChart_ExtraEnv(t *testing.T) {
	rendered, err := helm(t, productionArgs()...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	if strings.Contains(rendered, "\n          env:") {
		t.Fatal("default render (extraEnv unset) unexpectedly contains an env: block")
	}

	args := append(productionArgs(),
		"--set", "extraEnv[0].name=OPENROUTER_API_KEY",
		"--set", "extraEnv[0].valueFrom.secretKeyRef.name=openrouter-key",
		"--set", "extraEnv[0].valueFrom.secretKeyRef.key=api-key",
	)
	rendered, err = helm(t, args...)
	if err != nil {
		t.Fatalf("render with extraEnv: %v", err)
	}
	for _, want := range []string{
		"name: OPENROUTER_API_KEY",
		"name: openrouter-key",
		"key: api-key",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("render with extraEnv set missing %q", want)
		}
	}
}

func TestMecak8sHelmChart_ConfigMountDefaultsAreEmpty(t *testing.T) {
	rendered, err := helm(t, productionArgs()...)
	if err != nil {
		t.Fatalf("render production values: %v", err)
	}
	deployment := deploymentFromRender(t, rendered)
	container := deployment.Spec.Template.Spec.Containers[0]
	if slices.Contains(container.Args, "--skills-conventional=true") || slices.Contains(container.Args, "--no-user-model") {
		t.Fatal("default render unexpectedly enables mounted configuration")
	}
	for _, env := range container.Env {
		if env.Name == "XDG_CONFIG_HOME" {
			t.Fatal("default render unexpectedly sets XDG_CONFIG_HOME")
		}
	}
	for _, mount := range container.VolumeMounts {
		if mount.Name == "mecatl-config" || mount.MountPath == "/etc/mecatl-config" {
			t.Fatal("default render unexpectedly mounts mecatl configuration")
		}
	}
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == "mecatl-config" {
			t.Fatal("default render unexpectedly defines a mecatl configuration volume")
		}
	}
}

func TestMecak8sHelmChart_SkillsAutoDiscover(t *testing.T) {
	args := append(productionArgs(), "--set", "skills.autoDiscover=true")
	rendered, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render with skill auto-discovery: %v", err)
	}
	container := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0]
	if !slices.Contains(container.Args, "--skills-conventional=true") {
		t.Fatal("skills.autoDiscover did not render --skills-conventional=true")
	}
}

func TestMecak8sHelmChart_XDGConfigMapMount(t *testing.T) {
	rendered, err := helm(t, "template", "production", ".", "-f", "ci/config-mount-values.yaml")
	if err != nil {
		t.Fatalf("render XDG ConfigMap mount fixture: %v", err)
	}

	deployment := deploymentFromRender(t, rendered)
	container := deployment.Spec.Template.Spec.Containers[0]
	for _, want := range []string{"--skills-conventional=true", "--no-user-model"} {
		if !slices.Contains(container.Args, want) {
			t.Fatalf("agent args missing %q", want)
		}
	}
	if !slices.ContainsFunc(container.Env, func(env corev1.EnvVar) bool {
		return env.Name == "XDG_CONFIG_HOME" && env.Value == "/etc/mecatl-config"
	}) {
		t.Fatal("agent env missing XDG_CONFIG_HOME=/etc/mecatl-config")
	}
	if !slices.ContainsFunc(container.VolumeMounts, func(mount corev1.VolumeMount) bool {
		return mount.Name == "mecatl-config" && mount.MountPath == "/etc/mecatl-config" && mount.ReadOnly
	}) {
		t.Fatal("agent volume mounts missing read-only mecatl configuration mount")
	}
	if !slices.ContainsFunc(deployment.Spec.Template.Spec.Volumes, func(volume corev1.Volume) bool {
		if volume.Name != "mecatl-config" || volume.ConfigMap == nil || volume.ConfigMap.Name != "mecatl-config-v1" {
			return false
		}
		return slices.ContainsFunc(volume.ConfigMap.Items, func(item corev1.KeyToPath) bool {
			return item.Key == "review-skill" && item.Path == "mecatl/skills/review/SKILL.md" && item.Mode != nil && *item.Mode == 0o444
		})
	}) {
		t.Fatal("pod volumes missing projected review skill")
	}
}

func TestMecak8sHelmChart_XDGImageVolumeSkillMount(t *testing.T) {
	rendered, err := helm(t, "template", "production", ".", "-f", "ci/config-mount-values.yaml")
	if err != nil {
		t.Fatalf("render XDG image volume fixture: %v", err)
	}

	deployment := deploymentFromRender(t, rendered)
	container := deployment.Spec.Template.Spec.Containers[0]
	if !slices.ContainsFunc(container.VolumeMounts, func(mount corev1.VolumeMount) bool {
		return mount.Name == "toolhive-review-skill" &&
			mount.MountPath == "/etc/mecatl-config/mecatl/skills/toolhive-review" && mount.ReadOnly
	}) {
		t.Fatal("agent volume mounts missing read-only ToolHive skill mount")
	}
	if !slices.ContainsFunc(deployment.Spec.Template.Spec.Volumes, func(volume corev1.Volume) bool {
		return volume.Name == "toolhive-review-skill" && volume.Image != nil &&
			volume.Image.Reference == "ghcr.io/example/toolhive-review-skill@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" &&
			volume.Image.PullPolicy == corev1.PullIfNotPresent
	}) {
		t.Fatal("pod volumes missing digest-pinned ToolHive skill image")
	}
}

func TestMecak8sHelmChart_NewValuesAreSchemaValidated(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  string
	}{
		{name: "unknown root key", set: "unknownConfigMountValue=true"},
		{name: "extraArgs scalar", set: "extraArgs=--no-user-model"},
		{name: "skills auto-discover string", set: "skills.autoDiscover=not-a-bool"},
		{name: "unknown skills setting", set: "skills.unknown=true"},
		{name: "removed skillsConventional setting", set: "skillsConventional=true"},
		{name: "volume mount missing path", set: "extraVolumeMounts[0].name=mecatl-config"},
		{name: "volume name wrong type", set: "extraVolumes[0].name=true"},
		{name: "volume hostPath source", set: "extraVolumes[0].name=unsafe,extraVolumes[0].hostPath.path=/tmp"},
		{name: "volume unknown source", set: "extraVolumes[0].name=unknown,extraVolumes[0].unknown.name=value"},
		{name: "volume without source", set: "extraVolumes[0].name=missing-source"},
		{name: "volume with multiple sources", set: "extraVolumes[0].name=multiple,extraVolumes[0].configMap.name=config,extraVolumes[0].secret.secretName=secret"},
		{name: "image volume missing reference", set: "extraVolumes[0].name=skill,extraVolumes[0].image.pullPolicy=Always"},
		{name: "image volume empty reference", set: "extraVolumes[0].name=skill,extraVolumes[0].image.reference="},
		{name: "image volume invalid pull policy", set: "extraVolumes[0].name=skill,extraVolumes[0].image.reference=registry.example/skill@sha256:abc,extraVolumes[0].image.pullPolicy=Sometimes"},
		{name: "image volume unknown field", set: "extraVolumes[0].name=skill,extraVolumes[0].image.reference=registry.example/skill@sha256:abc,extraVolumes[0].image.unknown=true"},
		{name: "image volume with another source", set: "extraVolumes[0].name=multiple,extraVolumes[0].image.reference=registry.example/skill@sha256:abc,extraVolumes[0].configMap.name=config"},
		{name: "unsafe mount propagation", set: "extraVolumeMounts[0].name=config,extraVolumeMounts[0].mountPath=/config,extraVolumeMounts[0].mountPropagation=Bidirectional"},
		{name: "mount subpath and expression", set: "extraVolumeMounts[0].name=config,extraVolumeMounts[0].mountPath=/config,extraVolumeMounts[0].subPath=one,extraVolumeMounts[0].subPathExpr=$(VALUE)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(productionArgs(), "--set", tc.set)
			if _, err := helm(t, args...); err == nil {
				t.Fatalf("render accepted malformed value %q", tc.set)
			}
		})
	}
}

func renderMCPValues(t *testing.T, values string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp-values.yaml")
	if err := os.WriteFile(path, []byte(values), 0o600); err != nil {
		t.Fatalf("write MCP values: %v", err)
	}
	return helm(t, append(productionArgs(), "-f", path)...)
}

func configMapFromRender(t *testing.T, rendered, name string) *corev1.ConfigMap {
	t.Helper()
	for _, document := range strings.Split(rendered, "\n---") {
		var cm corev1.ConfigMap
		if err := yaml.Unmarshal([]byte(document), &cm); err == nil && cm.Kind == "ConfigMap" && cm.Name == name {
			return &cm
		}
	}
	t.Fatalf("rendered chart has no ConfigMap %q", name)
	return nil
}

func TestMecak8sHelmChart_MCPDefaultsAreEmpty(t *testing.T) {
	rendered, err := helm(t, productionArgs()...)
	if err != nil {
		t.Fatalf("render defaults: %v", err)
	}
	for _, forbidden := range []string{"--mcp-server", "--permission-config=/etc/mecatl-mcp/settings.yaml", "MCP_", "MECATL_MCP_", "mcp-profile", "-mcp\n"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("default render unexpectedly contains MCP output %q", forbidden)
		}
	}
}

func TestMecak8sHelmChart_MCPStaticBearerNoneAndInsecureHTTP(t *testing.T) {
	rendered, err := renderMCPValues(t, `
mcp:
  servers:
    - name: public
      url: https://public.example/mcp
      auth: {mode: none}
    - name: internal_api
      url: http://mcp.mecatl.svc.cluster.local/mcp
      insecureHTTP: true
      auth:
        mode: staticBearer
        staticBearer:
          secretKeyRef: {name: internal-mcp, key: bearer-token}
`)
	if err != nil {
		t.Fatalf("render MCP static/no-auth values: %v", err)
	}
	container := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0]
	for _, want := range []string{
		"--mcp-server=public=https://public.example/mcp",
		"--mcp-server=internal_api=http://mcp.mecatl.svc.cluster.local/mcp",
		"--mcp-server-insecure-http=internal_api",
	} {
		if !slices.Contains(container.Args, want) {
			t.Fatalf("MCP args missing %q: %#v", want, container.Args)
		}
	}
	if len(container.Env) != 1 || container.Env[0].Name != "MCP_INTERNAL_API_TOKEN" ||
		container.Env[0].Value != "" || container.Env[0].ValueFrom == nil ||
		container.Env[0].ValueFrom.SecretKeyRef == nil ||
		container.Env[0].ValueFrom.SecretKeyRef.Name != "internal-mcp" ||
		container.Env[0].ValueFrom.SecretKeyRef.Key != "bearer-token" {
		t.Fatalf("static bearer environment = %#v", container.Env)
	}
	if strings.Contains(rendered, "kind: ConfigMap") || strings.Contains(rendered, "--permission-config=/etc/mecatl-mcp/settings.yaml") {
		t.Fatal("static/no-auth MCP render unexpectedly created an OAuth profile")
	}
}

func TestMecak8sHelmChart_MCPNoAuthDoesNotRenderEnv(t *testing.T) {
	rendered, err := renderMCPValues(t, `
mcp:
  servers:
    - name: public
      url: https://public.example/mcp
      auth: {mode: none}
`)
	if err != nil {
		t.Fatalf("render no-auth MCP values: %v", err)
	}
	container := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0]
	if len(container.Env) != 0 {
		t.Fatalf("no-auth MCP environment = %#v, want empty", container.Env)
	}
	if strings.Contains(rendered, "\n          env:") {
		t.Fatal("no-auth MCP render unexpectedly contains an env block")
	}
}

func TestMecak8sHelmChart_MCPRuntimeOwnsLoopbackClassification(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1:9090/mcp",
		"http://LOCALHOST:9090/mcp",
		"http://[::1]:9090/mcp",
		"http://[0:0:0:0:0:0:0:1]:9090/mcp",
		"http://[0::1]:9090/mcp",
		"http://[::ffff:127.0.0.1]:9090/mcp",
		"http://[::ffff:7f00:1]:9090/mcp",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if err := mcpadapter.ValidateClientURL(rawURL); err != nil {
				t.Fatalf("runtime does not recognize fixture as loopback: %v", err)
			}
			values := fmt.Sprintf(`
mcp:
  servers:
    - name: local
      url: %s
      auth:
        mode: staticBearer
        staticBearer:
          secretKeyRef: {name: local-mcp, key: token}
`, rawURL)
			rendered, err := renderMCPValues(t, values)
			if err != nil {
				t.Fatalf("chart rejected URL accepted by the runtime: %v", err)
			}
			args := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0].Args
			if !slices.Contains(args, "--mcp-server=local="+rawURL) {
				t.Fatalf("loopback MCP server arg missing: %#v", args)
			}
			if slices.Contains(args, "--mcp-server-insecure-http=local") {
				t.Fatalf("loopback MCP server rendered an unrequested insecure acknowledgement: %#v", args)
			}
		})
	}
}

func TestMecak8sHelmChart_MCPLegacyURLSemanticsAreRuntimeValidated(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		insecure    bool
		wantRuntime bool
	}{
		{name: "off-host HTTP bearer", url: "http://mcp.example/mcp"},
		{name: "stale HTTPS acknowledgement", url: "https://mcp.example/mcp", insecure: true},
		{name: "stale loopback acknowledgement", url: "http://[::ffff:7f00:1]/mcp", insecure: true},
		{name: "acknowledged off-host HTTP bearer", url: "http://mcp.example/mcp", insecure: true, wantRuntime: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ack := ""
			if tc.insecure {
				ack = "\n      insecureHTTP: true"
			}
			rendered, err := renderMCPValues(t, fmt.Sprintf(`
mcp:
  servers:
    - name: protected
      url: %s%s
      auth:
        mode: staticBearer
        staticBearer:
          secretKeyRef: {name: protected-mcp, key: token}
`, tc.url, ack))
			if err != nil {
				t.Fatalf("chart rejected structurally valid MCP values: %v", err)
			}

			fs := flag.NewFlagSet("mcp-runtime", flag.ContinueOnError)
			servers := cliconfig.RegisterMCPServerFlag(fs, "")
			var mcpArgs []string
			for _, arg := range deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0].Args {
				if strings.HasPrefix(arg, "--mcp-server=") || strings.HasPrefix(arg, "--mcp-server-insecure-http=") {
					mcpArgs = append(mcpArgs, arg)
				}
			}
			if err := fs.Parse(mcpArgs); err != nil {
				t.Fatalf("parse rendered MCP args: %v", err)
			}
			t.Setenv("MCP_PROTECTED_TOKEN", "secret")
			err = servers.Finalize()
			if tc.wantRuntime && err != nil {
				t.Fatalf("runtime rejected rendered MCP args: %v", err)
			}
			if !tc.wantRuntime && err == nil {
				t.Fatal("runtime accepted semantically invalid rendered MCP args")
			}
		})
	}
}

func runtimeMCPProfileValidationError(t *testing.T, profile string) error {
	t.Helper()
	if err := permconfig.ValidateYAML([]byte(profile)); err != nil {
		return err
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatalf("write generated MCP profile: %v", err)
	}
	operator := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}}).OperatorMCP()
	credential := base64.StdEncoding.EncodeToString([]byte("opaque-credential-record"))
	profiles, err := cliconfig.LoadMCPProfiles(cliconfig.MCPProfileLoadOptions{
		Operator: operator,
		LookupEnv: func(name string) (string, bool) {
			values := map[string]string{
				"MECATL_MCP_OAUTH_CLIENT_SECRET": "client-secret",
				"MECATL_MCP_OAUTH_CREDENTIAL":    credential,
			}
			value, ok := values[name]
			return value, ok
		},
	})
	if profiles != nil {
		t.Cleanup(func() { _ = profiles.Close() })
	}
	return err
}

func runtimeMCPProfilesFromConfigMap(t *testing.T, profile string) *cliconfig.MCPProfiles {
	t.Helper()
	if err := permconfig.ValidateYAML([]byte(profile)); err != nil {
		t.Fatalf("generated MCP profile fails the runtime settings parser: %v\n%s", err, profile)
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatalf("write generated MCP profile: %v", err)
	}
	resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	operator := resolver.OperatorMCP()
	if operator == nil {
		t.Fatal("runtime settings resolver did not retain generated MCP profile")
	}
	credential := base64.StdEncoding.EncodeToString([]byte("opaque-credential-record"))
	values := map[string]string{
		"MECATL_MCP_OAUTH_REGISTERED_CLIENT_SECRET": "client-secret",
		"MECATL_MCP_OAUTH_REGISTERED_CREDENTIAL":    credential,
		"MECATL_MCP_OAUTH_CIMD_CREDENTIAL":          credential,
	}
	profiles, err := cliconfig.LoadMCPProfiles(cliconfig.MCPProfileLoadOptions{
		Operator: operator,
		LookupEnv: func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		},
	})
	if err != nil {
		t.Fatalf("load generated MCP profile through runtime loader: %v", err)
	}
	t.Cleanup(func() {
		if err := profiles.Close(); err != nil {
			t.Errorf("close runtime MCP profiles: %v", err)
		}
	})
	return profiles
}

func TestMecak8sHelmChart_MCPOAuthProfileModes(t *testing.T) {
	rendered, err := renderMCPValues(t, `
mcp:
  servers:
    - name: oauth_registered
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: cluster
          principal: service-account:mecak8s
          issuer: https://issuer.example
          client:
            mode: preregistered
            preregistered:
              id: mecak8s
              secretKeyRef: {name: oauth-registered, key: client-secret}
          scopes: [mcp.read, mcp.write]
          requestRefreshToken: true
          credentials:
            secretKeyRef: {name: oauth-registered, key: credential-record}
            allowProcessLocalRefresh: true
          network: {additionalOrigins: [], privateOrigins: [], maxRedirects: 2}
    - name: oauth_cimd
      url: https://other.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: workload
          principal: workload:mecak8s
          issuer: https://login.example
          client:
            mode: cimd
            cimd: {documentURL: https://client.example/mecatl.json}
          scopes: [mcp.read]
          credentials:
            secretKeyRef: {name: oauth-cimd, key: credential-record}
          network:
            additionalOrigins: [https://client.example]
            privateOrigins: []
            maxRedirects: 0
`)
	if err != nil {
		t.Fatalf("render OAuth MCP values: %v", err)
	}
	deployment := deploymentFromRender(t, rendered)
	container := deployment.Spec.Template.Spec.Containers[0]
	if !slices.Contains(container.Args, "--permission-config=/etc/mecatl-mcp/settings.yaml") {
		t.Fatal("OAuth render missing chart-managed --permission-config")
	}
	if slices.ContainsFunc(container.Args, func(arg string) bool { return strings.HasPrefix(arg, "--mcp-server=") }) {
		t.Fatal("OAuth entries must come only from the strict profile, not legacy flags")
	}
	wantEnv := map[string]string{
		"MECATL_MCP_OAUTH_REGISTERED_CLIENT_SECRET": "oauth-registered/client-secret",
		"MECATL_MCP_OAUTH_REGISTERED_CREDENTIAL":    "oauth-registered/credential-record",
		"MECATL_MCP_OAUTH_CIMD_CREDENTIAL":          "oauth-cimd/credential-record",
	}
	for _, env := range container.Env {
		if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("OAuth env is not SecretKeyRef-only: %#v", env)
		}
		got := env.ValueFrom.SecretKeyRef.Name + "/" + env.ValueFrom.SecretKeyRef.Key
		if wantEnv[env.Name] != got {
			t.Fatalf("OAuth env %s = %q, want %q", env.Name, got, wantEnv[env.Name])
		}
		delete(wantEnv, env.Name)
	}
	if len(wantEnv) != 0 {
		t.Fatalf("missing OAuth environment variables: %#v", wantEnv)
	}
	cm := configMapFromRender(t, rendered, "production-mecak8s-mcp")
	profile := cm.Data["settings.yaml"]
	profiles := runtimeMCPProfilesFromConfigMap(t, profile)
	if len(profiles.Servers) != 2 {
		t.Fatalf("runtime MCP servers = %#v, want two", profiles.Servers)
	}
	registered, cimd := profiles.Servers[0], profiles.Servers[1]
	if registered.Name != "oauth_registered" || registered.URL != "https://mcp.example/mcp" || registered.OAuth == nil ||
		registered.OAuth.Subject.Profile != "cluster" || registered.OAuth.Subject.Principal != "service-account:mecak8s" ||
		registered.OAuth.Issuer != "https://issuer.example" || registered.OAuth.Client.Preregistered == nil ||
		registered.OAuth.Client.Preregistered.ClientID != "mecak8s" || !slices.Equal(registered.OAuth.AllowedScopes, []string{"mcp.read", "mcp.write"}) ||
		!registered.OAuth.RequestRefreshToken || !registered.OAuth.AllowInMemoryRefresh || registered.OAuth.Network.MaxRedirects != 2 ||
		registered.OAuth.CredentialReader == nil {
		t.Fatalf("runtime preregistered OAuth profile lost fields: %#v", registered)
	}
	if cimd.Name != "oauth_cimd" || cimd.URL != "https://other.example/mcp" || cimd.OAuth == nil ||
		cimd.OAuth.Subject.Profile != "workload" || cimd.OAuth.Subject.Principal != "workload:mecak8s" ||
		cimd.OAuth.Issuer != "https://login.example" || cimd.OAuth.Client.ClientIDMetadataDocumentURL != "https://client.example/mecatl.json" ||
		!slices.Equal(cimd.OAuth.AllowedScopes, []string{"mcp.read"}) || cimd.OAuth.RequestRefreshToken || cimd.OAuth.AllowInMemoryRefresh ||
		!slices.Equal(cimd.OAuth.Network.AdditionalOrigins, []string{"https://client.example"}) || len(cimd.OAuth.Network.PrivateOrigins) != 0 ||
		cimd.OAuth.Network.MaxRedirects != 0 || cimd.OAuth.CredentialReader == nil {
		t.Fatalf("runtime CIMD OAuth profile lost fields: %#v", cimd)
	}
	for _, want := range []string{
		"mode: oauth", "mode: preregistered", "mode: cimd", "mode: environment",
		"secret_env: MECATL_MCP_OAUTH_REGISTERED_CLIENT_SECRET",
		"credential_env: MECATL_MCP_OAUTH_CIMD_CREDENTIAL",
		"allow_process_local_refresh: true", `document_url: "https://client.example/mecatl.json"`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("OAuth profile missing %q:\n%s", want, profile)
		}
	}
	for _, forbidden := range []string{"oauth-registered", "oauth-cimd", "client-secret", "credential-record", "static_bearer", "mode: local", "root:"} {
		if strings.Contains(profile, forbidden) {
			t.Fatalf("OAuth profile leaks or enables forbidden field %q:\n%s", forbidden, profile)
		}
	}
	if len(container.VolumeMounts) == 0 || !slices.ContainsFunc(container.VolumeMounts, func(m corev1.VolumeMount) bool {
		return m.Name == "mcp-profile" && m.MountPath == "/etc/mecatl-mcp" && m.ReadOnly
	}) {
		t.Fatal("OAuth profile is not mounted read-only")
	}
	if !slices.ContainsFunc(deployment.Spec.Template.Spec.Volumes, func(volume corev1.Volume) bool {
		return volume.Name == "mcp-profile" && volume.ConfigMap != nil && volume.ConfigMap.Name == cm.Name &&
			slices.ContainsFunc(volume.ConfigMap.Items, func(item corev1.KeyToPath) bool {
				return item.Key == "settings.yaml" && item.Path == "settings.yaml"
			})
	}) {
		t.Fatal("OAuth profile volume is not coupled to the rendered ConfigMap settings.yaml")
	}
	wantChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(profile)))
	if got := deployment.Spec.Template.Annotations["checksum/mcp-profile"]; got != wantChecksum {
		t.Fatalf("OAuth profile checksum = %q, want ConfigMap content checksum %q", got, wantChecksum)
	}
}

func TestMecak8sHelmChart_MCPStructuralValidation(t *testing.T) {
	validStatic := `
mcp:
  servers:
    - name: github
      url: https://mcp.example/mcp
      auth:
        mode: staticBearer
        staticBearer:
          secretKeyRef: {name: mcp-secret, key: token}
`
	validOAuth := `
mcp:
  servers:
    - name: oauth
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: cluster
          principal: service-account:mecak8s
          issuer: https://issuer.example
          client:
            mode: cimd
            cimd: {documentURL: https://client.example/mecatl.json}
          scopes: [mcp.read]
          credentials:
            secretKeyRef: {name: oauth, key: credential}
          network:
            additionalOrigins: [https://client.example]
            privateOrigins: []
            maxRedirects: 0
`
	if _, err := renderMCPValues(t, strings.Replace(validOAuth, "https://mcp.example/mcp", "https://mcp.example/MCP%2Fv1", 1)); err != nil {
		t.Fatalf("render rejected a canonical escaped OAuth resource URL: %v", err)
	}

	cases := map[string]string{
		"bad name":           strings.Replace(validStatic, "name: github", "name: bad-name", 1),
		"double underscore":  strings.Replace(validStatic, "name: github", "name: bad__name", 1),
		"missing secret ref": strings.Replace(validStatic, "secretKeyRef: {name: mcp-secret, key: token}", "secretKeyRef: {name: mcp-secret}", 1),
		"inline token":       strings.Replace(validStatic, "secretKeyRef: {name: mcp-secret, key: token}", "token: literal-secret", 1),
		"malformed union":    strings.Replace(validStatic, "staticBearer:\n", "oauth: {}\n        staticBearer:\n", 1),
		"duplicate case insensitive": validStatic + `    - name: GitHub
      url: https://other.example/mcp
      auth: {mode: none}
`,
		"generated env collision": validStatic + `extraEnv:
  - name: MCP_GITHUB_TOKEN
    value: forbidden
`,
		"oauth insecure": `
mcp:
  servers:
    - name: oauth
      url: http://mcp.example/mcp
      insecureHTTP: true
      auth:
        mode: oauth
        oauth: {}
`,
		"OAuth principal control character": strings.Replace(validOAuth, "principal: service-account:mecak8s", `principal: "service-account:\u0007mecak8s"`, 1),
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := renderMCPValues(t, values); err == nil {
				t.Fatal("render accepted invalid MCP values")
			}
		})
	}
}

func TestMecak8sHelmChart_MCPOAuthURLSemanticsAreRuntimeValidated(t *testing.T) {
	valid := `
mcp:
  servers:
    - name: oauth
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: cluster
          principal: service-account:mecak8s
          issuer: https://issuer.example
          client:
            mode: cimd
            cimd: {documentURL: https://client.example/mecatl.json}
          scopes: [mcp.read]
          credentials:
            secretKeyRef: {name: oauth, key: credential}
          network:
            additionalOrigins: [https://client.example]
            privateOrigins: []
            maxRedirects: 0
`
	cases := map[string]string{
		"HTTP resource":                strings.Replace(valid, "https://mcp.example/mcp", "http://mcp.example/mcp", 1),
		"malformed resource escape":    strings.Replace(valid, "https://mcp.example/mcp", "https://mcp.example/%zz", 1),
		"resource underscore hostname": strings.Replace(valid, "https://mcp.example/mcp", "https://mcp_bad.example/mcp", 1),
		"issuer zero port":             strings.Replace(valid, "https://issuer.example", "https://issuer.example:0", 1),
		"issuer port out of range":     strings.Replace(valid, "https://issuer.example", "https://issuer.example:65536", 1),
		"expanded IPv6 issuer":         strings.Replace(valid, "https://issuer.example", "https://[0:0:0:0:0:0:0:1]", 1),
		"padded IPv6 issuer":           strings.Replace(valid, "https://issuer.example", "https://[2001:0db8::1]", 1),
		"CIMD origin not allowed":      strings.Replace(valid, "additionalOrigins: [https://client.example]", "additionalOrigins: []", 1),
		"private origin not allowed":   strings.Replace(valid, "privateOrigins: []", "privateOrigins: [https://private.example]", 1),
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			rendered, err := renderMCPValues(t, values)
			if err != nil {
				t.Fatalf("chart rejected structurally valid OAuth values before runtime validation: %v", err)
			}
			profile := configMapFromRender(t, rendered, "production-mecak8s-mcp").Data["settings.yaml"]
			if err := runtimeMCPProfileValidationError(t, profile); err == nil {
				t.Fatalf("runtime settings parser accepted semantically invalid OAuth profile:\n%s", profile)
			}
		})
	}
}

func TestMecak8sHelmChart_MCPOAuthProfileChecksumDrivesRollout(t *testing.T) {
	values := `
mcp:
  servers:
    - name: oauth
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: cluster
          principal: service-account:mecak8s
          issuer: https://issuer.example
          client:
            mode: cimd
            cimd: {documentURL: https://issuer.example/client.json}
          scopes: [mcp.read]
          credentials:
            secretKeyRef: {name: oauth, key: credential}
          network: {additionalOrigins: [], privateOrigins: [], maxRedirects: 0}
`
	first, err := renderMCPValues(t, values)
	if err != nil {
		t.Fatalf("render first OAuth profile: %v", err)
	}
	second, err := renderMCPValues(t, strings.Replace(values, "scopes: [mcp.read]", "scopes: [mcp.read, mcp.write]", 1))
	if err != nil {
		t.Fatalf("render changed OAuth profile: %v", err)
	}
	firstSum := deploymentFromRender(t, first).Spec.Template.Annotations["checksum/mcp-profile"]
	secondSum := deploymentFromRender(t, second).Spec.Template.Annotations["checksum/mcp-profile"]
	if firstSum == "" || secondSum == "" || firstSum == secondSum {
		t.Fatalf("OAuth profile checksums = %q and %q; profile change must roll pods", firstSum, secondSum)
	}
}
