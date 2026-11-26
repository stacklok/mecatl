package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/agentcredential"
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

// identityBrokerOwner converts a session's or a run's owner to the broker's
// owner, the user's company identity. It is the only place that conversion
// happens. A scheduled session's owner is the schedule's captured owner, so it
// converts the same way.
func identityBrokerOwner(owner *session.Principal) (identitybroker.Owner, bool) {
	if owner == nil {
		return identitybroker.Owner{}, false
	}
	return identitybroker.Owner{Issuer: owner.Issuer, Subject: owner.Subject}, true
}

// identityAgentsFor returns cfg's credential holder, creating one when cfg has
// an identity broker and no holder. cfg.Now must be set.
func identityAgentsFor(cfg Config) *agentcredential.Holder {
	if cfg.IdentityBroker == nil || cfg.IdentityAgents != nil {
		return cfg.IdentityAgents
	}
	return agentcredential.New(cfg.IdentityBroker, cfg.Now)
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
	owner, mayPresent, ok := s.identityBrokerOpenFor(sess)
	if !ok {
		return nil
	}
	for attempt := 0; ; attempt++ {
		brokerSession, presented, err := s.openIdentityBrokerSessionOnce(ctx, sess, owner, mayPresent)
		if err != nil {
			return err
		}
		err = s.registerIdentityAgent(ctx, sess, owner, brokerSession)
		if errors.Is(err, errIdentityBrokerSessionClosed) {
			if attempt == 0 && presented {
				// This open took and converted the request's token for the
				// session that just closed, and a token converts once: a new
				// broker session needs a fresh one. Only a request's first
				// presenting open can get here, and every path makes that open
				// before it changes anything, so the client may refresh and
				// retry as if nothing happened.
				s.logIdentityBroker(ctx, "broker session closed after its open converted the request's token", string(sess.ID), brokerSession.ID)
				return errBrokerSessionClosedAfterToken
			}
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

// identityBrokerOpenFor reports whether sess opens a broker session, its
// owner, and whether the open may present the request's access token. Only a
// root session with an owner opens one, with an identity broker configured. A
// scheduled session never presents a token, even when a user's fire-now
// request creates it: its authority comes from the schedule, not the user's
// sign-in.
func (s *Service) identityBrokerOpenFor(sess *session.Session) (owner identitybroker.Owner, mayPresent, ok bool) {
	if s.cfg.IdentityBroker == nil || !identityBrokerRoot(sess) {
		return identitybroker.Owner{}, false, false
	}
	owner, ok = identityBrokerOwner(sess.Owner)
	return owner, ok && sess.Kind != session.SessionKindScheduled, ok
}

// requireEarlierOpen fails a late open that would still present the request's
// token: the path changed state before opening, which would let a refused token
// surface as a retryable 401 after a change.
func (s *Service) requireEarlierOpen(ctx context.Context, sess *session.Session) error {
	if _, mayPresent, ok := s.identityBrokerOpenFor(sess); ok && mayPresent && !accessTokenFromRequest(ctx).IsZero() {
		return fmt.Errorf("%w: identity broker open skipped before a change", ErrInternal)
	}
	return nil
}

// lateEngineAndEnvironmentFor is engineAndEnvironmentFor for a path that may
// already have saved something, so its broker open must not be the request's
// first: an earlier open took the request's token (requireEarlierOpen).
func (s *Service) lateEngineAndEnvironmentFor(ctx context.Context, sess *session.Session) (*agent.Engine, tool.Environment, string, error) {
	if err := s.requireEarlierOpen(ctx, sess); err != nil {
		return nil, tool.Environment{}, "", err
	}
	return s.engineAndEnvironmentFor(ctx, sess)
}

// presentsToken reports whether the request's first open presents the token
// it took to a broker session: one found open for this incarnation and owner
// (found), or a new one. A token converts once, so it goes to the broker only
// while the broker session holds no live refresh token; presenting it on every
// request would make the next request carrying it fail. A token the first open
// does not present is discarded, never kept for a later open.
func presentsToken(open identitybroker.Session, found bool) bool {
	return !found || !open.HoldsRefreshToken
}

// openIdentityBrokerSessionOnce returns sess's open broker session, opening
// one if needed, and whether it presented the request's access token. The
// request's first open that may present a token takes it from the request
// (takeRequestAccessToken) whether or not it presents it, so no later open in
// the same request, which may run after the request saved something, presents
// a token: an earlier request may have converted it, and a refusal then would
// be a tagged 401 after a write. Such a later open pairs without a token, and
// the session's gateway calls fail closed until the next request.
func (s *Service) openIdentityBrokerSessionOnce(ctx context.Context, sess *session.Session, owner identitybroker.Owner, mayPresent bool) (identitybroker.Session, bool, error) {
	broker := s.cfg.IdentityBroker
	host, incarnation := string(sess.ID), string(sess.Incarnation())
	open, found, err := broker.FindSession(ctx, identitybroker.FindSessionRequest{RootHostSessionID: host})
	if err != nil {
		return identitybroker.Session{}, false, s.identityBrokerFailure(ctx, "find", host, "", err)
	}
	matched := found && open.Incarnation == incarnation && open.Owner == owner
	var token identitybroker.Token
	if mayPresent {
		token = takeRequestAccessToken(ctx)
	}
	if !presentsToken(open, matched) {
		token = identitybroker.Token{} // taken, not presented: discarded
	}
	if matched && token.IsZero() {
		return open, false, nil
	}
	if found && open.Incarnation != incarnation && open.Owner == owner {
		// A broker session left open for an earlier incarnation of this host
		// session ID: that incarnation no longer exists, so its broker session
		// cannot be used again. Close it so this incarnation can open.
		if err := broker.CloseSession(ctx, identitybroker.CloseSessionRequest{SessionID: open.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
			return identitybroker.Session{}, false, s.identityBrokerFailure(ctx, "close stale", host, open.ID, err)
		}
		s.logIdentityBroker(ctx, "closed stale broker session", host, open.ID)
	}
	// A broker session open under another owner is never closed here: the
	// broker refuses the open with session_owned, which the design treats as a
	// pairing bug that fails the open. This open runs for a saved session, which
	// owns its ID; a create naming an explicit ID that another owner's broker
	// session holds is refused before saving (preOpenIdentityBrokerSession).
	opened, err := broker.OpenSession(ctx, identitybroker.OpenSessionRequest{HostSessionID: host, Incarnation: incarnation, Owner: owner, AccessToken: token})
	if err != nil {
		err = s.openFailure(ctx, host, owner, err)
		if errors.Is(err, errBrokerSessionClosedAfterToken) {
			return identitybroker.Session{}, false, s.closedOpenFailure(ctx, sess)
		}
		if token.IsZero() && errors.Is(err, ErrBearerRejected) {
			// The tag says "this request's token was refused before anything
			// happened"; an open that presented no token cannot mean that.
			err = fmt.Errorf("%w: identity broker refused an access token this open did not present", ErrInternal)
		}
		return identitybroker.Session{}, false, err
	}
	s.openRecordRefusals.succeeded(owner)
	s.logIdentityBroker(ctx, "opened broker session", host, opened.ID)
	return opened, !token.IsZero(), nil
}

// preOpenIdentityBrokerSession opens the broker session of sess before sess is
// saved, converting the request's access token, so a refused token fails the
// request before it changed anything. It returns the broker session it
// opened, which the caller closes with closePreOpenedBrokerSession if sess is
// then not saved, or "" when it opened none. It only acts when the open would
// present a token: without one, an open cannot be refused for the token, and
// the open after the save covers it.
//
// It is the request's first open, so it takes the request's token whether or
// not it presents it (see openIdentityBrokerSessionOnce); a skipped open
// discards it, and the open after the save presents none.
//
// It never closes a broker session another request may own. One found for
// another incarnation of the same owner can only be a concurrent create's of
// the same session ID on another replica (same-process creates reserve their
// ID), so it is left alone and the open after the save handles it; that open's
// refusal, if any, is then logged rather than returned, as the session is
// saved. One found under another owner means that owner holds the ID, which a
// create naming an explicit ID can hit: it is refused with the error a create
// gets for another owner's saved session (foreignCreateIDError), so the create
// cannot tell another user's broker session from their saved session.
func (s *Service) preOpenIdentityBrokerSession(ctx context.Context, sess *session.Session) (string, error) {
	owner, mayPresent, ok := s.identityBrokerOpenFor(sess)
	if !ok || !mayPresent {
		return "", nil
	}
	token := takeRequestAccessToken(ctx)
	if token.IsZero() {
		return "", nil
	}
	broker := s.cfg.IdentityBroker
	host, incarnation := string(sess.ID), string(sess.Incarnation())
	open, found, err := broker.FindSession(ctx, identitybroker.FindSessionRequest{RootHostSessionID: host})
	if err != nil {
		return "", s.identityBrokerFailure(ctx, "find", host, "", err)
	}
	if found && open.Owner != owner {
		return "", foreignCreateIDError(sess.ID)
	}
	if found && open.Incarnation != incarnation {
		return "", nil
	}
	if !presentsToken(open, found) {
		return "", nil
	}
	opened, err := broker.OpenSession(ctx, identitybroker.OpenSessionRequest{HostSessionID: host, Incarnation: incarnation, Owner: owner, AccessToken: token})
	if err != nil {
		var refusal *identitybroker.Error
		if !found && errors.As(err, &refusal) {
			// The broker pairs a new broker session before it converts, and
			// keeps it paired when the conversion is refused. This request
			// created it, and the session it belongs to will not be saved.
			s.closeUnsavedPairing(ctx, host, incarnation, owner)
		}
		return "", s.openFailure(ctx, host, owner, err)
	}
	s.openRecordRefusals.succeeded(owner)
	s.logIdentityBroker(ctx, "opened broker session before saving", host, opened.ID)
	if found {
		// The session was already paired before this request: it is not this
		// request's to close if the save fails.
		return "", nil
	}
	return opened.ID, nil
}

// closePreOpenedBrokerSession closes a broker session preOpenIdentityBrokerSession
// opened for a session that was then not saved. It closes by broker session ID,
// never by host session: a create that lost a race shares its host session ID
// with the winner, whose broker session must stay open. A failure is logged.
func (s *Service) closePreOpenedBrokerSession(ctx context.Context, host, brokerSessionID string) {
	if brokerSessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	defer cancel()
	if err := s.cfg.IdentityBroker.CloseSession(ctx, identitybroker.CloseSessionRequest{SessionID: brokerSessionID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		_ = s.identityBrokerFailure(ctx, "close unsaved", host, brokerSessionID, err)
		return
	}
	s.logIdentityBroker(ctx, "closed broker session of an unsaved session", host, brokerSessionID)
}

// closeUnsavedPairing closes the broker session a refused preOpen left paired
// for host, if the broker reports one for exactly this incarnation and owner.
func (s *Service) closeUnsavedPairing(ctx context.Context, host, incarnation string, owner identitybroker.Owner) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	defer cancel()
	open, found, err := s.cfg.IdentityBroker.FindSession(ctx, identitybroker.FindSessionRequest{RootHostSessionID: host})
	if err != nil {
		_ = s.identityBrokerFailure(ctx, "find unsaved", host, "", err)
		return
	}
	if found && open.Incarnation == incarnation && open.Owner == owner {
		s.closePreOpenedBrokerSession(ctx, host, open.ID)
	}
}

// closedOpenFailure answers an open of sess, a saved session, that the broker
// refused with session_closed, with or without the request's token. The
// broker refuses so when a close lands while the open converts or records,
// or for a while after a close of the host session, as on a delete or when
// a scheduled run ends, which keeps the session for result delivery. Only the
// store tells these apart. A session that is gone, or was created again
// under its ID, is ErrNotFound. One that remains gets
// ErrIdentityBrokerUnavailable, which the client retries: the open may have
// spent the request's token, or, refused for a closed host session, converted
// nothing, so it is not answered bearer_rejected, and after a 503 on a
// request that presented a token the client refreshes it anyway.
func (s *Service) closedOpenFailure(ctx context.Context, sess *session.Session) error {
	saved, err := s.cfg.Store.Load(ctx, sess.ID)
	if errors.Is(err, port.ErrSessionNotFound) || err == nil && saved.Incarnation() != sess.Incarnation() {
		return fmt.Errorf("%w: session %q was closed while its identity broker session opened", ErrNotFound, sess.ID)
	}
	if err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker open refused for a closed session, and the session could not be loaded",
			"session", string(sess.ID), "err", err.Error())
	}
	return fmt.Errorf("%w: the identity broker session was closed while it opened", ErrIdentityBrokerUnavailable)
}

// errBrokerSessionClosedAfterToken is an open's error when the broker session
// closed while the open converted the request's token (design: the request
// fails as for a refused token).
var errBrokerSessionClosedAfterToken = fmt.Errorf("%w: the broker session closed after the access token was used", ErrBearerRejected)

// errIdentityBrokerSessionClosed marks a registration refused because the
// broker session closed, so openIdentityBrokerSession opens a new one.
var errIdentityBrokerSessionClosed = errors.New("identity broker session closed")

// registerIdentityAgent registers sess's main agent in brokerSession, unless
// the credential held for it is still current there: registered to the same
// broker session, with more than half its lifetime left. The main agent's
// incarnation is the session's own, which the broker session was opened with.
// The returned agent must be the one registered: an agent instance's SPIFFE
// ID, named by its credential, and the same ID registering again returns.
func (s *Service) registerIdentityAgent(ctx context.Context, sess *session.Session, owner identitybroker.Owner, brokerSession identitybroker.Session) error {
	host := string(sess.ID)
	// admit runs only once a registration passed the holder's checks, so it
	// also marks that one happened.
	var registered bool
	var admitErr error
	admit := func() error {
		registered = true
		admitErr = s.admitIdentityAgent(sess.ID)
		return admitErr
	}
	err := s.cfg.IdentityAgents.Ensure(ctx, agentcredential.Key{RootSessionID: host, AgentSessionID: host}, identitybroker.RegisterAgentRequest{
		SessionID:     brokerSession.ID,
		HostSessionID: host,
		Owner:         owner,
		Incarnation:   string(sess.Incarnation()),
		Definition:    s.identityAgentDefinition(ctx, sess),
	}, admit)
	switch {
	case err == nil:
		if registered {
			s.logIdentityBroker(ctx, "registered agent", host, brokerSession.ID)
			// A reopened broker session also needs the tree's helpers, which
			// keep their identities, registered again, parents first, each
			// with its current delegation reported there. A helper that fails
			// to move here moves, and reports, on its next lookup. With no
			// reopen, nothing has moved and this does nothing.
			if err := s.cfg.IdentityAgents.MoveHelpers(ctx, host); err != nil {
				s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker could not register every helper agent again",
					"session", host, "broker_session", brokerSession.ID, "err", err.Error())
			}
		}
		return nil
	case admitErr != nil:
		return admitErr
	case errors.Is(err, agentcredential.ErrDropped):
		// Dropped while the broker call was in flight: by a lease loss or Close,
		// which admit's checks now report, or by the session's broker session
		// closing, such as a delete in flight. Report the last as retryable,
		// like an unavailable broker.
		if err := s.admitIdentityAgent(sess.ID); err != nil {
			return err
		}
		s.logIdentityBroker(ctx, "agent credential dropped during registration", host, brokerSession.ID)
		return ErrIdentityBrokerUnavailable
	case errors.Is(err, agentcredential.ErrExpired):
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker returned an expired credential",
			"session", host, "broker_session", brokerSession.ID)
		return fmt.Errorf("%w: identity broker returned an expired credential", ErrInternal)
	case errors.Is(err, agentcredential.ErrOtherAgent):
		// The holder keeps the held credential, so every later run fails the
		// same way until the session is released (see the holder's checks).
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker returned another agent",
			"session", host, "broker_session", brokerSession.ID)
		return fmt.Errorf("%w: identity broker returned another agent", ErrInternal)
	case errors.Is(err, identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "")):
		s.logIdentityBroker(ctx, "broker session closed during registration", host, brokerSession.ID)
		return errIdentityBrokerSessionClosed
	default:
		return s.identityBrokerFailure(ctx, "register", host, brokerSession.ID, err)
	}
}

