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
	// ExchangeGrantType is the RFC 9396 authorization_details type of
	// ToolHive's generic grant (stacklok/toolhive#6897): a refresh token
	// whose use is fixed when it is issued.
	ExchangeGrantType = "https://toolhive.dev/exchange_grant"
	// TokenTypeIDJAG is the ID-JAG token type, the only output AS-1 issues
	// the broker's grants for.
	TokenTypeIDJAG = "urn:ietf:params:oauth:token-type:id-jag" // #nosec G101 -- token type URN, not a credential.
	// DefaultGrantMaxLifetime is AS-1's configured maximum lifetime for a
	// grant that issues ID-JAGs.
	DefaultGrantMaxLifetime = 12 * time.Hour

	grantTokenExchange   = "urn:ietf:params:oauth:grant-type:token-exchange" // #nosec G101 -- RFC 8693 grant type URN, not a credential.
	tokenTypeAccess      = "urn:ietf:params:oauth:token-type:access_token"   // #nosec G101 -- RFC 8693 token type URN, not a credential.
	tokenTypeRefresh     = "urn:ietf:params:oauth:token-type:refresh_token"  // #nosec G101 -- RFC 8693 token type URN, not a credential.
	paramAuthDetails     = "authorization_details"
	errInvalidAuthDetail = "invalid_authorization_details"

	// Reason codes AS-1 returns for grants (design "Error reasons").
	reasonTokenUsed = "token_used"
	reasonRevoked   = "revoked"

	// Reasons for an invalid_authorization_details refusal
	// (stacklok/toolhive#6897).
	reasonSingleEntryRequired   = "single_entry_required"
	reasonMalformedEntry        = "malformed_entry"
	reasonInvalidIssuableTypes  = "invalid_issuable_token_types"
	reasonTokenTypeNotPermitted = "token_type_not_permitted"
	reasonInvalidExpiresAt      = "invalid_expires_at"
)

// Owner is a company identity: the upstream IdP's issuer and subject, as in
// an access token's sub_id.
type Owner struct {
	Issuer  string `json:"iss"`
	Subject string `json:"sub"`
}

// ExchangeGrant is the exchange-grant authorization_details entry a session
// open sends and AS-1 echoes with the expiry it granted. ExpiresAt is Unix
// seconds; zero leaves it out.
type ExchangeGrant struct {
	Type               string   `json:"type"`
	IssuableTokenTypes []string `json:"issuable_token_types"`
	ExpiresAt          int64    `json:"expires_at,omitempty"`
}

// Grant is AS-1's record of a grant and its refresh token. It names the
// client and the user, never a Mecatl session or owner.
type Grant struct {
	ClientID string
	// Subject is the user's AS-1 subject, and SubID their company identity.
	Subject string
	SubID   Owner
	// SourceJTI is the jti of the access token the exchange converted.
	SourceJTI string
	// Output is the one token type the grant issues.
	Output    string
	ExpiresAt time.Time
	// Active reports whether the refresh token is still usable. Otherwise
	// Reason is what a later use (the ID-JAG exchange, not built yet) would
	// get: revoked for a revoked grant, and no reason for an expired one.
	Active bool
	Reason string
}

// Refusal is a refused exchange AS-1 recorded: its OAuth error code and its
// reason, empty when it has none.
type Refusal struct {
	Code   string
	Reason string
}

type as1Grant struct {
	clientID, subject, sourceJTI, output string
	subID                                Owner
	expiresAt                            time.Time
	revoked                              bool
}

// FailNextExchange makes the next token exchange from an authenticated
// broker fail with fault before any other check, for a 503 or any other
// injected error.
func (a *AS1) FailNextExchange(fault Fault) { a.faults.push("exchange", fault) }

// LoseNextExchangeResponse makes AS-1 process the next token exchange in
// full and then drop the connection without a response, as a lost response.
func (a *AS1) LoseNextExchangeResponse() { a.faults.push("exchange-lost", Fault{}) }

