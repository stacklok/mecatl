package mecak8s_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
)

// Decode the actual process configuration, not a reconstructed agent profile.
type renderedBrokerConfig struct {
	CallbackURL string `json:"callback_url"`
	Profiles    []struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Auth  string `json:"auth"`
		OAuth struct {
			Issuer                string   `json:"issuer"`
			ClientMode            string   `json:"client_mode"`
			ClientID              string   `json:"client_id"`
			ClientSecretFile      string   `json:"client_secret_file"`
			CIMDDocumentURL       string   `json:"cimd_document_url"`
			DCRDiscoveryURL       string   `json:"dcr_discovery_url"`
			AuthorizationEndpoint string   `json:"authorization_endpoint"`
			TokenEndpoint         string   `json:"token_endpoint"`
			Scopes                []string `json:"scopes"`
			RequestRefreshToken   bool     `json:"request_refresh_token"`
		} `json:"oauth"`
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Schema      map[string]any `json:"schema"`
			ReadOnly    bool           `json:"read_only"`
		} `json:"tools"`
	} `json:"profiles"`
	WorkloadJWT struct {
		Subject             string `json:"subject"`
		Audience            string `json:"audience"`
		TrustBundleFile     string `json:"trust_bundle_file"`
		KubernetesBootstrap struct {
			DiscoveryURL string `json:"discovery_url"`
			JWKSURI      string `json:"jwks_uri"`
			TokenFile    string `json:"token_file"`
		} `json:"kubernetes_bootstrap"`
	} `json:"workload_jwt"`
	Listener struct {
		PublicAddress string `json:"public_address"`
		TLSCertFile   string `json:"tls_cert_file"`
		TLSKeyFile    string `json:"tls_key_file"`
	} `json:"listener"`
}

func agentDeploymentYAML(t *testing.T, rendered string) string {
	t.Helper()
	data, err := yaml.Marshal(deploymentFromRender(t, rendered))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func brokerConfigFromRender(t *testing.T, rendered string) renderedBrokerConfig {
	t.Helper()
	body := configMapFromRender(t, rendered, brokerDeploymentFromRender(t, rendered).Name+"-config").Data["broker.json"]
	var cfg renderedBrokerConfig
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func brokerDeploymentFromRender(t *testing.T, rendered string) *appsv1.Deployment {
	t.Helper()
	for _, document := range strings.Split(rendered, "\n---") {
		var d appsv1.Deployment
		if yaml.Unmarshal([]byte(document), &d) == nil && d.Kind == "Deployment" && d.Spec.Template.Labels["app.kubernetes.io/component"] == "broker" {
			return &d
		}
	}
	t.Fatal("render has no broker Deployment")
	return nil
}

func TestUnifiedChartReleaseStagesFreshWorkspace(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]json.RawMessage
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var job struct {
		Needs []string
		Steps []struct{ Name, Run string }
	}
	if err := json.Unmarshal(workflow.Jobs["publish-helm-chart"], &job); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(job.Needs, "publish-mecak8s") || !slices.Contains(job.Needs, "publish-mecabroker") {
		t.Fatal("chart publication must wait for both signed images")
	}
	chart, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range job.Steps {
		if step.Name != "helm package" {
			continue
		}
		// The release-PR flow (create-release-pr.yml) keeps Chart.yaml's version
		// and values.yaml's default image tag in sync with the release tag before
		// it is ever cut, so the package step packages the chart as committed —
		// no runtime digest rewrite. Execute the real staging commands in a fresh
		// workspace to prove they still produce the expected package.
		cmd := exec.Command("sh", "-eu", "-c", step.Run)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "CHART_PATH="+chart, "CHART_VERSION=0.0.0-test")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("stage release chart in fresh workspace: %v\n%s", err, output)
		}
		if _, err := os.Stat(filepath.Join(cmd.Dir, "dist", "mecak8s-0.0.0-test.tgz")); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("release has no helm package step")
}

