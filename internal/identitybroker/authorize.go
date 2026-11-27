package identitybroker

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AuthorizeCall implements Core, in the order Service.AuthorizeCall
// documents. An invalid request is recorded as refused, naming only the
// calling workload, as are refusals after the session lookup.
func (b *Broker) AuthorizeCall(ctx context.Context, workload Workload, req AuthorizeCallRequest) (Authorization, error) {
	if err := checkCall(workload, req); err != nil {
		return Authorization{}, b.refuseInvalid(&DecisionRecord{RecordHeader: b.newHeader(TypeToolDecision), Workload: workload}, err)
	}
	t := b.sessions
	t.mu.Lock()
	s, err := t.callSessionLocked(workload, req.SessionID)
	if s == nil {
		t.mu.Unlock()
		return Authorization{}, err
	}
	rec := decisionRecord(t.now(), workload, s, req)
	// The rejection report is checked before the grant, so a session without
	// one still refuses a report it never issued.
	if err == nil && req.RejectedAttemptID != "" && !t.attempts.knownLocked(s.ID, req.RejectedAttemptID) {
		err = ReasonError(ReasonDenied, "the rejected attempt is unknown or already retried")
	}
	if err == nil {
		_, err = grantForCallLocked(s)
	}
	t.mu.Unlock()
	if err != nil {
		return Authorization{}, b.refuse(rec, err)
	}

	// Check 3: the credential identifies an agent registered to this open
	// session.
	agent, err := b.VerifyCredential(ctx, VerifyCredentialRequest{SessionID: s.ID, Credential: req.Credential})
	if err != nil {
		return Authorization{}, b.refuse(rec, err)
	}
	rec.Agent, rec.AgentID = agent.Agent, agent.AgentID

	// Checks 4 and 5: the run belongs to this session and is bound to the
	// agent, and its reported owner is the run's and the grant's.
	t.mu.Lock()
	if a, ok := t.agents[agent.Agent.Subject]; ok {
		rec.AgentInstance, rec.RegistrationRecord = a.incarnation, a.registrationRecord
	}
	err = t.checkRunLocked(s, agent.Agent.Subject, req)
	if err == nil && req.DelegationID != "" {
		rec.DelegationRecord = t.runs.forLocked(s.ID).delegations[req.DelegationID].record
	}
	t.mu.Unlock()
	if err != nil {
		return Authorization{}, b.refuse(rec, err)
	}

	// Check 6: a configured gateway routes the server.
	if !b.gateway.routes(req.Server) {
		return Authorization{}, b.refuse(rec, ReasonError(ReasonDenied, "no gateway routes the server"))
	}

	// Check 7: the agent's stored effective ceiling allows the exact server
	// and tool.
	// Shortcut: the ceiling follows the definition the agent reported at registration, so a root reporting system/main gets every gateway server, and a child reporting a built-in gets its parent's whole ceiling rather than a narrower one; removed by #2551 (root eligibility and nesting checks).
	t.mu.Lock()
	ceiling := t.agents[agent.Agent.Subject].ceiling
	t.mu.Unlock()
	rec.Ceiling = ceiling.String()
	// Validate checked that a call's tool is named for its server.
	if err := checkCeiling(ceiling, req.Method, req.Server, strings.TrimPrefix(req.Tool, req.Server+".")); err != nil {
		return Authorization{}, b.refuse(rec, err)
	}

	// AS-2 may not be bound yet (the #2559 shortcut on BindGatewayAS2).
	as2, release, ok := b.acquireAS2()
	if !ok {
		return Authorization{}, b.refuse(rec, NewError(CodeUnavailable, "", "the gateway's authorization server is not bound yet"))
	}
	defer release()
	auth, err := b.authorizeAtGateway(ctx, as2, s, req, &rec)
	if err != nil {
		return Authorization{}, b.refuse(rec, err)
	}
	return auth, nil
}

// retryRule is a design rule that gives a call one more try with a new
// ID-JAG. Each rule allows one retry per call, whatever the others used.
type retryRule int

const (
	// retryLostResponse: AS-1 or AS-2 sent no response (steps (7) and (8)).
	retryLostResponse retryRule = iota
	// retryReasonlessInvalidGrant: AS-2 refused the ID-JAG with
	// invalid_grant and no reason code (step (8)).
	retryReasonlessInvalidGrant
	// retryNewerGrant: AS-1 reported the grant revoked while the session
	// holds a newer one ("Error reasons", revoked).
	retryNewerGrant
	retryRules
)

