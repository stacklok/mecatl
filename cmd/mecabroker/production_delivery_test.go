package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialProductionMCPBroker_Scenario5_BuildSurface(t *testing.T) {
	root := filepath.Join("..", "..")
	files := map[string][]string{
		"Taskfile.yml": {"bin/mecabroker", "./cmd/mecabroker", "ko:build:broker", "ko:publish:broker"},
		".ko.yaml":     {"id: mecabroker", "main: ./cmd/mecabroker"},
		filepath.Join(".github", "workflows", "release.yml"): {"publish-mecabroker", "./cmd/mecabroker", "org.opencontainers.image.title=mecabroker"},
	}
	for name, required := range files {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, want := range required {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s missing %q", name, want)
			}
		}
	}
}

func TestADR_0305_StandaloneBrokerTopology(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "adr", "0305-single-replica-mcp-broker-topology.md"))
	if err != nil {
		t.Fatalf("read topology ADR: %v", err)
	}
	text := string(body)
	for _, want := range []string{"replica", "Recreate", "restart interrupts", "Resource ledger", "operator-provided", "No PDB"} {
		if !strings.Contains(text, want) {
			t.Errorf("topology ADR missing %q", want)
		}
	}
}

func TestInitialProductionMCPBroker_Scenario5_DeploymentSecurity(t *testing.T) {
	chart := filepath.Join("..", "..", "deploy", "helm", "mecabroker")
	files := []string{"Chart.yaml", "values.yaml", "templates/deployment.yaml", "templates/service.yaml", "templates/serviceaccount.yaml", "templates/networkpolicy.yaml"}
	var deployment string
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(chart, name))
		if err != nil {
			t.Fatalf("read dedicated mecabroker chart %s: %v", name, err)
		}
		if name == "templates/deployment.yaml" || name == "values.yaml" {
			deployment += string(body)
		}
	}
	for _, want := range []string{
		"replicas: 1", "type: Recreate", "serviceAccountName:", "automountServiceAccountToken: false",
		"runAsNonRoot: true", "RuntimeDefault", "readOnlyRootFilesystem: true", "allowPrivilegeEscalation: false",
		"drop: [\"ALL\"]", "requests:", "limits:", "readinessProbe:", "livenessProbe:", "preStop:",
		"terminationGracePeriodSeconds:", "tls", "workload-identity", "admin",
	} {
		if !strings.Contains(deployment, want) {
			t.Errorf("broker deployment missing %q", want)
		}
	}
	entries, err := os.ReadDir(filepath.Join(chart, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Name()), "pdb") || strings.Contains(strings.ToLower(entry.Name()), "hpa") {
			t.Errorf("one-replica chart must not contain %s", entry.Name())
		}
	}
	network, err := os.ReadFile(filepath.Join(chart, "templates", "networkpolicy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	policy := string(network)
	for _, want := range []string{"publicFrom", "operatorEgress", "namespaceSelector", "podSelector", "ipBlock"} {
		if !strings.Contains(policy, want) {
			t.Errorf("broker NetworkPolicy missing %q", want)
		}
	}
	for _, forbidden := range []string{"fqdn", "FQDN", "example.com"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("broker NetworkPolicy makes hostname-shaped enforcement claim %q", forbidden)
		}
	}
}
