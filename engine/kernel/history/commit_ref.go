package history

import "github.com/stacklok/mecatl/engine/kernel/history/format"

// CommitRef identifies a proposed or accepted commit in one history. It proves
// neither the commit's validity nor acceptance. Its zero value is unset, except
// that ReadRequest.After accepts it as the boundary before the first commit.
// That boundary is not a commit and still fails Validate.
type CommitRef struct {
	// Sequence is the positive, one-based commit ordinal.
	Sequence int64
	// CID is the nonzero canonical content identity of the commit.
	CID format.CID
}

// Validate reports whether the reference has a positive sequence and a set CID.
func (r CommitRef) Validate() error {
	if r.Sequence <= 0 {
		return invalid("commit sequence must be positive")
	}
	if r.CID == (format.CID{}) {
		return invalid("commit CID must be set")
	}
	return nil
}
