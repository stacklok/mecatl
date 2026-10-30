package mecak8s_idbroker

import (
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The profile's names, ports, and identities live in four files that must
// agree: kind-config.yaml, the chart overlay, dex.yaml, and the Taskfile.

type taskfile struct {
	Vars  map[string]string `json:"vars"`
	Tasks map[string]struct {
		Cmds []any `json:"cmds"`
	} `json:"tasks"`
}

type chartValues struct {
	Service struct {
		Type      string `json:"type"`
		GRPCPort  int    `json:"grpcPort"`
		HTTPPort  int    `json:"httpPort"`
		NodePorts struct {
			GRPC int `json:"grpc"`
			HTTP int `json:"http"`
		} `json:"nodePorts"`
	} `json:"service"`
	OIDC struct {
		Audience string `json:"audience"`
		Resource string `json:"resource"`
	} `json:"oidc"`
	IdentityBroker struct {
		AS1 struct {
			Issuer      string `json:"issuer"`
			TUIClientID string `json:"tuiClientID"`
			Upstream    struct {
				Issuer       string `json:"issuer"`
				ClientID     string `json:"clientID"`
				ClientSecret struct {
					SecretName string `json:"secretName"`
					Key        string `json:"key"`
				} `json:"clientSecret"`
			} `json:"upstream"`
		} `json:"as1"`
	} `json:"identityBroker"`
}

type dexConfig struct {
	Issuer string `json:"issuer"`
	Web    struct {
		HTTPS string `json:"https"`
	} `json:"web"`
	StaticClients []struct {
		ID           string   `json:"id"`
		SecretEnv    string   `json:"secretEnv"`
		RedirectURIs []string `json:"redirectURIs"`
	} `json:"staticClients"`
}

const valuesFile = "../helm/mecak8s/values-kind-idbroker.yaml"

func readYAML(t *testing.T, path string, into any) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(body, into); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func loadTaskfile(t *testing.T) taskfile {
	t.Helper()
	var tf taskfile
	readYAML(t, "Taskfile.yml", &tf)
	return tf
}

var templateVar = regexp.MustCompile(`\{\{\.([A-Z0-9_]+)\}\}`)

// taskCommands returns task's shell commands with the Taskfile's vars expanded.
func taskCommands(t *testing.T, tf taskfile, task string) string {
	t.Helper()
	def, ok := tf.Tasks[task]
	if !ok {
		t.Fatalf("Taskfile has no %q task", task)
	}
	var out strings.Builder
	for _, c := range def.Cmds {
		switch c := c.(type) {
		case string:
			out.WriteString(c + "\n")
		case map[string]any:
			if s, ok := c["cmd"].(string); ok {
				out.WriteString(s + "\n")
			}
		}
	}
	return expand(tf, out.String())
}

func expand(tf taskfile, s string) string {
	for range 3 {
		s = templateVar.ReplaceAllStringFunc(s, func(m string) string {
			if v, ok := tf.Vars[templateVar.FindStringSubmatch(m)[1]]; ok {
				return v
			}
			return m
		})
	}
	return s
}

func dexDocs(t *testing.T) (dexConfig, map[string]any, string) {
	t.Helper()
	body, err := os.ReadFile("dex.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg dexConfig
	var service map[string]any
	for _, doc := range strings.Split(string(body), "\n---\n") {
		var obj struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("parse dex.yaml: %v", err)
		}
		switch obj.Kind {
		case "ConfigMap":
			if err := yaml.Unmarshal([]byte(obj.Data["config.yaml"]), &cfg); err != nil {
				t.Fatalf("parse Dex config: %v", err)
			}
		case "Service":
			if err := yaml.Unmarshal([]byte(doc), &service); err != nil {
				t.Fatal(err)
			}
		}
	}
	if cfg.Issuer == "" || service == nil {
		t.Fatal("dex.yaml lacks the Dex ConfigMap or Service")
	}
	return cfg, service, string(body)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		t.Fatalf("%q is not an absolute https URL", raw)
	}
	return u
}

