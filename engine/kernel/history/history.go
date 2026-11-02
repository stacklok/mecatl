package history

import (
	"context"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// History accepts and reads independent linear histories through a Backend. New
// fixes an explicit immutable Policy; the zero value is not usable. History is
// safe for concurrent calls when callers keep mutable inputs stable during each
// call. Accepted storage and returned DTOs are independently owned.
//
// Root sequence 1 atomically creates a history. Successors extend the current
// parent at exactly its next sequence, without wrap, queues, or write retries.
// Proposal identity is (HistoryID, Sequence, CID). At head H, idempotent retries
// cover max(1,H-W+1)..H; expiry does not delete stored history.
//
// Nil-error Append and Resolve echo the proposal with coherent status/head/window
// evidence. Append conflict rejects only this attempt; a gap may later become
// eligible. Resolve absence is not final rejection. Errors return zero results
// and no authoritative verdict; cancellation does not imply rollback. A known
// acceptance is never replaced by a late cancellation error.
type History struct {
	backend Backend
	policy  Policy
}

// New validates and copies policy without defaults. A nil backend or invalid
// policy returns nil and ErrInvalid. A typed-nil backend is not supported.
func New(backend Backend, policy Policy) (*History, error) {
	if backend == nil {
		return nil, invalid("backend must be set")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &History{backend: backend, policy: policy}, nil
}

// Head returns the accepted head, or ErrNotFound for an unknown history. Errors
// return a zero reference. It does not scan or decode historical commits.
func (h *History) Head(ctx context.Context, id HistoryID) (CommitRef, error) {
	if err := checkScope(ctx, id); err != nil {
		return CommitRef{}, err
	}
	o, err := h.backend.Observe(ctx, id, 0)
	if err != nil {
		return CommitRef{}, err
	}
	if err := o.validate(0); err != nil {
		return CommitRef{}, err
	}
	if o.Head == (CommitRef{}) {
		return CommitRef{}, ErrNotFound
	}
	return o.Head, nil
}

// Identify validates scope and the DTO against this History's encoding policy
// and returns its canonical reference without backend calls or an acceptance
// claim. Errors return a zero reference. Keep mutable input stable during the
// call, and unchanged until Append if both calls must identify the same proposal:
// mutation changes identity. Identify does not freeze or retain the DTO.
func (h *History) Identify(id HistoryID, commit format.Commit) (CommitRef, error) {
	_, ref, err := h.encode(id, commit)
	return ref, err
}

// encode shares bounded canonical encoding and scope validation for local
// identification and submission. Failures return no bytes or reference.
func (h *History) encode(id HistoryID, commit format.Commit) ([]byte, CommitRef, error) {
	if id == (HistoryID{}) {
		return nil, CommitRef{}, invalid("history ID must be set")
	}
	if commit.Sequence == 1 && commit.HistoryID != id {
		return nil, CommitRef{}, invalid("root history ID does not match scope")
	}
	canonical, cid, err := format.EncodeCommit(commit, h.policy.CommitLimits)
	if err != nil {
		return nil, CommitRef{}, invalid("commit is invalid or exceeds policy")
	}
	return canonical, CommitRef{Sequence: commit.Sequence, CID: cid}, nil
}

// Append validates and canonically encodes a DTO, derives its identity and
// expected parent, and makes one atomic expected-head append with no prior
// observation unless canceled before submission. A failed CAS is adjudicated
// from its coherent evidence, never by retrying a write. Backend errors return
// zero results even if the proposal was accepted. Retain Identify's reference
// and an unchanged DTO before submission to Resolve an uncertain outcome with
// a fresh context.
func (h *History) Append(ctx context.Context, id HistoryID, commit format.Commit) (Resolution, error) {
	if err := checkScope(ctx, id); err != nil {
		return Resolution{}, err
	}
	canonical, proposal, err := h.encode(id, commit)
	if err != nil {
		return Resolution{}, err
	}
	expected := CommitRef{}
	if commit.Sequence > 1 {
		expected = CommitRef{Sequence: commit.Sequence - 1, CID: commit.Parent}
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	written, err := h.backend.Append(ctx, id, expected, ByteCommit{Ref: proposal, Bytes: canonical})
	if err != nil {
		return Resolution{}, err
	}
	if written.Head == expected {
		// Exact expected-head equality cannot legitimately follow a valid CAS,
		// even for an absent root. Other absent, lower, or wrong-parent heads can.
		return Resolution{}, ErrCorrupt
	}
	result, err := h.resolve(written, proposal)
	if err != nil {
		return Resolution{}, err
	}
	if result.Status == StatusNotObserved {
		result.Status = StatusConflict
	}
	return result, nil
}

// Resolve observes a proposal and adjudicates acceptance within the retry window.
// Unknown histories and future ordinals are StatusNotObserved, not final rejection.
// Head, status, and retry-window classification belong to one observation.
func (h *History) Resolve(ctx context.Context, id HistoryID, proposal CommitRef) (Resolution, error) {
	if err := checkScope(ctx, id); err != nil {
		return Resolution{}, err
	}
	if err := proposal.Validate(); err != nil {
		return Resolution{}, err
	}
	o, err := h.backend.Observe(ctx, id, proposal.Sequence)
	if err != nil {
		return Resolution{}, err
	}
	return h.resolve(o, proposal)
}

// resolve interprets coherent indexed evidence, without historical adjudication
// outside the instance's retry window. It performs no I/O or write retry.
func (h *History) resolve(o Observation, proposal CommitRef) (Resolution, error) {
	if err := o.validate(proposal.Sequence); err != nil {
		return Resolution{}, err
	}
	r := Resolution{Proposal: proposal, Head: o.Head, Status: StatusNotObserved}
	if proposal.Sequence > o.Head.Sequence {
		return r, nil
	}
	if proposal.Sequence < max(int64(1), o.Head.Sequence-h.policy.RetryWindow+1) {
		r.Status = StatusRetryExpired
	} else if o.Commit == proposal {
		r.Status = StatusAccepted
	} else {
		r.Status = StatusConflict
	}
	return r, nil
}

// Read returns decoded, owned whole commits through a fixed accepted endpoint.
// It verifies canonical bytes, indexed CID/sequence, and ancestry anchored at
// request.After, including the upper identity when reached. Any malformed or
// inconsistent result returns a zero page; it trusts the backend's coherent
// validation of indexed request boundaries and does not scan from genesis.
func (h *History) Read(ctx context.Context, id HistoryID, request ReadRequest) (Page, error) {
	if err := checkScope(ctx, id); err != nil {
		return Page{}, err
	}
	if err := request.Validate(h.policy); err != nil {
		return Page{}, err
	}
	r := ByteReadRequest{ReadRequest: request, MaxBytes: h.policy.MaxPageBytes, MaxCommitBytes: h.policy.CommitLimits.MaxCommitBytes}
	b, err := h.backend.Read(ctx, id, r)
	if err != nil {
		return Page{}, err
	}
	if err := r.validateResult(b); err != nil {
		return Page{}, err
	}
	page := Page{Next: request.After}
	if len(b) > 0 {
		page.Commits = make([]Commit, len(b))
	}
	parent := request.After
	for i, e := range b {
		if err := ctx.Err(); err != nil {
			return Page{}, err
		}
		c, cid, err := format.DecodeCommit(e.Bytes, h.policy.CommitLimits)
		if err != nil || cid != e.Ref.CID || c.Sequence != e.Ref.Sequence || c.Sequence != parent.Sequence+1 || c.Parent != parent.CID ||
			(c.Sequence == 1 && c.HistoryID != id) || (c.Sequence == request.Through.Sequence && e.Ref != request.Through) {
			return Page{}, ErrCorrupt
		}
		page.Commits[i] = Commit{Ref: e.Ref, Commit: c}
		parent = e.Ref
		page.Next = e.Ref
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	return page, nil
}

// checkScope validates the shared public operation boundary without leaking input.
func checkScope(ctx context.Context, id HistoryID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == (HistoryID{}) {
		return invalid("history ID must be set")
	}
	return nil
}
