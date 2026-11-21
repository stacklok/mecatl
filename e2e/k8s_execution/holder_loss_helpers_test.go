//go:build kind_execution_e2e

package k8s_execution_test

import (
	corev1 "k8s.io/api/core/v1"
)

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
