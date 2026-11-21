//go:generate go -C ../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/session/id -emit cbor,clone,equal -out zz_cbor_generated.go

package id

import (
	"errors"

	resourceid "github.com/stacklok/mecatl/engine/id"
)

// sessionKind belongs to Session, not the generic resource identity utility.
const sessionKind resourceid.Kind = "SES"

// SessionID identifies a Session and its canonical history under the same key.
// Its zero value is unset and invalid. SessionIDs are comparable map keys and
// contain no mutable storage. CBOR is the resource ID text without wrapping.
// Reconstruction proves neither allocation, membership, nor authority.
//
//dtogen:validate validateCBOR
//dtogen:include
type SessionID struct {
	value resourceid.ID `cbor:",value"` //nolint:revive // dtogen's explicit transparent representation, not a standard CBOR tag option.
}

// validateCBOR checks the domain kind after the child validates resource syntax.
func (v SessionID) validateCBOR() error {
	if v.value.Kind() != sessionKind {
		return errors.New("expected session ID")
	}
	return nil
}

// NewSessionID generates a Session identity, panicking if secure randomness is
// unavailable. Storage must enforce create-only uniqueness; generation reserves
// nothing, and an identity must not be reused after deletion.
func NewSessionID() SessionID {
	return SessionID{value: resourceid.New(sessionKind)}
}

// ParseSessionID validates exact Session-kind text without normalization.
// Failure returns the zero identity.
func ParseSessionID(s string) (SessionID, error) {
	v, err := resourceid.Parse(s)
	if err != nil {
		return SessionID{}, err
	}
	id := SessionID{value: v}
	if err := id.validateCBOR(); err != nil {
		return SessionID{}, err
	}
	return id, nil
}

// ResourceID returns the same identity for generic history operations, or zero
// for an unset SessionID. It does not allocate or map to another identity.
func (v SessionID) ResourceID() resourceid.ID { return v.value }

// String returns the full identity, or an empty string for zero.
func (v SessionID) String() string { return v.value.String() }
