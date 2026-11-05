package session

import (
	"context"
	"errors"
	"math"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ErrTransition identifies illegal lifecycle ordering or input ownership.
var ErrTransition = errors.New("illegal session transition")

// ErrIdentity identifies scope, ancestry, sequence or canonical reference mismatch.
var ErrIdentity = errors.New("session history identity mismatch")

// Session owns accepted state and submission for one bound History. Construct it
// with New or Load; zero is unusable. Callers serialize operations and keep borrowed
// inputs stable during calls. Do not copy a Session after use. It provides no
// automatic retries, execution, interruption or exclusive writer fencing.
type Session struct {
	history *history.History
	policy  Policy
	state   sessionState
	batch   *SessionCommit
	reload  bool
}

// sessionState owns append-only accepted backing storage and admission counters.
// Published prefixes and their DTOs are immutable; candidates stage only new facts.
type sessionState struct {
	view         Snapshot
	records      int
	payloadBytes uint64
}

// New binds a History and scope with a copied explicit policy, without I/O.
// Invalid policy returns nil, ErrInvalidPolicy; nil History or invalid scope
// returns nil, ErrIdentity. Creation must be explicitly added to the first batch.
func New(h *history.History, id historyid.HistoryID, policy Policy) (*Session, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, ErrIdentity
	}
	if err := (Created{HistoryID: id}).Validate(); err != nil {
		return nil, ErrIdentity
	}
	return &Session{history: h, policy: policy, state: sessionState{view: Snapshot{historyID: id}}}, nil
}

// loadError preserves the storage cause without projecting backend-controlled
// text into a session error. Loading makes no writes or acceptance claims.
type loadError struct{ cause error }

func (loadError) Error() string   { return "session load failed" }
func (e loadError) Unwrap() error { return e.cause }

// Load constructs a new bound Session by pinning History's accepted head and
// replaying verified pages from genesis through that endpoint. Later appends are
// not included. Active runs stay active; loading never writes, interrupts, retries
// an old submission or grants exclusive writer ownership. Policy is copied as in
// New. Replay enforces hard bounds, not soft write admission or future closeout
// room. Unknown histories return history.ErrNotFound, not an uncreated session.
// Any error returns nil, never a partially reconstructed object.
func Load(ctx context.Context, h *history.History, id historyid.HistoryID, policy Policy) (*Session, error) {
	s, err := New(h, id, policy)
	if err != nil {
		return nil, err
	}
	endpoint, err := h.Head(ctx, id)
	if err != nil {
		return nil, loadError{cause: err}
	}
	// History requires an explicit positive page count, with no default. One
	// commit respects every valid History policy; its byte bounds still apply.
	request := history.ReadRequest{Through: endpoint, MaxCommits: 1}
	for request.After != endpoint {
		page, err := h.Read(ctx, id, request)
		if err != nil {
			return nil, loadError{cause: err}
		}
		for _, c := range page.Commits {
			if err := s.apply(c); err != nil {
				return nil, err
			}
		}
		request.After = page.Next
	}
	if s.state.view.head != endpoint {
		return nil, ErrIdentity
	}
	return s, nil
}

// Snapshot captures accepted slice bounds and scalars in O(1), without copying
// records or payloads. Capture must be serialized with all other Session calls;
// subsequent reads of the returned handle may run concurrently with appends.
// A zero Session returns the zero handle.
func (s *Session) Snapshot() Snapshot { return s.state.view }

// prepareInterruption prepares the actual one-record closeout of the active run,
// without applying, persisting, cancelling work or starting a successor. Idle
// (including uncreated) returns zero, nil: there is nothing to append.
// Errors return zero. The same builder and admission path reserves closeout
// capacity for every candidate that would leave a run active.
func (s *Session) prepareInterruption() (prepared, error) {
	if err := s.writeBarrier(); err != nil {
		return prepared{}, err
	}
	if err := s.policy.Validate(); err != nil {
		return prepared{}, err
	}
	if s.state.view.lifecycle.activeRun == (RunID{}) {
		return prepared{}, nil
	}
	r, err := NewRunInterruptedRecord(RunInterrupted{ID: s.state.view.lifecycle.activeRun}, s.policy.PayloadLimits)
	if err != nil {
		return prepared{}, err
	}
	return s.prepare([]Record{r})
}

