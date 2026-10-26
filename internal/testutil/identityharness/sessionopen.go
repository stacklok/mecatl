package identityharness

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// SessionDetailsType is the RFC 9396 authorization_details type naming
	// the broker session and its owner at session open.
	SessionDetailsType = "mecatl_session"
	// DefaultSessionRefreshTokenLifetime is a session's hard expiry, counted
	// from its first open.
	DefaultSessionRefreshTokenLifetime = 12 * time.Hour

	grantTokenExchange   = "urn:ietf:params:oauth:grant-type:token-exchange" // #nosec G101 -- RFC 8693 grant type URN, not a credential.
	tokenTypeAccess      = "urn:ietf:params:oauth:token-type:access_token"   // #nosec G101 -- RFC 8693 token type URN, not a credential.
	tokenTypeRefresh     = "urn:ietf:params:oauth:token-type:refresh_token"  // #nosec G101 -- RFC 8693 token type URN, not a credential.
	paramAuthDetails     = "authorization_details"
	errInvalidAuthDetail = "invalid_authorization_details"

	// Reason codes from the design's error reasons.
	reasonSubjectInvalid = "subject_invalid"
	reasonOwnerMismatch  = "owner_mismatch"
	reasonSessionOwned   = "session_owned"
	reasonTokenUsed      = "token_used"
	reasonSuperseded     = "superseded"
	reasonSessionClosed  = "session_closed"
)

// Owner is a company identity: the upstream IdP's issuer and subject, as in
// an access token's sub_id.
type Owner struct {
	Issuer  string `json:"iss"`
	Subject string `json:"sub"`
}

// SessionDetails is the mecatl_session authorization_details entry a session
// open sends and AS-1 echoes.
type SessionDetails struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Owner   Owner  `json:"owner"`
}

// SessionGrant is AS-1's record of a session refresh token.
type SessionGrant struct {
	Session  string
	Owner    Owner
	ClientID string
	// Subject is the user's AS-1 subject.
	Subject string
	// SourceJTI is the jti of the access token the open converted.
	SourceJTI string
	// HardExpiry is the session's hard expiry; a later open never extends it.
	HardExpiry time.Time
	// Active reports whether the token is still usable. Otherwise Reason is
	// the reason a later use (the ID-JAG exchange, not built yet) would get,
	// never a session-open outcome: superseded when a later open replaced it,
	// session_closed when the session was revoked or passed its hard expiry.
	Active bool
	Reason string
}

type as1Session struct {
	owner      Owner
	hardExpiry time.Time
	current    string
	closed     bool
}

type as1SessionToken struct {
	session, clientID, subject, sourceJTI string
	owner                                 Owner
	replaced                              bool
}

type as1Conversion struct {
	clientID, session string
	expires           time.Time
}

// FailNextSessionOpen makes the next session open from an authenticated
// broker fail with fault before any other check, for a 503 or any other
// injected error.
func (a *AS1) FailNextSessionOpen(fault Fault) { a.faults.push("session-open", fault) }

// LoseNextSessionOpenResponse makes AS-1 process the next session open in
// full and then drop the connection without a response, as a lost response.
func (a *AS1) LoseNextSessionOpenResponse() { a.faults.push("session-open-lost", Fault{}) }

// MismatchNextSessionOpenEcho makes the next successful session open echo
// echo instead of the entry it was sent. The refresh token is still bound to
// the entry sent.
func (a *AS1) MismatchNextSessionOpenEcho(echo SessionDetails) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.echoes = append(a.echoes, echo)
}

// ExpireSession forces session past its hard expiry: its refresh token stops
// working with session_closed and the session is closed, as if the clock had
// passed the hard expiry.
func (a *AS1) ExpireSession(session string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[session]; ok && !s.closed {
		s.hardExpiry = a.now()
	}
}

// SessionClosed reports whether AS-1 holds a closed-session record for
// session: revoked, or past its hard expiry.
func (a *AS1) SessionClosed(session string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessionLocked(session)
	return s != nil && s.closed
}

// SessionOpenRefusals returns the reasons AS-1 recorded for refused opens of
// session, oldest first.
func (a *AS1) SessionOpenRefusals(session string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.refusals[session])
}

// SessionRefreshTokensIssued returns how many session refresh tokens AS-1
// has issued for session, including any a lost response orphaned.
func (a *AS1) SessionRefreshTokensIssued(session string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, rec := range a.sessionTokens {
		if rec.session == session {
			n++
		}
	}
	return n
}

