package authserverclient

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

var _ identitybroker.IDJAGIssuer = (*Client)(nil)

// IssueIDJAG implements identitybroker.IDJAGIssuer: AS-1's per-call exchange
// (design step (7)). It posts an RFC 8693 token exchange of the session's
// refresh token, with the agent's SVID as the actor token, for an ID-JAG for
// req.Audience and req.Resource, naming the call's operation in
// mecatl_operation. A success response that is not valid JSON of the expected
// shape, lacks access_token, or does not issue an ID-JAG wraps both
// identitybroker.ErrMalformedResponse and
// identitybroker.ErrTransportConfiguration; other errors are Token's.
func (c *Client) IssueIDJAG(ctx context.Context, req identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
	form := url.Values{
		"grant_type":           {grantTypeTokenExchange},
		"subject_token":        {req.SubjectToken.Value()},
		"subject_token_type":   {identitybroker.TokenTypeRefreshToken},
		"actor_token":          {req.ActorToken.Value()},
		"actor_token_type":     {identitybroker.TokenTypeJWT},
		"requested_token_type": {identitybroker.TokenTypeIDJAG},
		"audience":             {req.Audience},
		"resource":             {req.Resource},
		"mecatl_operation":     {req.OperationID},
	}
	var out struct {
		AccessToken     string `json:"access_token"`
		IssuedTokenType string `json:"issued_token_type"`
	}
	if err := c.Token(ctx, form, &out); err != nil {
		return identitybroker.IDJAGResult{}, err
	}
	if out.AccessToken == "" || out.IssuedTokenType != identitybroker.TokenTypeIDJAG {
		return identitybroker.IDJAGResult{}, fmt.Errorf("authserverclient: %w (%w): the ID-JAG exchange response lacks access_token or does not issue an ID-JAG",
			identitybroker.ErrMalformedResponse, identitybroker.ErrTransportConfiguration)
	}
	return identitybroker.IDJAGResult{IDJAG: identitybroker.NewToken(out.AccessToken)}, nil
}

// seconds is a positive expires_in as a duration, and zero otherwise.
func seconds(n int64) time.Duration {
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