// admitIdentityAgent reports whether a registration for session id may be
// stored and its run start.
func (s *Service) admitIdentityAgent(id session.SessionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Losing the session's lease takes leaseLossMu, not runEntryMu, so it can
	// land while the broker call is in flight. Another process now runs the
	// session's agent, so this one keeps no credential for it and refuses the
	// run rather than start the agent without one.
	if _, lost := s.lostOwnership[id]; lost {
		return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
	}
	// Close cancels shutdownCtx before it closes the credential holder, so a
	// registration that finishes once shutdown began stores nothing, and one
	// that stored earlier is cleared. It refuses the run, as a draining server
	// does, rather than start the agent without a credential.
	if s.shutdownCtx.Err() != nil {
		return fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	return nil
}

// openSavedIdentityBrokerSession opens the broker session of session id and
// registers its agent, as create, fork, and clear do after saving it. It loads
// the session under id's runEntryMu, as run entry does, so a delete that won
// the lock first leaves nothing to open instead of a broker session that no
// delete will close. Every failure but that expected one, a session already
// deleted, is logged, broker failures where they happen; callers discard the
// error, as registration is retried at run entry.
func (s *Service) openSavedIdentityBrokerSession(ctx context.Context, id session.SessionID) error {
	if s.cfg.IdentityBroker == nil {
		return nil
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if !errors.Is(err, port.ErrSessionNotFound) {
			s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker registration could not load the session",
				"session", string(id), "err", err.Error(), "class", port.ClassifySessionLoadFailure(err).String())
		}
		return err
	}
	err = s.openIdentityBrokerSession(ctx, sess)
	if errors.Is(err, ErrSessionLeasedElsewhere) || errors.Is(err, ErrUnavailable) {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker registration refused: the session's lease was lost or the server is closing", "session", string(id))
	}
	return err
}

