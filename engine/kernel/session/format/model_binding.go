package format

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ModelBinding retains declared logical preparation choices, not observed upstream
// identity or compatibility. Settings is exact explicit JSON supplied by the caller,
// never resolved defaults or credentials. Assignment shares Settings; Clone detaches
// it. Zero is invalid. History identity is omitted here and restored from the
// enclosing envelope at the domain conversion boundary.
//
//dtogen:validate Validate
//dtogen:include
type ModelBinding struct {
	// Route is a required nonempty exact UTF-8 logical provider/route name interpreted
	// relative to the preparing harness's configuration, not a global provider
	// identity or an observed upstream endpoint. Loading does not resolve it.
	Route string `cbor:"r"`
	// API is the required nonempty exact UTF-8 declared API name, not a compatibility
	// assertion. Validate checks syntax only, without provider interpretation.
	API string `cbor:"a"`
	// Model is the required nonempty exact UTF-8 declared model name, not an observed
	// upstream model identity. Loading does not resolve or verify it.
	Model string `cbor:"m"`
	// Settings is required explicit valid JSON, preserved byte-for-byte, not resolved
	// defaults or credentials. Validate checks JSON syntax, not provider policy.
	Settings []byte `cbor:"s"`
}

// Validate checks immediate binding strings and JSON syntax, without provider
// semantics. Bounded codec traversal precedes JSON parsing on codec paths.
func (v ModelBinding) Validate() error {
	if v.Route == "" || v.API == "" || v.Model == "" ||
		!utf8.ValidString(v.Route) || !utf8.ValidString(v.API) || !utf8.ValidString(v.Model) {
		return errors.New("model binding requires nonempty UTF-8 logical identifiers")
	}
	if !json.Valid(v.Settings) {
		return errors.New("model settings must be explicit valid JSON")
	}
	return nil
}