// MismatchNextExchangeEcho makes the next successful token exchange echo
// echo instead of the entry it granted. The grant is still the one granted.
func (a *AS1) MismatchNextExchangeEcho(echo ExchangeGrant) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.echoes = append(a.echoes, echo)
}

// ExpireGrant forces the grant of refresh token past its expiry, as if the
// clock had passed it.
func (a *AS1) ExpireGrant(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if g, ok := a.grants[token]; ok {
		g.expiresAt = a.now()
	}
}

// ExchangeRefusals returns the refused exchanges AS-1 recorded, oldest
// first.
func (a *AS1) ExchangeRefusals() []Refusal {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.refusals)
}

// GrantsIssued returns how many grants AS-1 has issued, including any a lost
// response orphaned.
func (a *AS1) GrantsIssued() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.grants)
}

// Grant returns AS-1's record of the grant of refresh token.
func (a *AS1) Grant(token string) (Grant, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.grants[token]
	if !ok {
		return Grant{}, false
	}
	grant := Grant{
		ClientID: g.clientID, Subject: g.subject, SubID: g.subID, SourceJTI: g.sourceJTI,
		Output: g.output, ExpiresAt: g.expiresAt, Active: true,
	}
	switch {
	case g.revoked:
		grant.Active, grant.Reason = false, reasonRevoked
	case !a.now().Before(g.expiresAt):
		grant.Active = false
	}
	return grant, true
}

// exchange is AS-1's side of session open, design step (3): the RFC 8693
// exchange of an access token for a grant that issues only ID-JAGs, for an
// authenticated broker. Check 1 (client authentication) and check 2 (the
// broker may use this grant) have passed.
func (a *AS1) exchange(w http.ResponseWriter, r *http.Request) {
	if fault, ok := a.faults.pop("exchange"); ok {
		writeOAuthError(w, fault.Status, fault.Error, fault.Reason)
		return
	}
	now := a.now()
	entry, code, reason := checkExchangeForm(r.PostForm, now)
	if code != "" {
		writeOAuthError(w, http.StatusBadRequest, code, reason)
		return
	}
	// Check 4: the subject token is a live AS-1 access token for the Mecatl
	// API, issued to a public client. A refused token gets no reason.
	subject, ok := a.verifySubjectToken(r.PostForm.Get("subject_token"))
	if !ok {
		a.refuse(w, "invalid_request", "")
		return
	}
	refresh, granted, ok := a.convert(subject, entry, now)
	if !ok {
		a.refuse(w, "invalid_request", reasonTokenUsed)
		return
	}
	echo := ExchangeGrant{Type: ExchangeGrantType, IssuableTokenTypes: entry.IssuableTokenTypes, ExpiresAt: granted.Unix()}
	a.mu.Lock()
	if len(a.echoes) > 0 {
		echo = a.echoes[0]
		a.echoes = a.echoes[1:]
	}
	a.mu.Unlock()

	if _, lost := a.faults.pop("exchange-lost"); lost {
		panic(http.ErrAbortHandler) // closes the connection with no response
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":      refresh,
		"issued_token_type": tokenTypeRefresh,
		"token_type":        "N_A",
		"expires_in":        int(granted.Sub(now).Seconds()),
		paramAuthDetails:    []ExchangeGrant{echo},
	})
}

// checkExchangeForm runs check 3: the request is well formed. It returns the
// entry, or the OAuth error code and any reason.
func checkExchangeForm(form url.Values, now time.Time) (ExchangeGrant, string, string) {
	for _, values := range form {
		if len(values) > 1 { // RFC 6749 §3.2
			return ExchangeGrant{}, "invalid_request", ""
		}
	}
	switch {
	case form.Has("scope"):
		return ExchangeGrant{}, "invalid_scope", ""
	case form.Has("resource") || form.Has("audience"):
		return ExchangeGrant{}, errInvalidTarget, ""
	case form.Get("subject_token") == "" || form.Get("subject_token_type") != tokenTypeAccess ||
		form.Get("requested_token_type") != tokenTypeRefresh || form.Has("actor_token") ||
		form.Has("actor_token_type") || !form.Has(paramAuthDetails):
		return ExchangeGrant{}, "invalid_request", ""
	}
	entry, reason := parseExchangeGrant(form.Get(paramAuthDetails), now)
	if reason != "" {
		return ExchangeGrant{}, errInvalidAuthDetail, reason
	}
	return entry, "", ""
}

