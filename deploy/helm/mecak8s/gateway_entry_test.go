package mecak8s_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
)

// gatewayEntryValues adds to the production identity broker profile a
// protected MCP server, an embedded gateway entry for the embedded gateway
// mecak8s derives from its callback URL, and the embedded gateway's signing
// key with a fallback.
const gatewayEntryValues = `
mcp:
  broker:
    callbackURL: https://mecatl.example.com/v1/mcp/authorization/callback
    signingKey:
      secretName: mcp-gateway-signing
      key: key.pem
      fallbacks:
        - {secretName: mcp-gateway-signing-old, key: key.pem}
  servers:
    - name: calendar
      url: https://calendar.example.com/mcp
      auth:
        mode: oauth
        oauth:
          issuer: https://calendar.example.com
          client:
            mode: preregistered
            preregistered:
              id: mecatl
              secretKeyRef: {name: calendar-oauth, key: client-secret}
          scopes: [openid]
          network: {additionalOrigins: [], privateOrigins: [], maxRedirects: 0}
identityBroker:
  gateways:
    - kind: embedded
      as2Issuer: https://mecatl.example.com/v1/mcp/broker
      tokenEndpoint: https://mecatl.example.com/v1/mcp/broker/oauth/token
      resource: https://mecatl.example.com/v1/mcp/broker/mcp
      servers: []
`

// renderGatewayEntry renders the production identity broker profile with
// gatewayEntryValues and then extra values.
func renderGatewayEntry(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway-values.yaml")
	if err := os.WriteFile(path, []byte(gatewayEntryValues), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"template", "ib", ".", "-f", "ci/production-identity-broker-values.yaml", "-f", path}
	for _, value := range extra {
		args = append(args, "--set", value)
	}
	return helm(t, args...)
}

