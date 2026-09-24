package mecak8s_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

const managedRedisDigest = "sha256:bb186d083732f669da90be8b0f975a37812b15e913465bb14d845db72a4e3e08"

func renderedKind[T any](t *testing.T, rendered, kind string) []T {
	t.Helper()
	var out []T
	for _, document := range strings.Split(rendered, "\n---") {
		var meta struct {
			Kind string `json:"kind"`
		}
		if yaml.Unmarshal([]byte(document), &meta) != nil || meta.Kind != kind {
			continue
		}
		var value T
		if err := yaml.Unmarshal([]byte(document), &value); err != nil {
			t.Fatalf("decode %s: %v", kind, err)
		}
		out = append(out, value)
	}
	return out
}

func renderedBrokerJSON(t *testing.T, rendered string) map[string]any {
	t.Helper()
	cm := configMapFromRender(t, rendered, "production-mecak8s-broker-config")
	var config map[string]any
	if err := json.Unmarshal([]byte(cm.Data["broker.json"]), &config); err != nil {
		t.Fatalf("broker.json: %v", err)
	}
	return config
}

const managedCredentialValues = `
broker:
  credentialStore:
    redis:
      address: ""
    managedRedis:
      enabled: true
      tlsSecret: credential-redis-tls
      aclSecret: credential-redis-acl
`

func managedCredentialArgs() []string {
	all := credentialStoreArgs()
	args := []string{}
	for i := 0; i+1 < len(all); i += 2 {
		if !strings.HasPrefix(all[i+1], "broker.credentialStore.redis.address=") {
			args = append(args, all[i], all[i+1])
		}
	}
	return args
}

const oauthServerValues = `
mcp:
  broker:
    callbackURL: https://agent.example/mcp/authorization/callback
  servers:
    - name: oauth_registered
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          issuer: https://issuer.example
          client:
            mode: preregistered
            preregistered:
              id: mecak8s
              secretKeyRef: {name: oauth-registered, key: client-secret}
          scopes: [mcp.read]
          network: {additionalOrigins: [], privateOrigins: [], maxRedirects: 0}
`

// renderMCPValuesWithArgsNoDefaults adds the broker TLS/image values an OAuth
// render needs but, unlike renderMCPValuesWithArgs, no credential store.
func renderMCPValuesWithArgsNoDefaults(t *testing.T, args []string, values string) (string, error) {
	t.Helper()
	return renderMCPValuesWithArgs(t, append(append([]string(nil), args...),
		"--set", "broker.image.digest=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"--set", "broker.tls.secretName=mecabroker-tls",
		"--set", "broker.clientCA.secretName=mecabroker-ca",
		"--set", "broker.clientCA.serverName=production-mecak8s-broker"), values)
}

// OAuth broker renders external credential storage into the standalone JSON
// and projects each Secret item only into the broker.
func TestBrokerCredentialContinuity_Scenario4_IndependentRedisTargets(t *testing.T) {
	rendered, err := helm(t, "template", "production", ".", "-f", "ci/broker-mcp-values.yaml")
	if err != nil {
		t.Fatal(err, rendered)
	}
	storage, ok := renderedBrokerJSON(t, rendered)["protected_storage"].(map[string]any)
	if !ok {
		t.Fatal("OAuth broker config has no protected_storage")
	}
	redis := storage["redis"].(map[string]any)
	if redis["address"] != "broker-redis.example.invalid:6379" || redis["ca_file"] != "/var/run/mecabroker/credential-store/redis/ca.pem" || redis["username_file"] != "/var/run/mecabroker/credential-store/redis/username" {
		t.Fatalf("protected_storage.redis = %#v", redis)
	}
	broker := brokerDeploymentFromRender(t, rendered)
	agent := deploymentFromRender(t, rendered)
	for _, secret := range []string{"broker-redis-credentials", "broker-redis-ca", "broker-credential-keks"} {
		if !podReferencesSecret(broker.Spec.Template.Spec, secret) {
			t.Fatalf("broker does not project Secret %q", secret)
		}
		if podReferencesSecret(agent.Spec.Template.Spec, secret) {
			t.Fatalf("agent projects broker credential Secret %q", secret)
		}
	}
	for _, mount := range broker.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.SubPath != "" {
			t.Fatalf("broker mount %q uses subPath", mount.Name)
		}
	}
	for _, set := range renderedKind[appsv1.StatefulSet](t, rendered, "StatefulSet") {
		if strings.Contains(set.Name, "credential-redis") {
			t.Fatal("external credential store rendered managed Redis")
		}
	}
}

