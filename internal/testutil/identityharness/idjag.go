package identityharness

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// DefaultIDJAGLifetime is the lifetime of the ID-JAGs AS-1 issues, before
	// the cap at the actor token's expiry.
	DefaultIDJAGLifetime = 2 * time.Minute
	// IDJAGHeaderType is the typ header of an ID-JAG.
	IDJAGHeaderType = "oauth-id-jag+jwt"

	tokenTypeJWT = "urn:ietf:params:oauth:token-type:jwt" // #nosec G101 -- RFC 8693 token type URN, not a credential.

	// Reasons AS-1 returns for an ID-JAG exchange (design step (7)).
	reasonGrantInvalid = "grant_invalid"
	reasonSVIDExpired  = "svid_expired"
	reasonDenied       = "denied"
)

// ActorValidator is AS-1's pluggable actor-token validator (design step (7),
// check 6), called with the grant's company identity and the actor token. It
// returns the agent's identifier, which becomes act.sub, and the actor
// token's expiry, which caps the ID-JAG's exp. An error matching
// ErrActorExpired answers svid_expired, one matching ErrActorDenied answers
// denied, and any other answers 503 server_error. It is the harness's own
// type, so the harness never imports the broker; tests adapt the broker's
// validator to it.
type ActorValidator func(ctx context.Context, owner Owner, actorToken string) (agentID string, expiresAt time.Time, err error)

var (
	// ErrActorExpired is an ActorValidator's refusal of an expired actor token.
	ErrActorExpired = errors.New("identityharness: actor token expired")
	// ErrActorDenied is an ActorValidator's refusal of any other actor token.
	ErrActorDenied = errors.New("identityharness: actor token denied")
)

// IDJAGIssuance is AS-1's record of an ID-JAG it issued.
type IDJAGIssuance struct {
	// JTI is the ID-JAG's jti and Subject its sub, the grant's AS-1 subject.
	JTI, Subject string
	// Owner is the grant's company identity.
	Owner Owner
	// AgentID is the validator's agent identifier, the ID-JAG's act.sub.
	AgentID string
	// Audience and Resource are the ID-JAG's aud and resource.
	Audience, Resource string
	// Operation is the request's mecatl_operation.
	Operation string
	ExpiresAt time.Time
}

type idjagTarget struct{ audience, resource string }

// idjagClaims are an ID-JAG's claims (design step (7)). Audience replaces
// the registered claims' array with one string, as the design shows it.
type idjagClaims struct {
	jwt.RegisteredClaims
	Audience string     `json:"aud"`
	Resource string     `json:"resource"`
	ClientID string     `json:"client_id"`
	Actor    idjagActor `json:"act"`
}

// idjagActor is act: AS-1, which minted it, and the agent.
type idjagActor struct {
	Issuer  string `json:"iss"`
	Subject string `json:"sub"`
}

// SetActorValidator sets the validator the ID-JAG exchange calls. Without
// one, every actor token is denied.
func (a *AS1) SetActorValidator(v ActorValidator) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.validator = v
}

// AddIDJAGTarget configures an AS-2 issuer, audience, and a vMCP resource for
// it that the ID-JAG exchange issues for. Any other pair is invalid_target.
func (a *AS1) AddIDJAGTarget(audience, resource string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.idjagTargets[idjagTarget{audience, resource}] = true
}

// RevokeGrants revokes every grant of the company identity user, as AS-1
// does when it ends a user's grants, and returns how many it revoked. A later
// ID-JAG exchange for one gets revoked.
func (a *AS1) RevokeGrants(user string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, g := range a.grants {
		if g.subID.Subject == user && g.subID.Issuer == a.idp.Issuer() && !g.revoked {
			g.revoked = true
			n++
		}
	}
	return n
}

// IDJAGsIssued returns the ID-JAGs AS-1 has issued, oldest first.
func (a *AS1) IDJAGsIssued() []IDJAGIssuance {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.idjags)
}

// issueIDJAG is AS-1's side of a call, design step (7): the RFC 8693
// exchange of a grant's refresh token, with the agent's SVID as the actor
// token, for an ID-JAG addressed to AS-2. Check 1 (client authentication) has
// passed. Check 5, the upstream re-check, is not modelled: tests inject its
// reasons as faults. Check 6 is the validator's.
func (a *AS1) issueIDJAG(w http.ResponseWriter, r *http.Request) {
	form := r.PostForm
	if !validIDJAGForm(form) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}
	target := idjagTarget{form.Get("audience"), form.Get(paramResource)}
	now := a.now()
	a.mu.Lock()
	configured := a.idjagTargets[target]
	g, known := a.grants[form.Get("subject_token")]
	var grant as1Grant
	if known {
		grant = *g
	}
	validator := a.validator
	a.mu.Unlock()
	if !configured {
		writeOAuthError(w, http.StatusBadRequest, errInvalidTarget, "")
		return
	}
	switch {
	case !known || grant.clientID != BrokerClientID || grant.output != TokenTypeIDJAG || !now.Before(grant.expiresAt):
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", reasonGrantInvalid)
		return
	case grant.revoked:
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", reasonRevoked)
		return
	}
	if validator == nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", reasonDenied)
		return
	}
	agentID, actorExpires, err := validator(r.Context(), grant.subID, form.Get("actor_token"))
	switch {
	case errors.Is(err, ErrActorExpired):
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", reasonSVIDExpired)
		return
	case errors.Is(err, ErrActorDenied):
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", reasonDenied)
		return
	case err != nil:
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "")
		return
	}
	expires := now.Add(a.idjagLifetime)
	if !actorExpires.IsZero() && actorExpires.Before(expires) {
		expires = actorExpires
	}
	if expires.Unix() <= now.Unix() {
		// Guess: an actor token at its expiry is expired, whatever the
		// validator said.
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", reasonSVIDExpired)
		return
	}
	jti := randomToken("jag-")
	token := jwt.NewWithClaims(jwt.SigningMethodES256, idjagClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: a.Issuer(), Subject: grant.subject, ID: jti,
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expires),
		},
		Audience: target.audience, Resource: target.resource, ClientID: BrokerClientID,
		Actor: idjagActor{Issuer: a.Issuer(), Subject: agentID},
	})
	token.Header["typ"], token.Header["kid"] = IDJAGHeaderType, as1KeyID
	signed, err := token.SignedString(a.key)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	a.mu.Lock()
	a.idjags = append(a.idjags, IDJAGIssuance{
		JTI: jti, Subject: grant.subject, Owner: grant.subID, AgentID: agentID,
		Audience: target.audience, Resource: target.resource, Operation: form.Get("mecatl_operation"),
		ExpiresAt: time.Unix(expires.Unix(), 0),
	})
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":      signed,
		"issued_token_type": TokenTypeIDJAG,
		"token_type":        "N_A",
		"expires_in":        expires.Unix() - now.Unix(),
	})
}

// validIDJAGForm runs check 2: one value per field, the three token types,
// the subject and actor tokens, an audience and a resource, and no scope or
// authorization_details.
func validIDJAGForm(form url.Values) bool {
	for _, values := range form {
		if len(values) > 1 { // RFC 6749 §3.2
			return false
		}
	}
	return form.Get("subject_token") != "" && form.Get("subject_token_type") == tokenTypeRefresh &&
		form.Get("actor_token") != "" && form.Get("actor_token_type") == tokenTypeJWT &&
		form.Get("requested_token_type") == TokenTypeIDJAG &&
		form.Get("audience") != "" && form.Get(paramResource) != "" &&
		!form.Has("scope") && !form.Has(paramAuthDetails)
}
