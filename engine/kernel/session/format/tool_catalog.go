package format

import "errors"

// ToolCatalog is a complete session-scoped ordered advertised catalog replacement.
// Nil and empty Tools are equivalent explicit empty replacements. Assignment shares
// Tools and schema bytes; Clone detaches them. History identity is omitted here
// and restored from the enclosing envelope at the domain conversion boundary.
//
//cborgen:validate Validate
type ToolCatalog struct {
	// Tools preserves advertised order. Names must be unique within this replacement
	// (checked locally); codec traversal validates each definition. Nil and empty
	// explicitly replace the catalog with no tools, without granting execution rights.
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
