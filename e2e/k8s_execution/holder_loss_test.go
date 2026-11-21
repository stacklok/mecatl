//go:build kind_execution_e2e

package k8s_execution_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHolderLossTerminalProofRequiresExactCompleteEvidence(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-uid"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "executor"}}, InitContainers: []corev1.Container{{Name: "init"}}}, Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{Name: "executor", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}, InitContainerStatuses: []corev1.ContainerStatus{{Name: "init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}}}
	if !executorTerminalProof(pod, "pod-uid") {
		t.Fatal("terminal proof rejected")
	}
	for _, mutate := range []func(*corev1.Pod){func(p *corev1.Pod) { p.UID = "foreign" }, func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning }, func(p *corev1.Pod) { p.Status.InitContainerStatuses = nil }, func(p *corev1.Pod) { p.Status.ContainerStatuses[0].Name = "other" }, func(p *corev1.Pod) { p.Status.ContainerStatuses[0].State.Terminated = nil }} {
		candidate := pod.DeepCopy()
		mutate(candidate)
		if executorTerminalProof(candidate, "pod-uid") {
			t.Fatal("incomplete or foreign evidence accepted")
		}
	}
	if executorTerminalProof(nil, "pod-uid") {
		t.Fatal("absence is not terminal proof")
	}
}