// identityAgentDefinition is the reference of the definition sess's main
// agent runs (see identityAgentDefinition), logging when the broker loaded no
// definition of that name, so the refusal that follows can be told apart from
// an authorization refusal.
func (s *Service) identityAgentDefinition(ctx context.Context, sess *session.Session) identitybroker.DefinitionRef {
	ref, loaded := identityAgentDefinition(s.cfg.IdentityDefinitions, sess)
	if !loaded {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker loaded no definition the session's agent runs; the broker refuses its registration",
			"session", string(sess.ID), "definition", sess.AgentDefinitionName)
	}
	return ref
}

// identityAgentDefinition is the reference of the definition a session's
// main agent runs, from loaded, the references of the definitions the broker
// loaded: the global definition of its name, otherwise the built-in main. The
// harness reads the operator's explicit agent directories, the broker's global
// source, before any other, so a global definition of the name is the one the
// session runs. A named definition with no global one comes from a source the
// broker does not load and is sent by name only, with loaded false; the broker
// refuses it.
//
// Shortcut: a session knows its definition by name only, so this sends the revision the broker loaded, not the one the session runs; removed by #2548.
func identityAgentDefinition(loaded []identitybroker.DefinitionRef, sess *session.Session) (identitybroker.DefinitionRef, bool) {
	return loadedDefinitionRef(loaded, sess.AgentDefinitionName, identitybroker.BuiltinMain)
}