// maxGatewayTries bounds the ID-JAG exchanges, and the redemptions, of one
// attempt: the first try and one retry per rule.
const maxGatewayTries = 1 + int(retryRules)

// retriesUsed holds which rules a call has used its retry for.
type retriesUsed [retryRules]bool

// take reports whether rule r still allows a retry, and uses it up.
func (u *retriesUsed) take(r retryRule) bool {
	if u[r] {
		return false
	}
	u[r] = true
	return true
}

// authorizeAtGateway runs design steps (7), (8), and (8a) for a call that
// passed the checks before a grant: it consumes a reported rejected attempt,
// exchanges the session's grant at AS-1 for an ID-JAG carrying the call's
// entry, records it, redeems it at as2 for a gateway token, checks that the
// token's response carries the entry sent, and records the decision. Each
// try gets a new ID-JAG; one is never submitted twice. A token that fails
// the output check is discarded and the call fails without a retry. It fills
// rec with what it learns, so a refusal it returns is recorded with it.
func (b *Broker) authorizeAtGateway(ctx context.Context, as2 GatewayAS2, s *brokerSession, req AuthorizeCallRequest, rec *DecisionRecord) (Authorization, error) {
	t := b.sessions
	key := callKey{
		operationID: req.OperationID, agent: rec.Agent.Subject, method: req.Method,
		server: req.Server, tool: req.Tool, argumentsSHA256: req.ArgumentsSHA256,
	}
	t.mu.Lock()
	g, err := t.startAttemptLocked(s, req, key)
	t.mu.Unlock()
	if err != nil {
		return Authorization{}, err
	}
	rec.useGrant(g)
	attemptID := "att_" + strings.ToLower(rand.Text())
	rec.AttemptID = attemptID
	entry := callEntryFor(req)

	var used retriesUsed
	for try := range maxGatewayTries {
		if try > 0 {
			// A closed session gets no further ID-JAG, and a replaced grant
			// is presented in place of the one the last try used.
			if g, err = t.retryGrant(s); err != nil {
				return Authorization{}, err
			}
			rec.useGrant(g)
		}
		res, err := b.idjag.IssueIDJAG(ctx, IDJAGRequest{
			SubjectToken: g.refreshToken, ActorToken: req.Credential,
			Audience: as2.Issuer, Resource: as2.Resource, OperationID: req.OperationID,
			AuthorizationDetails: entry,
		})
		if err != nil {
			rule, retry, final := t.exchangeFailure(ctx, s, g, err)
			if final != nil {
				return Authorization{}, final
			}
			if !used.take(rule) {
				return Authorization{}, retry
			}
			continue
		}

		claims, ok := readIDJAG(res.IDJAG)
		if !ok {
			return Authorization{}, NewError(CodeInternal, "", "the authorization server's answer is not an ID-JAG")
		}
		rec.IDJAG = claims.ref
		jag := &IDJAGRecord{
			RecordHeader: RecordHeader{AuditID: newAuditID(), Type: TypeIDJAG, At: t.now(), Outcome: OutcomeSuccess},
			SessionID:    s.ID, Workload: rec.Workload, Agent: rec.Agent, AgentID: rec.AgentID,
			RunID: req.RunID, DelegationID: req.DelegationID, RunOwner: req.RunOwner, Owner: g.owner,
			UserRecord: g.openRecord, User: g.user, AgentInstance: rec.AgentInstance,
			RegistrationRecord: rec.RegistrationRecord, DelegationRecord: rec.DelegationRecord,
			OperationID: req.OperationID, AttemptID: attemptID,
			IDJAG: claims.ref, ExpiresAt: claims.exp,
		}
		if err := b.record(ctx, jag); err != nil {
			return Authorization{}, b.refuseIDJAG(*jag, errNotRecorded("call"))
		}
		if err := t.stillOpen(s); err != nil {
			return Authorization{}, err
		}

		redeemedAt := t.now()
		v, err := as2.AuthServer.Redeem(ctx, Redemption{Assertion: res.IDJAG, Resource: as2.Resource})
		if err != nil {
			rule, retry, final := t.redeemFailure(ctx, s, err)
			if final != nil {
				return Authorization{}, final
			}
			if !used.take(rule) {
				return Authorization{}, retry
			}
			continue
		}
		rec.CallEntryCheck = checkCallEntry(entry, v.AuthorizationDetails)
		if err := callEntryError(rec.CallEntryCheck); err != nil {
			rec.nameGatewayToken(v.AccessToken)
			return Authorization{}, err
		}
		return b.allow(ctx, s, req, rec, key, v, gatewayTokenExpiry(redeemedAt, claims.exp, v))
	}
	// Each retry uses up its rule's allowance, so the loop never ends here.
	return Authorization{}, NewError(CodeInternal, "", "the call ran out of tries")
}

