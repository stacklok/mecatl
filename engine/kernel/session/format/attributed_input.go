package format

import (
	"errors"
	"unicode/utf8"
)

// AttributedInput retains content, a nonempty UTF-8 logical-origin label, purpose
// and provenance. Actor is not a provider role, authentication or trust claim.
// Assignment shares Parts and Source storage; Clone detaches them.
//
//cborgen:validate Validate
type AttributedInput struct {
	Parts   []ContentPart `cbor:"c"`
	Actor   string        `cbor:"a"`
	Purpose string        `cbor:"p"`
	Source  ContentSource `cbor:"s"`
}

// Validate checks actor and purpose locally, not child validity or authority.
func (v AttributedInput) Validate() error {
	if v.Actor == "" || !utf8.ValidString(v.Actor) {
		return errors.New("actor must be nonempty UTF-8")
	}
	if v.Purpose != "instructions" && v.Purpose != "context" {
		return errors.New("unknown input purpose")
	}
	return nil
}