// loadedDefinitionRef is the loaded reference of the global definition name,
// or, with no name, of the built-in builtin. A name with no loaded global
// definition is sent by name only, and a built-in the broker did not load
// with no revision; loaded is false for both, and the broker refuses them. A
// name is never matched to a built-in.
func loadedDefinitionRef(loaded []identitybroker.DefinitionRef, name, builtin string) (identitybroker.DefinitionRef, bool) {
	want := identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: builtin}
	if name != "" {
		want = identitybroker.DefinitionRef{Source: identitybroker.SourceGlobal, Name: name}
	}
	for _, ref := range loaded {
		if ref.Source == want.Source && ref.Name == want.Name {
			return ref, true
		}
	}
	if name != "" {
		return identitybroker.DefinitionRef{Name: name}, false
	}
	return want, false
}

// startTeamRootRun starts a RunTeam call as a run of the team root's main
// agent, first opening the broker session of the session the team is rooted
// in. Team runs do not pass run entry, so without this a team could run in a
// session tree whose broker session was lost at a restart. A team on the default placement has no root session and
// its members belong to no session tree, so they get no agent identity.
//
// It returns ctx carrying the team run: a RunTeam call is a run of the root's
// main agent, owned by the verified caller on the RunTeam request, the person
// who started it, and each member's run is delegated from it, the main agent
// being the members' stand-in creation parent. A team with no broker, no root
// session, or no verified caller has no such run, and its members' gateway
// calls fail closed.
func (s *Service) startTeamRootRun(ctx context.Context, root session.SessionID) (context.Context, error) {
	if s.cfg.IdentityBroker == nil {
		return ctx, nil
	}
	// A team with no run of its own clears any run the caller's context
	// carries, as rootedRunContext does, so its members cannot inherit it.
	runCtx := agentcredential.WithRun(ctx, agentcredential.Run{})
	if root == "" {
		return runCtx, nil
	}
	// The team run fails if registration does, so it keeps the caller's
	// cancellation; the timeout keeps a slow broker from holding the root's
	// runEntryMu, as fork and clear bound theirs.
	ctx, cancel := context.WithTimeout(ctx, engineCloseTimeout)
	defer cancel()
	// Load and register under the root's runEntryMu, as run entry does: a
	// delete of the root then either finishes first, so the load fails, or
	// waits, so it cannot close the broker session mid-registration and have
	// it reopened for a deleted session. A root deleted first is not found, not
	// a server fault.
	unlock := s.runEntryMu.lock(root)
	defer unlock()
	sess, err := s.cfg.Store.Load(ctx, root)
	if errors.Is(err, port.ErrSessionNotFound) {
		return runCtx, fmt.Errorf("%w: team root session %q", ErrNotFound, root)
	}
	if err != nil {
		return runCtx, fmt.Errorf("%w: load team root session: %v", ErrInternal, err)
	}
	if err := s.openIdentityBrokerSession(ctx, sess); err != nil {
		return runCtx, err
	}
	if owner, ok := identityBrokerOwner(newRunOwner(ctx, sess, runPurposeChat)); ok {
		runCtx = agentcredential.WithRun(runCtx, agentcredential.Run{ID: newRunID(), Owner: owner})
	}
	return runCtx, nil
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
	s.closeIdentityBrokerSession(ctx, sess)
}

