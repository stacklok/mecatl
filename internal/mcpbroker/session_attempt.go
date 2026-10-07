package mcpbroker

import "github.com/stacklok/mecatl/engine/session"

// BrokerAttempt identifies a reusable slot, independently of provider call IDs.
type BrokerAttempt = session.BrokerAttempt

// AttemptStatus is nonexecuting evidence about one exact attempt.
type AttemptStatus struct {
	Attempt     BrokerAttempt
	Phase       string
	Disposition session.BrokerAttemptDisposition
	Outcome     *InvocationOutcome
}

// Valid checks the closed phase/disposition/payload combinations.
func (s AttemptStatus) Valid() bool {
	if !s.Attempt.Valid() || (s.Outcome != nil && !s.Outcome.Valid()) {
		return false
	}
	switch s.Phase {
	case "not_admitted", "reserved", "dispatched":
		return s.Disposition == "" && s.Outcome == nil
	case "parked":
		return s.Disposition == "" && (s.Outcome == nil || s.Outcome.Kind == InvocationAuthorizationRequired)
	case "terminal":
		switch s.Disposition {
		case session.BrokerAttemptCompleted:
			return s.Outcome != nil && s.Outcome.Kind == InvocationCompleted
		case session.BrokerAttemptNotDispatched:
			return s.Outcome == nil || s.Outcome.Kind == InvocationNotDispatched
		case session.BrokerAttemptUnknown:
			return s.Outcome == nil || s.Outcome.Kind == InvocationOutcomeUnknown
		}
	}
	return false
}