// exchangeFailure classifies a failed ID-JAG exchange of grant g in s:
// either rule allows it a retry with a new ID-JAG, and retry is the error the
// call ends with once that rule's retry is used, or final ends the call. A
// session closed during the exchange ends the call with its closing reason,
// whatever AS-1 said: AS-1's actor check denies an agent of a closed session.
// Otherwise a lost response is retried, as is a revocation when s holds a
// newer grant (afterRevocation); anything else is mapped by idjagError.
func (t *sessionTable) exchangeFailure(ctx context.Context, s *brokerSession, g *heldGrant, err error) (rule retryRule, retry, final error) {
	if closed := t.stillOpen(s); closed != nil {
		return 0, nil, closed
	}
	switch {
	case lostResponse(ctx, err):
		return retryLostResponse, NewError(CodeUnavailable, "", "no response from the authorization server"), nil
	case revoked(err):
		if err := t.afterRevocation(s, g); err != nil {
			return 0, nil, err
		}
		return retryNewerGrant, NewError(CodeUnavailable, "", "the broker session's grant was replaced during the call"), nil
	}
	return 0, nil, idjagError(err)
}

// redeemFailure classifies a failed redemption in s as exchangeFailure does
// an exchange: a session closed during the redemption ends the call with its
// closing reason, whatever AS-2 said. Otherwise a lost response is retried, as is an
// invalid_grant without a reason code; consent_required is the user's to
// give; access_denied with actor_denied denies the call, as AS-1's denied
// does; anything else, including a 502 or 504, is mapped by redeemError.
func (t *sessionTable) redeemFailure(ctx context.Context, s *brokerSession, err error) (rule retryRule, retry, final error) {
	if closed := t.stillOpen(s); closed != nil {
		return 0, nil, closed
	}
	var r *ServerRefusal
	isRefusal := errors.As(err, &r)
	switch {
	case lostResponse(ctx, err):
		return retryLostResponse, NewError(CodeUnavailable, "", "no response from the gateway's authorization server"), nil
	// Shortcut: AS-2's error_uri is dropped (ServerRefusal keeps only the status, code and reason), so no approval link reaches the user; removed by #2735 (the approval link, shown under step (8)'s rule, through an ErrorURI the AS-2 port sets).
	case isRefusal && !r.transient() && r.Reason == string(ReasonConsentRequired):
		return 0, nil, ReasonError(ReasonConsentRequired, "the gateway's authorization server requires the user's consent")
	case isRefusal && !r.transient() && r.Code == "access_denied" && r.Reason == "actor_denied":
		return 0, nil, ReasonError(ReasonDenied, "the gateway's authorization server refused the acting agent")
	case isRefusal && !r.transient() && r.Code == "invalid_grant" && (r.Reason == "" || r.Reason == unknownCode):
		return retryReasonlessInvalidGrant, NewError(CodeInternal, "", "the gateway's authorization server refused the ID-JAG: gateway or configuration error"), nil
	}
	return 0, nil, redeemError(err)
}

// startAttemptLocked consumes the rejected attempt req reports, if any, and
// returns the grant the call uses. The attempt is consumed only here, after
// check 3, so a forged credential cannot use up its retry; a report that does
// not match the call consumes it too. The caller holds t.mu.
func (t *sessionTable) startAttemptLocked(s *brokerSession, req AuthorizeCallRequest, key callKey) (*heldGrant, error) {
	if err := t.closedLocked(s); err != nil {
		return nil, err
	}
	if req.RejectedAttemptID != "" && !t.attempts.consumeLocked(s.ID, req.RejectedAttemptID, key) {
		return nil, ReasonError(ReasonDenied, "the rejected attempt is unknown, already retried, or for another call")
	}
	return grantForCallLocked(s)
}

