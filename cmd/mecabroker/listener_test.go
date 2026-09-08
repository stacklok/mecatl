package main

import "testing"

func TestProductionListenerDefaults(t *testing.T) {
	if defaultPublicAddress != ":8443" {
		t.Fatalf("public listener default = %q, want :8443", defaultPublicAddress)
	}
	if defaultAdminAddress != "127.0.0.1:8081" {
		t.Fatalf("admin listener and local client default = %q, want 127.0.0.1:8081", defaultAdminAddress)
	}
}
