package main

import "testing"

func TestWorkspaceAuthorityFlagIsRemoved(t *testing.T) {
	if _, err := parseFlags([]string{"--workspace-authority", "client-selected"}); err == nil {
		t.Fatal("removed --workspace-authority flag was accepted")
	}
}