// lostResponse reports whether err is a request that got no response, while
// the call still runs. A lost response is one of the three causes of a new
// ID-JAG (see retryRule); unlike at session open, a 502 or 504 fails the call
// as UNAVAILABLE (design "Error reasons").
func lostResponse(ctx context.Context, err error) bool {
	return errors.Is(err, ErrNoResponse) && ctx.Err() == nil
}

// afterRevocation decides what a call does when AS-1 reports its grant g
// revoked: a closed session refuses with its closing reason; a session whose
// call grant (grantForCallLocked) is now another lets the call retry, with
// that grant; otherwise the session is closed with ReasonSessionClosed.
func (t *sessionTable) afterRevocation(s *brokerSession, g *heldGrant) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.closedLocked(s); err != nil {
		return err
	}
	if next, err := grantForCallLocked(s); err == nil && next != g {
		return nil
	}
	t.closeLocked(s, ReasonSessionClosed, CloseGrantLost)
	return ReasonError(ReasonSessionClosed, "the broker session's grant was revoked")
}

// retryGrant returns the grant the next try of a call in s presents, or s's
// closing reason as an error if s closed during the call.
func (t *sessionTable) retryGrant(s *brokerSession) (*heldGrant, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.closedLocked(s); err != nil {
		return nil, err
	}
	return grantForCallLocked(s)
}

// stillOpen returns s's closing reason as an error if s closed during the
// call.
func (t *sessionTable) stillOpen(s *brokerSession) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closedLocked(s)
}

// allow finishes a redeemed call: it drops v if s closed meanwhile, records
// the success, and issues the attempt unless the call is itself a retry, so a
// retry's token cannot be reported rejected again. A success that cannot be
// recorded returns no token, as does one whose session closed while it was
// recorded. Either way the success is followed by a refusal for the same
// attempt that supersedes it, so the records show that the token was
// withheld.
func (b *Broker) allow(ctx context.Context, s *brokerSession, req AuthorizeCallRequest, rec *DecisionRecord, key callKey, v GatewayTokenResult, expiresAt time.Time) (Authorization, error) {
	t := b.sessions
	rec.nameGatewayToken(v.AccessToken)
	if err := t.stillOpen(s); err != nil {
		return Authorization{}, err
	}
	success := *rec
	success.AuditID, success.Outcome = newAuditID(), OutcomeSuccess
	if err := b.record(ctx, &success); err != nil {
		// The caller records the refusal, which names the success the sink
		// may have kept without confirming it.
		rec.Supersedes = success.AuditID
		return Authorization{}, errNotRecorded("call")
	}
	t.mu.Lock()
	closed := t.closedLocked(s)
	if closed == nil && req.RejectedAttemptID == "" {
		t.attempts.issueLocked(s.ID, rec.AttemptID, key)
	}
	t.mu.Unlock()
	if closed != nil {
		// The caller records the refusal, after the success.
		rec.Supersedes = success.AuditID
		return Authorization{}, closed
	}
	return Authorization{GatewayToken: v.AccessToken, ExpiresAt: expiresAt, AttemptID: rec.AttemptID}, nil
}

// nameGatewayToken names token in r by its (iss, jti), or marks it
// unreferenced when it is not a JWT with a jti.
func (r *DecisionRecord) nameGatewayToken(token Token) {
	if ref, ok := readJWTRef(token); ok {
		r.GatewayToken = ref.ref
		return
	}
	// Shortcut: a gateway token that is not a JWT has no reference; removed by #2848 (references to AS-2's redemption events).
	r.GatewayTokenUnreferenced = true
}

