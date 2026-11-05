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
func (s *lifecycleState) reduce(r Record) error {
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
		return s.validateEffectiveInput(r)
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

// validateEffectiveInput checks every source without changing lifecycle state.
// Inputs are admitted contiguously within each run, so the current ordinal count
// proves earlier membership, including successful earlier same-batch additions.
func (s *lifecycleState) validateEffectiveInput(r Record) error {
	if !s.created || s.activeRun == (RunID{}) || r.effectiveInput.RunID != s.activeRun.Ordinal {
		return ErrTransition
	}
	for _, in := range r.effectiveInput.Inputs {
		if in.UserInput != nil && (*in.UserInput == 0 || *in.UserInput > s.inputs) {
			return ErrTransition
		}
	}
	return nil
}

// preflight rechecks owned payloads and reduces them without scanning accepted
// prefixes. Ordered scalar admission includes earlier same-batch submissions.
func (s *Session) preflight(records []Record, ref history.CommitRef, payloadBytes uint64) error {
	candidate := s.state.view.lifecycle
	for _, r := range records {
		if err := r.checkPayload(s.policy.PayloadLimits); err != nil {
			return err
		}
		if err := candidate.reduce(r); err != nil {
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

// nextOrdinal avoids wrapping a provisional identifier before constructor checks.
func nextOrdinal(n uint64) (uint64, error) {
	if n == math.MaxUint64 {
		return 0, codec.ErrLimitExceeded
	}
	return n + 1, nil
}
