//go:build microvm_dev

package main

import (
	"strings"
	"testing"
)

func TestMicroVMDevelopmentFlagsRequirePairAndLocalProfile(t *testing.T) {
	if _, err := parseFlags([]string{"--default-placement=microvm-local", "--microvm-dev-release=/absolute/release.json"}); err == nil || !strings.Contains(err.Error(), "required together") {
		t.Fatalf("unpaired descriptor error = %v", err)
	}
	if _, _, err := parseTransportFlags(modeConnect, t.Output(), []string{"--microvm-dev-release=/absolute/release.json", "--microvm-dev-acknowledge-untrusted-local-artifacts"}); err == nil {
		t.Fatal("connect mode accepted development release flags")
	}
	if _, err := parseFlags([]string{"--microvm-dev-release=/absolute/release.json", "--microvm-dev-acknowledge-untrusted-local-artifacts"}); err == nil || !strings.Contains(err.Error(), "--default-placement microvm-local") {
		t.Fatalf("missing profile error = %v", err)
	}
}

func TestMicroVMDevelopmentModeRejectsReleaseStampedBinary(t *testing.T) {
	_, enabled, err := microVMDevelopmentReadyRequest("/absolute/release.json", true, "source", true)
	if !enabled || err == nil || !strings.Contains(err.Error(), "published release binaries") {
		t.Fatalf("release-stamped development request: enabled=%v err=%v", enabled, err)
	}
}