// parseExchangeGrant accepts exactly one exchange-grant entry listing only
// id-jag, with an optional expires_at in the future, and returns it, or the
// invalid_authorization_details reason. Member names match exactly, appear
// once, and no other member is allowed; encoding/json alone would match them
// case-insensitively, let a duplicate win, and ignore unknown ones.
func parseExchangeGrant(value string, now time.Time) (ExchangeGrant, string) {
	var entries []json.RawMessage
	if json.Unmarshal([]byte(value), &entries) != nil {
		return ExchangeGrant{}, reasonMalformedEntry
	}
	if len(entries) != 1 {
		return ExchangeGrant{}, reasonSingleEntryRequired
	}
	members, ok := strictObject(entries[0])
	if !ok || strictString(members["type"]) != ExchangeGrantType {
		return ExchangeGrant{}, reasonMalformedEntry
	}
	for name := range members {
		if name != "type" && name != "issuable_token_types" && name != "expires_at" {
			return ExchangeGrant{}, reasonMalformedEntry
		}
	}
	var types []string
	if json.Unmarshal(members["issuable_token_types"], &types) != nil || len(types) != 1 {
		return ExchangeGrant{}, reasonInvalidIssuableTypes
	}
	switch types[0] {
	case TokenTypeIDJAG:
	case tokenTypeRefresh:
		// The broker's client is configured for ID-JAG grants only.
		return ExchangeGrant{}, reasonTokenTypeNotPermitted
	default:
		return ExchangeGrant{}, reasonInvalidIssuableTypes
	}
	entry := ExchangeGrant{Type: ExchangeGrantType, IssuableTokenTypes: types}
	if raw, ok := members["expires_at"]; ok {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var n json.Number
		if dec.Decode(&n) != nil {
			return ExchangeGrant{}, reasonInvalidExpiresAt
		}
		exp, err := n.Int64()
		if err != nil || exp <= now.Unix() {
			return ExchangeGrant{}, reasonInvalidExpiresAt
		}
		entry.ExpiresAt = exp
	}
	return entry, ""
}

// convert runs check 5 and, if it passes, in one transaction records the
// converted jti and writes the grant, expiring at the requested expires_at
// capped at AS-1's maximum. It returns false when the jti was converted
// before; a retry is not an exception.
func (a *AS1) convert(subject as1Subject, entry ExchangeGrant, now time.Time) (string, time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Check 4 already holds the token within exp, so a converted jti is kept
	// until exp plus the clock skew.
	maps.DeleteFunc(a.conversions, func(_ string, until time.Time) bool {
		return now.After(until.Add(as1ClockSkew))
	})
	if _, used := a.conversions[subject.jti]; used {
		return "", time.Time{}, false
	}
	a.conversions[subject.jti] = subject.expires
	expires := now.Add(a.grantMax)
	if requested := time.Unix(entry.ExpiresAt, 0); entry.ExpiresAt != 0 && requested.Before(expires) {
		expires = requested
	}
	refresh := randomToken("as1-grant-rt-")
	// Only BrokerClientID reaches this exchange.
	a.grants[refresh] = &as1Grant{
		clientID: BrokerClientID, subject: subject.sub, subID: subject.owner,
		sourceJTI: subject.jti, output: TokenTypeIDJAG, expiresAt: expires,
	}
	return refresh, expires, true
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

// verifySubjectToken runs check 4 on an exchange's subject token.
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

// refuse records a refused exchange and writes the error.
//
// Guess: checks 4 and 5 are recorded, since they concern the user's token;
// malformed requests are not.
func (a *AS1) refuse(w http.ResponseWriter, code, reason string) {
	a.mu.Lock()
	a.refusals = append(a.refusals, Refusal{Code: code, Reason: reason})
	a.mu.Unlock()
	writeOAuthError(w, http.StatusBadRequest, code, reason)
}
