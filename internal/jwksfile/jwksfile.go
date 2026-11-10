// Package jwksfile hands a JSON JWK Set to ToolHive, which reads a delegate
// client's keys only from a file.
package jwksfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// HasKeys reports whether raw is a JSON JWK Set with at least one key.
func HasKeys(raw []byte) bool {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	return json.Unmarshal(raw, &set) == nil && len(set.Keys) > 0
}

// Write writes the key set raw to a new owner-only file in a new private
// temporary directory named with prefix, and returns the file's path and a
// function that removes the directory. ToolHive reads the file once, at
// construction, so callers remove it as soon as construction returns. Errors
// never contain raw.
func Write(prefix string, raw []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, fmt.Errorf("jwksfile: key set directory: %w", err)
	}
	remove := func() { _ = os.RemoveAll(dir) }
	file := filepath.Join(dir, "jwks.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("jwksfile: write key set: %w", err)
	}
	return file, remove, nil
}
