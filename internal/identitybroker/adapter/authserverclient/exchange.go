package authserverclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange" // #nosec G101 -- RFC 8693 grant type URN, not a credential.
	tokenTypeAccessToken   = "urn:ietf:params:oauth:token-type:access_token"   // #nosec G101 -- RFC 8693 token type URN, not a credential.
)

var _ identitybroker.AuthServer = (*Client)(nil)

// exchangeGrant is an exchange-grant authorization_details entry on the wire.
type exchangeGrant struct {
	Type               string   `json:"type"`
	IssuableTokenTypes []string `json:"issuable_token_types"`
	ExpiresAt          *int64   `json:"expires_at,omitempty"`
}

// Exchange implements identitybroker.AuthServer: AS-1's session-open
// exchange (design step (3)). It posts an RFC 8693 token exchange of
// req.SubjectToken for a refresh token, with req.Grant as the one
// authorization_details entry and its expires_at, when set, in Unix seconds.
// A success response that is not valid JSON of the expected shape, or lacks
// access_token or issued_token_type, wraps both
// identitybroker.ErrMalformedResponse and
// identitybroker.ErrTransportConfiguration; other errors are Token's.
func (c *Client) Exchange(ctx context.Context, req identitybroker.TokenExchange) (identitybroker.TokenExchangeResult, error) {
	entry := exchangeGrant{Type: req.Grant.Type, IssuableTokenTypes: req.Grant.IssuableTokenTypes}
	if !req.Grant.ExpiresAt.IsZero() {
		exp := req.Grant.ExpiresAt.Unix()
		entry.ExpiresAt = &exp
	}
	details, err := json.Marshal([]exchangeGrant{entry})
	if err != nil {
		return identitybroker.TokenExchangeResult{}, fmt.Errorf("authserverclient: encode authorization_details: %w", err)
	}
	form := url.Values{
		"grant_type":            {grantTypeTokenExchange},
		"subject_token":         {req.SubjectToken.Value()},
		"subject_token_type":    {tokenTypeAccessToken},
		"requested_token_type":  {identitybroker.TokenTypeRefreshToken},
		"authorization_details": {string(details)},
	}
	var out struct {
		AccessToken          string          `json:"access_token"`
		IssuedTokenType      string          `json:"issued_token_type"`
		AuthorizationDetails []exchangeGrant `json:"authorization_details"`
	}
	if err := c.Token(ctx, form, &out); err != nil {
		return identitybroker.TokenExchangeResult{}, err
	}
	if out.AccessToken == "" || out.IssuedTokenType == "" {
		return identitybroker.TokenExchangeResult{}, fmt.Errorf("authserverclient: %w (%w): the token exchange response lacks access_token or issued_token_type",
			identitybroker.ErrMalformedResponse, identitybroker.ErrTransportConfiguration)
	}
	res := identitybroker.TokenExchangeResult{
		RefreshToken:    identitybroker.NewToken(out.AccessToken),
		IssuedTokenType: out.IssuedTokenType,
		Grants:          make([]identitybroker.ExchangeGrant, len(out.AuthorizationDetails)),
	}
	for i, d := range out.AuthorizationDetails {
		g := identitybroker.ExchangeGrant{Type: d.Type, IssuableTokenTypes: d.IssuableTokenTypes}
		if d.ExpiresAt != nil {
			g.ExpiresAt = time.Unix(*d.ExpiresAt, 0)
		}
		res.Grants[i] = g
	}
	return res, nil
}
