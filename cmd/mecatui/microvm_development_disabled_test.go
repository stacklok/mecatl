//go:build !microvm_dev

package main

import "testing"

func TestOrdinaryBuildDoesNotExposeMicroVMDevelopmentFlags(t *testing.T) {
	if _, err := parseFlags([]string{"--microvm-dev-release=/absolute/release.json"}); err == nil {
		t.Fatal("ordinary mecatui build exposed --microvm-dev-release")
	}
}
