//go:build microvm_dev

package main

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
)

func TestMicroVMDevelopmentFlagsRequirePairAndStayLocal(t *testing.T) {
	if _, err := parseFlags([]string{"--microvm-dev-release=/absolute/release.json"}); err == nil || !strings.Contains(err.Error(), "required together") {
		t.Fatalf("unpaired descriptor error = %v", err)
	}
	if _, _, err := parseTransportFlags(modeConnect, t.Output(), []string{"--microvm-dev-release=/absolute/release.json", "--microvm-dev-acknowledge-untrusted-local-artifacts"}); err == nil {
		t.Fatal("connect mode accepted development release flags")
	}
}

func TestEmbeddedMicroVMDevelopmentActivationUsesCentralExecutionReadiness(t *testing.T) {
	cfg := embeddedConfig(config{
		workspace:             "/workspace",
		model:                 "m",
		mock:                  true,
		microVMDevRelease:     "/nonexistent/development-release.json",
		microVMDevAcknowledge: true,
	}, nil)
	if _, err := cfg.MicroVMReadyRequest(microvmmanager.NewGuestEgressSelection()); err == nil || !strings.Contains(err.Error(), "development release descriptor") {
		t.Fatalf("embedded development readiness error = %v", err)
	}
}

func TestMicroVMDevelopmentModeRejectsReleaseStampedBinary(t *testing.T) {
	_, enabled, err := microVMDevelopmentReadyRequest("/absolute/release.json", true, "source", true)
	if !enabled || err == nil || !strings.Contains(err.Error(), "published release binaries") {
		t.Fatalf("release-stamped development request: enabled=%v err=%v", enabled, err)
	}
}
