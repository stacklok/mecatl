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
// documents. Every refusal after the session lookup is recorded.
func (b *Broker) AuthorizeCall(ctx context.Context, workload Workload, req AuthorizeCallRequest) (Authorization, error) {
	if err := checkCall(workload, req); err != nil {
		return Authorization{}, err
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
		return Authorization{}, b.refuse(ctx, rec, err)
	}

	// Check 3: the credential identifies an agent registered to this open
	// session.
	agent, err := b.VerifyCredential(ctx, VerifyCredentialRequest{SessionID: s.ID, Credential: req.Credential})
	if err != nil {
		return Authorization{}, b.refuse(ctx, rec, err)
	}
	rec.Agent, rec.AgentID = agent.Agent, agent.AgentID

	// Shortcut: step (6) checks 4 and 5 (run and owner) and 7 (ceiling) accept their inputs; removed by #2557 (checks 4 and 5) and the authority work (docs/drafts/agent-identity/README.md, "Order of work") for check 7.

	// Check 6: the gateway routes the server.
	// Shortcut: check 6 matches the call's server, not the tool's route; removed by #2538 (gateway entries carry their routed tools, or the design moves the tool check to the vMCP).
	if !b.gateway.routes(req.Server) {
		return Authorization{}, b.refuse(ctx, rec, ReasonError(ReasonDenied, "no gateway routes the server"))
	}

	// AS-2 may not be bound yet (the #2560 shortcut on BindGatewayAS2).
	as2, release, ok := b.acquireAS2()
	if !ok {
		return Authorization{}, b.refuse(ctx, rec, NewError(CodeUnavailable, "", "the gateway's authorization server is not bound yet"))
	}
	defer release()
	auth, err := b.authorizeAtGateway(ctx, as2, s, req, &rec)
	if err != nil {
		return Authorization{}, b.refuse(ctx, rec, err)
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

// authorizeAtGateway runs design steps (7) and (8) for a call that passed
// the checks before a grant: it consumes a reported rejected attempt,
// exchanges the session's grant at AS-1 for an ID-JAG, records it, redeems
// it at as2 for a gateway token, and records the decision. Each try gets a
// new ID-JAG; one is never submitted twice. It fills rec with what it learns,
// so a refusal it returns is recorded with it.
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
	attemptID := "att_" + strings.ToLower(rand.Text())
	rec.AttemptID = attemptID

	var used retriesUsed
	for try := range maxGatewayTries {
		if try > 0 {
			// A closed session gets no further ID-JAG, and a replaced grant
			// is presented in place of the one the last try used.
			if g, err = t.retryGrant(s); err != nil {
				return Authorization{}, err
			}
		}
		res, err := b.idjag.IssueIDJAG(ctx, IDJAGRequest{
			SubjectToken: g.refreshToken, ActorToken: req.Credential,
			Audience: as2.Issuer, Resource: as2.Resource, OperationID: req.OperationID,
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
		if err := b.recorder.RecordIDJAG(ctx, IDJAGRecord{
			At: t.now(), SessionID: s.ID, Agent: rec.Agent, AgentID: rec.AgentID,
			RunID: req.RunID, OperationID: req.OperationID, AttemptID: attemptID,
			IDJAG: claims.ref, ExpiresAt: claims.exp,
		}); err != nil {
			return Authorization{}, NewError(CodeUnavailable, "", "the call could not be recorded")
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
// give; anything else, including a 502 or 504, is mapped by redeemError.
func (t *sessionTable) redeemFailure(ctx context.Context, s *brokerSession, err error) (rule retryRule, retry, final error) {
	if closed := t.stillOpen(s); closed != nil {
		return 0, nil, closed
	}
	var r *ServerRefusal
	isRefusal := errors.As(err, &r)
	switch {
	case lostResponse(ctx, err):
		return retryLostResponse, NewError(CodeUnavailable, "", "no response from the gateway's authorization server"), nil
	case isRefusal && !r.transient() && r.Reason == string(ReasonConsentRequired):
		return 0, nil, ReasonError(ReasonConsentRequired, "the gateway's authorization server requires the user's consent")
	case isRefusal && !r.transient() && r.Code == "invalid_grant" && (r.Reason == "" || r.Reason == unknownCode):
		// AS-2 sends no reason codes until stacklok/toolhive#7030, so every
		// invalid_grant takes this path (the #2533 AS-2 shortcut).
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
	t.closeLocked(s, ReasonSessionClosed)
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
// recorded. In that case the success record is followed by a refusal for the
// same attempt, so the records show that the token was withheld.
func (b *Broker) allow(ctx context.Context, s *brokerSession, req AuthorizeCallRequest, rec *DecisionRecord, key callKey, v GatewayTokenResult, expiresAt time.Time) (Authorization, error) {
	t := b.sessions
	if ref, ok := readJWTRef(v.AccessToken); ok {
		rec.GatewayToken = ref.ref
	} else {
		// Shortcut: a gateway token that is not a JWT has no reference; removed by #2541 (the record sink references AS-2's redemption event).
		rec.GatewayTokenUnreferenced = true
	}
	if err := t.stillOpen(s); err != nil {
		return Authorization{}, err
	}
	success := *rec
	success.Outcome = OutcomeSuccess
	if err := b.recorder.RecordDecision(ctx, success); err != nil {
		return Authorization{}, NewError(CodeUnavailable, "", "the call could not be recorded")
	}
	t.mu.Lock()
	closed := t.closedLocked(s)
	if closed == nil && req.RejectedAttemptID == "" {
		t.attempts.issueLocked(s.ID, rec.AttemptID, key)
	}
	t.mu.Unlock()
	if closed != nil {
		return Authorization{}, closed
	}
	return Authorization{GatewayToken: v.AccessToken, ExpiresAt: expiresAt, AttemptID: rec.AttemptID}, nil
}

// gatewayTokenExpiry returns when v expires: the earliest of its expires_in
// counted from redeemedAt, when AS-2 sent one, the ID-JAG's exp, and v's own
// exp, when v is a JWT with one.
//
// Shortcut: the gateway token is unbound and lives as long as AS-2 issues it; removed by #2538 (lifetime) and #2727 (binding).
func gatewayTokenExpiry(redeemedAt, idjagExp time.Time, v GatewayTokenResult) time.Time {
	exp := idjagExp
	if v.ExpiresIn > 0 {
		exp = earliest(exp, redeemedAt.Add(v.ExpiresIn))
	}
	if ref, ok := readJWTRef(v.AccessToken); ok && !ref.exp.IsZero() {
		exp = earliest(exp, ref.exp)
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
// text.
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
	if exp, err := claims.GetExpirationTime(); err == nil && exp != nil && exp.Unix() > 0 {
		out.exp = exp.Time
	}
	return out, true
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
// when the session has none. The caller holds t.mu.
func decisionRecord(now time.Time, workload Workload, s *brokerSession, req AuthorizeCallRequest) DecisionRecord {
	owner := s.Owner
	if s.verifiedOwner != nil {
		owner = *s.verifiedOwner
	}
	return DecisionRecord{
		At: now, SessionID: s.ID, Workload: workload, Owner: owner,
		RunID: req.RunID, DelegationID: req.DelegationID, RunOwner: req.RunOwner,
		OperationID: req.OperationID, RejectedAttemptID: req.RejectedAttemptID,
		Method: req.Method, Server: req.Server, Tool: req.Tool, ArgumentsSHA256: req.ArgumentsSHA256,
	}
}

// refuse records err as the refusal of rec's call and returns err. The
// refusal is recorded even when ctx is cancelled, as a cancelled call is
// still a decision.
func (b *Broker) refuse(ctx context.Context, rec DecisionRecord, err error) error {
	rec.Code = CodeInternal
	var e *Error
	if errors.As(err, &e) {
		rec.Code, rec.Reason = e.Code, e.Reason
	}
	rec.Outcome = outcomeFor(rec.Code)
	b.recordDecision(context.WithoutCancel(ctx), rec)
	return err
}
