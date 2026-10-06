//go:build microvm_dev

package main

import (
	"strings"
	"testing"
)

func TestMicroVMDevelopmentFlagsRequirePairAndServeMode(t *testing.T) {
	if _, err := parseFlagsMode(modeServe, []string{"--microvm-dev-release=/absolute/release.json"}); err == nil || !strings.Contains(err.Error(), "required together") {
		t.Fatalf("unpaired descriptor error = %v", err)
	}
	if _, err := parseFlagsMode(modeACP, []string{"--microvm-dev-release=/absolute/release.json", "--microvm-dev-acknowledge-untrusted-local-artifacts"}); err == nil || !strings.Contains(err.Error(), "mecated serve") {
		t.Fatalf("ACP development release error = %v", err)
	}
}

func TestMicroVMDevelopmentModeRejectsReleaseStampedBinary(t *testing.T) {
	_, enabled, err := microVMDevelopmentReadyRequest("/absolute/release.json", true, "source", true)
	if !enabled || err == nil || !strings.Contains(err.Error(), "published release binaries") {
		t.Fatalf("release-stamped development request: enabled=%v err=%v", enabled, err)
	}
}
