package mcpbrokergrpc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectedTokenCredentialsRereadEachRPCAndFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	credentials := ProjectedTokenCredentials{Path: path}
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("first-token\n")
	metadata, err := credentials.GetRequestMetadata(t.Context())
	if err != nil || metadata["authorization"] != "Bearer first-token" {
		t.Fatalf("first RPC metadata = %v, %v", metadata, err)
	}
	write("second-token\n")
	metadata, err = credentials.GetRequestMetadata(t.Context())
	if err != nil || metadata["authorization"] != "Bearer second-token" {
		t.Fatalf("rotated RPC metadata = %v, %v", metadata, err)
	}

	write("\n")
	if metadata, err = credentials.GetRequestMetadata(t.Context()); err == nil || metadata != nil {
		t.Fatalf("empty rotated credential = %v, %v; want fail-closed", metadata, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if metadata, err = credentials.GetRequestMetadata(t.Context()); err == nil || metadata != nil {
		t.Fatalf("missing rotated credential = %v, %v; want fail-closed", metadata, err)
	}
}