func TestUnifiedChartIdleAndDirectRoutes(t *testing.T) {
	for _, values := range []string{"mcp: {servers: []}", `mcp:
  servers:
    - name: public
      url: https://public.example/mcp
      auth: {mode: none}
    - name: private
      url: https://private.example/mcp
      auth: {mode: staticBearer, staticBearer: {secretKeyRef: {name: upstream, key: token}}}
`} {
		rendered, err := renderMCPValues(t, values)
		if err != nil {
			t.Fatal(err)
		}
		cfg := brokerConfigFromRender(t, rendered)
		if len(cfg.Profiles) != 0 || cfg.CallbackURL != "" {
			t.Fatalf("idle broker has routes: %#v", cfg)
		}
		if strings.Count(rendered, "kind: Deployment") != 2 {
			t.Fatal("idle installation must retain exactly two workloads")
		}
		if cfg.Listener.TLSCertFile == "" || cfg.Listener.TLSKeyFile == "" || cfg.WorkloadJWT.Subject == "" {
			t.Fatal("idle broker lost TLS or workload authentication")
		}
		agent := deploymentFromRender(t, rendered)
		broker := brokerDeploymentFromRender(t, rendered)
		if broker.Spec.Replicas == nil || *broker.Spec.Replicas != 1 || broker.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
			t.Fatalf("broker spec = %#v", broker.Spec)
		}
		if agent.Spec.Template.Spec.ServiceAccountName == broker.Spec.Template.Spec.ServiceAccountName {
			t.Fatal("shared workload identity")
		}
		args := agent.Spec.Template.Spec.Containers[0].Args
		if slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "--mcp-broker-") }) {
			t.Fatalf("idle agent connected to broker: %v", args)
		}
		if strings.Contains(values, "staticBearer") && (!slices.Contains(args, "--mcp-server=public=https://public.example/mcp") || !slices.Contains(args, "--mcp-server=private=https://private.example/mcp")) {
			t.Fatalf("direct routes lost: %v", args)
		}
	}
}

