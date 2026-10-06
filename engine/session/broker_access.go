package session

import (
	"encoding/base64"
	"errors"
	"slices"
	"time"
)

// BrokerAccess contains only public stable references and host execution fences.
// Attempted calls are never replayed, including after an uncertain result.
type BrokerAccess struct {
	Session          BrokerSessionRef   `json:"session"`
	Catalogue        BrokerCatalogueRef `json:"catalogue"`
	ExpiresAt        time.Time          `json:"expires_at"`
	Withdrawn        bool               `json:"withdrawn,omitempty"`
	Attempted        []ToolCallID       `json:"attempted,omitempty"`
	Pending          ToolCallID         `json:"pending,omitempty"`
	IndependentTools []string           `json:"independent_tools"`
	BrokerTools      []string           `json:"broker_tools"`
}

func validBrokerReference(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return len(s) == 43 && err == nil && len(b) == 32
}

// BrokerAccess returns an independent copy of the durable broker state.
func (s *Session) BrokerAccess() (BrokerAccess, bool) {
	if s.brokerAccess == nil {
		return BrokerAccess{}, false
	}
	a := *s.brokerAccess
	a.Attempted = slices.Clone(a.Attempted)
	a.IndependentTools = slices.Clone(a.IndependentTools)
	a.BrokerTools = slices.Clone(a.BrokerTools)
	if a.Pending != "" && a.Pending == s.brokerCompleted && s.Conversation != nil {
		for _, message := range s.Conversation.Messages {
			if message.Role == RoleTool && message.ToolResult != nil && message.ToolResult.CallID == a.Pending {
				a.Pending = ""
				break
			}
		}
	}
	return a, true
}

// RestoreBrokerAccess validates stable metadata without importing broker custody.
func (s *Session) RestoreBrokerAccess(a BrokerAccess) error {
	if err := s.validateBrokerAccess(a, s.Authority); err != nil {
		return err
	}
	a.Attempted = slices.Clone(a.Attempted)
	a.IndependentTools = slices.Clone(a.IndependentTools)
	a.BrokerTools = slices.Clone(a.BrokerTools)
	s.brokerAccess = &a
	return nil
}

func (s *Session) validateBrokerAccess(a BrokerAccess, authority Authority) error {
	if a.IndependentTools == nil || a.BrokerTools == nil || !validAuthorityNames(a.IndependentTools) ||
		!ValidWorkspaceEnrollmentToolNames(a.BrokerTools) || !authority.Valid() ||
		(a.Withdrawn && len(a.BrokerTools) != 0) || !validAuthorityNames(authority.CapabilitySet.Tools) {
		return errors.New("session: invalid broker contributions")
	}
	for _, name := range a.BrokerTools {
		if authority.BrokerToolScope != nil && !slices.Contains(*authority.BrokerToolScope, name) {
			return errors.New("session: broker contribution exceeds scope")
		}
	}
	projected := unionToolNames(a.IndependentTools, a.BrokerTools)
	if len(projected) != len(authority.CapabilitySet.Tools) {
		return errors.New("session: broker authority projection mismatch")
	}
	for _, name := range projected {
		if !slices.Contains(authority.CapabilitySet.Tools, name) {
			return errors.New("session: broker authority projection mismatch")
		}
	}
	if !validBrokerReference(string(a.Session)) || !validBrokerReference(string(a.Catalogue)) || a.ExpiresAt.IsZero() || len(a.Attempted) > 64 || s.ExternalBinding != "" || s.brokerCredentialCustody != nil || !s.authorityBound {
		return errors.New("session: invalid broker access")
	}
	seen := make(map[ToolCallID]bool)
	for _, id := range a.Attempted {
		if id == "" || len(id) > 256 || seen[id] {
			return errors.New("session: invalid broker attempt")
		}
		seen[id] = true
	}
	if a.Pending != "" && !seen[a.Pending] {
		return errors.New("session: invalid pending broker invocation")
	}
	if s.brokerAccess != nil && s.brokerAccess.Session != a.Session {
		return errors.New("session: broker session cannot change")
	}
	return nil
}

// AdoptBrokerCatalogue installs exact authority while preserving all replay fences.
func (s *Session) AdoptBrokerCatalogue(ref BrokerSessionRef, catalogue BrokerCatalogueRef, expires time.Time, names []string) error {
	if !ValidWorkspaceEnrollmentToolNames(names) {
		return errors.New("session: invalid broker tool authority")
	}
	a, ok := s.BrokerAccess()
	if a.Withdrawn {
		return errors.New("session: broker authority withdrawn")
	}
	if !ok {
		a.IndependentTools = unionToolNames(s.Authority.CapabilitySet.Tools, nil)
	}
	a.Session, a.Catalogue, a.ExpiresAt = ref, catalogue, expires
	a.BrokerTools = []string{}
	for _, name := range names {
		if s.Authority.BrokerToolScope == nil || slices.Contains(*s.Authority.BrokerToolScope, name) {
			a.BrokerTools = append(a.BrokerTools, name)
		}
	}
	return s.applyBrokerProjection(a)
}

func (s *Session) applyBrokerProjection(a BrokerAccess) error {
	authority := s.Authority.Clone()
	authority.CapabilitySet.Tools = unionToolNames(a.IndependentTools, a.BrokerTools)
	if err := s.validateBrokerAccess(a, authority); err != nil {
		return err
	}
	s.Authority = authority
	s.brokerAccess = &a
	return nil
}

func validAuthorityNames(names []string) bool {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !validToolAuthorityName(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func unionToolNames(first, second []string) []string {
	out := append([]string{}, first...)
	for _, name := range second {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// FenceBrokerInvocation must be saved before dispatch. It deliberately cannot
// distinguish a crashed completed call from an unresolved one and never retries.
func (s *Session) FenceBrokerInvocation(ref BrokerSessionRef, catalogue BrokerCatalogueRef, id ToolCallID, now time.Time) error {
	a, ok := s.BrokerAccess()
	if !ok || a.Withdrawn || (a.Pending != "" && s.brokerCompleted != a.Pending) || a.Session != ref || a.Catalogue != catalogue || !a.ExpiresAt.After(now) || id == "" || len(id) > 256 || slices.Contains(a.Attempted, id) || len(a.Attempted) >= 64 {
		return errors.New("session: broker invocation fenced; do not repeat an uncertain operation")
	}
	a.Attempted = append(a.Attempted, id)
	a.Pending = id
	s.brokerCompleted = ""
	return s.RestoreBrokerAccess(a)
}

// RecordBrokerInvocationResult releases the fence only when the engine later
// records the paired result. A crash before that save remains unresolved.
func (s *Session) RecordBrokerInvocationResult(id ToolCallID) {
	if s.brokerAccess != nil && s.brokerAccess.Pending == id {
		s.brokerCompleted = id
	}
}

// WithdrawBrokerAccess durably fences every old catalogue and completion.
func (s *Session) WithdrawBrokerAccess() error {
	a, ok := s.BrokerAccess()
	if !ok {
		return errors.New("session: broker access not installed")
	}
	a.Withdrawn = true
	a.BrokerTools = []string{}
	return s.applyBrokerProjection(a)
}
