package groupstorage

import "errors"

var (
	// ErrReleased reports that this exact generation was released. Release is
	// immediate, idempotent, and exact-generation scoped, so stale cleanup cannot
	// harm a successor.
	ErrReleased = errors.New("group generation released")
	// ErrSuperseded reports that a different generation took over the group.
	ErrSuperseded = errors.New("group generation superseded")
	// ErrValidityUnknown means the backend cannot establish current validity. It
	// is not observed release or takeover and does not itself establish expiry.
	ErrValidityUnknown = errors.New("group generation validity unknown")
	// ErrNotMember means a well-formed resource is definitively not a permanent
	// member of this group. It is not an infrastructure or identity error.
	ErrNotMember = errors.New("resource is not a group member")
)
