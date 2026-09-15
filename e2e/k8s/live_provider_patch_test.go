//go:build kind_e2e

package k8s_e2e_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestOIDCPatchArgsPreserveLiveProviderAndRedisPlaintextOptIn(t *testing.T) {
	t.Parallel()
	current := []string{
		"--grpc-addr=0.0.0.0:8080",
		"--redis-url=mecak8s-mecak8s-redis:6379",
		"--redis-allow-plaintext",
		"--default-provider=openrouter",
		"--default-model=anthropic/claude-haiku-4.5",
		"--oidc-issuer", "stale-issuer",
		"--oidc-audience=stale-audience",
		"--oidc-insecure-allow-private-issuer",
	}
	got := withoutOIDCArgs(current)
	want := []string{
		"--grpc-addr=0.0.0.0:8080",
		"--redis-url=mecak8s-mecak8s-redis:6379",
		"--redis-allow-plaintext",
		"--default-provider=openrouter",
		"--default-model=anthropic/claude-haiku-4.5",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("withoutOIDCArgs() = %#v, want %#v", got, want)
	}
}

func TestLiveProviderPatchPreservesRenderedArgsAndLocatesAgent(t *testing.T) {
	t.Parallel()
	deployment := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"sidecar","args":["keep-sidecar"]},{"name":"agent","args":["--grpc-addr=0.0.0.0:8080","--redis-allow-plaintext","--mock","--model","chart-model","--model=stale-model","--default-provider=mock","--future-flag=value","--default-model","old-model"],"env":[{"name":"KEEP_ME","value":"yes"}]}]}}}}`)

	patchJSON, err := liveProviderPatch(deployment)
	if err != nil {
		t.Fatalf("liveProviderPatch: %v", err)
	}
	var patch []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(patchJSON, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if len(patch) != 2 {
		t.Fatalf("patch operation count = %d, want 2: %s", len(patch), patchJSON)
	}
	if patch[0].Op != "replace" || patch[0].Path != "/spec/template/spec/containers/1/args" {
		t.Fatalf("args operation = %#v, want replace on agent container index 1", patch[0])
	}
	var args []string
	if err := json.Unmarshal(patch[0].Value, &args); err != nil {
		t.Fatalf("decode args: %v", err)
	}
	want := []string{
		"--grpc-addr=0.0.0.0:8080",
		"--redis-allow-plaintext",
		"--future-flag=value",
		"--default-provider=openrouter",
		"--default-model=anthropic/claude-haiku-4.5",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("patched args = %#v, want %#v", args, want)
	}

	var env map[string]any
	if err := json.Unmarshal(patch[1].Value, &env); err != nil {
		t.Fatalf("decode env entry: %v", err)
	}
	if patch[1].Op != "add" || patch[1].Path != "/spec/template/spec/containers/1/env/-" || env["name"] != "OPENROUTER_API_KEY" {
		t.Fatalf("env patch did not preserve existing entries and append key ref: op=%q path=%q env=%#v", patch[1].Op, patch[1].Path, env)
	}
}

func TestLiveProviderPatchAddsMissingEnvField(t *testing.T) {
	t.Parallel()
	deployment := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"agent","args":["--mock"]}]}}}}`)

	patchJSON, err := liveProviderPatch(deployment)
	if err != nil {
		t.Fatalf("liveProviderPatch: %v", err)
	}
	var patch []struct {
		Op   string `json:"op"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(patchJSON, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if len(patch) != 2 {
		t.Fatalf("patch operation count = %d, want 2", len(patch))
	}
	if patch[1].Op != "add" || patch[1].Path != "/spec/template/spec/containers/0/env" {
		t.Fatalf("missing env operation = %#v, want JSON Patch add", patch[1])
	}
}

func TestBoundedRedactedRemovesEveryExactSecretAndCapsOutput(t *testing.T) {
	t.Parallel()
	const (
		secret = "exact-key"
		limit  = 64
	)
	got := boundedRedacted(strings.Repeat("prefix-"+secret+"-suffix;", 20), secret, limit)
	if strings.Contains(got, secret) {
		t.Fatalf("diagnostics retained exact secret: %q", got)
	}
	if occurrences := strings.Count(got, "[REDACTED]"); occurrences < 2 {
		t.Fatalf("redacted occurrences = %d, want multiple before the cap: %q", occurrences, got)
	}
	const marker = "\n...[diagnostics truncated]...\n"
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("diagnostics were not marked truncated: %q", got)
	}
	if len(got) != limit+len(marker) {
		t.Fatalf("bounded diagnostics length = %d, want %d", len(got), limit+len(marker))
	}
}

func TestBoundedDrainWriterKeepsDrainingAfterCaptureLimit(t *testing.T) {
	t.Parallel()
	writer := &boundedDrainWriter{limit: 8}
	for _, chunk := range []string{"abcd", "efgh", "secret-tail"} {
		n, err := writer.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", chunk, n, err, len(chunk))
		}
	}
	if got := writer.String(); got != "abcdefgh" {
		t.Fatalf("captured output = %q (len %d), want exact 8-byte prefix", got, len(got))
	}
}

func TestLiveProviderPatchReplacesExistingKeyRefWithoutCopyingValue(t *testing.T) {
	t.Parallel()
	const oldSecret = "must-not-enter-patch"
	deployment := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"agent","args":[],"env":[{"name":"KEEP","value":"safe"},{"name":"OPENROUTER_API_KEY","value":"` + oldSecret + `"}] }]}}}}`)

	patchJSON, err := liveProviderPatch(deployment)
	if err != nil {
		t.Fatalf("liveProviderPatch: %v", err)
	}
	var patch []struct {
		Op   string `json:"op"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(patchJSON, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patch[1].Op != "replace" || patch[1].Path != "/spec/template/spec/containers/0/env/1" {
		t.Fatalf("existing key operation = %#v, want targeted replace", patch[1])
	}
	if strings.Contains(string(patchJSON), oldSecret) {
		t.Fatalf("patch copied the existing secret value: %s", patchJSON)
	}
}

func TestResolveAgentDeploymentRevisionSelectsCurrentReplicaSet(t *testing.T) {
	t.Parallel()
	deployment := []byte(`{"metadata":{"name":"mecak8s-agent","uid":"deployment-uid","annotations":{"deployment.kubernetes.io/revision":"8"}},"spec":{"replicas":2}}`)
	replicaSets := []byte(`{"items":[
		{"metadata":{"name":"mecak8s-agent-old","labels":{"pod-template-hash":"old-hash"},"annotations":{"deployment.kubernetes.io/revision":"7"},"ownerReferences":[{"kind":"Deployment","name":"mecak8s-agent","uid":"deployment-uid"}]}},
		{"metadata":{"name":"mecak8s-agent-current","labels":{"pod-template-hash":"current-hash"},"annotations":{"deployment.kubernetes.io/revision":"8"},"ownerReferences":[{"kind":"Deployment","name":"mecak8s-agent","uid":"deployment-uid"}]}}
	]}`)

	got, err := resolveAgentDeploymentRevision(deployment, replicaSets)
	if err != nil {
		t.Fatalf("resolveAgentDeploymentRevision: %v", err)
	}
	want := agentDeploymentRevision{ReplicaSet: "mecak8s-agent-current", PodTemplateHash: "current-hash", Desired: 2}
	if got != want {
		t.Fatalf("revision = %#v, want %#v", got, want)
	}
}

func TestReadyPodNamesForRevisionExcludesOldRolloutPods(t *testing.T) {
	t.Parallel()
	revision := agentDeploymentRevision{
		ReplicaSet: "mecak8s-agent-current", PodTemplateHash: "current-hash", Desired: 2,
	}
	const currentPods = `
		{"metadata":{"name":"current-b","labels":{"pod-template-hash":"current-hash"},"ownerReferences":[{"kind":"ReplicaSet","name":"mecak8s-agent-current"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}},
		{"metadata":{"name":"current-a","labels":{"pod-template-hash":"current-hash"},"ownerReferences":[{"kind":"ReplicaSet","name":"mecak8s-agent-current"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}`
	tests := map[string]string{
		"terminating current-hash pod":        `{"metadata":{"name":"old-terminating","deletionTimestamp":"2026-09-03T12:00:00Z","labels":{"pod-template-hash":"current-hash"},"ownerReferences":[{"kind":"ReplicaSet","name":"mecak8s-agent-current"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}`,
		"ready old-hash pod":                  `{"metadata":{"name":"old-ready","labels":{"pod-template-hash":"old-hash"},"ownerReferences":[{"kind":"ReplicaSet","name":"mecak8s-agent-old"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}`,
		"current hash wrong ReplicaSet owner": `{"metadata":{"name":"wrong-owner","labels":{"pod-template-hash":"current-hash"},"ownerReferences":[{"kind":"ReplicaSet","name":"mecak8s-agent-other"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}`,
		"old hash current ReplicaSet owner":   `{"metadata":{"name":"old-hash","labels":{"pod-template-hash":"old-hash"},"ownerReferences":[{"kind":"ReplicaSet","name":"mecak8s-agent-current"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}`,
	}
	for name, oldPod := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pods := []byte(`{"items":[` + oldPod + `,` + currentPods + `]}`)
			got, err := readyPodNamesForRevision(pods, revision)
			if err != nil {
				t.Fatalf("readyPodNamesForRevision: %v", err)
			}
			want := []string{"current-a", "current-b"}
			if !slices.Equal(got, want) {
				t.Fatalf("selected pods = %v, want %v", got, want)
			}
		})
	}
}

func TestValidateReadyPodCount(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		names   []string
		wantErr bool
	}{
		"exact": {names: []string{"current-a", "current-b"}},
		"fewer": {names: []string{"current-a"}, wantErr: true},
		"more":  {names: []string{"current-a", "current-b", "current-c"}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if gotErr := validateReadyPodCount(test.names, 2) != nil; gotErr != test.wantErr {
				t.Fatalf("validateReadyPodCount(%v, 2) error = %t, want %t", test.names, gotErr, test.wantErr)
			}
		})
	}
}
