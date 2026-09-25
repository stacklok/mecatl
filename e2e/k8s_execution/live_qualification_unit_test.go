//go:build kind_execution_e2e

package k8s_execution_test

import (
	"encoding/json"
	"go/format"
	"testing"

	corev1 "k8s.io/api/core/v1"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestReplacementProofRequestsUsePostClaimEpochForPositiveRequest(t *testing.T) {
	ref := executionenv.EnvironmentRef{ID: "fixture", Revision: "revision"}
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	staleStatus := executionStatus{Epoch: 4, PodUID: "pod", PVCUID: "pvc"}
	current := executionStatus{Epoch: 6, PodUID: "pod", PVCUID: "pvc"}
	stale, wrong, exact := replacementProofRequests(ref, owner, staleStatus, current)
	if stale.ExpectedEpoch != staleStatus.Epoch || stale.ExpectedPodUID != staleStatus.PodUID {
		t.Fatal("stale negative request did not preserve the pre-claim authority")
	}
	if wrong.ExpectedEpoch != current.Epoch || wrong.ExpectedPodUID == current.PodUID {
		t.Fatal("wrong-UID negative request did not use current authority")
	}
	if exact.ExpectedEpoch != current.Epoch || exact.ExpectedPodUID != current.PodUID || exact.ExpectedPVCUID != current.PVCUID {
		t.Fatal("positive exact request did not use post-release authoritative state")
	}
}

func TestModelRanExactShellRequiresDecodedCommandAndMatchingCallID(t *testing.T) {
	call := func(id, args string) *mecatlv1.Event {
		return &mecatlv1.Event{ToolCall: &mecatlv1.ToolCall{Id: id, Name: "Shell", Args: args}}
	}
	result := func(id string) *mecatlv1.Event {
		return &mecatlv1.Event{ToolResult: &mecatlv1.ToolResult{CallId: id, Content: "[exit code: 0]"}}
	}
	for name, events := range map[string][]*mecatlv1.Event{
		"echo":              {call("shell", `{"command":"echo ok"}`), result("shell")},
		"invalid JSON":      {call("shell", `{"command":`), result("shell")},
		"wrong result ID":   {call("shell", `{"command":"go test ./..."}`), result("other")},
		"unrelated success": {call("echo", `{"command":"echo ok"}`), result("echo"), call("test", `{"command":"go test ./..."}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if modelRanExactShell(events, "go test ./...") {
				t.Fatal("unqualified Shell call counted")
			}
		})
	}
	if !modelRanExactShell([]*mecatlv1.Event{call("test", `{"command":"  go test ./...  "}`), result("test")}, "go test ./...") {
		t.Fatal("matching successful Shell call was not counted")
	}
}

func TestLiveRestartProofHelpersAcceptExactArtifactsAndCleanupOnce(t *testing.T) {
	before := map[string][]byte{
		"arithmetic/sum.go":      []byte("package arithmetic\n// qualification: qual_fixture\nfunc Sum(a, b int) int { return a + b }\n"),
		"arithmetic/sum_test.go": []byte("package arithmetic\nfunc ExampleSum() { _ = Sum(1, 2) }\n"),
	}
	after := map[string][]byte{
		"arithmetic/sum.go":      append([]byte(nil), before["arithmetic/sum.go"]...),
		"arithmetic/sum_test.go": append([]byte(nil), before["arithmetic/sum_test.go"]...),
	}
	if !qualificationArtifactsEqual(before, after) {
		t.Fatal("byte-identical fixed qualification artifacts were rejected")
	}
	if !containsExactLine(before["arithmetic/sum.go"], "// qualification: qual_fixture") {
		t.Fatal("exact nonce marker line was rejected")
	}

	calls := 0
	cleanup := cleanupOnce(func() { calls++ })
	cleanup()
	cleanup()
	if calls != 1 {
		t.Fatalf("cleanup calls=%d, want 1", calls)
	}
}

func TestQualificationHelperIsValidCanonicalGo(t *testing.T) {
	formatted, err := format.Source([]byte(qualificationHelperFile))
	if err != nil {
		t.Fatalf("fixed qualification helper is invalid Go: %v", err)
	}
	if string(formatted) != qualificationHelperFile {
		t.Fatal("fixed qualification helper is not gofmt-canonical")
	}
}

func TestLiveSummaryIncludesFixedRestartEvidence(t *testing.T) {
	blob, err := json.Marshal(liveSummary{
		Cluster:               "fixture",
		Provider:              "openrouter",
		Model:                 liveModel,
		InputTokens:           1,
		OutputTokens:          2,
		ToolNames:             []string{"Write", "Shell"},
		ArtifactCount:         2,
		RestartVerified:       true,
		BeforeRestartExitCode: 0,
		AfterRestartExitCode:  0,
		Timestamp:             "2026-09-23T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(blob, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"restart_verified", "before_restart_exit_code", "after_restart_exit_code"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("summary omitted %s", name)
		}
	}
	if len(fields) != 11 {
		t.Fatalf("summary field count=%d, want fixed allowlist of 11", len(fields))
	}
	var verified bool
	if err := json.Unmarshal(fields["restart_verified"], &verified); err != nil || !verified {
		t.Fatal("summary did not preserve positive restart verdict")
	}
}

func TestRealProviderArgsRejectExecutionMockSources(t *testing.T) {
	tests := []corev1.PodSpec{
		{Volumes: []corev1.Volume{{Name: "execution-mock"}}},
		{Volumes: []corev1.Volume{{Name: "other", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "execution-mock"}}}}}},
		{
			Volumes: []corev1.Volume{{
				Name: "other",
				VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
					ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "execution-mock"}},
				}}}},
			}},
		},
		{Containers: []corev1.Container{{Name: "sidecar", VolumeMounts: []corev1.VolumeMount{{Name: "execution-mock"}}}}},
	}
	for i, spec := range tests {
		if err := realProviderConfigError(spec); err == nil {
			t.Fatalf("case %d did not reject execution-mock source", i)
		}
	}
}

func TestSecretReferencesFindEveryCredentialEscape(t *testing.T) {
	const sentinel = "synthetic-live-secret-sentinel"
	tests := map[string]corev1.PodSpec{
		"sidecar env": {
			Containers: []corev1.Container{{Name: "sidecar", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: sentinel}, Key: "token"}}}}}},
		},
		"sidecar envFrom": {
			Containers: []corev1.Container{{Name: "sidecar", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: sentinel}}}}}},
		},
		"init container env": {
			InitContainers: []corev1.Container{{Name: "setup", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: sentinel}, Key: "token"}}}}}},
		},
		"secret volume": {
			Volumes: []corev1.Volume{{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: sentinel}}}},
		},
		"projected secret volume": {
			Volumes: []corev1.Volume{{Name: "credentials", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: sentinel}}}}}}}},
		},
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			if !containsSecretReference(secretReferences(spec), sentinel) {
				t.Fatal("credential escape was not detected")
			}
		})
	}
}

func TestExpectedHarnessCredentialReferenceIsExact(t *testing.T) {
	const sentinel = "synthetic-live-secret-sentinel"
	spec := corev1.PodSpec{Containers: []corev1.Container{{
		Name: "agent",
		Env: []corev1.EnvVar{{Name: liveCredentialKey, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: sentinel},
			Key:                  liveCredentialKey,
		}}}},
	}}}
	want := secretReference{Container: "agent", Source: "env", Name: sentinel, Key: liveCredentialKey, EnvName: liveCredentialKey}
	refs := secretReferences(spec)
	if len(refs) != 1 || refs[0] != want {
		t.Fatal("expected harness credential reference was not projected exactly")
	}
}
