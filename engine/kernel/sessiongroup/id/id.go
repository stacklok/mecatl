//go:generate go -C ../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/sessiongroup/id -emit cbor,clone,equal -out zz_cbor_generated.go

package id

import (
	"errors"

	resourceid "github.com/stacklok/mecatl/engine/id"
)

// groupKind belongs to SessionGroup, not the generic resource identity utility.
const groupKind resourceid.Kind = "GRP"

// GroupID identifies a SessionGroup independently of its members and hosts.
// Its zero value is unset and invalid. GroupIDs are comparable map keys with no
// mutable storage. CBOR is the resource ID text without wrapping.
// Reconstruction proves neither existence, membership, nor hosting authority.
// A group keeps its identity across residency and lease-incarnation changes.
//
//dtogen:validate validateCBOR
//dtogen:include
type GroupID struct {
	value resourceid.ID `cbor:",value"` //nolint:revive // dtogen's explicit transparent representation, not a standard CBOR tag option.
}

// validateCBOR checks the domain kind after the child validates resource syntax.
func (v GroupID) validateCBOR() error {
	if v.value.Kind() != groupKind {
		return errors.New("expected session group ID")
	}
	return nil
}

// NewGroupID generates a group identity, panicking if secure randomness is
// unavailable. It reserves nothing; storage must enforce create-only uniqueness
// and prevent reuse after deletion.
func NewGroupID() GroupID {
	return GroupID{value: resourceid.New(groupKind)}
}

// ParseGroupID validates exact group-kind text without normalization.
// Failure returns the zero identity.
func ParseGroupID(s string) (GroupID, error) {
	v, err := resourceid.Parse(s)
	if err != nil {
		return GroupID{}, err
	}
	id := GroupID{value: v}
	if err := id.validateCBOR(); err != nil {
		return GroupID{}, err
	}
	return id, nil
}

// ResourceID returns the same generic resource identity, or zero for unset.
// A group is not a Session and does not imply a group history exists.
func (v GroupID) ResourceID() resourceid.ID { return v.value }

// String returns the full identity, or an empty string for zero.
func (v GroupID) String() string { return v.value.String() }
