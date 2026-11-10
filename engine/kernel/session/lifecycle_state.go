package session

import (
	"math"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// lifecycleState contains active-run admission scalars and immutable tool state.
// Historical facts remain in immutable records when an ending or successor changes these.
type lifecycleState struct {
	created                       bool
	activeRun                     RunID
	runs, inputs, requests        uint64
	binding                       bool
	frontier, accounted, captured uint64 // Admitted contributions, last outcome coverage, pending request coverage.
	pending                       ModelRequestID
	calls                         uint64 // Calls without individual dispositions in tools; retained on bulk end.
	tools                         *activeToolResponse
}

// admissionContext pins the accepted source and head independently of the ordered
// candidate. Pinning the source makes construction reproducible and requires
// accepted prerequisites; same-batch facts cannot manufacture them. The candidate
// tracks prospective order. Callers copy this context before admitting an addition,
// so a rejected fact consumes neither state nor IDs.
//
// frontier is the admitted contribution coverage; a request captures the accepted
// frontier, and its outcome accounts that captured value. For example, input
// advances frontier, a request captures it, an outcome accounts it, then later
// input advances frontier again: the older outcome cannot justify a completed end.
// This coverage is structural lifecycle bookkeeping, not evidence that a model
// understood input, that work was fulfilled, or that usage was billed. An empty
// authored submission advances frontier; an empty effective contribution does not.
// Input gating is a chosen admission policy: pending outcomes or unresolved calls
// prevent new input. Queues and steering live above this boundary. The pinned source
// rule is also an accepted-prerequisite policy: same-batch facts cannot provide it.
// Those choices differ from structural integrity, which requires valid scoped
// identity, membership, and fact pairing. Policy separately controls capacity.
// Noncompleted endings may retain pending model coverage; only explicit bulk
// endings may leave calls without individual dispositions. Ending local ownership
// does not assert that external processing or spend has ceased.
// Ordinary writes must fit soft limits and reserve each actual minimal emergency
// ending at the resulting candidate under both the hard Session policy and the
// History policy; replay uses only hard retained limits.
type admissionContext struct {
	head      history.CommitRef
	source    lifecycleState
	candidate lifecycleState
}

func newAdmission(v Snapshot) admissionContext {
	return admissionContext{head: v.head, source: v.lifecycle, candidate: v.lifecycle}
}

// admit validates a complete locally valid fact in explicit history scope against
// the pinned source and ordered candidate. On success it mutates only the
// receiver's candidate; on error it leaves the entire context unchanged, including
// its pinned accepted source and head. It establishes neither persistence, storage
// acceptance, nor publication. Outcomes account only their captured frontier.
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
		return s.startRun(scope, r.started)
	case sessionformat.KindUserInput:
		return s.admitUserInput(UserInputID{runID: RunID{historyID: scope, ordinal: r.input.RunID}, ordinal: r.input.ID})
	case sessionformat.KindInstructions, sessionformat.KindToolCatalog, sessionformat.KindModelBinding:
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
	case sessionformat.KindRunEnded:
		return a.endRun(scope, r.ended)
	default:
		return ErrUnsupportedRecord
	}
	return nil
}

// startRun resets only active-run counters after proving the next run identity.
func (s *lifecycleState) startRun(scope historyid.HistoryID, v sessionformat.RunStarted) error {
	if !s.created || s.activeRun != (RunID{}) || s.runs == math.MaxUint64 || v.ID != s.runs+1 {
		return ErrTransition
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

// admitRequest pins eligibility and coverage to the accepted source, independently
// of prospective context replacements. Failure consumes no request ordinal.
func (a *admissionContext) admitRequest(scope historyid.HistoryID, v sessionformat.ModelRequest) error {
	s := &a.candidate
	src := a.source
	id := requestID(scope, v.RunID, v.ID)
	if !s.inputEligible() || src.activeRun != s.activeRun || !src.binding || src.pending != (ModelRequestID{}) || src.calls != 0 || s.frontier != src.frontier || id.runID != s.activeRun || s.requests == math.MaxUint64 || v.ID != s.requests+1 {
		return ErrTransition
	}
	if v.Source.Sequence != a.head.Sequence || v.Source.CID != a.head.CID {
		return ErrIdentity
	}
	if v.RetryOf != nil && (*v.RetryOf == 0 || *v.RetryOf > src.requests) {
		return ErrTransition
	}
	s.requests++
	s.pending = id
	s.captured = src.frontier
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
