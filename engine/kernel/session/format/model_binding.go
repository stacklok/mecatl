package format

import (
	"errors"
	"unicode/utf8"
)

// ModelBinding retains declared logical preparation choices, not observed upstream
// identity or compatibility. Settings retains explicit prepared typed controls,
// never inherited defaults or credentials. Assignment shares Settings pointers;
// Clone detaches them. Zero is invalid. Session identity is omitted here and
// restored from the root history envelope at the domain conversion boundary.
//
//dtogen:validate validateLocal
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
	// Settings is required explicit prepared controls. Nil members intentionally
	// omit controls, not inherit defaults. Codec traversal validates children.
	Settings ModelSettings `cbor:"s"`
}

// Validate checks binding strings and typed settings, without provider semantics.
func (v ModelBinding) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	return v.Settings.Validate()
}

// validateLocal checks identifiers after codec traversal validates Settings.
func (v ModelBinding) validateLocal() error {
	if v.Route == "" || v.API == "" || v.Model == "" ||
		!utf8.ValidString(v.Route) || !utf8.ValidString(v.API) || !utf8.ValidString(v.Model) {
		return errors.New("model binding requires nonempty UTF-8 logical identifiers")
	}
	return nil
}