// prepare is also used by submission while its own batch holds the write slot.
func (s *Session) prepare(records []Record) (prepared, error) {
	if err := s.policy.Validate(); err != nil {
		return prepared{}, err
	}
	ordinary := len(records) != 1 || records[0].value.Kind != sessionformat.KindRunInterrupted
	if err := s.admit(len(records), 0, ordinary); err != nil {
		return prepared{}, err
	}
	var total uint64
	for _, r := range records {
		if r.scope == (historyid.HistoryID{}) {
			return prepared{}, codec.ErrInvalidData
		}
		if r.scope != s.state.view.historyID {
			return prepared{}, ErrIdentity
		}
		if uint64(len(r.value.Payload)) > s.policy.MaxPayloadBytes-total || len(r.value.Payload) > s.policy.PayloadLimits.MaxBytes {
			return prepared{}, codec.ErrLimitExceeded
		}
		total += uint64(len(r.value.Payload))
	}
	if err := s.admit(len(records), total, ordinary); err != nil {
		return prepared{}, err
	}
	v := s.state.view
	if v.head.Sequence == math.MaxInt64 {
		return prepared{}, codec.ErrLimitExceeded
	}
	c := format.Commit{FormatVersion: 1, Sequence: v.head.Sequence + 1, Records: make([]format.Record, len(records))}
	if v.head == (history.CommitRef{}) {
		c.HistoryID = new(v.historyID)
	} else {
		c.Parent = new(v.head.CID)
	}
	for i, r := range records {
		c.Records[i] = r.value
	}
	ref, err := s.verifyCommit(c, nil)
	if err != nil {
		return prepared{}, err
	}
	if err := s.preflight(records, ref, total); err != nil {
		return prepared{}, err
	}
	return prepared{commit: c, ref: ref}, nil
}

// Apply atomically publishes a fully verified accepted commit and its head.
// The caller must establish storage acceptance first; supplying a Commit is an
// assertion, not an unforgeable receipt. Mutable DTOs are canonically re-encoded
// and CID-verified. Any error preserves all prior state. Failure categories are
// ErrInvalidPolicy, ErrIdentity, ErrTransition, ErrUnsupportedRecord, ErrBatch,
// ErrReloadRequired, and codec's ErrInvalidData/ErrLimitExceeded. Errors never
// include payloads or external errors.
// Feed genesis-to-pinned-endpoint commits through this same method for replay;
// the caller checks that the pinned endpoint was actually reached. Open batches
// and reload-required sessions reject this low-level path. Hard bounds apply, but
// soft write admission and reservation for hypothetical future writes do not.
func (s *Session) Apply(accepted history.Commit) error {
	if err := s.writeBarrier(); err != nil {
		return err
	}
	return s.apply(accepted)
}

// apply is the common decoded publication path for submit and fresh replay.
func (s *Session) apply(accepted history.Commit) error {
	if err := s.policy.Validate(); err != nil {
		return err
	}
	candidate, _, err := s.evaluate(accepted.Commit, &accepted.Ref)
	if err != nil {
		return err
	}
	s.state = candidate
	return nil
}

// evaluate stages only new records and locators after hard admission and
// canonical identity checks. Replay requires no hypothetical future write room.
func (s *Session) evaluate(c format.Commit, expected *history.CommitRef) (sessionState, history.CommitRef, error) {
	if err := s.admit(len(c.Records), 0, false); err != nil {
		return sessionState{}, history.CommitRef{}, err
	}
	var total uint64
	for _, r := range c.Records {
		if uint64(len(r.Payload)) > s.policy.MaxPayloadBytes-total || len(r.Payload) > s.policy.PayloadLimits.MaxBytes {
			return sessionState{}, history.CommitRef{}, codec.ErrLimitExceeded
		}
		total += uint64(len(r.Payload))
	}
	if err := s.admit(len(c.Records), total, false); err != nil {
		return sessionState{}, history.CommitRef{}, err
	}
	ref, err := s.verifyCommit(c, expected)
	if err != nil {
		return sessionState{}, history.CommitRef{}, err
	}
	candidate := s.state
	lifecycle := candidate.view.lifecycle
	staged := make([]*acceptedRecord, len(c.Records))
	var runs []runEntry
	var inputs []int
	for i, r := range c.Records {
		fact, err := decodeAccepted(r, s.policy.PayloadLimits)
		if err != nil {
			return sessionState{}, history.CommitRef{}, err
		}
		metadata := fact.metadata(candidate.view.historyID)
		if err := lifecycle.reduce(metadata); err != nil {
			return sessionState{}, history.CommitRef{}, err
		}
		staged[i] = fact
		position := len(candidate.view.records) + i
		switch r.Kind {
		case sessionformat.KindRunStarted:
			runs = append(runs, runEntry{id: lifecycle.activeRun, recordStart: position, inputStart: len(candidate.view.inputs) + len(inputs)})
		case sessionformat.KindUserInput:
			inputs = append(inputs, position)
		case sessionformat.KindInstructions:
			candidate.view.instructions = fact
		case sessionformat.KindToolCatalog:
			candidate.view.catalog = fact
		case sessionformat.KindModelBinding:
			candidate.view.binding = fact
		}
	}
	candidate.records += len(c.Records)
	candidate.payloadBytes += total
	entry := commitEntry{ref: ref, formatVersion: c.FormatVersion, recordStart: len(candidate.view.records), recordCount: len(staged)}
	if c.HistoryID != nil {
		entry.historyID = *c.HistoryID
	}
	if c.Parent != nil {
		entry.parent = *c.Parent
	}
	// Publication begins here: only non-failing appends and scalar assignment
	// remain. No published prefix element or decoded DTO is ever changed.
	v := &candidate.view
	v.commits = append(v.commits, entry)
	v.records = append(v.records, staged...)
	v.runs = append(v.runs, runs...)
	v.inputs = append(v.inputs, inputs...)
	v.lifecycle = lifecycle
	v.head = ref
	return candidate, ref, nil
}

