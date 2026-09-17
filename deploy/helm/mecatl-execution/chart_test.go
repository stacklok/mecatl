package chart_test

import (
	"os"
	"strings"
	"testing"
)

func TestChartRetainsCRDAndDoesNotGrantSecretAPI(t *testing.T) {
	crd, err := os.ReadFile("crds/executionenvironment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(crd), "helm.sh/resource-policy: keep") {
		t.Fatal("CRD is not retained on uninstall")
	}
	rbac, err := os.ReadFile("templates/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rbac), "secrets") || strings.Contains(string(rbac), "clusterrole") {
		t.Fatal("provider RBAC must be namespaced and have no Secret access")
	}
}
func TestChartHasNoDeletionHook(t *testing.T) {
	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, err := os.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "helm.sh/hook") || strings.Contains(string(b), "PersistentVolumeClaim\nmetadata") {
			t.Fatalf("unsafe lifecycle resource in %s", entry.Name())
		}
	}
}

func TestProductionHardeningIsFailClosed(t *testing.T) {
	provider, err := os.ReadFile("templates/provider.yaml")
	if err != nil {
		t.Fatal(err)
	}
	network, err := os.ReadFile("templates/network-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	values, err := os.ReadFile("values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"runAsNonRoot: true", "seccompProfile", "capabilities: {drop: [\"ALL\"]}", "readOnlyRootFilesystem: true", "httpGet: {path: /ready", "security-authority-configmap", "projected:", "maxUnavailable: 0"} {
		if !strings.Contains(string(provider), required) {
			t.Fatalf("provider hardening missing %q", required)
		}
	}
	for _, required := range []string{"workload-default-deny", "ingress: []", "egress: []", "ResourceQuota", "LimitRange", "count/executionenvironments.execution.mecatl.dev"} {
		if !strings.Contains(string(network), required) {
			t.Fatalf("network/resource hardening missing %q", required)
		}
	}
	if strings.Contains(string(network), "0.0.0.0/0") || !strings.Contains(string(values), `"minimum":2`) {
		t.Fatal("chart permits an unsafe replica or egress default")
	}
}
