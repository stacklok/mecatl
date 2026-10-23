package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// The identity broker ties a user's access and a session's agents to a broker
// session. With Config.IdentityBroker set, every root session that has an
// owner gets one: opened, and its main agent registered, when the session is
// created and again, idempotently, before each run; closed when the session is
// deleted. Releasing a session locally leaves its broker session open and drops
// the credential held for its agent.

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

// identityAgent is the credential held for a root session's main agent. It is
// kept in memory only, never persisted and never given to the engine or the
// model, so the MCP broker can present it later.
type identityAgent struct {
	// incarnation is the session incarnation the agent was registered with.
	incarnation string
	// brokerSessionID is the broker session the agent was registered to; the
	// credential verifies only there.
	brokerSessionID string
	registration    identitybroker.Registration
	// renewAt is when half the credential's lifetime has passed, after which
	// run entry registers again, as the design renews credentials.
	renewAt time.Time
}

// openIdentityBrokerSession opens sess's broker session, or confirms the one
// already open, and registers the session's main agent in it. It does nothing
// without an identity broker, for a session that is not a root, or for a
// session without an owner, whose gateway calls then fail closed. sess must be
// the saved session, so its incarnation is the persisted one. The caller holds
// sess's runEntryMu, so a close or delete cannot race the registration.
//
// If registration finds the broker session closed, it opens a new one and
// registers again, once; closed a second time, it fails as unavailable.
func (s *Service) openIdentityBrokerSession(ctx context.Context, sess *session.Session) error {
	if s.cfg.IdentityBroker == nil || !identityBrokerRoot(sess) {
		return nil
	}
	owner, ok := identityBrokerOwner(sess.Owner)
	if !ok {
		return nil
	}
	for attempt := 0; ; attempt++ {
		brokerSession, err := s.openIdentityBrokerSessionOnce(ctx, sess, owner)
		if err != nil {
			return err
		}
		err = s.registerIdentityAgent(ctx, sess, owner, brokerSession)
		if errors.Is(err, errIdentityBrokerSessionClosed) {
			if attempt == 0 {
				continue
			}
			// Closed again: something keeps closing it, such as a delete in
			// flight. Report it as retryable, like an unavailable broker.
			return ErrIdentityBrokerUnavailable
		}
		return err
	}
}

