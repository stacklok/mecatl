//go:build !linux

package permconfig

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

func TestUpdateDefaultsUnsupportedDoesNotTouchFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	before := []byte("models:\n  default_provider: old\n  default: old/model\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := UpdateDefaults(context.Background(), path, DefaultUpdate{Provider: "openai", Model: "gpt-5"})
	if state != authfile.CommitNotApplied || err == nil {
		t.Fatalf("UpdateDefaults = %q, %v", state, err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("unsupported update changed file: %q, %v", after, readErr)
	}
}
