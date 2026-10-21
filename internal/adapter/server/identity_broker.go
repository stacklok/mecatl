package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// The identity broker ties a user's access and a session's agents to a broker
// session. With Config.IdentityBroker set, every root session that has an
// owner gets one: opened when the session is created and again, idempotently,
// before each run, and closed when the session is deleted. Releasing a session
// locally leaves its broker session open.

// identityBrokerRoot reports whether sess is the root of a session tree, which
// gets a broker session of its own. Subagents, parallel branches, and team
// members belong to their parent's tree.
func identityBrokerRoot(sess *session.Session) bool {
	switch sess.Kind {
	case session.SessionKindMain, session.SessionKindScheduled, session.SessionKindDebug, session.SessionKindUnknown:
		return true
	default:
		return false
	}
}

// identityBrokerOwner converts a session's owner to the broker's owner, the
// user's company identity. It is the only place that conversion happens. A
// scheduled session's owner is the schedule's captured owner, so it converts
// the same way.
func identityBrokerOwner(owner *session.Principal) (identitybroker.Owner, bool) {
	if owner == nil {
		return identitybroker.Owner{}, false
	}
	return identitybroker.Owner{Issuer: owner.Issuer, Subject: owner.Subject}, true
}

// openIdentityBrokerSession opens sess's broker session, or confirms the one
// already open. It does nothing without an identity broker, for a session that
// is not a root, or for a session without an owner, whose gateway calls then
// fail closed. sess must be the saved session, so its incarnation is the
// persisted one.
func (s *Service) openIdentityBrokerSession(ctx context.Context, sess *session.Session) error {
	broker := s.cfg.IdentityBroker
	if broker == nil || !identityBrokerRoot(sess) {
		return nil
	}
	owner, ok := identityBrokerOwner(sess.Owner)
	if !ok {
		return nil
	}
	host, incarnation := string(sess.ID), string(sess.Incarnation())
	open, found, err := broker.FindSession(ctx, identitybroker.FindSessionRequest{RootHostSessionID: host})
	if err != nil {
		return s.identityBrokerFailure(ctx, "find", host, err)
	}
	if found && open.Incarnation == incarnation && open.Owner == owner {
		return nil
	}
	if found && open.Incarnation != incarnation && open.Owner == owner {
		// A broker session left open for an earlier incarnation of this host
		// session ID: that incarnation no longer exists, so its broker session
		// cannot be used again. Close it so this incarnation can open.
		if err := broker.CloseSession(ctx, identitybroker.CloseSessionRequest{SessionID: open.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
			return s.identityBrokerFailure(ctx, "close stale", host, err)
		}
		s.logIdentityBroker(ctx, "closed stale broker session", host, open.ID)
	}
	opened, err := broker.OpenSession(ctx, identitybroker.OpenSessionRequest{HostSessionID: host, Incarnation: incarnation, Owner: owner})
	if err != nil {
		return s.identityBrokerFailure(ctx, "open", host, err)
	}
	s.logIdentityBroker(ctx, "opened broker session", host, opened.ID)
	return nil
}

// closeIdentityBrokerSession closes the broker session of a deleted host
// session. The durable record is already gone, so a failure is logged, not
// returned.
func (s *Service) closeIdentityBrokerSession(ctx context.Context, id session.SessionID) {
	broker := s.cfg.IdentityBroker
	if broker == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	host := string(id)
	open, found, err := broker.FindSession(ctx, identitybroker.FindSessionRequest{RootHostSessionID: host})
	if err == nil && found {
		err = broker.CloseSession(ctx, identitybroker.CloseSessionRequest{SessionID: open.ID, Reason: identitybroker.ReasonSessionClosed})
	}
	if err != nil {
		_ = s.identityBrokerFailure(ctx, "close", host, err)
		return
	}
	if found {
		s.logIdentityBroker(ctx, "closed broker session", host, open.ID)
	}
}

// identityBrokerFailure logs a failed identity broker call and returns the
// error for the client: ErrIdentityBrokerUnavailable when the broker is
// unavailable, so the client can retry, and ErrInternal otherwise.
func (s *Service) identityBrokerFailure(ctx context.Context, call, host string, err error) error {
	code := identitybroker.CodeInternal
	var brokerErr *identitybroker.Error
	if errors.As(err, &brokerErr) {
		code = brokerErr.Code
	}
	s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker call failed",
		"call", call, "session", host, "code", string(code))
	if code == identitybroker.CodeUnavailable {
		return ErrIdentityBrokerUnavailable
	}
	return fmt.Errorf("%w: identity broker %s failed with %s", ErrInternal, call, code)
}

func (s *Service) logIdentityBroker(ctx context.Context, msg, host, brokerSession string) {
	s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelInfo, msg, "session", host, "broker_session", brokerSession)
}
