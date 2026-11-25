package identitybroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// Token types and the authorization_details type of the session-open
// exchange (design step (3)).
const (
	// ExchangeGrantType is the RFC 9396 authorization_details type of
	// ToolHive's exchange grant: a refresh token whose use is fixed when it is
	// issued.
	ExchangeGrantType = "https://toolhive.dev/exchange_grant"
	// TokenTypeIDJAG is the ID-JAG token type, the only output the broker's
	// grants issue.
	TokenTypeIDJAG = "urn:ietf:params:oauth:token-type:id-jag" // #nosec G101 -- token type URN, not a credential.
	// TokenTypeRefreshToken is the RFC 8693 refresh token type, the token a
	// session-open exchange issues.
	TokenTypeRefreshToken = "urn:ietf:params:oauth:token-type:refresh_token" // #nosec G101 -- token type URN, not a credential.
)

// AuthServer is AS-1's token endpoint as session open uses it: the RFC 8693
// exchange of the user's access token for a refresh token that issues only
// ID-JAGs. The adapter authenticates as BrokerClientID with a fresh client
// assertion on every call and serialises req as it is. A refusal is a
// *ServerRefusal; a request with no response wraps ErrNoResponse; a
// misconfigured connection wraps ErrTransportConfiguration; a success
// response it cannot decode wraps both ErrMalformedResponse and
// ErrTransportConfiguration. Errors never carry a token.
type AuthServer interface {
	Exchange(ctx context.Context, req TokenExchange) (TokenExchangeResult, error)
}

// TokenExchange is a session-open exchange request.
type TokenExchange struct {
	// SubjectToken is the user's AS-1 access token.
	SubjectToken Token
	// Grant is the one authorization_details entry the request carries. A
	// zero ExpiresAt leaves expires_at out, and AS-1 applies its maximum.
	Grant ExchangeGrant
}

// TokenExchangeResult is AS-1's answer to a session-open exchange.
type TokenExchangeResult struct {
	// RefreshToken is the issued token, which RFC 8693 returns in
	// access_token.
	RefreshToken    Token
	IssuedTokenType string
	// Grants are the authorization_details entries AS-1 echoed.
	Grants []ExchangeGrant
}

// ExchangeGrant is an exchange-grant authorization_details entry.
type ExchangeGrant struct {
	Type               string
	IssuableTokenTypes []string
	// ExpiresAt is zero when the entry has no expires_at.
	ExpiresAt time.Time
}

// Option configures a Broker in New.
type Option func(*Broker)

// WithAuthServer has OpenSession convert the access token of a request that
// carries one at as1 (design step (3)). Without it, or with nil, the broker
// ignores access tokens and every open only pairs its session.
func WithAuthServer(as1 AuthServer) Option {
	return func(b *Broker) { b.as1 = as1 }
}

// WithClock replaces the broker's clock, which times session hard expiry,
// converted-token memory, and agent credentials, so a test can move past a
// session's hard expiry. Without it the broker uses time.Now.
func WithClock(now func() time.Time) Option {
	return func(b *Broker) { b.sessions.now = now }
}

// heldGrant is the refresh token a broker session holds, with what the broker
// keeps about it (design "Broker grant record"). The token's value never
// leaves the broker through Session.
type heldGrant struct {
	refreshToken Token
	// expiresAt is the expires_at AS-1 echoed: the session's hard expiry.
	expiresAt time.Time
	// sourceJTI is the converted access token's jti, read unverified, which
	// joins this record to AS-1's conversion record.
	sourceJTI string
	// owner is the user the grant is held for, the company identity AS-1
	// verified (docs/drafts/agent-identity/run-owner.md, item 3: grants keyed by session and user, one
	// per session for now). A call's run owner must equal it.
	owner Owner
	// user is the user AS-1 accepted the converted token for, owner among
	// them, and openRecord the AuditID of the open that stored the grant.
	user       User
	openRecord string
}

// accessTokenClaims are the claims the broker reads from an access token
// without verifying it. iss, sub, and client_id are kept only once AS-1
// accepts the token; a claim that is missing or not a string is empty.
type accessTokenClaims struct {
	owner    Owner
	jti      string
	iss      string
	sub      string
	clientID string
	// exp is zero when the token has no numeric exp.
	exp time.Time
}