// openIdentityBrokerSessionOnce returns sess's open broker session, opening
// one if needed.
func (s *Service) openIdentityBrokerSessionOnce(ctx context.Context, sess *session.Session, owner identitybroker.Owner) (identitybroker.Session, error) {
	broker := s.cfg.IdentityBroker
	host, incarnation := string(sess.ID), string(sess.Incarnation())
	open, found, err := broker.FindSession(ctx, identitybroker.FindSessionRequest{RootHostSessionID: host})
	if err != nil {
		return identitybroker.Session{}, s.identityBrokerFailure(ctx, "find", host, "", err)
	}
	if found && open.Incarnation == incarnation && open.Owner == owner {
		return open, nil
	}
	if found && open.Incarnation != incarnation && open.Owner == owner {
		// A broker session left open for an earlier incarnation of this host
		// session ID: that incarnation no longer exists, so its broker session
		// cannot be used again. Close it so this incarnation can open.
		if err := broker.CloseSession(ctx, identitybroker.CloseSessionRequest{SessionID: open.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
			return identitybroker.Session{}, s.identityBrokerFailure(ctx, "close stale", host, open.ID, err)
		}
		s.logIdentityBroker(ctx, "closed stale broker session", host, open.ID)
	}
	// A broker session open under another owner is never closed here: the
	// broker refuses the open with session_owned, which the design treats as a
	// pairing bug that fails the open. Session IDs are server-generated, so
	// another user cannot reuse this one.
	opened, err := broker.OpenSession(ctx, identitybroker.OpenSessionRequest{HostSessionID: host, Incarnation: incarnation, Owner: owner})
	if err != nil {
		return identitybroker.Session{}, s.identityBrokerFailure(ctx, "open", host, "", err)
	}
	s.logIdentityBroker(ctx, "opened broker session", host, opened.ID)
	return opened, nil
}

// errIdentityBrokerSessionClosed marks a registration refused because the
// broker session closed, so openIdentityBrokerSession opens a new one.
var errIdentityBrokerSessionClosed = errors.New("identity broker session closed")

// registerIdentityAgent registers sess's main agent in brokerSession, unless
// the credential held for it is still current there: registered to the same
// broker session, with more than half its lifetime left. The main agent's
// incarnation is the session's own, which the broker session was opened with.
// The returned agent must be the one registered: its SPIFFE ID ends in the
// incarnation, and registering again returns the same ID.
func (s *Service) registerIdentityAgent(ctx context.Context, sess *session.Session, owner identitybroker.Owner, brokerSession identitybroker.Session) error {
	host, incarnation := string(sess.ID), string(sess.Incarnation())
	now := s.cfg.Now()
	s.mu.Lock()
	held, ok := s.identityAgents[sess.ID]
	s.mu.Unlock()
	// A credential held for another incarnation of this session ID belongs to
	// an agent that no longer exists here: replace it rather than compare.
	ok = ok && held.incarnation == incarnation
	if ok && held.brokerSessionID == brokerSession.ID && now.Before(held.renewAt) {
		return nil
	}
	reg, err := s.cfg.IdentityBroker.RegisterAgent(ctx, identitybroker.RegisterAgentRequest{
		SessionID:     brokerSession.ID,
		HostSessionID: host,
		Owner:         owner,
		Incarnation:   incarnation,
		Definition:    identityAgentDefinition(sess),
	})
	if errors.Is(err, identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "")) {
		s.logIdentityBroker(ctx, "broker session closed during registration", host, brokerSession.ID)
		return errIdentityBrokerSessionClosed
	}
	if err != nil {
		return s.identityBrokerFailure(ctx, "register", host, brokerSession.ID, err)
	}
	// A mismatch keeps the held credential, so every later run fails the same
	// way until the session is released. That is deliberate: in this phase the
	// ID is built from the incarnation, so it changes only if the broker
	// misbehaves, and dropping the held agent would let the next run accept
	// the wrong one. A legitimate change, such as another trust domain or
	// per-agent identities, comes with a restart, which clears what is held.
	if !strings.HasSuffix(reg.Agent.Subject, "/inst/"+incarnation) || ok && reg.Agent != held.registration.Agent {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker returned another agent",
			"session", host, "broker_session", brokerSession.ID)
		return fmt.Errorf("%w: identity broker returned another agent", ErrInternal)
	}
	s.mu.Lock()
	// Losing the session's lease takes leaseLossMu, not runEntryMu, so it can
	// land while the broker call is in flight. Another process now runs the
	// session's agent, so this one keeps no credential for it.
	if _, lost := s.lostOwnership[sess.ID]; lost {
		s.mu.Unlock()
		return nil
	}
	s.identityAgents[sess.ID] = identityAgent{
		incarnation:     incarnation,
		brokerSessionID: brokerSession.ID,
		registration:    reg,
		renewAt:         now.Add(reg.ExpiresAt.Sub(now) / 2),
	}
	s.mu.Unlock()
	s.logIdentityBroker(ctx, "registered agent", host, brokerSession.ID)
	return nil
}

// openSavedIdentityBrokerSession opens the broker session of session id and
// registers its agent, as create, fork, and clear do after saving it. It loads
// the session under id's runEntryMu, as run entry does, so a delete that won
// the lock first leaves nothing to open instead of a broker session that no
// delete will close. A failure is the caller's to log; registration is retried
// at run entry.
func (s *Service) openSavedIdentityBrokerSession(ctx context.Context, id session.SessionID) error {
	if s.cfg.IdentityBroker == nil {
		return nil
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		return err
	}
	return s.openIdentityBrokerSession(ctx, sess)
}

// identityAgentDefinition is the definition a session's main agent runs: its
// named definition, otherwise the built-in main. A session knows a named
// definition by name only, so its source and revision are not sent.
func identityAgentDefinition(sess *session.Session) identitybroker.DefinitionRef {
	if sess.AgentDefinitionName != "" {
		return identitybroker.DefinitionRef{Name: sess.AgentDefinitionName}
	}
	return identitybroker.DefinitionRef{Source: "system", Name: "main"}
}

