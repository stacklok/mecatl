package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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
	// run entry registers again. The design renews on next use; renewal on
	// each call comes with the gateway calls that present the credential.
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
				return fmt.Errorf("%w: the broker session closed after the access token was used", ErrBearerRejected)
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
		err = s.identityBrokerFailure(ctx, "open", host, "", err)
		if token.IsZero() && errors.Is(err, ErrBearerRejected) {
			// The tag says "this request's token was refused before anything
			// happened"; an open that presented no token cannot mean that.
			err = fmt.Errorf("%w: identity broker refused an access token this open did not present", ErrInternal)
		}
		return identitybroker.Session{}, false, err
	}
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
		return "", s.identityBrokerFailure(ctx, "open", host, "", err)
	}
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
	// The in-process broker never returns an expired credential; this guards a
	// remote broker, whose credential would otherwise be renewed at no point.
	if !reg.ExpiresAt.After(now) {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker returned an expired credential",
			"session", host, "broker_session", brokerSession.ID)
		return fmt.Errorf("%w: identity broker returned an expired credential", ErrInternal)
	}
	// A mismatch keeps the held credential, so every later run fails the same
	// way until the session is released. That is deliberate: in this phase the
	// ID is built from the incarnation, so it changes only if the broker
	// misbehaves, and dropping the held agent would let the next run accept
	// the wrong one. While the broker runs in process, a legitimate change,
	// such as another trust domain or per-agent identities, comes with a
	// restart, which clears what is held; a remote broker can change without
	// one. The comparison lasts only while this process holds the credential;
	// across a restart, release, or lease loss the suffix check is the
	// guarantee, as the server does not know the broker's trust domain to
	// predict the full ID. The suffix check is enough only while agent IDs end
	// in .../shared/inst/<incarnation>; per-agent IDs need a full-ID match that
	// the broker package owns.
	if !strings.HasSuffix(reg.Agent.Subject, "/inst/"+incarnation) || (ok && reg.Agent != held.registration.Agent) {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker returned another agent",
			"session", host, "broker_session", brokerSession.ID)
		return fmt.Errorf("%w: identity broker returned another agent", ErrInternal)
	}
	s.mu.Lock()
	// Losing the session's lease takes leaseLossMu, not runEntryMu, so it can
	// land while the broker call is in flight. Another process now runs the
	// session's agent, so this one keeps no credential for it and refuses the
	// run rather than start the agent without one.
	if _, lost := s.lostOwnership[sess.ID]; lost {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, sess.ID)
	}
	// Close cancels shutdownCtx before it clears identityAgents under s.mu, so
	// a registration that finishes once shutdown began stores nothing, and one
	// that stored earlier is cleared. It refuses the run, as a draining server
	// does, rather than start the agent without a credential.
	if s.shutdownCtx.Err() != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrUnavailable, sess.ID)
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
// delete will close. Every failure is logged, broker failures where they
// happen; callers discard the error, as registration is retried at run entry.
func (s *Service) openSavedIdentityBrokerSession(ctx context.Context, id session.SessionID) error {
	if s.cfg.IdentityBroker == nil {
		return nil
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker registration could not load the session", "session", string(id))
		return err
	}
	err = s.openIdentityBrokerSession(ctx, sess)
	if errors.Is(err, ErrSessionLeasedElsewhere) || errors.Is(err, ErrUnavailable) {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker registration refused: the session's lease was lost or the server is closing", "session", string(id))
	}
	return err
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
	// The team run fails if registration does, so it keeps the caller's
	// cancellation; the timeout keeps a slow broker from holding the root's
	// runEntryMu, as fork and clear bound theirs.
	ctx, cancel := context.WithTimeout(ctx, engineCloseTimeout)
	defer cancel()
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
}

// closeIdentityBrokerSession closes the broker session of a host session that
// was deleted or whose scheduled run ended, and drops the credential held for
// its agent, which verified only there. A failure is logged, not returned:
// the session's state has already moved on. Every deletion path calls it once
// the durable record is gone, before anything that can still fail and return;
// no local release does. Delete paths hold the session's locks, so the calls
// are bounded by the same timeout as the MCP broker cleanup.
func (s *Service) closeIdentityBrokerSession(ctx context.Context, id session.SessionID) {
	broker := s.cfg.IdentityBroker
	if broker == nil {
		return
	}
	// Dropped once the close returns, failed or not, so a registration that
	// finished before it, which EndScheduledRun does not exclude, is dropped too.
	defer func() {
		s.mu.Lock()
		delete(s.identityAgents, id)
		s.mu.Unlock()
	}()
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
// error for the client (design "Error reasons", mecak8s outcome). For the
// session open (call "open"):
//   - the caller's cancellation when the caller went away;
//   - ErrBearerRejected for a refused access token (token_used,
//     subject_invalid), so the client refreshes it and retries once;
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
	case reason == identitybroker.ReasonTokenUsed, reason == identitybroker.ReasonSubjectInvalid:
		return fmt.Errorf("%w: identity broker refused the access token", ErrBearerRejected)
	case reason.RequiresSignIn():
		return fmt.Errorf("%w: sign in again", ErrSignInRequired)
	case reason == identitybroker.ReasonOwnerMismatch, reason == identitybroker.ReasonSessionOwned:
		return fmt.Errorf("%w: identity broker refused the session", ErrIdentityBrokerSessionRefused)
	}
	if code == identitybroker.CodeUnavailable {
		return ErrIdentityBrokerUnavailable
	}
	return fmt.Errorf("%w: identity broker call failed", ErrInternal)
}

func (s *Service) logIdentityBroker(ctx context.Context, msg, host, brokerSession string) {
	s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelInfo, msg, "session", host, "broker_session", brokerSession)
}