// TestMecak8sHelmChart_GatewayEntryAndSigningKey pins the gateway entry in
// the identity broker's configuration file, which the broker's loader reads,
// and the embedded gateway's signing key, projected read-only to the paths
// settings.yaml names, which the runtime resolver carries to the MCP broker.
func TestMecak8sHelmChart_GatewayEntryAndSigningKey(t *testing.T) {
	rendered, err := renderGatewayEntry(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, rendered)
	}
	raw := identityBrokerConfigFromRender(t, rendered)
	var doc struct {
		Gateways []map[string]any `json:"gateways"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("identity-broker.json: %v", err)
	}
	wantEntry := map[string]any{
		"kind": "embedded", "as2_issuer": "https://mecatl.example.com/v1/mcp/broker",
		"token_endpoint": "https://mecatl.example.com/v1/mcp/broker/oauth/token",
		"resource":       "https://mecatl.example.com/v1/mcp/broker/mcp", "servers": []any{},
	}
	if len(doc.Gateways) != 1 || !reflect.DeepEqual(doc.Gateways[0], wantEntry) {
		t.Fatalf("gateways = %v, want [%v]", doc.Gateways, wantEntry)
	}
	// The broker's own loader accepts the rendered entry beside the hosted
	// AS-1, whose issuer is on the same host under another path.
	const dir = "/var/run/secrets/identity-broker/"
	keys := t.TempDir()
	write := func(rel string) string {
		path := filepath.Join(keys, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	client := brokerconfigtest.NewKey(t)
	brokerconfigtest.WriteKey(t, write("agent-issuer/key.pem"), brokerconfigtest.NewKey(t))
	brokerconfigtest.WriteKey(t, write("broker-client/key.pem"), client)
	brokerconfigtest.WriteKeySet(t, write("broker-client-key-set/jwks.json"), client)
	brokerconfigtest.WriteKey(t, write("as1-signing/signing-key.pem"), brokerconfigtest.NewKey(t))
	configFile := filepath.Join(t.TempDir(), "identity-broker.json")
	if err := os.WriteFile(configFile, []byte(strings.ReplaceAll(raw, dir, keys+"/")), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := brokerconfig.Load(configFile, brokerconfig.Host{AS1: true, AS1Issuer: "https://mecatl.example.com/as1"})
	if err != nil {
		t.Fatalf("brokerconfig.Load of the rendered file: %v", err)
	}
	if len(loaded.Gateways) != 1 || loaded.Gateways[0].Kind != identitybroker.GatewayEmbedded || len(loaded.Gateways[0].Servers) != 0 {
		t.Fatalf("loaded gateways = %+v, want the embedded entry routing no servers", loaded.Gateways)
	}

	profile := configMapFromRender(t, rendered, "ib-mecak8s-mcp").Data["settings.yaml"]
	broker, ok := runtimeMCPAuthorityFromConfigMap(t, profile).Broker()
	const keyDir = "/var/run/secrets/mcp-broker-signing-key/"
	if !ok || broker.SigningKeyFile != keyDir+"signing-key/key.pem" || !slices.Equal(broker.FallbackKeyFiles, []string{keyDir + "fallback-0/key.pem"}) {
		t.Fatalf("resolved signing key = %q %v, want the projected paths\n%s", broker.SigningKeyFile, broker.FallbackKeyFiles, profile)
	}
	spec := deploymentFromRender(t, rendered).Spec.Template.Spec
	m := slices.IndexFunc(spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == "mcp-broker-signing-key" })
	if m < 0 || !spec.Containers[0].VolumeMounts[m].ReadOnly || spec.Containers[0].VolumeMounts[m].MountPath != strings.TrimSuffix(keyDir, "/") {
		t.Fatalf("the signing key is not mounted read-only at %s: %+v", keyDir, spec.Containers[0].VolumeMounts)
	}
	i := slices.IndexFunc(spec.Volumes, func(v corev1.Volume) bool { return v.Name == "mcp-broker-signing-key" })
	if i < 0 || spec.Volumes[i].Projected == nil || spec.Volumes[i].Projected.DefaultMode == nil || *spec.Volumes[i].Projected.DefaultMode != 0o440 {
		t.Fatalf("mcp-broker-signing-key is not a 0440 projected volume: %+v", spec.Volumes)
	}
	projected := map[string]string{}
	for _, source := range spec.Volumes[i].Projected.Sources {
		for _, item := range source.Secret.Items {
			projected[item.Path] = source.Secret.Name + "/" + item.Key
		}
	}
	if want := map[string]string{"signing-key/key.pem": "mcp-gateway-signing/key.pem", "fallback-0/key.pem": "mcp-gateway-signing-old/key.pem"}; !reflect.DeepEqual(projected, want) {
		t.Fatalf("projected signing keys = %v, want %v", projected, want)
	}
}

// TestMecak8sHelmChart_GatewayEntryRefusals mirrors mecak8s's startup rules
// at render time.
func TestMecak8sHelmChart_GatewayEntryRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		set  []string
		want string
	}{
		"servers before AS-2 trusts AS-1":     {[]string{"identityBroker.gateways[0].servers={calendar}"}, "must stay empty until the embedded gateway trusts AS-1"},
		"a server that is not an OAuth route": {[]string{"identityBroker.gateways[0].servers={github}"}, `"github" is not an mcp.servers name`},
		"an external entry":                   {[]string{"identityBroker.gateways[0].kind=external"}, "external gateways are not supported yet (#2812)"},
		"two entries": {[]string{
			"identityBroker.gateways[1].kind=embedded", "identityBroker.gateways[1].as2Issuer=https://other.example.com/v1/mcp/broker",
			"identityBroker.gateways[1].tokenEndpoint=https://other.example.com/v1/mcp/broker/oauth/token", "identityBroker.gateways[1].resource=https://other.example.com/v1/mcp/broker/mcp",
		}, "at most one entry"},
		"another issuer":         {[]string{"identityBroker.gateways[0].as2Issuer=https://other.example.com/v1/mcp/broker"}, "gateways[0].as2Issuer must be https://mecatl.example.com/v1/mcp/broker"},
		"another token endpoint": {[]string{"identityBroker.gateways[0].tokenEndpoint=https://mecatl.example.com/token"}, "gateways[0].tokenEndpoint must be"},
		"another resource":       {[]string{"identityBroker.gateways[0].resource=https://mecatl.example.com/mcp"}, "gateways[0].resource must be"},
		"AS-1's issuer": {[]string{
			"identityBroker.as1.issuer=https://mecatl.example.com/v1/mcp/broker", "oidc.issuer=",
		}, "must differ from identityBroker.as1.issuer"},
		"no signing key":                     {[]string{"mcp.broker.signingKey.secretName=", "mcp.broker.signingKey.key=", "mcp.broker.signingKey.fallbacks=null"}, "identityBroker.gateways requires mcp.broker.signingKey"},
		"a signing key without an entry":     {[]string{"identityBroker.gateways=null"}, "mcp.broker.signingKey requires identityBroker.enabled with an identityBroker.gateways entry"},
		"a signing key that is a broker key": {[]string{"mcp.broker.signingKey.secretName=identity-broker-agent-issuer"}, "name the same Secret key identity-broker-agent-issuer/key.pem"},
		"no embedded gateway":                {[]string{"mcp.broker.callbackURL="}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			output, err := renderGatewayEntry(t, tc.set...)
			if err == nil || !strings.Contains(output, tc.want) {
				t.Fatalf("render = %v, want failure %q:\n%s", err, tc.want, output)
			}
		})
	}
	// With the broker off, a signing key fails the render.
	output, err := helm(t, "template", "off", ".", "--set", "mockProvider=true,permissionMode=yolo,redis.local.enabled=true",
		"--set", "mcp.broker.signingKey.secretName=mcp-gateway-signing,mcp.broker.signingKey.key=key.pem")
	if err == nil || !strings.Contains(output, "mcp.broker.signingKey requires identityBroker.enabled") {
		t.Fatalf("render with the broker off = %v, want the signing key refused:\n%s", err, output)
	}
}