// verifyCommit checks ancestry and bounded canonical envelope identity before
// either metadata analysis or decoded application allocates candidate storage.
func (s *Session) verifyCommit(c format.Commit, expected *history.CommitRef) (history.CommitRef, error) {
	if err := s.checkAncestry(c, expected); err != nil {
		return history.CommitRef{}, err
	}
	wire, err := codec.Encode(c, s.policy.CommitLimits)
	if err != nil {
		return history.CommitRef{}, err
	}
	if expected != nil && contentid.Verify(wire, expected.CID) != nil {
		return history.CommitRef{}, ErrIdentity
	}
	return history.CommitRef{Sequence: c.Sequence, CID: contentid.Sum(wire)}, nil
}

// checkAncestry requires an exact next ordinal and parent, not just a valid DTO.
// Sequence exhaustion is an operational limit; reapplication is a stale identity.
func (s *Session) checkAncestry(c format.Commit, expected *history.CommitRef) error {
	v := s.state.view
	if v.head.Sequence == math.MaxInt64 {
		return codec.ErrLimitExceeded
	}
	if c.Sequence != v.head.Sequence+1 || (expected != nil && expected.Sequence != c.Sequence) {
		return ErrIdentity
	}
	if v.head == (history.CommitRef{}) {
		if c.HistoryID == nil || *c.HistoryID != v.historyID || c.Parent != nil {
			return ErrIdentity
		}
	} else if c.HistoryID != nil || c.Parent == nil || *c.Parent != v.head.CID {
		return ErrIdentity
	}
	return nil
}

// admit bounds the entire retained candidate before any payload decode or clone.
// Subtraction and division keep extreme policies safe on both 32- and 64-bit hosts.
func (s *Session) admit(count int, payloadBytes uint64, ordinary bool) error {
	if count == 0 {
		return codec.ErrInvalidData
	}
	p := s.policy
	recordLimit, byteLimit, workLimit := p.MaxRetainedRecords, p.MaxPayloadBytes, p.MaxDecodedWork
	if ordinary {
		recordLimit, byteLimit, workLimit = p.SoftMaxRetainedRecords, p.SoftMaxPayloadBytes, p.SoftMaxDecodedWork
	}
	if count > p.MaxRecordsPerCommit || s.state.records > recordLimit || count > recordLimit-s.state.records ||
		s.state.payloadBytes > byteLimit || payloadBytes > byteLimit-s.state.payloadBytes {
		return codec.ErrLimitExceeded
	}
	n := uint64(s.state.records + count) //nolint:gosec // Nonnegative counts sum to at most the validated retained limit.
	remaining := workLimit / n
	for _, ceiling := range [...]int{p.PayloadLimits.MaxBytes, p.PayloadLimits.MaxNodes, p.PayloadLimits.MaxCollectionElements} {
		units := uint64(ceiling) //nolint:gosec // Policy validation requires every ceiling to be positive.
		if units > remaining {
			return codec.ErrLimitExceeded
		}
		remaining -= units
	}
	return nil
}