// SessionRefreshToken returns AS-1's record of a session refresh token.
func (a *AS1) SessionRefreshToken(token string) (SessionGrant, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.sessionTokens[token]
	if !ok {
		return SessionGrant{}, false
	}
	s := a.sessionLocked(rec.session)
	grant := SessionGrant{
		Session: rec.session, Owner: rec.owner, ClientID: rec.clientID, Subject: rec.subject,
		SourceJTI: rec.sourceJTI, HardExpiry: s.hardExpiry, Active: true,
	}
	switch {
	case rec.replaced:
		grant.Active, grant.Reason = false, reasonSuperseded
	case s.closed:
		grant.Active, grant.Reason = false, reasonSessionClosed
	}
	return grant, true
}

// sessionLocked returns the session record, closing it once its hard expiry
// has passed. Losing the refresh token closes the session.
func (a *AS1) sessionLocked(id string) *as1Session {
	s, ok := a.sessions[id]
	if !ok {
		return nil
	}
	if !s.closed && !a.now().Before(s.hardExpiry) {
		s.closed = true
	}
	return s
}

// openSession is the session-open token exchange of design step (3), for an
// authenticated broker. Check 1 (client authentication) and check 2 (the
// broker may use this grant) have passed.
func (a *AS1) openSession(w http.ResponseWriter, r *http.Request) {
	if fault, ok := a.faults.pop("session-open"); ok {
		writeOAuthError(w, fault.Status, fault.Error, fault.Reason)
		return
	}
	entry, echo, code := checkSessionOpenForm(r.PostForm)
	if code != "" {
		writeOAuthError(w, http.StatusBadRequest, code, "")
		return
	}
	// Check 4: the subject token is a live AS-1 access token for the Mecatl
	// API, issued to a public client.
	subject, ok := a.verifySubjectToken(r.PostForm.Get("subject_token"))
	if !ok {
		a.refuse(w, entry.Session, "invalid_request", reasonSubjectInvalid)
		return
	}
	refresh, expiresIn, code, reason := a.convert(subject, entry)
	if reason != "" {
		a.refuse(w, entry.Session, code, reason)
		return
	}
	a.mu.Lock()
	if len(a.echoes) > 0 {
		echo, _ = json.Marshal(a.echoes[0])
		a.echoes = a.echoes[1:]
	}
	a.mu.Unlock()

	if _, lost := a.faults.pop("session-open-lost"); lost {
		panic(http.ErrAbortHandler) // closes the connection with no response
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":      refresh,
		"issued_token_type": tokenTypeRefresh,
		"token_type":        "N_A",
		"expires_in":        expiresIn,
		paramAuthDetails:    []json.RawMessage{echo},
	})
}

// checkSessionOpenForm runs check 3: the request is well formed. It returns
// the entry and its compact JSON for the echo, or the OAuth error code.
func checkSessionOpenForm(form url.Values) (SessionDetails, json.RawMessage, string) {
	for _, values := range form {
		if len(values) > 1 { // RFC 6749 §3.2
			return SessionDetails{}, nil, "invalid_request"
		}
	}
	switch {
	case form.Has("scope"):
		return SessionDetails{}, nil, "invalid_scope"
	case form.Get("subject_token") == "" || form.Get("subject_token_type") != tokenTypeAccess ||
		form.Get("requested_token_type") != tokenTypeRefresh || form.Has("actor_token") ||
		form.Has("actor_token_type") || !form.Has(paramAuthDetails):
		return SessionDetails{}, nil, "invalid_request"
	}
	entry, raw, ok := parseSessionDetails(form.Get(paramAuthDetails))
	if !ok {
		return SessionDetails{}, nil, errInvalidAuthDetail
	}
	return entry, raw, ""
}

// convert runs checks 5 to 7 and, if they pass, in one transaction records
// the conversion and the owner, writes the new refresh token, and revokes the
// session's previous one. A refusal returns its OAuth error code and reason.
func (a *AS1) convert(subject as1Subject, entry SessionDetails) (refresh string, expiresIn int, code, reason string) {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	// Check 5: the token's company identity is the owner sent.
	if subject.owner != entry.Owner {
		return "", 0, "invalid_request", reasonOwnerMismatch
	}
	// Check 6: the session was never closed and has no other owner.
	s := a.sessionLocked(entry.Session)
	if s != nil && (s.closed || s.owner != entry.Owner) {
		return "", 0, errInvalidAuthDetail, reasonSessionOwned
	}
	// Check 7: the jti has not opened a session, or this is a retry with the
	// same jti, client, and session. Check 4 already holds it within exp, and
	// check 6 holds the session open.
	// Only BrokerClientID reaches this exchange, so the client always matches.
	maps.DeleteFunc(a.conversions, func(_ string, c as1Conversion) bool {
		return now.After(c.expires.Add(as1ClockSkew))
	})
	if c, used := a.conversions[subject.jti]; used && c.session != entry.Session {
		return "", 0, "invalid_request", reasonTokenUsed
	}
	a.conversions[subject.jti] = as1Conversion{clientID: BrokerClientID, session: entry.Session, expires: subject.expires}
	// Guess (stacklok/toolhive#6897): the hard expiry runs from the first
	// open; a retry or later open does not extend it.
	if s == nil {
		s = &as1Session{owner: entry.Owner, hardExpiry: now.Add(a.sessionLifetime)}
		a.sessions[entry.Session] = s
	}
	if prev, ok := a.sessionTokens[s.current]; ok {
		prev.replaced = true
	}
	refresh = randomToken("as1-session-rt-")
	a.sessionTokens[refresh] = &as1SessionToken{
		session: entry.Session, clientID: BrokerClientID, subject: subject.sub,
		sourceJTI: subject.jti, owner: entry.Owner,
	}
	s.current = refresh
	return refresh, int(s.hardExpiry.Sub(now).Seconds()), "", ""
}

