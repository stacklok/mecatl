package format

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ToolDefinition retains an advertised tool, not an executable implementation.
// ArgumentSchema is exact nonempty JSON, including boolean schemas, without schema
// interpretation. Assignment shares schema bytes; Clone detaches them.
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
