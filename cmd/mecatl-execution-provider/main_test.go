package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
)

func TestKeepaliveGraceOutlastsTheLongestCommand(t *testing.T) {
	grace := serverKeepalive().MaxConnectionAgeGrace
	if grace < executioncontroller.MaxCommandDuration+time.Minute {
		t.Fatalf("grace %v cannot finish a %v command started as the connection ages out",
			grace, executioncontroller.MaxCommandDuration)
	}
}

func TestProviderStartupRejectsLegacyProfileConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte("profiles:\n  go: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFlags, oldArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
	flag.CommandLine = flag.NewFlagSet("execution-provider-test", flag.ContinueOnError)
	os.Args = []string{
		"execution-provider",
		"--namespace=ns",
		"--executor-service-account=executor",
		"--security-authority-configmap=authority",
		"--templates=" + path,
		"--executor-client-uri=spiffe://mecatl.test/provider",
		"--executor-ca-secret=roots",
		"--executor-trust-configmap=trust",
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "decode execution templates") {
		t.Fatalf("legacy operator config was accepted or reached Kubernetes: %v", err)
	}
}

func TestProviderStartupRequiresExecutorTrust(t *testing.T) {
	oldFlags, oldArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
	flag.CommandLine = flag.NewFlagSet("execution-provider-test", flag.ContinueOnError)
	os.Args = []string{
		"execution-provider",
		"--namespace=ns",
		"--executor-service-account=executor",
		"--security-authority-configmap=authority",
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "--executor-client-uri") {
		t.Fatalf("provider started without executor trust: %v", err)
	}
}