func TestUnifiedChartMixedOAuthNoneAndSecretIsolation(t *testing.T) {
	values, err := os.ReadFile("ci/broker-mcp-values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderMCPValues(t, string(values)+`    - name: public
      url: https://public.example/mcp
      auth: {mode: none}
`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := brokerConfigFromRender(t, rendered)
	if len(cfg.Profiles) != 2 || cfg.Profiles[0].Auth != "oauth" || cfg.Profiles[1].Auth != "none" {
		t.Fatalf("mixed routes = %#v", cfg.Profiles)
	}
	agent := deploymentFromRender(t, rendered)
	if strings.Contains(agentDeploymentYAML(t, rendered), "github-oauth") {
		t.Fatal("OAuth secret exposed to agent")
	}
	if slices.ContainsFunc(agent.Spec.Template.Spec.Containers[0].Args, func(arg string) bool { return strings.HasPrefix(arg, "--mcp-server=") }) {
		t.Fatal("mixed routes split across authorities")
	}
	broker := brokerDeploymentFromRender(t, rendered)
	var found bool
	for _, v := range broker.Spec.Template.Spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == "github-oauth" {
			found = true
		}
	}
	if !found || cfg.Profiles[0].OAuth.ClientSecretFile != "/var/run/mecabroker/mcp-oauth/0/client-secret" {
		t.Fatal("broker missing OAuth secret projection")
	}
	if cfg.WorkloadJWT.Subject != "system:serviceaccount:default:"+agent.Spec.Template.Spec.ServiceAccountName {
		t.Fatalf("workload subject = %q", cfg.WorkloadJWT.Subject)
	}
}

func TestUnifiedChartBoundedNamesAndBootstrapRBAC(t *testing.T) {
	var previousRole string
	for _, namespace := range []string{"tenant-one", "tenant-two"} {
		rendered, err := helm(t, "template", "production", ".", "-f", "ci/broker-mcp-values.yaml", "--namespace", namespace, "--set", "fullnameOverride="+strings.Repeat("a", 63))
		if err != nil {
			t.Fatal(err, rendered)
		}
		agent := deploymentFromRender(t, rendered)
		broker := brokerDeploymentFromRender(t, rendered)
		cfg := brokerConfigFromRender(t, rendered)
		if cfg.WorkloadJWT.Subject != "system:serviceaccount:"+namespace+":"+agent.Spec.Template.Spec.ServiceAccountName {
			t.Fatal("subject did not follow exact agent SA")
		}
		var role rbacv1.ClusterRole
		var binding rbacv1.ClusterRoleBinding
		for _, doc := range strings.Split(rendered, "\n---") {
			var meta corev1.ConfigMap
			if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
				t.Fatal(err)
			}
			if meta.Labels["app.kubernetes.io/component"] != "broker" {
				continue
			}
			if len(meta.Name) > 63 {
				t.Fatalf("overlong broker name: %s", meta.Name)
			}
			switch meta.Kind {
			case "ClusterRole":
				if err := yaml.Unmarshal([]byte(doc), &role); err != nil {
					t.Fatal(err)
				}
			case "ClusterRoleBinding":
				if err := yaml.Unmarshal([]byte(doc), &binding); err != nil {
					t.Fatal(err)
				}
			}
		}
		if len(role.Rules) != 1 || !slices.Equal(role.Rules[0].Verbs, []string{"get"}) || !slices.Equal(role.Rules[0].NonResourceURLs, []string{"/.well-known/openid-configuration", "/openid/v1/jwks"}) || len(role.Rules[0].Resources) != 0 {
			t.Fatalf("discovery privileges = %#v", role.Rules)
		}
		if role.Name == previousRole || binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 || binding.Subjects[0].Name != broker.Spec.Template.Spec.ServiceAccountName || binding.Subjects[0].Namespace != namespace {
			t.Fatalf("bootstrap binding = %#v", binding)
		}
		previousRole = role.Name
	}
}

func TestUnifiedChartRejectsObsoleteSurfacesAndUnsupportedOAuth(t *testing.T) {
	for _, setting := range []string{"broker.enabled=false", "installation.mode=external", "remoteBroker.address=elsewhere:8443", "broker.replicaCount=2", "broker.profiles[0].name=shadow", "broker.tls.secretName=", "broker.clientCA.key=token"} {
		if rendered, err := helm(t, "template", "production", ".", "-f", "ci/broker-mcp-values.yaml", "--set", setting); err == nil {
			t.Fatalf("accepted %s:\n%s", setting, rendered)
		}
	}
	for _, setting := range []string{"mcp.servers[0].auth.oauth.network.maxRedirects=1", "mcp.servers[0].auth.oauth.network.privateOrigins[0]=https://private.example", "mcp.servers[0].auth.oauth.network.additionalOrigins[0]=https://extra.example"} {
		if rendered, err := helm(t, "template", "production", ".", "-f", "ci/broker-mcp-values.yaml", "--set", setting); err == nil || !strings.Contains(rendered, "network is unsupported") {
			t.Fatalf("unsupported network not rejected: %v\n%s", err, rendered)
		}
	}
}

func TestUnifiedChartCIMDRendersStandaloneOAuthMapping(t *testing.T) {
	for _, tc := range []struct {
		name       string
		oauth      string
		wantIssuer string
		wantAuth   string
		wantToken  string
	}{
		{
			name:       "issuer discovery",
			oauth:      "issuer: https://issuer.example\n          client: {mode: cimd, cimd: {documentURL: https://issuer.example/client.json}}",
			wantIssuer: "https://issuer.example",
		},
		{
			name: "explicit OAuth2 endpoints",
			oauth: `upstream:
            mode: oauth2
            oauth2:
              authorizationEndpoint: https://issuer.example/authorize
              tokenEndpoint: https://issuer.example/token
          client: {mode: cimd, cimd: {documentURL: https://issuer.example/client.json}}`,
			wantAuth:  "https://issuer.example/authorize",
			wantToken: "https://issuer.example/token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := renderOAuthMCPValues(t, "mcp:\n  broker: {callbackURL: https://broker.example/callback}\n  servers:\n    - name: cimd\n      url: https://issuer.example/mcp\n      auth:\n        mode: oauth\n        oauth:\n          "+tc.oauth+"\n          scopes: [read]\n          network: {additionalOrigins: [], privateOrigins: [], maxRedirects: 0}\n")
			if err != nil {
				t.Fatalf("render CIMD configuration: %v\n%s", err, rendered)
			}
			oauth := brokerConfigFromRender(t, rendered).Profiles[0].OAuth
			if oauth.ClientMode != "cimd" || oauth.CIMDDocumentURL != "https://issuer.example/client.json" || oauth.ClientID != "" || oauth.ClientSecretFile != "" || oauth.Issuer != tc.wantIssuer || oauth.AuthorizationEndpoint != tc.wantAuth || oauth.TokenEndpoint != tc.wantToken {
				t.Fatalf("CIMD broker configuration = %#v", oauth)
			}
		})
	}
}

func TestUnifiedChartImagesAndOperatorTLS(t *testing.T) {
	chartData, err := os.ReadFile("Chart.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		Version      string `json:"version"`
		Dependencies []any  `json:"dependencies"`
	}
	if err := yaml.Unmarshal(chartData, &chart); err != nil {
		t.Fatal(err)
	}
	if len(chart.Dependencies) != 0 {
		t.Fatal("chart has child dependencies")
	}
	rendered, err := helm(t, "template", "production", ".", "-f", "ci/broker-mcp-values.yaml", "--set", "image.tag=", "--set", "image.digest=", "--set", "broker.image.digest=")
	if err != nil {
		t.Fatal(err, rendered)
	}
	agent, broker := deploymentFromRender(t, rendered), brokerDeploymentFromRender(t, rendered)
	for _, d := range []*appsv1.Deployment{agent, broker} {
		if !strings.HasSuffix(d.Spec.Template.Spec.Containers[0].Image, ":v"+chart.Version) {
			t.Fatalf("image fallback = %s", d.Spec.Template.Spec.Containers[0].Image)
		}
	}
	cfg := brokerConfigFromRender(t, rendered)
	if cfg.Listener.TLSCertFile != "/var/run/mecabroker/tls/tls.crt" || cfg.Listener.TLSKeyFile != "/var/run/mecabroker/tls/tls.key" {
		t.Fatalf("listener TLS = %#v", cfg.Listener)
	}
	if broker.Spec.Template.Spec.AutomountServiceAccountToken == nil || *broker.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("broker automounts ambient token")
	}
	c := broker.Spec.Template.Spec.Containers[0]
	if c.ReadinessProbe == nil || c.ReadinessProbe.Exec == nil || !slices.Equal(c.ReadinessProbe.Exec.Command, []string{"/ko-app/mecabroker", "ready"}) || c.Lifecycle == nil || c.Lifecycle.PreStop == nil || c.Lifecycle.PreStop.Exec == nil || !slices.Equal(c.Lifecycle.PreStop.Exec.Command, []string{"/ko-app/mecabroker", "drain"}) {
		t.Fatal("broker readiness/drain contract missing")
	}
	var tlsSecret bool
	for _, v := range broker.Spec.Template.Spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == "mecabroker-tls" {
			tlsSecret = true
		}
	}
	if !tlsSecret {
		t.Fatal("operator TLS Secret missing")
	}
	for _, doc := range strings.Split(rendered, "\n---") {
		var meta corev1.ConfigMap
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Kind == "Secret" || meta.Kind == "Certificate" || meta.Kind == "Issuer" {
			t.Fatalf("chart generates PKI: %s", meta.Kind)
		}
	}
}

// Same URL validator used by cmd/mecabroker before it opens any listener.
func brokerURLValidationError(cfg renderedBrokerConfig) error {
	if cfg.CallbackURL != "" {
		if err := mcpbroker.ValidateProtectedURL(cfg.CallbackURL, "callback"); err != nil {
			return err
		}
	}
	for _, p := range cfg.Profiles {
		if p.Auth != "oauth" {
			continue
		}
		for _, u := range []string{p.URL, p.OAuth.Issuer, p.OAuth.AuthorizationEndpoint, p.OAuth.TokenEndpoint, p.OAuth.DCRDiscoveryURL} {
			if u != "" {
				if err := mcpbroker.ValidateProtectedURL(u, "OAuth URL"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