// closeIdentityBrokerSession closes the broker session of sess, a host
// session that was deleted or whose scheduled run ended, and drops the
// credential held for its agent, which verified only there. It closes by host
// session and sess's incarnation, so the broker also closes a session an open
// in progress is creating, which that open then fails, without waiting for
// it, and refuses later opens of that incarnation for a while, never those
// of a host session created again under the same ID. A failure is logged,
// not returned: the session's state has already moved on. Every deletion path calls it once
// the durable record is gone, before anything that can still fail and return;
// no local release does. Delete paths hold the session's locks, so the call
// is bounded by the same timeout as the MCP broker cleanup.
func (s *Service) closeIdentityBrokerSession(ctx context.Context, sess *session.Session) {
	broker := s.cfg.IdentityBroker
	if broker == nil {
		return
	}
	id := sess.ID
	// Dropped once the close returns, failed or not, so a registration that
	// finished before it, which EndScheduledRun does not exclude, is dropped too.
	defer func() {
		s.mu.Lock()
		s.cfg.IdentityAgents.Drop(string(id))
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	defer cancel()
	host := string(id)
	req := identitybroker.CloseSessionRequest{RootHostSessionID: host, RootHostIncarnation: string(sess.Incarnation()), Reason: identitybroker.ReasonSessionClosed}
	if err := broker.CloseSession(ctx, req); err != nil {
		_ = s.identityBrokerFailure(ctx, "close", host, "", err)
		return
	}
	s.logIdentityBroker(ctx, "closed host session's broker session", host, "")
}

// identityBrokerFailure logs a failed identity broker call and returns the
// error for the client (design "Error reasons", mecak8s outcome). For the
// session open (call "open"):
//   - the caller's cancellation when the caller went away;
//   - ErrBearerRejected for a refused access token (token_used,
//     subject_invalid) or a converted one whose open the broker could not
//     record (record_not_accepted with CodeUnauthenticated), so the client
//     refreshes it and retries once;
//   - ErrBearerRejected for session_closed, a broker session closed while
//     the open converted the token; an open of a saved session answers it by
//     what the store holds instead (closedOpenFailure);
//   - ErrSignInRequired when the user's authority was revoked (signed_out,
//     upstream_revoked, owner_action_required), so the client signs in again;
//   - ErrIdentityBrokerSessionRefused for a pairing refusal (owner_mismatch,
//     session_owned);
//   - ErrIdentityBrokerUnavailable when the broker is unavailable or did not
//     answer in time, so the client can retry;
//   - ErrInternal otherwise.
//
// Every other call maps to the caller's cancellation,
// ErrIdentityBrokerUnavailable, or ErrInternal, whatever its reason.
//
// Only an open that presents the request's token gets the token reasons, and
// every such open runs before the request changes anything, so the tagged 401
// keeps its "nothing happened" meaning. brokerSession is the broker session
// involved, or empty when none is known yet.
func (s *Service) identityBrokerFailure(ctx context.Context, call, host, brokerSession string, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelInfo, "identity broker call cancelled by the caller",
			"call", call, "session", host, "broker_session", brokerSession)
		return fmt.Errorf("identity broker %s: %w", call, ctx.Err())
	}
	code := identitybroker.CodeInternal
	var reason identitybroker.Reason
	var brokerErr *identitybroker.Error
	switch {
	case errors.As(err, &brokerErr):
		code, reason = brokerErr.Code, brokerErr.Reason
	case errors.Is(err, context.DeadlineExceeded):
		code = identitybroker.CodeUnavailable
	}
	s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker call failed",
		"call", call, "session", host, "broker_session", brokerSession, "code", string(code), "reason", string(reason))
	// The reasons map to client outcomes only for the session open, which this
	// change owns; other calls, registration among them (#2552), keep the
	// unavailable-or-internal mapping below.
	// The client sees each error's text, so it is fixed: the call and the
	// broker's reason and code are in the diagnostics record above.
	switch {
	case call != "open":
	case reason == identitybroker.ReasonTokenUsed, reason == identitybroker.ReasonSubjectInvalid,
		reason == identitybroker.ReasonRecordNotAccepted && code == identitybroker.CodeUnauthenticated:
		// The last is an open that converted the token but whose record the
		// broker's audit sink did not accept: the token is spent, so the
		// client refreshes it, as for token_used.
		return fmt.Errorf("%w: identity broker refused the access token", ErrBearerRejected)
	case reason.RequiresSignIn():
		return fmt.Errorf("%w: sign in again", ErrSignInRequired)
	case reason == identitybroker.ReasonOwnerMismatch, reason == identitybroker.ReasonSessionOwned:
		return fmt.Errorf("%w: identity broker refused the session", ErrIdentityBrokerSessionRefused)
	case reason == identitybroker.ReasonSessionClosed:
		// The broker session closed while the open converted the request's
		// token, and a token converts once: fail as for a refused token. An
		// open of a saved session, with or without a token, is answered by
		// what the store holds instead (closedOpenFailure).
		return errBrokerSessionClosedAfterToken
	}
	if code == identitybroker.CodeUnavailable {
		return ErrIdentityBrokerUnavailable
	}
	return fmt.Errorf("%w: identity broker call failed", ErrInternal)
}

