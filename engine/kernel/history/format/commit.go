package format

import (
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// Commit is a mutable persistence DTO for an atomic, nonempty ordered group of
// records. It is not an accepted-state aggregate or an immutable snapshot.
// Assignment shares pointers, record and payload storage; Clone detaches them.
// Construction, cloning, and equality do not validate the format contract.
//
//cborgen:validate validateCBOR
type Commit struct {
	// FormatVersion is the commit/record envelope version, currently 1.
	FormatVersion int64 `cbor:"v"`
	// HistoryID is present only for a root commit. Present-zero is invalid.
	HistoryID *historyid.HistoryID `cbor:"h,omitempty"`
	// Parent is present only for a successor. Present-zero is invalid.
	Parent *contentid.CID `cbor:"p,omitempty"`
	// Sequence is the one-based commit ordinal. Roots are exactly 1; successors
	// are greater than 1. Parent+1 is checked by acceptance or reconstruction
	// with the parent available, guarding overflow, not by this DTO.
	Sequence int64 `cbor:"s"`
	// Records is the nonempty group in authoritative order.
	Records []Record `cbor:"r"`
}

// validateCBOR checks only local envelope invariants; generated traversal owns
// child validation and resource accounting.
func (c Commit) validateCBOR() error {
	if c.FormatVersion != 1 {
		return errors.New("unsupported commit format version")
	}
	if (c.Parent == nil) == (c.HistoryID == nil) {
		return errors.New("commit requires exactly one of history ID or parent")
	}
	if c.Parent == nil {
		if c.Sequence != 1 {
			return errors.New("root commit sequence must be 1")
		}
	} else if c.Sequence <= 1 {
		return errors.New("successor commit sequence must be greater than 1")
	}
	if len(c.Records) == 0 {
		return errors.New("commit requires records")
	}
	return nil
}

// Validate checks the v1 envelope, present identities and every record without
// mutation or resource budgets. Keep the input stable during the call. It proves
// neither ancestry, membership, acceptance, authorization nor payload validity.
func (c Commit) Validate() error {
	if err := c.validateCBOR(); err != nil {
		return err
	}
	if c.HistoryID != nil {
		if _, err := historyid.ParseHistoryID(c.HistoryID.String()); err != nil {
			return errors.New("invalid history ID")
		}
	}
	if c.Parent != nil {
		binary := c.Parent.Binary()
		if _, err := contentid.ParseBinary(binary[:]); err != nil {
			return errors.New("invalid parent CID")
		}
	}
	for i, r := range c.Records {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
	}
	return nil
}
