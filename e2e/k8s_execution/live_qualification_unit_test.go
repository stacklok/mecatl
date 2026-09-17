//go:build kind_execution_e2e

package k8s_execution_test

import (
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
