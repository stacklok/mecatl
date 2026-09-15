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
