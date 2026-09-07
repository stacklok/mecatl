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

	deploy := exec.CommandContext(ctx, "task", "deploy:check")
	deploy.Dir = root
	if output, err := deploy.CombinedOutput(); err != nil {
		t.Fatalf("task deploy:check: %v\n%s", err, output)
	}
}
