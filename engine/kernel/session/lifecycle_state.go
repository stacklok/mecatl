package session

import (
	"math"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// lifecycleState is the compact reconstructed lifecycle state, not history: immutable
// records remain the source for historical facts. Its zero value is an uncreated,
// idle session with no retained counts. Ending a run preserves every field except
// activeRun; starting its successor resets only the run-local fields named below.
type lifecycleState struct {
	// created is true exactly after a Created fact has been admitted. It is not an
	// assertion that a Session was constructed or that a model binding is present.
	created bool

	// binding records that at least one ModelBinding replacement has been admitted.
	// Its presence permits requests and persists across run boundaries.
	binding bool

	// activeRun is the current scoped run, or zero while idle. endRun clears only
	// this field, so the just-ended run's local state remains available for later
	// correlation until a successor start resets it.
	activeRun RunID

	// runs is the history-wide number of admitted RunStarted facts. A successful
	// start allocates exactly its next ordinal by incrementing this count.
	runs uint64

	// usages is the history-wide number of first-defined Usage identities. Adding a
	// definition increments it once; replacing a report neither allocates nor changes it.
	usages uint64

	// inputs and requests are the last-admitted user-input and model-request
	// ordinals in the latest run, including after it ends. Each next identity uses
	// its count plus one; a successor start resets both to zero. Final request
	// counts for superseded runs live separately in Snapshot.finalRequests.
	inputs   uint64
	requests uint64

	// frontier counts structural input contributions admitted in this run; accounted
	// is the coverage of the latest admitted outcome; captured is the frontier saved
	// when the pending request was admitted. These are not token, spend, model-
	// comprehension, or fulfillment measurements. An authored user input advances
	// frontier even when empty; an effective-input record advances it only when it
	// has contributions. An outcome assigns accounted from captured, so it cannot
	// cover later input. A successor start resets all three.
	frontier  uint64
	accounted uint64
	captured  uint64

	// pending is the active request awaiting its outcome, or zero. It is cleared by
	// an outcome, but a noncompleted ending may retain it until the next start resets
	// run-local state. Completed endings require it to be zero.
	pending ModelRequestID

	// calls is the number of unsettled calls in tools. It is retained by a bulk
	// ending, cleared by a later outcome or successor start, and must be zero for an
	// ordinary ending.
	calls uint64

	// tools is immutable pairing state for the latest outcome's calls. It may remain
	// after a bulk ending; a later outcome or successor start discards it. It is nil
	// when that response had no calls or no current response pairing is retained.
	tools *activeToolResponse
}

// admissionContext holds a pinned accepted head and source beside an evolving
// ordered candidate. source and head supply prerequisites that must already be
// accepted, including execution outcomes and retries; candidate permits earlier
// same-batch preparation and usage correlation before commitment. Builders copy it
// before each addition, while preflight and replay use a disposable instance.
// admit returns an error without changing the context.
//
// acceptedRequests is a read-only snapshot of final request counts for superseded
// runs. stagedRequests privately accumulates corresponding counts for candidate
// successors, append-only after the accepted prefix. Together they make usage-to-
// request correlation O(1): latest-run counts come from candidate.requests and older
// runs index their retained final count. Appending staged successor counts is amortized
// O(1). Snapshot.finalRequests retains a count for every superseded run;
// lifecycleState.requests retains the latest count even after end.
// Shallow copies may share existing slice backing only as an immutable prefix; they
// append after all checks pass and make no promise that independently advanced
// copies can safely branch and mutate a shared future.
//
// Input gating is admission policy: pending outcomes or unsettled calls prevent new
// input. Structural integrity separately requires valid scoped identity, membership,
// and fact pairing; capacity policy separately reserves each actual minimal emergency
// ending after ordinary writes. Noncompleted endings may retain pending coverage, and
// only explicit bulk endings may retain unsettled calls; neither asserts that external
// processing or spend has ceased.
type admissionContext struct {
	// head is the accepted history head against which request source anchors match.
	head history.CommitRef

	// source is accepted lifecycle state, never advanced during this admission.
	source lifecycleState

	// candidate is lifecycle state after the ordered candidate facts admitted so far.
	candidate lifecycleState

	// acceptedRequests is the immutable accepted prefix of superseded-run counts.
	acceptedRequests []uint64

	// stagedRequests holds candidate-only successors' final counts for publication.
	stagedRequests []uint64
}

func newAdmission(v Snapshot) admissionContext {
	return admissionContext{head: v.head, source: v.lifecycle, candidate: v.lifecycle, acceptedRequests: v.finalRequests}
}

// admit validates a complete locally valid fact in explicit history scope against
// the pinned source and ordered candidate. On success it mutates only the
// receiver's candidate and staged counts; on error the entire context is unchanged.
// Staged request counts never share accepted backing; successful successor starts
// append only beyond the preceding staged length. It establishes neither
// persistence, storage acceptance, nor publication. Outcomes account only their
// captured frontier.
func (a *admissionContext) admit(scope historyid.HistoryID, r *materializedRecord) error {
	s := &a.candidate
	if scope != s.activeRun.historyID && s.activeRun != (RunID{}) {
		return ErrIdentity
	}
	switch r.value.Kind {
	case sessionformat.KindCreated:
		if s.created {
			return ErrTransition
		}
		s.created = true
	case sessionformat.KindRunStarted:
		return a.startRun(scope, r.started)
	case sessionformat.KindUserInput:
		return s.admitUserInput(UserInputID{runID: RunID{historyID: scope, ordinal: r.input.RunID}, ordinal: r.input.ID})
	case sessionformat.KindRootInstructions, sessionformat.KindUserInstructions, sessionformat.KindToolCatalog, sessionformat.KindModelBinding:
		if !s.created {
			return ErrTransition
		}
		if r.value.Kind == sessionformat.KindModelBinding {
			s.binding = true
		}
	case sessionformat.KindEffectiveInput:
		return s.admitEffectiveInput(r.effective)
	case sessionformat.KindModelRequest:
		return a.admitRequest(scope, r.request)
	case sessionformat.KindModelResponse:
		var calls uint64
		for _, item := range r.response.Items {
			if item.Call != nil {
				calls++
			}
		}
		return a.admitOutcome(requestID(scope, r.response.RunID, r.response.RequestID), calls)
	case sessionformat.KindToolInvocation:
		return a.admitToolInvocation(scope, &r.toolInvocation)
	case sessionformat.KindToolDisposition:
		return a.admitToolDisposition(scope, r.toolDisposition)
	case sessionformat.KindInvocationFailure:
		return a.admitOutcome(requestID(scope, r.failure.RunID, r.failure.RequestID), 0)
	case sessionformat.KindUsage:
		return a.admitUsage(r.usage)
	case sessionformat.KindRunEnded:
		return a.endRun(scope, r.ended)
	default:
		return ErrUnsupportedRecord
	}
	return nil
}

// startRun proves the next run identity, retains the preceding run's final request
// count if any, and resets only run-local state. Failure changes neither state nor counts.
func (a *admissionContext) startRun(scope historyid.HistoryID, v sessionformat.RunStarted) error {
	s := &a.candidate
	if !s.created || s.activeRun != (RunID{}) || s.runs == math.MaxUint64 || v.ID != s.runs+1 {
		return ErrTransition
	}
	if s.runs != 0 {
		a.stagedRequests = append(a.stagedRequests, s.requests)
	}
	s.runs++
	s.activeRun = RunID{historyID: scope, ordinal: v.ID}
	s.inputs, s.requests, s.frontier, s.accounted, s.captured, s.calls = 0, 0, 0, 0, 0, 0
	s.pending = ModelRequestID{}
	s.tools = nil
	return nil
}

// admitUserInput advances authored identity and coverage together, even for empty
// submissions. All eligibility and overflow checks precede scalar changes.
func (s *lifecycleState) admitUserInput(id UserInputID) error {
	if !s.inputEligible() || id.runID != s.activeRun || s.inputs == math.MaxUint64 || id.ordinal != s.inputs+1 {
		return ErrTransition
	}
	if s.frontier == math.MaxUint64 {
		return codec.ErrLimitExceeded
	}
	s.inputs++
	s.frontier++
	return nil
}

// admitEffectiveInput checks earlier input membership before advancing coverage.
// Empty contributions are retained without changing the frontier.
func (s *lifecycleState) admitEffectiveInput(v sessionformat.EffectiveInput) error {
	if !s.inputEligible() || v.RunID != s.activeRun.ordinal {
		return ErrTransition
	}
	for _, in := range v.Inputs {
		if in.UserInputID != nil && (*in.UserInputID == 0 || *in.UserInputID > s.inputs) {
			return ErrTransition
		}
	}
	if len(v.Inputs) != 0 {
		if s.frontier == math.MaxUint64 {
			return codec.ErrLimitExceeded
		}
		s.frontier++
	}
	return nil
}

// admitRequest uses the valid preceding candidate for preparation and coverage.
// Only the source anchor and RetryOf membership require earlier accepted history.
// Failure consumes no request ordinal.
func (a *admissionContext) admitRequest(scope historyid.HistoryID, v sessionformat.ModelRequest) error {
	s := &a.candidate
	src := a.source
	id := requestID(scope, v.RunID, v.ID)
	if !s.inputEligible() || !s.binding || id.runID != s.activeRun || s.requests == math.MaxUint64 || v.ID != s.requests+1 {
		return ErrTransition
	}
	if v.Source == nil {
		if a.head != (history.CommitRef{}) || src.created {
			return ErrIdentity
		}
	} else if v.Source.Sequence != a.head.Sequence || v.Source.CID != a.head.CID {
		return ErrIdentity
	}
	if v.RetryOf != nil && (src.activeRun != s.activeRun || *v.RetryOf == 0 || *v.RetryOf > src.requests) {
		return ErrTransition
	}
	s.requests++
	s.pending = id
	s.captured = s.frontier
	return nil
}

// admitOutcome resolves only the still-pending request from an earlier accepted
// commit. Duplicate or same-batch outcomes cannot change coverage or obligations.
func (a *admissionContext) admitOutcome(id ModelRequestID, calls uint64) error {
	s := &a.candidate
	if !s.created || s.activeRun == (RunID{}) || id.runID != s.activeRun || id != s.pending || id != a.source.pending {
		return ErrTransition
	}
	s.pending = ModelRequestID{}
	s.accounted = s.captured
	s.calls = calls
	s.tools = nil
	if calls != 0 {
		s.tools = &activeToolResponse{request: id, calls: make([]activeToolCall, calls)}
	}
	return nil
}

// endRun checks individual tool settlement, clean completion coverage and model
// capture membership before ending ownership. Bulk closeout is represented only
// by the terminal fact; it neither rewrites calls nor resolves a pending model.
func (a *admissionContext) endRun(scope historyid.HistoryID, v sessionformat.RunEnded) error {
	s := &a.candidate
	if !s.created || s.activeRun == (RunID{}) || (RunID{historyID: scope, ordinal: v.RunID}) != s.activeRun {
		return ErrTransition
	}
	if !v.BulkToolCloseout && s.calls != 0 {
		return ErrTransition
	}
	if v.Reason == sessionformat.RunEndedCompleted && (s.pending != (ModelRequestID{}) || s.accounted != s.frontier) {
		return ErrTransition
	}
	if v.Capture != nil {
		id := requestID(scope, v.RunID, v.Capture.RequestID)
		if id != s.pending || id != a.source.pending {
			return ErrTransition
		}
	}
	s.activeRun = RunID{}
	return nil
}

func (s lifecycleState) inputEligible() bool {
	return s.created && s.activeRun != (RunID{}) && s.pending == (ModelRequestID{}) && s.calls == 0
}

// preflight revalidates owner-validated backing under current limits and supplies
// enclosing scope to the same pinned-source admission used by additions and replay.
func (s *Session) preflight(records []*materializedRecord, ref history.CommitRef, payloadBytes uint64) error {
	a := newAdmission(s.state.view)
	for _, r := range records {
		if err := r.checkPayload(s.policy.PayloadLimits); err != nil {
			return err
		}
		if err := a.admit(s.state.view.historyID, r); err != nil {
			return err
		}
	}
	return s.reserveEnding(ref, a.candidate, s.state.records+len(records), s.state.payloadBytes+payloadBytes)
}

// reserveEnding prepares every actual minimal emergency ending after the ordinary
// candidate has passed soft limits. Each ending must fit the resulting state under
// the hard Session capacity policy and the History encoding policy, covering the
// largest supported encoded shape in each dimension despite unequal label lengths.
// Terminal candidates cannot recurse.
func (s *Session) reserveEnding(ref history.CommitRef, state lifecycleState, records int, payloadBytes uint64) error {
	if state.activeRun == (RunID{}) {
		return nil
	}
	next := Session{history: s.history, policy: s.policy, state: sessionState{
		view: Snapshot{historyID: s.state.view.historyID, head: ref, lifecycle: state}, records: records, payloadBytes: payloadBytes,
	}}
	for _, reason := range []RunEndedReason{RunEndedLimit, RunEndedCaptureFailed} {
		if _, err := next.prepareEnding(reason); err != nil {
			return err
		}
	}
	return nil
}

// nextOrdinal rejects overflow before consuming a provisional identifier.
func nextOrdinal(n uint64) (uint64, error) {
	if n == math.MaxUint64 {
		return 0, codec.ErrLimitExceeded
	}
	return n + 1, nil
}
