// Package standin is a trivial identity broker used until the real
// implementation exists. It keeps broker sessions in memory and follows the
// identitybroker.Service contract, registers every agent as one fixed agent
// with a placeholder credential no verifier accepts, and authorizes no calls,
// so nothing reaches a gateway through it.
package standin

import (
	"context"
	"crypto/rand"
	"sync"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// fixedAgent and placeholderCredential are what RegisterAgent returns for
// every agent. The credential is not a JWT, so no verifier accepts it.
var (
	fixedAgent            = identitybroker.AgentRef{Subject: "spiffe://standin.invalid/mecatl/agent/standin"}
	placeholderCredential = identitybroker.NewToken("standin-placeholder-credential")
)

// Broker is the stand-in identity broker. It is safe for concurrent use.
//
// State lives in one process. Session IDs are random, so they are not
// guessable and do not repeat after a restart, but a closed session is
// refused only by the process that closed it, and nothing is shared between
// replicas. Closed sessions are kept for the life of the process so they keep
// refusing calls; nothing evicts them. The real implementation replaces the
// stand-in before any of this matters beyond a single replica.
type Broker struct {
	mu       sync.Mutex
	open     map[string]string // root host session ID -> broker session ID
	sessions map[string]*session
}

type session struct {
	identitybroker.Session
	closed identitybroker.Reason
}

var _ identitybroker.Service = (*Broker)(nil)

// New returns an empty stand-in broker.
func New() *Broker {
	return &Broker{open: map[string]string{}, sessions: map[string]*session{}}
}

// OpenSession implements identitybroker.Service.
func (b *Broker) OpenSession(_ context.Context, req identitybroker.OpenSessionRequest) (identitybroker.Session, error) {
	if err := req.Validate(); err != nil {
		return identitybroker.Session{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if id, ok := b.open[req.HostSessionID]; ok {
		s := b.sessions[id].Session
		switch {
		case s.Owner != req.Owner:
			return identitybroker.Session{}, identitybroker.ReasonError(identitybroker.ReasonSessionOwned, "the host session's broker session belongs to another owner")
		case s.Incarnation != req.Incarnation:
			return identitybroker.Session{}, identitybroker.NewError(identitybroker.CodeAlreadyExists, "", "the host session has an open broker session for another incarnation")
		}
		return s, nil
	}
	s := identitybroker.Session{
		ID:            "standin-" + rand.Text(),
		HostSessionID: req.HostSessionID,
		Incarnation:   req.Incarnation,
		Owner:         req.Owner,
	}
	b.sessions[s.ID] = &session{Session: s}
	b.open[s.HostSessionID] = s.ID
	return s, nil
}

// FindSession implements identitybroker.Service.
func (b *Broker) FindSession(_ context.Context, req identitybroker.FindSessionRequest) (identitybroker.Session, bool, error) {
	if err := req.Validate(); err != nil {
		return identitybroker.Session{}, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.open[req.RootHostSessionID]
	if !ok {
		return identitybroker.Session{}, false, nil
	}
	return b.sessions[id].Session, true, nil
}

// CloseSession implements identitybroker.Service.
func (b *Broker) CloseSession(_ context.Context, req identitybroker.CloseSessionRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[req.SessionID]
	if !ok {
		return unknownSession()
	}
	if s.closed == "" {
		s.closed = req.Reason
		delete(b.open, s.HostSessionID)
	}
	return nil
}

// RegisterAgent implements identitybroker.Service. Every agent is registered
// as the same fixed agent with the placeholder credential.
func (b *Broker) RegisterAgent(_ context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
	if err := req.Validate(); err != nil {
		return identitybroker.Registration{}, err
	}
	s, err := b.usable(req.SessionID)
	if err != nil {
		return identitybroker.Registration{}, err
	}
	if req.Owner != s.Owner {
		return identitybroker.Registration{}, identitybroker.NewError(identitybroker.CodePermissionDenied, "", "the registration's owner is not the broker session's owner")
	}
	return identitybroker.Registration{Agent: fixedAgent, Credential: placeholderCredential}, nil
}

// AuthorizeCall implements identitybroker.Service. It denies every call.
func (b *Broker) AuthorizeCall(_ context.Context, req identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error) {
	if err := req.Validate(); err != nil {
		return identitybroker.Authorization{}, err
	}
	if _, err := b.usable(req.SessionID); err != nil {
		return identitybroker.Authorization{}, err
	}
	return identitybroker.Authorization{}, identitybroker.ReasonError(identitybroker.ReasonDenied, "the stand-in identity broker authorizes no calls")
}

// usable returns the open session for id, or the error for a session that
// does not exist or is closed.
func (b *Broker) usable(id string) (identitybroker.Session, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[id]
	if !ok {
		return identitybroker.Session{}, unknownSession()
	}
	if s.closed != "" {
		return identitybroker.Session{}, identitybroker.ReasonError(s.closed, "the broker session is closed")
	}
	return s.Session, nil
}

func unknownSession() error {
	return identitybroker.NewError(identitybroker.CodePermissionDenied, "", "unknown broker session")
}
