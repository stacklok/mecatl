package format

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ModelBinding retains declared logical preparation choices, not observed upstream
// identity or compatibility. Settings is exact explicit JSON supplied by the caller,
// never resolved defaults or credentials. Assignment shares Settings; Clone detaches
// it. Zero is invalid; history scope comes from the enclosing envelope.
//
//cborgen:validate Validate
type ModelBinding struct {
	Route    string `cbor:"r"`
	API      string `cbor:"a"`
	Model    string `cbor:"m"`
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
