package format

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ToolDefinition retains one advertised tool in a ToolCatalog, not an executable
// implementation. ArgumentSchema is exact nonempty JSON, including boolean schemas,
// without schema interpretation. Assignment shares schema bytes; Clone detaches them.
//
//cborgen:validate Validate
type ToolDefinition struct {
	// Name is the required nonempty exact UTF-8 advertised tool name, not an
	// implementation identity. The enclosing ToolCatalog checks name uniqueness.
	Name string `cbor:"n"`
	// Description is exact UTF-8 model-facing descriptive text; empty is valid.
	Description string `cbor:"d"`
	// ArgumentSchema is required explicit valid JSON, including boolean schemas.
	// Bytes are preserved exactly; Validate checks JSON syntax, not schema semantics.
	ArgumentSchema []byte `cbor:"s"`
}

// Validate checks immediate strings and JSON syntax. Bounded codec traversal runs
// before this local validator; direct calls impose no operational resource policy.
func (v ToolDefinition) Validate() error {
	if v.Name == "" || !utf8.ValidString(v.Name) || !utf8.ValidString(v.Description) {
		return errors.New("tool requires a nonempty UTF-8 name and UTF-8 description")
	}
	if !json.Valid(v.ArgumentSchema) {
		return errors.New("tool argument schema must be explicit valid JSON")
	}
	return nil
}

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
