package mcpbroker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// TEMPORARY, DELIBERATE DUPLICATE: hasKeys and writeKeySet have a twin in
// internal/identitybroker/adapter/as1server/keysetfile.go, which registers the
// broker client at AS-1; fix both together. They are not shared because the
// identity broker must move into its own service as one tree
// (docs/drafts/agent-identity/implementation.md, "Modulith constraints"), so
// as1server imports nothing from Mecatl outside internal/identitybroker.
// Both copies exist only because the broker client key is generated at
// startup: https://github.com/stacklok/mecatl/issues/2538 replaces it with an
// operator-supplied key-set file whose path both servers read, and deletes
// both copies.

// hasKeys reports whether raw is a JSON JWK Set with at least one key.
func hasKeys(raw []byte) bool {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	return json.Unmarshal(raw, &set) == nil && len(set.Keys) > 0
}

// writeKeySet writes the key set raw to a new owner-only file in a new private
// temporary directory named with prefix, for ToolHive, which reads a delegate
// client's keys only from a file. It returns the file's path and a function
// that removes the directory. ToolHive reads the file once, at construction,
// so callers remove it as soon as construction returns. Errors never contain
// raw.
func writeKeySet(prefix string, raw []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, fmt.Errorf("key set directory: %w", err)
	}
	remove := func() { _ = os.RemoveAll(dir) }
	file := filepath.Join(dir, "jwks.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("write key set: %w", err)
	}
	return file, remove, nil
}
