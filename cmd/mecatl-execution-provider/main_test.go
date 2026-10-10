package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderStartupRejectsLegacyProfileConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte("profiles:\n  go: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFlags, oldArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
	flag.CommandLine = flag.NewFlagSet("execution-provider-test", flag.ContinueOnError)
	os.Args = []string{"execution-provider", "--namespace=ns", "--executor-service-account=executor", "--security-authority-configmap=authority", "--templates=" + path}
	if err := run(); err == nil || !strings.Contains(err.Error(), "decode execution templates") {
		t.Fatalf("legacy operator config was accepted or reached Kubernetes: %v", err)
	}
}