// parseSessionDetails accepts exactly one mecatl_session entry with a session
// and an owner, and returns it with its compact JSON for the echo. Member
// names match exactly and appear once; encoding/json alone would match them
// case-insensitively and let a duplicate win.
func parseSessionDetails(value string) (SessionDetails, json.RawMessage, bool) {
	var entries []json.RawMessage
	if json.Unmarshal([]byte(value), &entries) != nil || len(entries) != 1 {
		return SessionDetails{}, nil, false
	}
	// Guess: members beyond type, session, and owner are kept and echoed
	// unchanged, as RFC 9396 allows extension members.
	members, ok := strictObject(entries[0])
	if !ok {
		return SessionDetails{}, nil, false
	}
	owner, ok := strictObject(members["owner"])
	if !ok {
		return SessionDetails{}, nil, false
	}
	entry := SessionDetails{
		Type: strictString(members["type"]), Session: strictString(members["session"]),
		Owner: Owner{Issuer: strictString(owner["iss"]), Subject: strictString(owner["sub"])},
	}
	if entry.Type != SessionDetailsType || entry.Session == "" || entry.Owner.Issuer == "" || entry.Owner.Subject == "" {
		return SessionDetails{}, nil, false
	}
	var compact bytes.Buffer
	if json.Compact(&compact, entries[0]) != nil {
		return SessionDetails{}, nil, false
	}
	return entry, compact.Bytes(), true
}

// strictObject decodes a JSON object into its members by exact name,
// refusing anything else and any repeated name.
func strictObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	members := make(map[string]json.RawMessage)
	for dec.More() {
		tok, err := dec.Token()
		name, isName := tok.(string)
		if err != nil || !isName {
			return nil, false
		}
		if _, dup := members[name]; dup {
			return nil, false
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return nil, false
		}
		members[name] = value
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	return members, true
}

// strictString returns raw as a string, or "" when it is absent or not one.
func strictString(raw json.RawMessage) string {
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

type as1Subject struct {
	sub, jti string
	owner    Owner
	expires  time.Time
}

// verifySubjectToken runs check 4 on a session-open subject token.
func (a *AS1) verifySubjectToken(token string) (as1Subject, bool) {
	var claims struct {
		jwt.RegisteredClaims
		ClientID string `json:"client_id"`
		SubID    struct {
			Format string `json:"format"`
			Owner
		} `json:"sub_id"`
	}
	parsed, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return &a.key.PublicKey, nil },
		jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer(a.Issuer()), jwt.WithAudience(a.audience),
		jwt.WithExpirationRequired(), jwt.WithTimeFunc(a.now))
	if err != nil {
		return as1Subject{}, false
	}
	if typ := parsed.Header["typ"]; typ != "at+jwt" && typ != "application/at+jwt" {
		return as1Subject{}, false
	}
	a.mu.Lock()
	_, publicClient := a.clients[claims.ClientID]
	a.mu.Unlock()
	if !publicClient || claims.ID == "" || claims.Subject == "" || claims.SubID.Format != "iss_sub" ||
		claims.SubID.Issuer == "" || claims.SubID.Subject == "" {
		return as1Subject{}, false
	}
	return as1Subject{sub: claims.Subject, jti: claims.ID, owner: claims.SubID.Owner, expires: claims.ExpiresAt.Time}, true
}

// refuse records a refused open with its reason and writes the error.
//
// Guess: checks 4 to 7 are recorded, since they name a session; the design
// requires it for checks 5 and 6 and records "a refused open with its reason".
func (a *AS1) refuse(w http.ResponseWriter, session, code, reason string) {
	a.mu.Lock()
	a.refusals[session] = append(a.refusals[session], reason)
	a.mu.Unlock()
	writeOAuthError(w, http.StatusBadRequest, code, reason)
}