// openTeamRootIdentityBrokerSession opens the broker session of the session a
// team is rooted in before the team runs. Team runs do not pass run entry, so
// without this a team could run in a session tree whose broker session was
// lost at a restart. A team on the default placement has no root session and
// its members belong to no session tree; helper agents get identities later.
func (s *Service) openTeamRootIdentityBrokerSession(ctx context.Context, root session.SessionID) error {
	if s.cfg.IdentityBroker == nil || root == "" {
		return nil
	}
	// Load and register under the root's runEntryMu, as run entry does: a
	// delete of the root then either finishes first, so the load fails, or
	// waits, so it cannot close the broker session mid-registration and have
	// it reopened for a deleted session.
	unlock := s.runEntryMu.lock(root)
	defer unlock()
	sess, err := s.cfg.Store.Load(ctx, root)
	if err != nil {
		return fmt.Errorf("%w: load team root session: %v", ErrInternal, err)
	}
	return s.openIdentityBrokerSession(ctx, sess)
}

// EndScheduledRun closes a scheduled session's broker session when its run
// ends: the design ends a scheduled run's authority with the run. The
// scheduler calls it with the fire's session as each fire returns; the session
// itself is kept for result delivery. Only a scheduled session's broker
// session is closed, so an interactive session passed by mistake keeps its
// broker session. A failure is logged, not returned.
func (s *Service) EndScheduledRun(ctx context.Context, sess *session.Session) {
	if sess == nil || sess.Kind != session.SessionKindScheduled {
		return
	}
	s.closeIdentityBrokerSession(ctx, sess.ID)
	// The held credential verified only in the closed broker session.
	s.mu.Lock()
	delete(s.identityAgents, sess.ID)
	s.mu.Unlock()
}

// closeIdentityBrokerSession closes the broker session of a host session that
// was deleted or whose scheduled run ended. A failure is logged, not returned:
// the session's state has already moved on. Every deletion path calls it once
// the durable record is gone, before anything that can still fail and return;
// no local release does. Delete paths hold the session's locks, so the calls
// are bounded by the same timeout as the MCP broker cleanup.
func (s *Service) closeIdentityBrokerSession(ctx context.Context, id session.SessionID) {
	broker := s.cfg.IdentityBroker
	if broker == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	defer cancel()
	host := string(id)
	open, found, err := broker.FindSession(ctx, identitybroker.FindSessionRequest{RootHostSessionID: host})
	if err == nil && found {
		err = broker.CloseSession(ctx, identitybroker.CloseSessionRequest{SessionID: open.ID, Reason: identitybroker.ReasonSessionClosed})
	}
	if err != nil {
		_ = s.identityBrokerFailure(ctx, "close", host, open.ID, err)
		return
	}
	if found {
		s.logIdentityBroker(ctx, "closed broker session", host, open.ID)
	}
}

// identityBrokerFailure logs a failed identity broker call and returns the
// error for the client: the caller's cancellation when the caller went away,
// ErrIdentityBrokerUnavailable when the broker is unavailable or did not answer
// in time, so the client can retry, and ErrInternal otherwise. brokerSession is
// the broker session involved, or empty when none is known yet.
func (s *Service) identityBrokerFailure(ctx context.Context, call, host, brokerSession string, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelInfo, "identity broker call cancelled by the caller",
			"call", call, "session", host, "broker_session", brokerSession)
		return fmt.Errorf("identity broker %s: %w", call, ctx.Err())
	}
	code := identitybroker.CodeInternal
	var brokerErr *identitybroker.Error
	switch {
	case errors.As(err, &brokerErr):
		code = brokerErr.Code
	case errors.Is(err, context.DeadlineExceeded):
		code = identitybroker.CodeUnavailable
	}
	s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker call failed",
		"call", call, "session", host, "broker_session", brokerSession, "code", string(code))
	if code == identitybroker.CodeUnavailable {
		return ErrIdentityBrokerUnavailable
	}
	return fmt.Errorf("%w: identity broker %s failed with %s", ErrInternal, call, code)
}

func (s *Service) logIdentityBroker(ctx context.Context, msg, host, brokerSession string) {
	s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelInfo, msg, "session", host, "broker_session", brokerSession)
}