// TestPortsAndHostsAgree pins that every host port maps to the NodePort of a
// Service whose port equals the host port, under host names that are the
// in-cluster Service names the certificates and hosts aliases carry.
func TestPortsAndHostsAgree(t *testing.T) {
	tf := loadTaskfile(t)
	var values chartValues
	readYAML(t, valuesFile, &values)
	var kind struct {
		Name  string `json:"name"`
		Nodes []struct {
			Role     string `json:"role"`
			Mappings []struct {
				ContainerPort int    `json:"containerPort"`
				HostPort      int    `json:"hostPort"`
				ListenAddress string `json:"listenAddress"`
			} `json:"extraPortMappings"`
		} `json:"nodes"`
	}
	readYAML(t, "kind-config.yaml", &kind)
	if kind.Name != tf.Vars["CLUSTER"] || tf.Vars["CLUSTER"] != "mecatl-idbroker" || tf.Vars["CONTEXT"] != "kind-mecatl-idbroker" {
		t.Fatalf("cluster names disagree: kind %q, Taskfile %q/%q", kind.Name, tf.Vars["CLUSTER"], tf.Vars["CONTEXT"])
	}
	dex, dexService, _ := dexDocs(t)
	dexPorts := dexService["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)

	want := map[int]int{ // NodePort -> host port
		values.Service.NodePorts.GRPC:       values.Service.GRPCPort,
		values.Service.NodePorts.HTTP:       values.Service.HTTPPort,
		int(dexPorts["nodePort"].(float64)): int(dexPorts["port"].(float64)),
	}
	if values.Service.GRPCPort != 18090 || values.Service.HTTPPort != 18091 || dexPorts["port"].(float64) != 5557 {
		t.Fatalf("Service ports = %d/%d/%v, want 18090/18091/5557", values.Service.GRPCPort, values.Service.HTTPPort, dexPorts["port"])
	}
	if values.Service.Type != "NodePort" || dexService["spec"].(map[string]any)["type"] != "NodePort" {
		t.Fatal("mecak8s and Dex Services must be NodePort")
	}
	var got int
	for _, node := range kind.Nodes {
		for _, m := range node.Mappings {
			got++
			if node.Role != "control-plane" || m.ListenAddress != "127.0.0.1" || want[m.ContainerPort] != m.HostPort {
				t.Errorf("unexpected Kind mapping %+v on %s; want NodePort->host %v on 127.0.0.1", m, node.Role, want)
			}
		}
	}
	if got != len(want) {
		t.Fatalf("Kind has %d port mappings, want %d", got, len(want))
	}

	mecak8sHost, dexHost := tf.Vars["MECAK8S_HOST"], tf.Vars["DEX_HOST"]
	ns := tf.Vars["NAMESPACE"]
	if mecak8sHost != tf.Vars["RELEASE"]+"-mecak8s."+ns+".svc.cluster.local" || dexHost != "dex."+ns+".svc.cluster.local" {
		t.Fatalf("host names %q, %q are not the in-cluster Service names in %s", mecak8sHost, dexHost, ns)
	}
	resource := mustURL(t, values.OIDC.Resource)
	if resource.Host != mecak8sHost+":18091" {
		t.Errorf("oidc.resource host = %q, want %s:18091", resource.Host, mecak8sHost)
	}
	if dex.Issuer != values.IdentityBroker.AS1.Upstream.Issuer || mustURL(t, dex.Issuer).Host != dexHost+":5557" {
		t.Errorf("Dex issuer %q, upstream issuer %q, want https://%s:5557", dex.Issuer, values.IdentityBroker.AS1.Upstream.Issuer, dexHost)
	}
	if dex.Web.HTTPS != "0.0.0.0:5557" {
		t.Errorf("Dex listens on %q, want 0.0.0.0:5557", dex.Web.HTTPS)
	}

	certs, err := os.ReadFile("fixture-tls.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"- " + mecak8sHost, "- " + dexHost} {
		if !strings.Contains(string(certs), name) {
			t.Errorf("fixture-tls.yaml has no certificate name %q", name)
		}
	}
	hosts := taskCommands(t, tf, "hosts-add") + taskCommands(t, tf, "hosts-remove")
	for _, want := range []string{
		"for host in " + mecak8sHost + " " + dexHost, `127.0.0.1 $host # mecatl-idbroker`, "grep -Fqx", "sudo sh -c", "sudo sed -i.bak",
		`127\.0\.0\.1 mecak8s-mecak8s\.mecatl-idbroker\.svc\.cluster\.local # mecatl-idbroker$`,
		`127\.0\.0\.1 dex\.mecatl-idbroker\.svc\.cluster\.local # mecatl-idbroker$`,
	} {
		if !strings.Contains(hosts, want) {
			t.Errorf("hosts tasks missing %q", want)
		}
	}
	ca := taskCommands(t, tf, "ca-refresh")
	for _, want := range []string{"wait_port mecak8s-gRPC 18090", "wait_port mecak8s-HTTPS 18091", "wait_port Dex 5557"} {
		if !strings.Contains(ca, want) {
			t.Errorf("ca-refresh missing %q", want)
		}
	}
}

// TestIdentitiesAgree pins AS-1's issuer, audience, and clients across the
// chart overlay, Dex, and the login task.
func TestIdentitiesAgree(t *testing.T) {
	tf := loadTaskfile(t)
	var values chartValues
	readYAML(t, valuesFile, &values)
	as1 := values.IdentityBroker.AS1
	resource := values.OIDC.Resource

	if as1.Issuer != resource+"/as1" {
		t.Errorf("AS-1 issuer %q is not under oidc.resource %q", as1.Issuer, resource)
	}
	// AS-1 accepts only an absolute http(s) URL as the audience.
	if aud := mustURL(t, values.OIDC.Audience); values.OIDC.Audience != resource+"/api" || aud.Path != "/api" {
		t.Errorf("oidc.audience = %q, want %s/api", values.OIDC.Audience, resource)
	}

	dex, _, dexText := dexDocs(t)
	if len(dex.StaticClients) != 1 {
		t.Fatalf("Dex has %d static clients, want AS-1's alone", len(dex.StaticClients))
	}
	client := dex.StaticClients[0]
	if client.ID != as1.Upstream.ClientID {
		t.Errorf("Dex client %q, AS-1 upstream client %q", client.ID, as1.Upstream.ClientID)
	}
	if len(client.RedirectURIs) != 1 || client.RedirectURIs[0] != as1.Issuer+"/oauth/callback" {
		t.Errorf("Dex redirect URIs = %v, want [%s/oauth/callback]", client.RedirectURIs, as1.Issuer)
	}
	// Dex and mecak8s read the client secret from the same generated Secret.
	secretRef := "name: " + client.SecretEnv + "\n              valueFrom:\n                secretKeyRef:\n                  name: " +
		as1.Upstream.ClientSecret.SecretName + "\n                  key: " + as1.Upstream.ClientSecret.Key
	if client.SecretEnv == "" || !strings.Contains(dexText, secretRef) {
		t.Errorf("Dex does not read its client secret from Secret %s/%s", as1.Upstream.ClientSecret.SecretName, as1.Upstream.ClientSecret.Key)
	}

	if tf.Vars["AS1_ISSUER"] != as1.Issuer || tf.Vars["AS1_AUDIENCE"] != values.OIDC.Audience || tf.Vars["TUI_CLIENT"] != as1.TUIClientID {
		t.Fatalf("Taskfile identity %q/%q/%q differs from values %q/%q/%q",
			tf.Vars["AS1_ISSUER"], tf.Vars["AS1_AUDIENCE"], tf.Vars["TUI_CLIENT"], as1.Issuer, values.OIDC.Audience, as1.TUIClientID)
	}
	host := tf.Vars["MECAK8S_HOST"]
	ca := tf.Vars["STATE"] + "/fixture-ca.crt"
	login := taskCommands(t, tf, "login")
	wantLogin := "./bin/mecatui login " + host + ":18090 --issuer " + as1.Issuer + " --client-id " + as1.TUIClientID +
		" --audience " + values.OIDC.Audience + " --tls-ca " + ca + " --private-issuer --scopes openid,profile,offline_access"
	if !strings.Contains(login, wantLogin) {
		t.Errorf("login task does not run\n  %s\ngot:\n%s", wantLogin, login)
	}
	if connect := taskCommands(t, tf, "connect"); !strings.Contains(connect, "./bin/mecatui connect "+host+":18090 --tls --tls-ca "+ca) {
		t.Errorf("connect task does not target %s:18090 with the fixture CA:\n%s", host, connect)
	}
}

// TestTaskfileStaysOnItsCluster pins that every command names the profile's
// own kubeconfig and context, and that no task touches another cluster.
func TestTaskfileStaysOnItsCluster(t *testing.T) {
	body, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if regexp.MustCompile(`(^|[|;'] *|- )kubectl `).MatchString(trimmed) && !strings.Contains(trimmed, "--kubeconfig={{.KUBECONFIG}}") {
			t.Errorf("kubectl without the profile kubeconfig: %s", trimmed)
		}
		if strings.Contains(trimmed, "kubectl --kubeconfig") && !strings.Contains(trimmed, "config current-context") && !strings.Contains(trimmed, "--context={{.CONTEXT}}") {
			t.Errorf("kubectl without the profile context: %s", trimmed)
		}
		if strings.Contains(trimmed, "helm ") && strings.Contains(trimmed, "--kubeconfig") && !strings.Contains(trimmed, "--kube-context={{.CONTEXT}}") {
			t.Errorf("helm without the profile context: %s", trimmed)
		}
		if strings.Contains(trimmed, "kind ") && regexp.MustCompile(`kind (create|delete|load|get kubeconfig)`).MatchString(trimmed) && !strings.Contains(trimmed, "--name={{.CLUSTER}}") {
			t.Errorf("kind command without the profile cluster name: %s", trimmed)
		}
	}
	for _, forbidden := range []string{"mecatl-dev", "toolhive", "stacklok-enterprise", "--from-literal", "port-forward", "0.0.0.0"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Taskfile mentions %q", forbidden)
		}
	}
	tf := loadTaskfile(t)
	destroy := taskCommands(t, tf, "destroy")
	for _, want := range []string{"kind delete cluster --name=mecatl-idbroker", "rm -rf .scratch/kind/mecatl-idbroker\n"} {
		if !strings.Contains(destroy, want) {
			t.Errorf("destroy missing %q", want)
		}
	}
	if !strings.HasPrefix(tf.Vars["KUBECONFIG"], tf.Vars["STATE"]+"/") || tf.Vars["SETUP_LOCK"] != ".scratch/kind/mecatl-idbroker.setup-lock" {
		t.Errorf("kubeconfig %q or setup lock %q is outside the profile's state", tf.Vars["KUBECONFIG"], tf.Vars["SETUP_LOCK"])
	}
}

