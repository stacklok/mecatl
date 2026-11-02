package history

import "context"

// Backend stores bytes for independent linear histories. Implementations guarantee
// atomic expected-head appends, durable acceptance according to their storage
// contract, and coherent indexed observations. They do not decode commits or
// classify retries. All errors return zero results; an error may leave acceptance
// unknown. A known acceptance must not be masked by late cancellation.
// Expected-head checks serialize appends; this contract does not yet provide
// writer ownership acquisition or fencing.
//
// Inputs stay stable until return. Accepted bytes and returned byte slices are
// independently owned. Reads enforce all byte/count bounds before allocating,
// copying, or reconstructing the bytes they govern, not just before returning.
// Implementations must be safe for concurrent calls.
type Backend interface {
	// Observe returns the head and the accepted reference at sequence from one
	// observation. An unknown history returns a zero Observation, not ErrNotFound.
	// Sequence zero requests only the head; a future ordinal has a zero Commit.
	Observe(context.Context, HistoryID, int64) (Observation, error)
	// Append atomically accepts commit only if the current head equals expected
	// (zero means absent). The reference must be the next ordinal after expected;
	// expected may be speculative and need not have been observed or accepted.
	// Invalid index metadata, including sequence overflow, returns ErrInvalid
	// before comparing heads.
	// New acceptance returns Head=Commit=commit.Ref. A failed CAS returns the
	// current head and occupied submitted ordinal, if any, with Head unequal to
	// expected (not necessarily higher). It never retries the write.
	Append(context.Context, HistoryID, CommitRef, ByteCommit) (Observation, error)
	// Read verifies both nonzero indexed boundaries in this history in one
	// coherent read, even at EOF or when returning only a prefix, and returns
	// whole commits in contiguous ordinal order, ignoring retry windows.
	// Unknown histories are ErrNotFound; unaccepted boundaries are ErrInvalid.
	// An oversized first commit returns PageTooSmallError; a commit exceeding
	// MaxCommitBytes fails without fetching its bytes. Successful empty results
	// are allowed only at the endpoint.
	Read(context.Context, HistoryID, ByteReadRequest) ([]ByteCommit, error)
}

// Observation is coherent head and ordinal evidence. Head is zero only for an
// absent history. Commit is zero iff the requested ordinal is zero or above Head;
// otherwise it names exactly that ordinal (including when it equals Head).
type Observation struct {
	// Head is the current accepted head, or zero for an absent history.
	Head CommitRef
	// Commit names the requested occupied ordinal, or is zero as described above.
	Commit CommitRef
}

// validate rejects contradictory or missing indexed evidence. It cannot prove
// storage atomicity/durability or discover a backend that invents a whole chain.
func (o Observation) validate(sequence int64) error {
	if o.Head != (CommitRef{}) && o.Head.Validate() != nil {
		return ErrCorrupt
	}
	if sequence == 0 || sequence > o.Head.Sequence {
		if o.Commit != (CommitRef{}) {
			return ErrCorrupt
		}
	} else if o.Commit.Validate() != nil || o.Commit.Sequence != sequence || (sequence == o.Head.Sequence && o.Commit != o.Head) {
		return ErrCorrupt
	}
	return nil
}

// ByteCommit associates independently owned canonical bytes with their index.
// History verifies the bytes against Ref; Backend treats their encoding as opaque.
type ByteCommit struct {
	// Ref is the indexed identity, not a claim trusted by the codec.
	Ref CommitRef
	// Bytes owns the complete canonical encoding; backends treat it as opaque.
	Bytes []byte
}

// ByteReadRequest adds policy-owned pre-materialization byte caps to the consumer
// request. Every limit is positive. No implementation may silently raise it.
type ByteReadRequest struct {
	// ReadRequest fixes the endpoints and commit count budget.
	ReadRequest
	// MaxBytes caps the total canonical bytes, supplied from Policy.MaxPageBytes.
	MaxBytes int
	// MaxCommitBytes caps each commit before byte materialization.
	MaxCommitBytes int
}

// validateResult checks byte/count bounds before DTO allocation. The request
// must already be valid; the backend must also enforce these bounds before
// materializing its byte result.
func (r ByteReadRequest) validateResult(commits []ByteCommit) error {
	if len(commits) > r.MaxCommits || int64(len(commits)) > r.Through.Sequence-r.After.Sequence ||
		(len(commits) == 0 && r.After != r.Through) {
		return ErrCorrupt
	}
	remaining := r.MaxBytes
	for _, e := range commits {
		if len(e.Bytes) > remaining || len(e.Bytes) > r.MaxCommitBytes {
			return ErrCorrupt
		}
		remaining -= len(e.Bytes)
	}
	return nil
}
