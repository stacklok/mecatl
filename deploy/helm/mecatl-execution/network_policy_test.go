package chart_test

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestChartAdmitsOnlyTheProviderToTheExecutorServer(t *testing.T) {
	objects, err := renderLifetime(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	executors := map[string]any{"matchExpressions": []any{map[string]any{
		"key": "execution.mecatl.dev/environment", "operator": "Exists",
	}}}
	serverPort := []any{map[string]any{
		"protocol": "TCP", "port": float64(executionenv.ExecutorServerPort),
	}}
	ingress := objects["NetworkPolicy/test-mecatl-execution-executor-server"]
	wantIngress := map[string]any{
		"podSelector": executors,
		"policyTypes": []any{"Ingress"},
		"ingress": []any{map[string]any{
			"from": []any{map[string]any{"podSelector": map[string]any{
				"matchLabels": map[string]any{
					"app.kubernetes.io/name": "test-mecatl-execution",
				},
			}}},
			"ports": serverPort,
		}},
	}
	if ingress == nil || !reflect.DeepEqual(ingress["spec"], wantIngress) {
		t.Fatalf("executor server ingress: %v", ingress)
	}
	annotations := (&unstructured.Unstructured{Object: ingress}).GetAnnotations()
	if annotations["helm.sh/resource-policy"] != "" {
		t.Fatal("executor server ingress outlives the provider release")
	}
	provider := objects["NetworkPolicy/test-mecatl-execution-provider"]
	egress, _, _ := unstructured.NestedSlice(provider, "spec", "egress")
	wantEgress := map[string]any{
		"to":    []any{map[string]any{"podSelector": executors}},
		"ports": serverPort,
	}
	if len(egress) == 0 || !reflect.DeepEqual(egress[0], wantEgress) {
		t.Fatalf("provider egress to executor servers: %v", egress)
	}
	deny := objects["NetworkPolicy/test-mecatl-execution-workload-default-deny"]
	denied, _, _ := unstructured.NestedSlice(deny, "spec", "ingress")
	if denied == nil || len(denied) != 0 {
		t.Fatalf("workload default deny changed: %v", deny)
	}
}
