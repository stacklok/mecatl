package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

func TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy(t *testing.T) {
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	old := os.Stderr
	os.Stderr = stderr
	t.Cleanup(func() { os.Stderr = old })
	diag := newDiagnostics()
	diag.Log(context.Background(), port.LevelInfo, "recovery decision", "decision", "recovered")
	if err := stderr.Sync(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "recovery decision") {
		t.Fatalf("mecatequi stderr did not receive recovery log: %q", body)
	}
}
