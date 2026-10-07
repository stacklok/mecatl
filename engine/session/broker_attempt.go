package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"time"
	"unicode/utf8"
)

// BrokerAttempt identifies a reusable broker slot, not a provider call ID.
type BrokerAttempt struct {
	Slot     uint32 `json:"slot"`
	Sequence uint64 `json:"sequence"`
}

// Valid checks attempt framing, not admission or execution authority.
func (a BrokerAttempt) Valid() bool { return a.Slot < 64 && a.Sequence > 0 }

// BrokerAttemptDisposition records effects, independently of transport errors.
type BrokerAttemptDisposition string

const (
	// BrokerAttemptCompleted requires the current occurrence's paired result.
	BrokerAttemptCompleted BrokerAttemptDisposition = "completed"
	// BrokerAttemptNotDispatched is affirmative evidence of no dispatch.
	BrokerAttemptNotDispatched BrokerAttemptDisposition = "not_dispatched"
	// BrokerAttemptUnknown cannot release the host's uncertain-effect fence.
	BrokerAttemptUnknown BrokerAttemptDisposition = "unknown"
)

// BrokerHostAttempt contains no arguments or execution authority.
type BrokerHostAttempt struct {
	Attempt     BrokerAttempt            `json:"attempt"`
	CallID      ToolCallID               `json:"call_id"`
	Digest      [32]byte                 `json:"digest"`
	Phase       string                   `json:"phase"`
	Disposition BrokerAttemptDisposition `json:"disposition,omitempty"`
}

var errBrokerAttemptFenced = errors.New("session: broker attempt fenced; reconcile without executing")

func validateBrokerHostAttempt(a BrokerAccess) error {
	if a.Current == nil {
		if a.AdmittedSequence != 0 && (len(a.Attempted) != 0 || a.Pending != "") {
			return errBrokerAttemptFenced
		}
		return nil
	}
	c := a.Current
	if len(a.Attempted) != 0 || a.Pending != "" || !c.Attempt.Valid() || c.Attempt.Slot != 0 ||
		c.CallID == "" || len(c.CallID) > 256 || !utf8.ValidString(string(c.CallID)) || c.Digest == ([32]byte{}) {
		return errBrokerAttemptFenced
	}
	if c.Attempt.Sequence != a.AdmittedSequence &&
		(a.AdmittedSequence == math.MaxUint64 || c.Attempt.Sequence != a.AdmittedSequence+1) {
		return errBrokerAttemptFenced
	}
	switch c.Phase {
	case "reserved", "dispatched":
		if c.Disposition != "" {
			return errBrokerAttemptFenced
		}
	case "terminal":
		if c.Disposition != BrokerAttemptUnknown && c.Attempt.Sequence != a.AdmittedSequence {
			return errBrokerAttemptFenced
		}
		switch c.Disposition {
		case BrokerAttemptCompleted, BrokerAttemptNotDispatched, BrokerAttemptUnknown:
		default:
			return errBrokerAttemptFenced
		}
	default:
		return errBrokerAttemptFenced
	}
	return nil
}

// BrokerCallDigest uses the byte-exact outer call, including query arguments.
// Its encoding matches the transport-neutral broker Call, without importing it.
func BrokerCallDigest(call ToolCall) [32]byte {
	b, _ := json.Marshal(struct {
		ID        ToolCallID
		Name      string
		Arguments []byte
	}{call.ID, call.Name, call.Args})
	return sha256.Sum256(b)
}

// PrepareBrokerInvocation allocates host slot 0. Save before any broker preflight.
// Allocation is not admission: only verified broker evidence advances high-water.
// The legacy execution path cannot be mixed with this staged slot path.
func (s *Session) PrepareBrokerInvocation(ref BrokerSessionRef, catalogue BrokerCatalogueRef, call ToolCall, now time.Time) (BrokerAttempt, error) {
	a, ok := s.BrokerAccess()
	if !ok || a.Withdrawn || a.Session != ref || a.Catalogue != catalogue || !a.ExpiresAt.After(now) ||
		len(a.Attempted) != 0 || a.Pending != "" || a.AdmittedSequence == math.MaxUint64 ||
		call.ID == "" || len(call.ID) > 256 || !utf8.ValidString(string(call.ID)) ||
		call.Name == "" || len(call.Name) > 256 || !utf8.ValidString(call.Name) || len(call.Args) > 256*1024 || !json.Valid(call.Args) {
		return BrokerAttempt{}, errBrokerAttemptFenced
	}
	if a.Current != nil && (a.Current.Phase != "terminal" || a.Current.Disposition == BrokerAttemptUnknown) {
		return BrokerAttempt{}, errBrokerAttemptFenced
	}
	attempt := BrokerAttempt{Sequence: a.AdmittedSequence + 1}
	a.Current = &BrokerHostAttempt{Attempt: attempt, CallID: call.ID, Digest: BrokerCallDigest(call), Phase: "reserved"}
	s.brokerAccess = &a
	s.brokerAttemptCompleted = BrokerAttempt{}
	s.brokerAttemptRestored = false
	return attempt, nil
}

// AdmitBrokerInvocation records verified exact admission without dispatching.
// The caller must validate broker status/outcome; readiness alone is not authority.
func (s *Session) AdmitBrokerInvocation(attempt BrokerAttempt) error {
	if s.brokerAccess == nil || s.brokerAccess.Current == nil || s.brokerAccess.Current.Attempt != attempt || s.brokerAccess.Current.Phase == "terminal" {
		return errBrokerAttemptFenced
	}
	s.brokerAccess.AdmittedSequence = attempt.Sequence
	return nil
}