// gatewayTokenExpiry returns when v expires: the earliest of its expires_in
// counted from redeemedAt, when AS-2 sent one, the ID-JAG's exp, and v's own
// exp, when v is a JWT with one. AS-2 issues the token for the lifetime its
// client is configured with, capped at the ID-JAG's remaining validity.
func gatewayTokenExpiry(redeemedAt, idjagExp time.Time, v GatewayTokenResult) time.Time {
	exp := idjagExp
	if v.ExpiresIn > 0 {
		exp = earliest(exp, redeemedAt.Add(v.ExpiresIn))
	}
	if tokenExp, ok := readJWTExp(v.AccessToken); ok {
		exp = earliest(exp, tokenExp)
	}
	return exp
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// revoked reports whether err is AS-1 refusing the grant as revoked, itself
// or through its parent.
func revoked(err error) bool {
	var r *ServerRefusal
	return errors.As(err, &r) && !r.transient() && (r.Reason == "revoked" || r.Reason == "parent_revoked")
}

// idjagError maps a failed ID-JAG exchange, other than a lost response or a
// revocation, to the Service error the design's error reasons assign. Its
// detail is a ServerRefusal's text, which holds only known codes, or fixed
// text. AS-1's actor_token_expired (svid_expired before ToolHive v0.66.0) maps
// to ReasonSVIDExpired, and approval_required, for which the broker has no
// approval flow, to ReasonDenied. AS-1's issuance refusals
// (upstream_subject_missing, sub_id_missing, dpop_not_supported, and
// bound_elapsed) are CodeInternal, named in the detail.
func idjagError(err error) error {
	var r *ServerRefusal
	isRefusal := errors.As(err, &r)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return NewError(CodeUnavailable, "", "the call authorization was cancelled")
	case IsUnavailable(err):
		if isRefusal {
			return NewError(CodeUnavailable, "", r.Error())
		}
		return NewError(CodeUnavailable, "", "no response from the authorization server")
	case isRefusal && r.Reason == "actor_token_expired":
		return ReasonError(ReasonSVIDExpired, "the authorization server refused the ID-JAG exchange")
	case isRefusal && r.Reason == "approval_required":
		return ReasonError(ReasonDenied, "the authorization server refused the ID-JAG exchange")
	case isRefusal && (r.Reason == string(ReasonSVIDExpired) || r.Reason == string(ReasonDenied) || r.Reason == string(ReasonSignedOut)):
		return ReasonError(Reason(r.Reason), "the authorization server refused the ID-JAG exchange")
	case isRefusal && (r.Reason == string(ReasonUpstreamRevoked) || r.Reason == string(ReasonOwnerActionRequired)):
		// Shortcut: upstream_revoked and owner_action_required are not recorded against the session; removed by #2568 (the session records the user's sign-in state).
		return ReasonError(Reason(r.Reason), "the user's authorization was revoked; sign in again")
	case isRefusal:
		return NewError(CodeInternal, "", r.Error())
	case errors.Is(err, ErrTransportConfiguration):
		return NewError(CodeInternal, "", "the connection to the authorization server is misconfigured")
	default:
		return NewError(CodeInternal, "", "the ID-JAG exchange failed")
	}
}

// redeemError maps a failed redemption, other than a lost response, a
// consent_required refusal, or a reasonless invalid_grant, to a Service
// error: UNAVAILABLE when AS-2 may answer later, INTERNAL otherwise.
func redeemError(err error) error {
	var r *ServerRefusal
	isRefusal := errors.As(err, &r)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return NewError(CodeUnavailable, "", "the call authorization was cancelled")
	case IsUnavailable(err):
		if isRefusal {
			return NewError(CodeUnavailable, "", r.Error())
		}
		return NewError(CodeUnavailable, "", "no response from the gateway's authorization server")
	case isRefusal:
		return NewError(CodeInternal, "", r.Error())
	case errors.Is(err, ErrTransportConfiguration):
		return NewError(CodeInternal, "", "the connection to the gateway's authorization server is misconfigured")
	default:
		return NewError(CodeInternal, "", "the ID-JAG redemption failed")
	}
}

// idjagTyp is an ID-JAG's JOSE header typ.
const idjagTyp = "oauth-id-jag+jwt"

// jwtRef is what the broker reads from a JWT without verifying it.
type jwtRef struct {
	typ string
	ref CredentialRef
	// exp is zero when the JWT has no numeric exp.
	exp time.Time
}

