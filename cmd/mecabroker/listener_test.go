package main

import "testing"

func TestProductionAdminAddressIsFixed(t *testing.T) {
	if defaultAdminAddress != "127.0.0.1:8081" {
		t.Fatalf("admin listener and local client = %q, want 127.0.0.1:8081", defaultAdminAddress)
	}
}
