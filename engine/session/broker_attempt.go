package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

// BrokerAttempt identifies one occurrence, never a reusable provider call ID.
// It carries no retry authority or broker-side idempotency promise.
type BrokerAttempt struct {
	ID string `json:"id"`
}

// NewBrokerAttempt allocates an unguessable occurrence identity.
func NewBrokerAttempt() BrokerAttempt {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return BrokerAttempt{ID: base64.RawURLEncoding.EncodeToString(b[:])}
}

// Valid checks framing, not execution authority.
func (a BrokerAttempt) Valid() bool { return validBrokerReference(a.ID) }

// BrokerAttemptDisposition describes the immediate execution response only.
type BrokerAttemptDisposition string

const (
	BrokerAttemptCompleted     BrokerAttemptDisposition = "completed"
	BrokerAttemptNotDispatched BrokerAttemptDisposition = "not_dispatched"
	BrokerAttemptUnknown       BrokerAttemptDisposition = "unknown"
)

// BrokerHostAttempt is the host's sole durable may-execute marker.
// Legacy slot snapshots have no valid opaque identity and fail closed.
type BrokerHostAttempt struct {
	Attempt BrokerAttempt `json:"attempt"`
	CallID  ToolCallID    `json:"call_id"`
	Digest  [32]byte      `json:"digest"`
}

var errBrokerAttemptFenced = errors.New("session: broker execution outcome uncertain; automatic resend forbidden")

func validateBrokerHostAttempt(a BrokerAccess) error {
	if c := a.Current; c != nil {
		if !c.Attempt.Valid() || c.CallID == "" || len(c.CallID) > 256 || !utf8.ValidString(string(c.CallID)) || c.Digest == ([32]byte{}) {
			return errBrokerAttemptFenced
		}
	}
	return nil
}

// BrokerCallDigest binds the byte-exact prepared outer call, including query args.
func BrokerCallDigest(call ToolCall) [32]byte {
	b, _ := json.Marshal(struct {
		ID        ToolCallID
		Name      string
		Arguments []byte
	}{call.ID, call.Name, call.Args})
	return sha256.Sum256(b)
}

// PrepareBrokerInvocation prepares process-local metadata. Preflight cannot
// execute and does not persist uncertainty or reserve broker execution.
func (s *Session) PrepareBrokerInvocation(ref BrokerSessionRef, catalogue BrokerCatalogueRef, call ToolCall, now time.Time) (BrokerAttempt, error) {
	a, ok := s.BrokerAccess()
	if !ok || a.Withdrawn || a.Current != nil || a.Session != ref || a.Catalogue != catalogue || !a.ExpiresAt.After(now) ||
		len(a.Attempted) != 0 || a.Pending != "" || call.ID == "" || len(call.ID) > 256 || !utf8.ValidString(string(call.ID)) ||
		call.Name == "" || len(call.Name) > 256 || !utf8.ValidString(call.Name) || len(call.Args) > 256*1024 || !json.Valid(call.Args) {
		return BrokerAttempt{}, errBrokerAttemptFenced
	}
	attempt := NewBrokerAttempt()
	s.brokerPrepared = &BrokerHostAttempt{Attempt: attempt, CallID: call.ID, Digest: BrokerCallDigest(call)}
	return attempt, nil
}

// ContinueBrokerInvocation accepts only the exact live prepared call.
func (s *Session) ContinueBrokerInvocation(call ToolCall) (BrokerAttempt, error) {
	if s.brokerAccess == nil || s.brokerAccess.Withdrawn || s.brokerAccess.Current != nil || s.brokerPrepared == nil {
		return BrokerAttempt{}, errBrokerAttemptFenced
	}
	c := s.brokerPrepared
	if c.CallID != call.ID || c.Digest != BrokerCallDigest(call) {
		return BrokerAttempt{}, errBrokerAttemptFenced
	}
	return c.Attempt, nil
}

// DispatchBrokerInvocation installs uncertainty; save it before sending once.
func (s *Session) DispatchBrokerInvocation(attempt BrokerAttempt) error {
	if s.brokerAccess == nil || s.brokerAccess.Withdrawn || s.brokerAccess.Current != nil || s.brokerPrepared == nil || s.brokerPrepared.Attempt != attempt {
		return errBrokerAttemptFenced
	}
	current := *s.brokerPrepared
	s.brokerAccess.Current = &current
	s.brokerPrepared = nil
	s.brokerAttemptRestored = false
	s.brokerAttemptCompleted = BrokerAttempt{}
	return nil
}

// RecordBrokerInvocationResult accepts only the immediate verified response for
// the current occurrence. Clearing happens together with the paired result.
func (s *Session) RecordBrokerInvocationResult(attempt BrokerAttempt, id ToolCallID) error {
	if s.brokerAccess == nil || s.brokerAccess.Current == nil || s.brokerAttemptRestored {
		return errBrokerAttemptFenced
	}
	c := s.brokerAccess.Current
	if c.Attempt != attempt || c.CallID != id {
		return errBrokerAttemptFenced
	}
	s.brokerAttemptCompleted = attempt
	return nil
}

// ParkBrokerInvocation replaces verified zero-dispatch uncertainty with the
// already-installed pending authorization. Save both changes atomically.
func (s *Session) ParkBrokerInvocation(attempt BrokerAttempt) error {
	pending, ok := s.PendingAuthorization()
	if !ok || s.brokerAccess == nil || s.brokerAccess.Current == nil || s.brokerAttemptRestored {
		return errBrokerAttemptFenced
	}
	current := s.brokerAccess.Current
	if current.Attempt != attempt || current.Digest != BrokerCallDigest(pending.Call) || pending.Authorization.Binding != AuthorizationBinding(s.brokerAccess.Session) {
		return errBrokerAttemptFenced
	}
	s.brokerAccess.Current = nil
	s.brokerAttemptCompleted = BrokerAttempt{}
	return nil
}

func (s *Session) recordBrokerAttemptPair(id ToolCallID) {
	if s.brokerAccess == nil || s.brokerAccess.Current == nil {
		return
	}
	c := s.brokerAccess.Current
	if c.CallID == id && c.Attempt == s.brokerAttemptCompleted && !s.brokerAttemptRestored {
		s.brokerAccess.Current = nil
		s.brokerAttemptCompleted = BrokerAttempt{}
	}
}