// openRecordRefusalWindow is how recent a caller's previous refused open must
// be for its repeated record_not_accepted to fail as unavailable
// (credentials-and-lifecycle.md step (3)).
const openRecordRefusalWindow = time.Minute

// openFailure is identityBrokerFailure for owner's open of host. An open that
// converted the request's token but whose record the broker's audit sink did
// not accept fails as for a refused token, so the client refreshes it and
// opens again. When the same caller's previous such refusal, of any host
// session, was within openRecordRefusalWindow, it fails as the broker being
// unavailable instead: a sink outage then does not have the client spend one
// fresh token after another. The rule keys on the caller alone because a
// create, fork, or clear opens a new host session on every attempt.
func (s *Service) openFailure(ctx context.Context, host string, owner identitybroker.Owner, err error) error {
	mapped := s.identityBrokerFailure(ctx, "open", host, "", err)
	var brokerErr *identitybroker.Error
	if errors.As(err, &brokerErr) && brokerErr.Reason == identitybroker.ReasonRecordNotAccepted &&
		brokerErr.Code == identitybroker.CodeUnauthenticated &&
		s.openRecordRefusals.refused(owner, s.cfg.Now()) {
		return fmt.Errorf("%w: the identity broker could not record the session open", ErrIdentityBrokerUnavailable)
	}
	return mapped
}