// ReattachBrokerAuthorization restores a reserved occurrence only after the host
// has verified its original process-local parked control and exact broker status.
// It grants neither permission nor catalogue authority.
func (s *Session) ReattachBrokerAuthorization(call ToolCall, attempt BrokerAttempt) error {
	if s.brokerAccess == nil || s.brokerAccess.Withdrawn || s.brokerAccess.Current == nil {
		return errBrokerAttemptFenced
	}
	c := s.brokerAccess.Current
	pending, ok := s.PendingAuthorization()
	if !ok || c.Attempt != attempt || c.Phase != "reserved" || c.Digest != BrokerCallDigest(call) || BrokerCallDigest(pending.Call) != c.Digest {
		return errBrokerAttemptFenced
	}
	s.brokerAttemptRestored = false
	return nil
}

// ContinueBrokerInvocation binds only a live original reserved occurrence.
// Restored calls require passive reconciliation, never automatic continuation.
func (s *Session) ContinueBrokerInvocation(call ToolCall) (BrokerAttempt, error) {
	if s.brokerAccess == nil || s.brokerAccess.Withdrawn || s.brokerAttemptRestored || s.brokerAccess.Current == nil {
		return BrokerAttempt{}, errBrokerAttemptFenced
	}
	c := s.brokerAccess.Current
	if c.Phase != "reserved" || c.CallID != call.ID || c.Digest != BrokerCallDigest(call) {
		return BrokerAttempt{}, errBrokerAttemptFenced
	}
	return c.Attempt, nil
}

// DispatchBrokerInvocation must be saved before Execute. Restored unfinished
// attempts cannot dispatch; their owner must first inspect and settle them.
func (s *Session) DispatchBrokerInvocation(attempt BrokerAttempt) error {
	if s.brokerAccess == nil || s.brokerAccess.Withdrawn || s.brokerAttemptRestored {
		return errBrokerAttemptFenced
	}
	c := s.brokerAccess.Current
	if c == nil || c.Attempt != attempt || c.Phase != "reserved" {
		return errBrokerAttemptFenced
	}
	c.Phase = "dispatched"
	return nil
}

// SettleBrokerInvocation records verified exact non-dispatch or an irreversible
// uncertain-effect fence. Generic Interrupted/FailedPrecondition is not evidence
// of non-dispatch or admission. Completed requires a newly recorded pair.
func (s *Session) SettleBrokerInvocation(attempt BrokerAttempt, disposition BrokerAttemptDisposition) error {
	if s.brokerAccess == nil || s.brokerAccess.Current == nil {
		return errBrokerAttemptFenced
	}
	c := s.brokerAccess.Current
	if c.Attempt != attempt || (disposition != BrokerAttemptNotDispatched && disposition != BrokerAttemptUnknown) {
		return errBrokerAttemptFenced
	}
	if c.Phase == "terminal" {
		if c.Disposition != disposition {
			return errBrokerAttemptFenced
		}
		return nil
	}
	c.Phase, c.Disposition = "terminal", disposition
	if disposition == BrokerAttemptNotDispatched {
		s.brokerAccess.AdmittedSequence = attempt.Sequence
	}
	s.brokerAttemptCompleted = BrokerAttempt{}
	return nil
}

// RejectUnadmittedBrokerInvocation discards an allocation only after a verified
// metadata-only not_admitted inspection of this exact next sequence. Errors,
// missing acknowledgements and generic refusals must never call this method.
func (s *Session) RejectUnadmittedBrokerInvocation(attempt BrokerAttempt) error {
	if s.brokerAccess == nil || s.brokerAccess.Current == nil {
		return errBrokerAttemptFenced
	}
	a := s.brokerAccess
	if a.Current.Attempt != attempt || a.Current.Phase == "terminal" || a.AdmittedSequence == math.MaxUint64 || attempt.Sequence != a.AdmittedSequence+1 {
		return errBrokerAttemptFenced
	}
	a.Current = nil
	s.brokerAttemptCompleted = BrokerAttempt{}
	s.brokerAttemptRestored = false
	return nil
}

// RecordBrokerInvocationResult marks an exact successful receipt for the next
// RecordToolResults call. The marker is process-local and never restored.
func (s *Session) RecordBrokerInvocationResult(attempt BrokerAttempt, id ToolCallID) error {
	if s.brokerAccess == nil || s.brokerAccess.Current == nil || s.brokerAttemptRestored {
		return errBrokerAttemptFenced
	}
	c := s.brokerAccess.Current
	if c.Attempt != attempt || c.CallID != id || c.Phase != "dispatched" {
		return errBrokerAttemptFenced
	}
	s.brokerAttemptCompleted = attempt
	return nil
}

func (s *Session) recordBrokerAttemptPair(id ToolCallID) {
	if s.brokerAccess == nil || s.brokerAccess.Current == nil {
		return
	}
	c := s.brokerAccess.Current
	if c.Phase == "dispatched" && c.CallID == id && c.Attempt == s.brokerAttemptCompleted {
		c.Phase, c.Disposition = "terminal", BrokerAttemptCompleted
		s.brokerAccess.AdmittedSequence = c.Attempt.Sequence
		s.brokerAttemptCompleted = BrokerAttempt{}
	}
}