// readAccessToken reads sub_id, jti, and exp from a compact JWT's payload
// without verifying its signature: AS-1 verifies the same token next. It
// reports false when the token has no sub_id of format iss_sub with a valid
// issuer and subject; a missing or malformed exp only leaves exp zero.
func readAccessToken(token Token) (accessTokenClaims, bool) {
	parts := strings.Split(token.Value(), ".")
	if len(parts) != 3 {
		return accessTokenClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return accessTokenClaims{}, false
	}
	var claims struct {
		JTI      json.RawMessage `json:"jti"`
		Exp      json.RawMessage `json:"exp"`
		SubID    json.RawMessage `json:"sub_id"`
		Iss      json.RawMessage `json:"iss"`
		Sub      json.RawMessage `json:"sub"`
		ClientID json.RawMessage `json:"client_id"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return accessTokenClaims{}, false
	}
	owner, err := OwnerFromSubjectIdentifier(claims.SubID)
	if err != nil {
		return accessTokenClaims{}, false
	}
	out := accessTokenClaims{
		owner: owner, jti: stringClaim(claims.JTI),
		iss: stringClaim(claims.Iss), sub: stringClaim(claims.Sub), clientID: stringClaim(claims.ClientID),
	}
	var exp float64
	if len(claims.Exp) > 0 && json.Unmarshal(claims.Exp, &exp) == nil && exp > 0 {
		out.exp = time.Unix(int64(exp), 0)
	}
	return out, true
}

// stringClaim returns raw as a string, or empty when it is not one.
func stringClaim(raw json.RawMessage) string {
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v
}

// OwnerFromSubjectIdentifier parses raw, an RFC 9493 subject identifier as
// JSON, such as an access token's or a grant's sub_id, into the owner it
// names. Only format iss_sub with a valid issuer and subject is accepted,
// the rule session open applies. Members are read by exact name, as other
// readers of sub_id do, not case-insensitively as decoding into a struct
// would. A failure is a CodeInvalidArgument *Error whose text never contains
// the identifier's values.
func OwnerFromSubjectIdentifier(raw []byte) (Owner, error) {
	var id map[string]json.RawMessage
	if json.Unmarshal(raw, &id) != nil || id == nil {
		return Owner{}, invalid("the subject identifier is not a valid JSON object")
	}
	member := func(name string) string {
		var s string
		if json.Unmarshal(id[name], &s) != nil {
			return ""
		}
		return s
	}
	if member("format") != "iss_sub" {
		return Owner{}, invalid("the subject identifier's format is not iss_sub")
	}
	owner := Owner{Issuer: member("iss"), Subject: member("sub")}
	if err := owner.validate(); err != nil {
		return Owner{}, err
	}
	return owner, nil
}

// recordConvertedLocked remembers that AS-1 may have converted claims' token,
// until its exp plus the clock skew. That retention must cover at least the
// skew AS-1 allows on an access token's exp, after which AS-1 refuses the
// token as expired anyway; AS-1 allows none, so any skew covers it. A token
// with no jti or exp is not remembered: AS-1 requires both, so it converts
// neither. The caller holds t.mu.
func (t *sessionTable) recordConvertedLocked(claims accessTokenClaims) {
	t.pruneConvertedLocked()
	if claims.jti != "" && !claims.exp.IsZero() && t.now().Before(claims.exp.Add(t.skew)) {
		t.converted[claims.jti] = claims.exp
	}
}

// wasConvertedLocked reports whether AS-1 may have converted the token with
// jti. The caller holds t.mu.
func (t *sessionTable) wasConvertedLocked(jti string) bool {
	t.pruneConvertedLocked()
	_, ok := t.converted[jti]
	return jti != "" && ok
}

// pruneConvertedLocked forgets tokens past their exp plus the clock skew.
// The caller holds t.mu.
func (t *sessionTable) pruneConvertedLocked() {
	now := t.now()
	maps.DeleteFunc(t.converted, func(_ string, exp time.Time) bool {
		return !now.Before(exp.Add(t.skew))
	})
}

// convert runs design step (3) for an open of s with an access token: the
// broker's checks, the exchange at AS-1 outside t.mu, the echo check, and
// storing the refresh token. The caller holds the host session's open lock
// and t.mu, which convert releases while AS-1 is called and holds again on
// return.
//
// A refused or failed conversion leaves s paired under the reported owner,
// with whatever refresh token it held, so a retry with a fresh access token
// finds it; only an answer that does not match the request or cannot be
// decoded closes it. s is open and, if it holds a refresh token, not past
// its hard expiry (pairLocked closes such a session first).
//
// It fills rec with what it learns and returns the refresh token to store
// once rec is accepted, or nil, with s as it is, for a token AS-1 may already
// have converted. It never stores a refresh token itself.
func (b *Broker) convert(ctx context.Context, s *brokerSession, req OpenSessionRequest, rec *SessionOpenRecord) (*heldGrant, error) {
	t := b.sessions
	// Check 1: the token's company identity is the owner mecak8s reports.
	claims, ok := readAccessToken(req.AccessToken)
	if !ok {
		return nil, ReasonError(ReasonSubjectInvalid, "the access token has no iss_sub sub_id")
	}
	rec.SourceJTI = claims.jti
	if claims.owner != req.Owner {
		return nil, ReasonError(ReasonOwnerMismatch, "the access token's sub_id is not the reported owner")
	}
	// Check 2: s is open (it came from t.open), and has no verified owner or
	// this one.
	if s.verifiedOwner != nil && *s.verifiedOwner != claims.owner {
		return nil, ReasonError(ReasonSessionOwned, "the broker session's verified owner is another user")
	}
	// A token AS-1 may already have converted, for any session, is never
	// presented to AS-1 again: AS-1 would refuse it with token_used and, once
	// replay-revoke lands, revoke the grant the first conversion created. A
	// repeated open with it, such as a concurrent request for a new host
	// session, gets the session as it is when it holds a live refresh token,
	// and token_used otherwise.
	if t.wasConvertedLocked(claims.jti) {
		if view := t.viewLocked(s); view.HoldsRefreshToken {
			rec.Kind = OpenAlreadyConverted
			rec.setSessionUser(s)
			return nil, nil
		}
		return nil, ReasonError(ReasonTokenUsed, "the access token was already converted; refresh it and open again")
	}
	// A new refresh token never extends the session: once it has a hard
	// expiry, ask for no more. pairLocked already closed s if that expiry had
	// passed, so a held grant here is live.
	var requested time.Time
	if s.grant != nil {
		requested = s.grant.expiresAt
	}

	sent := ExchangeGrant{Type: ExchangeGrantType, IssuableTokenTypes: []string{TokenTypeIDJAG}, ExpiresAt: requested}

	res, resent, err := b.exchangeUnlocked(ctx, TokenExchange{SubjectToken: req.AccessToken, Grant: sent})
	if mayHaveConverted(err, resent) {
		t.recordConvertedLocked(claims)
	}

	if errors.Is(err, ErrMalformedResponse) {
		// AS-1 may have issued a refresh token the broker cannot read: treat
		// it as a mismatched answer.
		t.closeLocked(s, ReasonSessionClosed, CloseMismatchedAnswer)
		return nil, NewError(CodeInternal, "", "the authorization server's answer could not be decoded")
	}
	if err != nil {
		return nil, exchangeError(err)
	}
	// Only a close can have changed s meanwhile: opens of its host session
	// wait for the open lock this call holds.
	if s.closed != "" {
		return nil, ReasonError(s.closed, "the broker session closed during the open")
	}
	if !echoMatches(res, sent, t.now()) {
		// Phase 1 has no revocation: discarding the refresh token and closing
		// the session stops its use by the broker.
		t.closeLocked(s, ReasonSessionClosed, CloseMismatchedAnswer)
		return nil, NewError(CodeInternal, "", "the authorization server's answer does not match the requested grant")
	}
	// AS-1 accepted the token, so the user it names is kept from now on.
	user := User{Owner: claims.owner, Issuer: claims.iss, Subject: claims.sub, ClientID: claims.clientID}
	rec.Kind, rec.User, rec.HardExpiry = OpenConverted, &user, res.Grants[0].ExpiresAt
	if s.grant != nil {
		rec.Replaced = &ReplacedGrant{OpenRecord: s.grant.openRecord, SourceJTI: s.grant.sourceJTI}
	}
	return &heldGrant{
		refreshToken: res.RefreshToken, expiresAt: res.Grants[0].ExpiresAt, sourceJTI: claims.jti,
		owner: claims.owner, user: user, openRecord: rec.AuditID,
	}, nil
}

// exchangeUnlocked is exchange with t.mu released, for a caller holding it.
// t.mu is taken again in a defer, so a panic in the AuthServer unwinds with
// t.mu held, as the caller's deferred Unlock expects, and stays an ordinary
// recoverable panic.
func (b *Broker) exchangeUnlocked(ctx context.Context, req TokenExchange) (TokenExchangeResult, bool, error) {
	t := b.sessions
	t.mu.Unlock()
	defer t.mu.Lock()
	return b.exchange(ctx, req)
}

// exchange calls AS-1 and, after a lost response or a 502 or 504, which AS-1
// may have processed, resends the same request once, reporting whether it
// did. The adapter mints a new client assertion for the resend; if AS-1 had
// converted the token, the resend gets token_used.
func (b *Broker) exchange(ctx context.Context, req TokenExchange) (TokenExchangeResult, bool, error) {
	res, err := b.as1.Exchange(ctx, req)
	if err != nil && resendable(err) && ctx.Err() == nil {
		res, err = b.as1.Exchange(ctx, req)
		return res, true, err
	}
	return res, false, err
}

// mayHaveConverted reports whether AS-1 may have converted the token of an
// exchange that ended with err: on success, an undecodable answer, a
// token_used refusal, an exchange that had to be resent, a final lost
// response, 502, or 504, or a cancellation, whose request may have been
// processed. A refusal of the token, an unavailable AS-1 that answered
// (503, 429, server_error), or a misconfigured connection converts nothing.
func mayHaveConverted(err error, resent bool) bool {
	var r *ServerRefusal
	switch {
	case err == nil, resent, resendable(err), errors.Is(err, ErrMalformedResponse):
		return true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.As(err, &r):
		return r.Reason == string(ReasonTokenUsed)
	}
	return false
}

func resendable(err error) bool {
	var r *ServerRefusal
	if errors.As(err, &r) {
		return r.Status == 502 || r.Status == 504
	}
	return errors.Is(err, ErrNoResponse)
}

// exchangeError maps a failed exchange to the Service error the design's
// error reasons assign: subject_invalid only for AS-1's
// subject_token_invalid, which a fresh access token can pass, and INTERNAL
// for any other refusal that is not transient, token_used, or a revocation,
// including an invalid_request with no reason. Its detail is a
// ServerRefusal's text, which holds only known codes, or fixed text.
func exchangeError(err error) error {
	var r *ServerRefusal
	isRefusal := errors.As(err, &r)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return NewError(CodeUnavailable, "", "the session open was cancelled")
	case IsUnavailable(err):
		if isRefusal {
			return NewError(CodeUnavailable, "", r.Error())
		}
		return NewError(CodeUnavailable, "", "no response from the authorization server")
	case isRefusal && r.Reason == string(ReasonTokenUsed):
		return ReasonError(ReasonTokenUsed, "the access token was already converted; refresh it and open again")
	case isRefusal && Reason(r.Reason).RequiresSignIn():
		// A user-wide revocation predating the token keeps the reason's
		// status; mecak8s answers it at session open with a 401, so the
		// client signs in again.
		return ReasonError(Reason(r.Reason), "the user's authorization was revoked; sign in again")
	case isRefusal && r.Reason == "subject_token_invalid":
		// Only this reason says a fresh access token can pass.
		return ReasonError(ReasonSubjectInvalid, "the authorization server refused the access token")
	case isRefusal && (r.Code == "invalid_request" && r.Reason == "" || r.Reason == string(ReasonSubjectInvalid)):
		// Not a refusal of the access token: AS-1 names that
		// subject_token_invalid, and subject_invalid is the broker's own.
		return NewError(CodeInternal, "", "the authorization server refused the request without saying the access token is invalid")
	case isRefusal:
		return NewError(CodeInternal, "", r.Error())
	case errors.Is(err, ErrTransportConfiguration):
		return NewError(CodeInternal, "", "the connection to the authorization server is misconfigured")
	default:
		return NewError(CodeInternal, "", "the token exchange failed")
	}
}

// echoMatches reports whether res is the grant the broker sent for: a
// refresh token and one entry with sent's type and issuable token types, with
// an expires_at in the future and, when sent has one, no later than it.
func echoMatches(res TokenExchangeResult, sent ExchangeGrant, now time.Time) bool {
	if res.RefreshToken.IsZero() || res.IssuedTokenType != TokenTypeRefreshToken || len(res.Grants) != 1 {
		return false
	}
	g := res.Grants[0]
	switch {
	case g.Type != sent.Type, !slices.Equal(g.IssuableTokenTypes, sent.IssuableTokenTypes):
		return false
	case g.ExpiresAt.IsZero(), !now.Before(g.ExpiresAt):
		return false
	case !sent.ExpiresAt.IsZero() && g.ExpiresAt.After(sent.ExpiresAt):
		return false
	}
	return true
}

// hostLock is one key's lock in keyedLocks. refs counts the callers holding
// or waiting for it, so the entry is dropped when none do.
type hostLock struct {
	sem  chan struct{}
	refs int
}

// keyedLocks serializes callers by key: opens of one root host session,
// registrations of one incarnation, or delegation reports to one broker
// session.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*hostLock
}

// lockHost waits for the open lock of host, or for ctx to end, and returns
// its release. It never holds t.mu.
func (t *sessionTable) lockHost(ctx context.Context, host string) (func(), error) {
	return t.opening.lock(ctx, host, "the session open was cancelled")
}

// lock waits for key's lock, or for ctx to end, and returns its release; a
// wait that ctx ends fails with CodeUnavailable and cancelled.
func (k *keyedLocks) lock(ctx context.Context, key, cancelled string) (func(), error) {
	k.mu.Lock()
	l := k.locks[key]
	if l == nil {
		l = &hostLock{sem: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()
	drop := func() {
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
	release := func() { <-l.sem; drop() }
	// An uncontended lock is taken whatever ctx's state, as before opens
	// were serialized; only waiting gives up when ctx ends.
	select {
	case l.sem <- struct{}{}:
		return release, nil
	default:
	}
	select {
	case l.sem <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		drop()
		return nil, NewError(CodeUnavailable, "", cancelled)
	}
}
