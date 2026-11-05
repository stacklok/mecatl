package format

import "errors"

// ToolCatalog is a complete session-scoped ordered advertised catalog replacement.
// Nil and empty Tools are equivalent explicit empty replacements. Assignment shares
// Tools and schema bytes; Clone detaches them. History scope is supplied externally.
//
//cborgen:validate Validate
type ToolCatalog struct {
	Tools []ToolDefinition `cbor:"t"`
}

// Validate checks name uniqueness only, not child validity. Codec traversal bounds
// all children before allocating the duplicate-name set.
func (v ToolCatalog) Validate() error {
	seen := make(map[string]struct{}, len(v.Tools))
	for _, tool := range v.Tools {
		if _, ok := seen[tool.Name]; ok {
			return errors.New("duplicate tool name")
		}
		seen[tool.Name] = struct{}{}
	}
	return nil
}
