package session

import (
	"math"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// lifecycleState is the complete scalar transition state. Copies are disposable
// candidates; metadata preflight and decoded application use the same reducer.
type lifecycleState struct {
	created   bool
	activeRun RunID
	runs      uint64
	inputs    uint64 // Inputs in the active (or most recently interrupted) run.
}

// reduce validates one locally valid typed fact before changing the candidate.
// Failed additions leave all scalars unchanged and consume no ordinal.
func (s *lifecycleState) reduce(r Record, source func(InputID) (uint64, bool)) error {
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
		if !s.created || s.activeRun == (RunID{}) || r.userInput.RunID != s.activeRun || s.inputs == math.MaxUint64 || r.userInput.Ordinal != s.inputs+1 {
			return ErrTransition
		}
		s.inputs++
	case sessionformat.KindInstructions, sessionformat.KindToolCatalog, sessionformat.KindModelBinding:
		if !s.created || s.activeRun == (RunID{}) {
			return ErrTransition
		}
	case sessionformat.KindEffectiveInput:
		return s.validateEffectiveInput(r, source)
	case sessionformat.KindRunInterrupted:
		if !s.created || s.activeRun == (RunID{}) || r.runInterrupted.ID != s.activeRun {
			return ErrTransition
		}
		s.activeRun = RunID{}
	default:
		return ErrUnsupportedRecord
	}
	return nil
}

// startRun advances only a valid next run, leaving failed candidates unchanged.
func (s *lifecycleState) startRun(r Record) error {
	if !s.created || s.activeRun != (RunID{}) || s.runs == math.MaxUint64 || r.runStarted.ID != s.runs+1 {
		return ErrTransition
	}
	s.runs++
	s.inputs = 0
	s.activeRun = RunID{HistoryID: r.scope, Ordinal: r.runStarted.ID}
	return nil
}

// validateEffectiveInput checks source admission without changing lifecycle state.
func (s *lifecycleState) validateEffectiveInput(r Record, source func(InputID) (uint64, bool)) error {
	if !s.created || s.activeRun == (RunID{}) || r.effectiveInput.RunID != s.activeRun || r.effectiveInput.Ordinal == 0 || r.effectiveInput.Ordinal > s.inputs {
		return ErrTransition
	}
	count, ok := source(r.effectiveInput)
	if !ok || (r.hasMessage && r.maxMessage >= count) {
		return ErrTransition
	}
	return nil
}

// preflight rechecks owned payloads and reduces their metadata without scanning
// accepted prefixes. Provisional source lookup uses only earlier batch records.
func (s *Session) preflight(records []Record, ref history.CommitRef, payloadBytes uint64) error {
	candidate := s.state.view.lifecycle
	for i, r := range records {
		if err := r.checkPayload(s.policy.PayloadLimits); err != nil {
			return err
		}
		if err := candidate.reduce(r, func(id InputID) (uint64, bool) { return pendingSourceMessages(s.state.view, records[:i], id) }); err != nil {
			return err
		}
	}
	return s.reserveInterruption(ref, candidate.activeRun, s.state.records+len(records), s.state.payloadBytes+payloadBytes)
}

// reserveInterruption uses the actual preparation path, including next-envelope
// widths and admission. Closing becomes idle, so reservation cannot recurse.
func (s *Session) reserveInterruption(ref history.CommitRef, active RunID, records int, payloadBytes uint64) error {
	if active == (RunID{}) {
		return nil
	}
	next := Session{policy: s.policy, state: sessionState{
		view:    Snapshot{historyID: s.state.view.historyID, head: ref, lifecycle: lifecycleState{created: true, activeRun: active}},
		records: records, payloadBytes: payloadBytes,
	}}
	_, err := next.prepareInterruption()
	return err
}

// pendingSourceMessages searches only earlier provisional facts, then the pinned
// accepted input index. No latest-revision map or mutable candidate index is needed.
func pendingSourceMessages(v Snapshot, records []Record, id InputID) (uint64, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].value.Kind == sessionformat.KindUserInput && records[i].userInput == id {
			return records[i].messageCount, true
		}
	}
	return v.sourceMessages(id)
}

// nextOrdinal avoids wrapping a provisional identifier before constructor checks.
func nextOrdinal(n uint64) (uint64, error) {
	if n == math.MaxUint64 {
		return 0, codec.ErrLimitExceeded
	}
	return n + 1, nil
}
