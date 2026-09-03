package server_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionHeaderVectorCopiesMatchCanonical(t *testing.T) {
	canonicalPath := filepath.Join("..", "..", "..", "testdata", "session_header_values.json")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical session-header vectors: %v", err)
	}
	for _, path := range []string{
		filepath.Join("..", "..", "..", "provider", "anthropic", "testdata", "session_header_values.json"),
		filepath.Join("..", "..", "..", "provider", "openai", "testdata", "session_header_values.json"),
		filepath.Join("..", "..", "..", "provider", "openaichat", "testdata", "session_header_values.json"),
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Errorf("read module-local vectors %s: %v", path, readErr)
			continue
		}
		if !bytes.Equal(got, canonical) {
			t.Errorf("module-local vectors %s differ from canonical %s", path, canonicalPath)
		}
	}
}