// readJWTRef reads a compact JWT's header typ and its iss, jti, and exp
// claims without verifying it: the broker only names and bounds a token its
// issuer and audience verify. It reports false when token is not a JWT or
// has no jti, so it has no reference.
func readJWTRef(token Token) (jwtRef, bool) {
	claims := jwt.MapClaims{}
	parsed, _, err := jwt.NewParser().ParseUnverified(token.Value(), claims)
	if err != nil {
		return jwtRef{}, false
	}
	jti, _ := claims["jti"].(string)
	if jti == "" {
		return jwtRef{}, false
	}
	typ, _ := parsed.Header["typ"].(string)
	iss, _ := claims["iss"].(string)
	out := jwtRef{typ: typ, ref: CredentialRef{Issuer: iss, ID: jti}}
	if exp, ok := expClaim(claims); ok {
		out.exp = exp
	}
	return out, true
}

// readJWTExp reads a compact JWT's exp without verifying it, whether or not
// the JWT has a jti. It reports false when token is not a JWT or has no
// numeric exp.
func readJWTExp(token Token) (time.Time, bool) {
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token.Value(), claims); err != nil {
		return time.Time{}, false
	}
	return expClaim(claims)
}

func expClaim(claims jwt.MapClaims) (time.Time, bool) {
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil || exp.Unix() <= 0 {
		return time.Time{}, false
	}
	return exp.Time, true
}

// readIDJAG reads an ID-JAG without verifying it, as AS-2 verifies it next.
// It reports false unless the header typ is an ID-JAG's and it has a jti and
// an exp: ToolHive accepts plain JWT assertions too, so the broker refuses to
// redeem anything else.
func readIDJAG(token Token) (jwtRef, bool) {
	ref, ok := readJWTRef(token)
	if !ok || ref.typ != idjagTyp || ref.exp.IsZero() {
		return jwtRef{}, false
	}
	return ref, true
}

// grantForCallLocked returns the grant a call in s uses: the refresh token s
// holds. It fails with ReasonNoGrant when s holds none. It is the one place
// that decides which grant a call uses; scheduled runs (#2569) and the
// chained exchange change only this function. The caller holds t.mu.
func grantForCallLocked(s *brokerSession) (*heldGrant, error) {
	if s.grant == nil {
		return nil, ReasonError(ReasonNoGrant, "the broker session holds no refresh token")
	}
	return s.grant, nil
}

// decisionRecord returns the record of a decision on req in s, before any
// outcome. Its owner is the session's verified owner, or the reported one
// when the session has none, and its user the session's verified user, until
// the call selects a grant (useGrant). The caller holds t.mu.
func decisionRecord(now time.Time, workload Workload, s *brokerSession, req AuthorizeCallRequest) DecisionRecord {
	owner := s.Owner
	if s.verifiedOwner != nil {
		owner = *s.verifiedOwner
	}
	userRecord, user := sessionUser(s)
	return DecisionRecord{
		RecordHeader: RecordHeader{Type: TypeToolDecision, At: now},
		SessionID:    s.ID, Workload: workload, Owner: owner, OwnerVerified: s.verifiedOwner != nil,
		UserRecord: userRecord, User: user,
		RunID: req.RunID, DelegationID: req.DelegationID, RunOwner: req.RunOwner,
		OperationID: req.OperationID, RejectedAttemptID: req.RejectedAttemptID,
		Method: req.Method, Server: req.Server, Tool: req.Tool, ArgumentsSHA256: req.ArgumentsSHA256,
	}
}

// useGrant names g's user and the open that stored g in r, so a decision
// names the grant its ID-JAG was exchanged from, a retry's newer grant
// included, rather than the session's user when the call began.
func (r *DecisionRecord) useGrant(g *heldGrant) {
	r.UserRecord, r.User = g.openRecord, g.user
}

// refuseIDJAG queues err as the refusal of the ID-JAG jag records, whose
// record the sink did not accept, and returns err. The sink may have kept
// that record without confirming it, so the refusal supersedes it (audit.md,
// "Write order and failure"); the ID-JAG is never redeemed.
func (b *Broker) refuseIDJAG(jag IDJAGRecord, err error) error {
	jag.RecordHeader = jag.supersede()
	jag.refused(err)
	b.recordAnyway(&jag)
	return err
}

// refuse queues err as the refusal of rec's call and returns err without
// waiting on the sink. The refusal is recorded even when the call was
// cancelled, as a cancelled call is still a decision.
func (b *Broker) refuse(rec DecisionRecord, err error) error {
	rec.refused(err)
	b.recordDecision(rec)
	return err
}