// openRecordRefusals holds, per caller, when an open that converted the
// caller's token last failed with record_not_accepted. An entry ends when it
// is openRecordRefusalWindow old or the caller's open succeeds; any other
// failure keeps it, token_used among them: after the 503 the token is spent,
// and a client that presents it again must not reset the rule. Entries past
// the window are pruned as others are noted, so it holds at most the callers
// refused in the last minute. The zero value is ready to use.
//
// The state is per replica and best effort: a retry that another replica
// serves, or one after a restart, counts as a first refusal and gets 401.
type openRecordRefusals struct {
	mu sync.Mutex
	at map[identitybroker.Owner]time.Time
}

// refused notes that owner's converted open was refused with
// record_not_accepted at now, and reports whether owner's previous such
// refusal was within openRecordRefusalWindow.
func (r *openRecordRefusals) refused(owner identitybroker.Owner, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for o, at := range r.at {
		if now.Sub(at) >= openRecordRefusalWindow {
			delete(r.at, o)
		}
	}
	_, repeat := r.at[owner]
	if r.at == nil {
		r.at = map[identitybroker.Owner]time.Time{}
	}
	r.at[owner] = now
	return repeat
}

// succeeded forgets owner's refusal: an open of theirs succeeded, so the
// sink accepts records again.
func (r *openRecordRefusals) succeeded(owner identitybroker.Owner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.at, owner)
}

func (s *Service) logIdentityBroker(ctx context.Context, msg, host, brokerSession string) {
	s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelInfo, msg, "session", host, "broker_session", brokerSession)
}