func TestBrokerCredentialContinuity_Scenario4_DurableManagedRedis(t *testing.T) {
	rendered, err := renderMCPValuesWithArgsNoDefaults(t, append(secureProductionArgs(), managedCredentialArgs()...), strings.TrimPrefix(oauthServerValues, "\n")+managedCredentialValues)
	if err != nil {
		t.Fatal(err, rendered)
	}
	sets := renderedKind[appsv1.StatefulSet](t, rendered, "StatefulSet")
	services := renderedKind[corev1.Service](t, rendered, "Service")
	policies := renderedKind[networkingv1.NetworkPolicy](t, rendered, "NetworkPolicy")
	if len(sets) != 1 {
		t.Fatalf("managed Redis StatefulSets = %d, want 1", len(sets))
	}
	set := sets[0]
	name := set.Name
	if *set.Spec.Replicas != 1 || set.Spec.ServiceName != name || set.Spec.PersistentVolumeClaimRetentionPolicy != nil {
		t.Fatalf("StatefulSet replicas/serviceName/retention = %v %q %v", *set.Spec.Replicas, set.Spec.ServiceName, set.Spec.PersistentVolumeClaimRetentionPolicy)
	}
	pod := set.Spec.Template.Spec
	container := pod.Containers[0]
	if container.Image != "redis@"+managedRedisDigest || !slices.Equal(container.Command, []string{"redis-server"}) {
		t.Fatalf("image/command = %q %v", container.Image, container.Command)
	}
	args := strings.Join(container.Args, " ")
	for _, want := range []string{"--port 0", "--tls-port 6379", "--appendonly yes", "--appendfsync everysec", "--maxmemory 384mb", "--maxmemory-policy noeviction", "--dir /data", "--aclfile /var/run/mecabroker-redis/acl/users.acl"} {
		if !strings.Contains(args, want) {
			t.Fatalf("redis args %q missing %q", args, want)
		}
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || container.SecurityContext == nil || !*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("managed Redis must not mount a service account token and must use a read-only root")
	}
	writable := []string{}
	for _, mount := range container.VolumeMounts {
		if !mount.ReadOnly {
			writable = append(writable, mount.MountPath)
		}
	}
	if !slices.Equal(writable, []string{"/data"}) {
		t.Fatalf("writable mounts = %v, want only /data", writable)
	}
	if container.ReadinessProbe == nil || container.ReadinessProbe.TCPSocket == nil || container.ReadinessProbe.Exec != nil {
		t.Fatal("readiness probe must be a secret-free TCP probe")
	}
	headless := false
	for _, service := range services {
		if service.Name == name {
			headless = service.Spec.ClusterIP == corev1.ClusterIPNone
		}
	}
	if !headless {
		t.Fatalf("no headless Service %q", name)
	}
	storage := renderedBrokerJSON(t, rendered)["protected_storage"].(map[string]any)["redis"].(map[string]any)
	if storage["address"] != name+".default.svc:6379" || storage["ca_file"] == nil {
		t.Fatalf("managed broker address/CA = %#v", storage)
	}
	var ingress *networkingv1.NetworkPolicy
	for i := range policies {
		if policies[i].Name == name {
			ingress = &policies[i]
		}
	}
	if ingress == nil || len(ingress.Spec.Ingress) != 1 || ingress.Spec.Ingress[0].Ports[0].Port.IntValue() != 6379 ||
		ingress.Spec.Ingress[0].From[0].PodSelector.MatchLabels["app.kubernetes.io/component"] != "broker" {
		t.Fatalf("managed Redis ingress policy = %#v", ingress)
	}
}

func TestBrokerCredentialContinuity_Scenario4_InvalidStorageConfigurationFails(t *testing.T) {
	for name, tc := range map[string]struct {
		args   []string
		values string
	}{
		"oauth without storage":    {args: secureProductionArgs(), values: oauthServerValues},
		"storage without oauth":    {args: append(productionArgs(), credentialStoreArgs()...), values: "mcp:\n  servers: []\n"},
		"url address":              {args: append(credentialStoreArgs(), "--set", "broker.credentialStore.redis.address=rediss://h:6379"), values: oauthServerValues},
		"active key missing":       {args: append(credentialStoreArgs(), "--set", "broker.credentialStore.encryption.activeID=other"), values: oauthServerValues},
		"health exceeds operation": {args: append(credentialStoreArgs(), "--set", "broker.credentialStore.redis.healthTimeout=6s"), values: oauthServerValues},
		"timeout over 30s":         {args: append(credentialStoreArgs(), "--set", "broker.credentialStore.redis.dialTimeout=31s"), values: oauthServerValues},
		"managed with address":     {args: append(credentialStoreArgs(), "--set", "broker.credentialStore.managedRedis.enabled=true", "--set", "broker.credentialStore.managedRedis.tlsSecret=t", "--set", "broker.credentialStore.managedRedis.aclSecret=a"), values: oauthServerValues},
		"managed digest changed":   {args: append(managedCredentialArgs(), "--set", "broker.credentialStore.managedRedis.image.digest=sha256:0000000000000000000000000000000000000000000000000000000000000000"), values: oauthServerValues + managedCredentialValues},
		"plaintext flag":           {args: append(credentialStoreArgs(), "--set", "broker.credentialStore.redis.allowPlaintext=true"), values: oauthServerValues},
	} {
		t.Run(name, func(t *testing.T) {
			args := tc.args
			if name != "oauth without storage" && name != "storage without oauth" {
				args = append(secureProductionArgs(), tc.args...)
			}
			rendered, err := renderMCPValuesWithArgsNoDefaults(t, args, tc.values)
			if err == nil {
				t.Fatalf("invalid credential store rendered:\n%s", rendered)
			}
			if !strings.Contains(rendered, "credentialStore") && !strings.Contains(rendered, "credential") {
				t.Fatalf("rejected for an unrelated reason: %s", rendered)
			}
		})
	}
}

func podReferencesSecret(pod corev1.PodSpec, name string) bool {
	for _, volume := range pod.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName == name {
			return true
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil && source.Secret.Name == name {
					return true
				}
			}
		}
	}
	return false
}
