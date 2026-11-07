package session

import (
	"math"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// lifecycleState contains only copyable active-run admission scalars. Historical
// facts remain in immutable records when an ending or successor changes these.
type lifecycleState struct {
	created                       bool
	activeRun                     RunID
	runs, inputs, requests        uint64
	binding                       bool
	frontier, accounted, captured uint64 // Admitted contributions, last outcome coverage, pending request coverage.
	pending                       ModelRequestID
	calls                         uint64
}

// admissionContext pins the accepted source independently of the ordered candidate.
// It is copied before an addition, so failure consumes neither state nor IDs.
type admissionContext struct {
	head      history.CommitRef
	source    lifecycleState
	candidate lifecycleState
}

func newAdmission(v Snapshot) admissionContext {
	return admissionContext{head: v.head, source: v.lifecycle, candidate: v.lifecycle}
}

// reduce validates a locally valid fact against the pinned source and ordered
// candidate. No error changes scalars; outcomes account only their captured frontier.
func (a *admissionContext) reduce(r Record) error {
	s := &a.candidate
	if r.scope != s.activeRun.HistoryID && s.activeRun != (RunID{}) {
		return ErrIdentity
	}
	switch r.value.Kind {
	case sessionformat.KindCreated:
		if s.created {
			return ErrTransition
		}
		s.created = true
	case sessionformat.KindRunStarted:
		return s.startRun(r)
	case sessionformat.KindUserInput:
		return s.admitUserInput(r.userInput)
	case sessionformat.KindInstructions, sessionformat.KindToolCatalog, sessionformat.KindModelBinding:
		if !s.created {
			return ErrTransition
		}
		if r.value.Kind == sessionformat.KindModelBinding {
			s.binding = true
		}
	case sessionformat.KindEffectiveInput:
		return s.admitEffectiveInput(r.effectiveInput)
	case sessionformat.KindModelRequest:
		return a.admitRequest(r.modelRequest)
	case sessionformat.KindModelResponse, sessionformat.KindInvocationFailure:
		return a.admitOutcome(r.outcome, r.calls)
	case sessionformat.KindRunEnded:
		return a.endRun(r.runEnded)
	default:
		return ErrUnsupportedRecord
	}
	return nil
}

// startRun resets only active-run counters after proving the next run identity.
func (s *lifecycleState) startRun(r Record) error {
	if !s.created || s.activeRun != (RunID{}) || s.runs == math.MaxUint64 || r.runStarted.ID != s.runs+1 {
		return ErrTransition
	}
	s.runs++
	s.activeRun = RunID{HistoryID: r.scope, Ordinal: r.runStarted.ID}
	s.inputs, s.requests, s.frontier, s.accounted, s.captured, s.calls = 0, 0, 0, 0, 0, 0
	s.pending = ModelRequestID{}
	return nil
}

// admitUserInput advances authored identity and coverage together, even for empty
// submissions. All eligibility and overflow checks precede scalar changes.
func (s *lifecycleState) admitUserInput(id UserInputID) error {
	if !s.inputEligible() || id.RunID != s.activeRun || s.inputs == math.MaxUint64 || id.Ordinal != s.inputs+1 {
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
	if !s.inputEligible() || v.RunID != s.activeRun.Ordinal {
		return ErrTransition
	}
	for _, in := range v.Inputs {
		if in.UserInput != nil && (*in.UserInput == 0 || *in.UserInput > s.inputs) {
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
func (a *admissionContext) admitRequest(v ModelRequest) error {
	s := &a.candidate
	src := a.source
	if !s.inputEligible() || src.activeRun != s.activeRun || !src.binding || src.pending != (ModelRequestID{}) || src.calls != 0 || s.frontier != src.frontier || v.ID.RunID != s.activeRun || s.requests == math.MaxUint64 || v.ID.Ordinal != s.requests+1 {
		return ErrTransition
	}
	if v.Source.Sequence != a.head.Sequence || v.Source.CID != a.head.CID {
		return ErrIdentity
	}
	if v.RetryOf != nil && (v.RetryOf.RunID != s.activeRun || v.RetryOf.Ordinal == 0 || v.RetryOf.Ordinal > src.requests) {
		return ErrTransition
	}
	s.requests++
	s.pending = v.ID
	s.captured = src.frontier
	return nil
}

// admitOutcome resolves only the still-pending request from an earlier accepted
// commit. Duplicate or same-batch outcomes cannot change coverage or obligations.
func (a *admissionContext) admitOutcome(id ModelRequestID, calls uint64) error {
	s := &a.candidate
	if !s.created || s.activeRun == (RunID{}) || id.RunID != s.activeRun || id != s.pending || id != a.source.pending {
		return ErrTransition
	}
	s.pending = ModelRequestID{}
	s.accounted = s.captured
	s.calls = calls
	return nil
}

// endRun checks clean coverage and capture membership before ending ownership.
// Explicit uncertain endings preserve all pending and unresolved scalars.
func (a *admissionContext) endRun(v RunEnded) error {
	s := &a.candidate
	if !s.created || s.activeRun == (RunID{}) || v.RunID != s.activeRun {
		return ErrTransition
	}
	if v.Reason == RunEndedCompleted && (s.pending != (ModelRequestID{}) || s.calls != 0 || s.accounted != s.frontier) {
		return ErrTransition
	}
	if v.Capture != nil && (v.Capture.RequestID != s.pending || v.Capture.RequestID != a.source.pending) {
		return ErrTransition
	}
	s.activeRun = RunID{}
	return nil
}

func (s lifecycleState) inputEligible() bool {
	return s.created && s.activeRun != (RunID{}) && s.pending == (ModelRequestID{}) && s.calls == 0
}

// preflight uses the same pinned-source reducer as builder additions and replay.
func (s *Session) preflight(records []Record, ref history.CommitRef, payloadBytes uint64) error {
	a := newAdmission(s.state.view)
	for _, r := range records {
		if err := r.checkPayload(s.policy.PayloadLimits); err != nil {
			return err
		}
		if err := a.reduce(r); err != nil {
			return err
		}
	}
	return s.reserveEnding(ref, a.candidate, s.state.records+len(records), s.state.payloadBytes+payloadBytes)
}

// reserveEnding prepares every promised emergency reason against the actual next
// envelope and both encoding policies. Terminal candidates cannot recurse.
func (s *Session) reserveEnding(ref history.CommitRef, state lifecycleState, records int, payloadBytes uint64) error {
	if state.activeRun == (RunID{}) {
		return nil
	}
	next := Session{history: s.history, policy: s.policy, state: sessionState{
		view: Snapshot{historyID: s.state.view.historyID, head: ref, lifecycle: state}, records: records, payloadBytes: payloadBytes,
	}}
	for _, reason := range []RunEndedReason{RunEndedError, RunEndedLimit, RunEndedCancelled, RunEndedInterrupted, RunEndedCaptureFailed} {
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
