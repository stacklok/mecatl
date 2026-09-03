//go:build kind_e2e

package k8s_e2e_test

import (
	"reflect"
	"strings"
	"testing"
)

func TestHardStopPodContainerSelectsApplicationContainerByName(t *testing.T) {
	for _, runtimeName := range []string{"docker", "podman"} {
		t.Run(runtimeName, func(t *testing.T) {
			var calls [][]string
			quiet := func(name string, args ...string) string {
				calls = append(calls, append([]string{name}, args...))
				switch name {
				case "kubectl":
					return `{
						"spec":{"nodeName":"mecatl-e2e-worker","containers":[{"name":"sidecar"},{"name":"agent"}]},
						"status":{"containerStatuses":[
							{"name":"sidecar","containerID":"containerd://wrong"},
							{"name":"agent","containerID":"containerd://deadbeef"}
						]}
					}`
				case "kind":
					return "mecatl-e2e-control-plane\nmecatl-e2e-worker\n"
				default:
					t.Fatalf("unexpected quiet command: %s %v", name, args)
					return ""
				}
			}
			run := func(name string, args ...string) {
				calls = append(calls, append([]string{name}, args...))
			}

			if err := hardStopPodContainerWith(runtimeName, "pod-b", quiet, run); err != nil {
				t.Fatalf("hardStopPodContainerWith: %v", err)
			}
			want := [][]string{
				{"kubectl", "get", "pod", "pod-b", "-n", k8sNamespace, "-o", "json"},
				{"kind", "get", "nodes", "--name", kindClusterName},
				{runtimeName, "exec", "mecatl-e2e-worker", "crictl", "stop", "--timeout", "0", "deadbeef"},
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %#v, want %#v", calls, want)
			}
		})
	}
}

func TestHardStopPodContainerRejectsNodeOutsideCluster(t *testing.T) {
	quiet := func(name string, _ ...string) string {
		if name == "kubectl" {
			return `{
				"spec":{"nodeName":"unrelated-container","containers":[{"name":"agent"}]},
				"status":{"containerStatuses":[{"name":"agent","containerID":"containerd://deadbeef"}]}
			}`
		}
		return "mecatl-e2e-control-plane\n"
	}
	runCalled := false
	err := hardStopPodContainerWith("docker", "pod-b", quiet, func(string, ...string) {
		runCalled = true
	})
	if err == nil || !strings.Contains(err.Error(), "is not in kind cluster") {
		t.Fatalf("hardStopPodContainerWith error = %v, want node-membership rejection", err)
	}
	if runCalled {
		t.Fatal("hardStopPodContainerWith ran stop command for a node outside the cluster")
	}
}
