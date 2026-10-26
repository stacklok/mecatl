package identityharness

import (
	"context"
	"encoding/json"
	"net/url"
)

// ScriptedBroker is a hand-driven caller holding the identity broker's
// credentials (client BrokerClientID and the harness's broker key), for
// exercising AS-1 directly: each request carries a fresh client assertion. It
// is not the identity broker and not its token-endpoint client; production
// code under test must use its own client, registering its key through
// Options.BrokerKeys.
type ScriptedBroker struct{ h *Harness }

// ScriptedBroker returns a hand-driven caller holding the broker's
// credentials.
func (h *Harness) ScriptedBroker() *ScriptedBroker { return &ScriptedBroker{h: h} }

// Assertion mints a private_key_jwt client assertion, with any defect spec
// selects.
func (b *ScriptedBroker) Assertion(spec ClientAssertionSpec) string {
	return b.h.AS1.signAssertion(spec)
}

// SessionOpenExchange is one session-open exchange: the user's AS-1 access
// token and the session and owner it is converted for.
type SessionOpenExchange struct {
	SubjectToken string
	Session      string
	Owner        Owner
	// Assertion replaces the fresh, valid client assertion.
	Assertion string
}

// SessionOpenResult is AS-1's RFC 8693 response to a session open. The
// session refresh token arrives in access_token (RFC 8693 §2.2.1).
type SessionOpenResult struct {
	RefreshToken         string           `json:"access_token"`
	IssuedTokenType      string           `json:"issued_token_type"`
	TokenType            string           `json:"token_type"`
	ExpiresIn            int              `json:"expires_in"`
	AuthorizationDetails []SessionDetails `json:"authorization_details"`
}

// OpenSession runs the session-open token exchange of design step (3). A
// refusal returns an *OAuthError; a lost response returns a transport error.
func (b *ScriptedBroker) OpenSession(ctx context.Context, ex SessionOpenExchange) (SessionOpenResult, error) {
	details, err := json.Marshal([]SessionDetails{{Type: SessionDetailsType, Session: ex.Session, Owner: ex.Owner}})
	if err != nil {
		return SessionOpenResult{}, err
	}
	form := url.Values{
		"grant_type":           {grantTokenExchange},
		"subject_token":        {ex.SubjectToken},
		"subject_token_type":   {tokenTypeAccess},
		"requested_token_type": {tokenTypeRefresh},
		paramAuthDetails:       {string(details)},
	}
	b.authenticate(form, ex.Assertion)
	var result SessionOpenResult
	err = b.h.postForm(ctx, b.h.AS1.TokenEndpoint(), form, &result)
	return result, err
}

// Revoke revokes a session refresh token at AS-1's RFC 7009 endpoint.
func (b *ScriptedBroker) Revoke(ctx context.Context, token string) error {
	form := url.Values{"token": {token}, "token_type_hint": {"refresh_token"}}
	b.authenticate(form, "")
	return b.h.postForm(ctx, b.h.AS1.RevocationEndpoint(), form, nil)
}

func (b *ScriptedBroker) authenticate(form url.Values, assertion string) {
	if assertion == "" {
		assertion = b.Assertion(ClientAssertionSpec{})
	}
	form.Set("client_assertion_type", clientAssertionTypeJWT)
	form.Set("client_assertion", assertion)
}