// TestChartRenders renders the overlay as kind-setup installs it, with the
// mock default and with the real-provider overlay.
func TestChartRenders(t *testing.T) {
	tf := loadTaskfile(t)
	chart := taskCommands(t, tf, "chart-apply")
	for _, want := range []string{
		"--values=deploy/helm/mecak8s/values-kind.yaml --values=deploy/helm/mecak8s/values-kind-idbroker.yaml",
		`if [ -n "${OPENROUTER_API_KEY:-}" ]; then`, "--values=deploy/mecak8s-idbroker/provider-real.yaml",
	} {
		if !strings.Contains(chart, want) {
			t.Errorf("chart-apply missing %q", want)
		}
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required to render the chart")
	}
	render := func(extra ...string) string {
		t.Helper()
		args := append([]string{"template", "mecak8s", ".", "--namespace", tf.Vars["NAMESPACE"], "-f", "values-kind.yaml", "-f", "values-kind-idbroker.yaml"}, extra...)
		cmd := exec.Command("helm", args...)
		cmd.Dir = "../helm/mecak8s"
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	issuer, audience, host := tf.Vars["AS1_ISSUER"], tf.Vars["AS1_AUDIENCE"], tf.Vars["MECAK8S_HOST"]
	mock := render()
	for _, want := range []string{
		"replicas: 1", "type: Recreate", "image: ko.local/mecak8s:idbroker",
		"port: 18090", "nodePort: 30090", "port: 18091", "nodePort: 30091",
		"- --mock", "- --no-shell", "- --enable-identity-broker",
		`"--oidc-issuer=` + issuer + `"`, `"--oidc-audience=` + audience + `"`,
		`"--oidc-resource=https://` + host + `:18091"`, `"--oidc-client-id=mecatui"`,
		`"--oidc-owner-identity=sub_id"`, `"--oidc-accepted-client-ids=mecatui"`,
		`"--identity-broker-as1-issuer=` + issuer + `"`,
		`"--identity-broker-as1-upstream-issuer=https://` + tf.Vars["DEX_HOST"] + `:5557"`,
		`"--identity-broker-as1-upstream-client-id=mecak8s-as1"`,
		`"--identity-broker-as1-signing-key-file=/var/run/secrets/as1-signing-key/signing-key.pem"`,
		`"--identity-broker-as1-upstream-ca-file=/var/run/secrets/as1-upstream-ca/tls.crt"`,
		"- --identity-broker-as1-upstream-allow-private-addresses", `"--identity-broker-subject-claim=sub"`,
		`"--tls-cert=/var/run/secrets/tls/tls.crt"`,
	} {
		if !strings.Contains(mock, want) {
			t.Errorf("rendered profile missing %q", want)
		}
	}
	for _, forbidden := range []string{"--oidc-allow-private-https-issuer", "--oidc-jwks-uri", "OPENROUTER_API_KEY", "--allow-unsafe"} {
		if strings.Contains(mock, forbidden) {
			t.Errorf("rendered profile contains %q", forbidden)
		}
	}
	withProvider := render("-f", "../../mecak8s-idbroker/provider-real.yaml")
	if strings.Contains(withProvider, "- --mock") || !strings.Contains(withProvider, "--model=openai/gpt-6-sol") || !strings.Contains(withProvider, "name: mecak8s-openrouter") {
		t.Error("real-provider render must drop --mock, pin the model, and project the mecak8s-openrouter Secret")
	}
}
