package mecak8s_test

import (
	"encoding/json"
	"path/filepath"
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

func TestBrokerCredentialContinuity_Scenario7_ManagedAndExternalRedisAreMutuallyExclusive(t *testing.T) {
	const fixture = "ci/broker-managed-redis-values.yaml"
	const guardMessage = "broker.credentialStore.managedRedis.enabled derives the address and CA; leave redis.address and redis.caSecret empty"

	for _, tc := range []struct {
		field string
		value string
	}{
		{field: "address", value: "external-redis.example.invalid:6379"},
		{field: "caSecret", value: "external-redis-ca"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			rendered, err := helm(t, "template", "production", ".", "-f", fixture,
				"--set", "broker.credentialStore.redis."+tc.field+"="+tc.value)
			if err == nil {
				t.Fatalf("chart rendered with managed Redis and redis.%s configured:\n%s", tc.field, rendered)
			}
			if !strings.Contains(rendered, guardMessage) {
				t.Fatalf("render error = %q, want guard message %q", rendered, guardMessage)
			}
		})
	}
}

func TestBrokerCredentialContinuity_Scenario7_ManagedRedisFixtureInDeploymentGate(t *testing.T) {
	const fixture = "ci/broker-managed-redis-values.yaml"
	fixtures, err := filepath.Glob("ci/*-values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fixtures, fixture) {
		t.Fatalf("managed Redis fixture %q is not in the deploy:check fixture set %v", fixture, fixtures)
	}

	rendered, err := helm(t, "template", "fixture", ".", "-f", fixture)
	if err != nil {
		t.Fatalf("render managed Redis fixture: %v\n%s", err, rendered)
	}
	for _, kind := range []string{"StatefulSet", "Service", "NetworkPolicy"} {
		found := false
		for _, document := range strings.Split(rendered, "\n---") {
			var meta struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
			}
			if yaml.Unmarshal([]byte(document), &meta) == nil && meta.Kind == kind &&
				strings.Contains(meta.Metadata.Name, "credential-redis") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("managed Redis fixture renders no credential-redis %s", kind)
		}
	}
}

func TestBrokerCredentialContinuity_MultiKeyRotationProjection(t *testing.T) {
	rendered, err := helm(t, "template", "fixture", ".", "-f", "ci/broker-managed-redis-values.yaml",
		"--set", "broker.credentialStore.encryption.activeID=active",
		"--set", "broker.credentialStore.encryption.keys[0].id=retired",
		"--set", "broker.credentialStore.encryption.keys[0].secretKey=kek-retired",
		"--set", "broker.credentialStore.encryption.keys[1].id=active",
		"--set", "broker.credentialStore.encryption.keys[1].secretKey=kek-active")
	if err != nil {
		t.Fatalf("render multi-key credential store: %v\n%s", err, rendered)
	}

	var encryptionVolume *corev1.Volume
	broker := brokerDeploymentFromRender(t, rendered)
	for i := range broker.Spec.Template.Spec.Volumes {
		volume := &broker.Spec.Template.Spec.Volumes[i]
		if volume.Name == "credential-encryption" {
			if encryptionVolume != nil {
				t.Fatal("broker has more than one credential-encryption volume")
			}
			encryptionVolume = volume
		}
	}
	if encryptionVolume == nil || encryptionVolume.Secret == nil {
		t.Fatal("broker has no credential-encryption Secret volume")
	}
	if encryptionVolume.Secret.SecretName != "broker-credential-keks" {
		t.Fatalf("credential-encryption Secret = %q, want broker-credential-keks", encryptionVolume.Secret.SecretName)
	}
	want := map[string]string{"kek-retired": "retired", "kek-active": "active"}
	if len(encryptionVolume.Secret.Items) != len(want) {
		t.Fatalf("credential-encryption items = %#v, want exactly %#v", encryptionVolume.Secret.Items, want)
	}
	for _, item := range encryptionVolume.Secret.Items {
		if path, ok := want[item.Key]; !ok || path != item.Path {
			t.Errorf("credential-encryption item = {%q, %q}, want key/path from %#v", item.Key, item.Path, want)
		}
		delete(want, item.Key)
	}
	if len(want) != 0 {
		t.Errorf("credential-encryption items missing keys/paths: %#v", want)
	}
}

func TestBrokerCredentialContinuity_BrokerRestartDoesNotTouchCustodyStore(t *testing.T) {
	const fixture = "ci/broker-managed-redis-values.yaml"
	render := func(digest string) string {
		t.Helper()
		rendered, err := helm(t, "template", "fixture", ".", "-f", fixture,
			"--set", "broker.image.digest="+digest)
		if err != nil {
			t.Fatalf("render broker image %s: %v\n%s", digest, err, rendered)
		}
		return rendered
	}
	first := render("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	second := render("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	brokerImage := func(rendered string) string {
		for _, container := range brokerDeploymentFromRender(t, rendered).Spec.Template.Spec.Containers {
			if container.Name == "broker" {
				return container.Image
			}
		}
		t.Fatal("broker Deployment has no broker container")
		return ""
	}
	if firstImage, secondImage := brokerImage(first), brokerImage(second); firstImage == secondImage {
		t.Fatalf("broker image did not change between renders: %q", firstImage)
	}

	credentialRedisResources := func(rendered string) map[string]string {
		resources := make(map[string]string)
		for _, document := range strings.Split(rendered, "\n---") {
			var meta struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
			}
			if err := yaml.Unmarshal([]byte(document), &meta); err != nil || !strings.Contains(meta.Metadata.Name, "credential-redis") {
				continue
			}
			key := meta.Kind + "/" + meta.Metadata.Name
			if _, exists := resources[key]; exists {
				t.Fatalf("render contains duplicate credential-redis resource %q", key)
			}
			resources[key] = strings.TrimSpace(document)
		}
		return resources
	}
	firstResources := credentialRedisResources(first)
	secondResources := credentialRedisResources(second)
	if len(firstResources) != 3 || len(secondResources) != 3 {
		t.Fatalf("credential-redis resource counts = %d and %d, want Service, StatefulSet, NetworkPolicy in both renders", len(firstResources), len(secondResources))
	}
	wantKinds := map[string]bool{"Service": false, "StatefulSet": false, "NetworkPolicy": false}
	for key := range firstResources {
		kind, _, _ := strings.Cut(key, "/")
		if _, ok := wantKinds[kind]; !ok {
			t.Errorf("unexpected credential-redis resource kind %q", kind)
			continue
		}
		wantKinds[kind] = true
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("render has no credential-redis %s", kind)
		}
	}
	for key, firstDocument := range firstResources {
		if secondDocument, ok := secondResources[key]; !ok || firstDocument != secondDocument {
			t.Errorf("credential-redis resource %q changed across a broker-only image digest bump\nfirst:\n%s\nsecond:\n%s", key, firstDocument, secondDocument)
		}
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
