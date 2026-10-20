// Package standin is a trivial identity broker used until the real
// implementation exists. It keeps broker sessions in memory and follows the
// identitybroker.Service contract, registers every agent as one fixed agent
// with a placeholder credential no verifier accepts, and authorizes no calls,
// so nothing reaches a gateway through it.
package standin

import (
	"context"
	"strconv"
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
// Closed sessions are kept, so they keep refusing calls and their IDs are
// never reused.
type Broker struct {
	mu       sync.Mutex
	nextID   uint64
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
	b.nextID++
	s := identitybroker.Session{
		ID:            "standin-" + strconv.FormatUint(b.nextID, 10),
		HostSessionID: req.HostSessionID,
		Incarnation:   req.Incarnation,
		Owner:         req.Owner,
	}
	b.sessions[s.ID] = &session{Session: s}
	b.open[s.HostSessionID] = s.ID
	return s, nil
}

// FindSession implements identitybroker.Service.
func (b *Broker) FindSession(_ context.Context, rootHostSessionID string) (identitybroker.Session, bool, error) {
	if rootHostSessionID == "" {
		return identitybroker.Session{}, false, identitybroker.NewError(identitybroker.CodeInvalidArgument, "", "root host session ID is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.open[rootHostSessionID]
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
	if err := b.usable(req.SessionID); err != nil {
		return identitybroker.Registration{}, err
	}
	return identitybroker.Registration{Agent: fixedAgent, Credential: placeholderCredential}, nil
}

// AuthorizeCall implements identitybroker.Service. It denies every call.
func (b *Broker) AuthorizeCall(_ context.Context, req identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error) {
	if err := req.Validate(); err != nil {
		return identitybroker.Authorization{}, err
	}
	if err := b.usable(req.SessionID); err != nil {
		return identitybroker.Authorization{}, err
	}
	return identitybroker.Authorization{}, identitybroker.ReasonError(identitybroker.ReasonDenied, "the stand-in identity broker authorizes no calls")
}

// usable returns the error for a session that does not exist or is closed.
func (b *Broker) usable(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[id]
	if !ok {
		return unknownSession()
	}
	if s.closed != "" {
		return identitybroker.ReasonError(s.closed, "the broker session is closed")
	}
	return nil
}

func unknownSession() error {
	return identitybroker.NewError(identitybroker.CodePermissionDenied, "", "unknown broker session")
}
