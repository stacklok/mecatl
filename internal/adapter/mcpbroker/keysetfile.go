package mcpbroker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DELIBERATE DUPLICATE: checkKeySetFile has a twin in
// internal/identitybroker/adapter/as1server/keysetfile.go, which registers the
// broker client at AS-1; fix both together. They are not shared because the
// identity broker must move into its own service as one tree, so as1server
// imports nothing from Mecatl outside internal/identitybroker.

// checkKeySetFile refuses path unless it is absolute and holds a JSON JWK Set
// with at least one key, so a bad file fails before ToolHive reads it.
// Errors never contain the file's contents.
func checkKeySetFile(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("JWK Set file %q is not an absolute path", path)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator names the file.
	if err != nil {
		return fmt.Errorf("JWK Set file: %w", err)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if json.Unmarshal(raw, &set) != nil {
		return fmt.Errorf("JWK Set file %s is not a JSON JWK Set", path)
	}
	if len(set.Keys) == 0 {
		return fmt.Errorf("JWK Set file %s holds no key", path)
	}
	return nil
}
