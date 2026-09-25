//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

func terminateOwnedExecutor(ctx context.Context, kube kubernetes.Interface, environmentID, environmentUID, podUID string) error {
	pods, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "execution.mecatl.dev/environment=" + environmentID})
	if err != nil {
		return err
	}
	if len(pods.Items) != 1 || podUID == "" || environmentUID == "" {
		return errors.New("exact owned executor unavailable")
	}
	pod := pods.Items[0]
	if string(pod.UID) != podUID || len(pod.OwnerReferences) != 1 {
		return errors.New("executor identity changed")
	}
	owner := pod.OwnerReferences[0]
	if owner.APIVersion != "execution.mecatl.dev/v1alpha1" || owner.Kind != "ExecutionEnvironment" || owner.Name != environmentID || string(owner.UID) != environmentUID || owner.Controller == nil || !*owner.Controller {
		return errors.New("executor owner changed")
	}
	uid, grace := types.UID(podUID), int64(30)
	return kube.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &grace, Preconditions: &metav1.Preconditions{UID: &uid}})
}

func executorTerminalProof(pod *corev1.Pod, uid string) bool {
	if pod == nil || string(pod.UID) != uid || uid == "" || (pod.Status.Phase != corev1.PodFailed && pod.Status.Phase != corev1.PodSucceeded) {
		return false
	}
	names := make(map[string]bool)
	for _, c := range pod.Spec.Containers {
		names[c.Name] = false
	}
	for _, c := range pod.Spec.InitContainers {
		names[c.Name] = false
	}
	for _, c := range pod.Spec.EphemeralContainers {
		names[c.Name] = false
	}
	statuses := append(append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...), pod.Status.EphemeralContainerStatuses...)
	if len(pod.Spec.Containers) == 0 || len(statuses) != len(names) {
		return false
	}
	for _, s := range statuses {
		done, found := names[s.Name]
		if !found || done || s.State.Terminated == nil {
			return false
		}
		names[s.Name] = true
	}
	return true
}
