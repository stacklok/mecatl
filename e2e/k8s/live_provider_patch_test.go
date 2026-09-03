//go:build kind_e2e

package k8s_e2e_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

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
