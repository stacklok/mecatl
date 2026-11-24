package groupstorage

import (
	"fmt"

	resourceid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

// MemberPageRequest selects permanent group members after a same-kind cursor.
type MemberPageRequest struct {
	// After is an exclusive cursor. Zero starts the scan. When nonzero, it must
	// have the requested kind. Full ID text orders cursors only; it does not imply
	// temporal ordering.
	After resourceid.ID
	// Limit is a positive maximum page size. Servers enforce it before allocating
	// the result.
	Limit int
}

// Validate checks the requested kind, positive limit, and same-kind cursor. It
// does not mint an ID. Errors wrap history.ErrInvalid without rendering caller
// values.
func (r MemberPageRequest) Validate(kind resourceid.Kind) error {
	if len(kind) != 3 {
		return fmt.Errorf("%w: invalid member kind", history.ErrInvalid)
	}
	for i := range len(kind) {
		if kind[i] < 'A' || kind[i] > 'Z' {
			return fmt.Errorf("%w: invalid member kind", history.ErrInvalid)
		}
	}
	if r.Limit <= 0 {
		return fmt.Errorf("%w: member page limit must be positive", history.ErrInvalid)
	}
	if r.After != (resourceid.ID{}) && r.After.Kind() != kind {
		return fmt.Errorf("%w: member cursor kind mismatch", history.ErrInvalid)
	}
	return nil
}

// MemberPage is one owned page of permanent group members. Members contains at
// most the request Limit entries, ordered by full ID text. Next is zero at
// exhaustion; otherwise it is the last returned ID. A successful nonterminal
// page is never empty. The page has no cross-page snapshot guarantee.
type MemberPage struct {
	// Members is an owned slice of permanent member IDs in full-text order. Its
	// length is bounded by the request Limit.
	Members []resourceid.ID
	// Next is zero at exhaustion; otherwise it equals the final Members entry.
	Next resourceid.ID
}
