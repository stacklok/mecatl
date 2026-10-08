//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestHolderLossTerminalStimulusPinsOwnedPod(t *testing.T) {
	for _, changed := range []string{"", "uid", "owner", "absent"} {
		t.Run(changed, func(t *testing.T) {
			controlled := true
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "executor", Namespace: namespace, UID: "pod-uid", Labels: map[string]string{"execution.mecatl.dev/environment": "env"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "execution.mecatl.dev/v1alpha1", Kind: "ExecutionEnvironment", Name: "env", UID: "env-uid", Controller: &controlled}}}}
			if changed == "uid" {
				pod.UID = "foreign"
			}
			if changed == "owner" {
				pod.OwnerReferences[0].UID = "foreign"
			}
			k := kubefake.NewClientset(pod)
			if changed == "absent" {
				k = kubefake.NewClientset()
			}
			deletes := 0
			k.PrependReactor("delete", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
				deletes++
				opts := a.(ktesting.DeleteAction).GetDeleteOptions()
				if opts.GracePeriodSeconds == nil || *opts.GracePeriodSeconds != 30 || opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "pod-uid" {
					t.Fatal("deletion must pin UID with normal grace")
				}
				return true, nil, nil
			})
			err := terminateOwnedExecutor(context.Background(), k, "env", "env-uid", "pod-uid")
			if (err == nil) != (changed == "") || deletes != map[bool]int{true: 1, false: 0}[changed == ""] {
				t.Fatalf("deletes=%d err=%v", deletes, err)
			}
			for _, a := range k.Actions() {
				if a.GetVerb() != "list" && a.GetVerb() != "delete" {
					t.Fatal("stimulus must not patch finalizers")
				}
			}
		})
	}
}

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
