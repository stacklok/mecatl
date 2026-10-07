package session

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
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

// BrokerHostAttempt is the host's sole durable may-execute marker.
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
