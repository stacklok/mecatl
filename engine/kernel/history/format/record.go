package format

import (
	"errors"
	"unicode/utf8"
)

// Record is a mutable persistence DTO carrying an owner-defined payload schema.
// Unknown kind/version pairs remain opaque; preservation does not establish safe
// interpretation, replay, or display. Assignment shares payload storage.
// Construction, cloning, and equality do not validate the format contract.
//
//dtogen:validate validateCBOR
//dtogen:include
type Record struct {
	// Timestamp is the author-supplied diagnostic recording time in signed Unix
	// nanoseconds. Session sets it on successful local admission; it claims no action-occurrence
	// or storage time. Every int64 value is valid, including zero (the Unix epoch)
	// and negatives.
	Timestamp int64 `cbor:"t"`
	// Kind identifies the payload's record kind.
	Kind string `cbor:"k"`
	// KindVersion is the positive payload schema version, not an instance revision.
	KindVersion int64 `cbor:"v"`
	// Payload is required opaque bytes. Nil and empty both represent empty bytes.
	Payload []byte `cbor:"p"`
}

// validateCBOR checks the local record envelope without interpreting payloads.
func (r Record) validateCBOR() error {
	if r.Kind == "" || !utf8.ValidString(r.Kind) {
		return errors.New("record kind must be nonempty UTF-8")
	}
	if r.KindVersion <= 0 {
		return errors.New("record version must be positive")
	}
	return nil
}

// Validate checks the record envelope without interpreting its opaque payload or
// restricting kind namespaces or positive versions. It neither mutates nor
// freezes storage and applies no resource budget. Keep the input stable during
// the call. Validation establishes no acceptance or authorization.
func (r Record) Validate() error { return r.validateCBOR() }
