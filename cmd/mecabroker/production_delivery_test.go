package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSingletonBrokerRemediation_Scenario5_ProductionArtifacts(t *testing.T) {
	root := filepath.Join("..", "..")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "task", "build:broker")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("task build:broker: %v\n%s", err, output)
	}
	binary := filepath.Join(root, "bin", "mecabroker")
	info, err := os.Stat(binary)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("built mecabroker is not executable: %v, %#o", err, info.Mode())
	}

	chart := exec.CommandContext(ctx, "helm", "lint", "deploy/helm/mecak8s", "-f", "deploy/helm/mecak8s/ci/broker-mcp-values.yaml")
	chart.Dir = root
	if output, err := chart.CombinedOutput(); err != nil {
		t.Fatalf("helm lint mecak8s: %v\n%s", err, output)
	}
}
