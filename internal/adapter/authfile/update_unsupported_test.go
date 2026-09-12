//go:build !linux

package authfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateAPIKeyUnsupportedDoesNotTouchFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.yaml")
	before := []byte("providers:\n  openai:\n    api_key: unchanged\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	key := "replacement"
	state, err := UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
	if state != CommitNotApplied || err == nil {
		t.Fatalf("UpdateAPIKey = %q, %v", state, err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("unsupported update changed file: %q, %v", after, readErr)
	}
	if UpdateSupported() {
		t.Fatal("unsupported platform advertised auth mutation")
	}
}
