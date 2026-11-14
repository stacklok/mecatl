package authserverclient

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

const grantTypeJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer" // #nosec G101 -- RFC 7523 grant type URN, not a credential.

var _ identitybroker.GatewayAuthServer = (*Client)(nil)

// Redeem implements identitybroker.GatewayAuthServer: AS-2's redemption of an
// ID-JAG for a gateway token (design step (8)), an RFC 7523 JWT-bearer grant
// for req.Resource. A refresh token in the response is ignored and never
// kept. A success response that is not valid JSON of the expected shape,
// lacks access_token, or is not a bearer token wraps both
// identitybroker.ErrMalformedResponse and
// identitybroker.ErrTransportConfiguration; other errors are Token's.
func (c *Client) Redeem(ctx context.Context, req identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
	// Shortcut: no authorization_details on redemption; removed by #2727 (bound gateway tokens).
	form := url.Values{
		"grant_type": {grantTypeJWTBearer},
		"assertion":  {req.Assertion.Value()},
		"resource":   {req.Resource},
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := c.Token(ctx, form, &out); err != nil {
		return identitybroker.GatewayTokenResult{}, err
	}
	if out.AccessToken == "" || !strings.EqualFold(out.TokenType, "bearer") {
		return identitybroker.GatewayTokenResult{}, fmt.Errorf("authserverclient: %w (%w): the redemption response lacks access_token or is not a bearer token",
			identitybroker.ErrMalformedResponse, identitybroker.ErrTransportConfiguration)
	}
	return identitybroker.GatewayTokenResult{AccessToken: identitybroker.NewToken(out.AccessToken), ExpiresIn: seconds(out.ExpiresIn)}, nil
}
