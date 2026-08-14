package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

// OAuthLoginPresenter adapts the host loopback callback to the official SDK
// authorization result. Validation remains owned by the loopback runtime and
// the SDK/controller; this bridge only converts their value types.
func OAuthLoginPresenter(present func(context.Context, string) (oauthlogin.Result, error)) OAuthPresenter {
	return OAuthPresenterFunc(func(ctx context.Context, authorizationURL string) (*auth.AuthorizationResult, error) {
		result, err := present(ctx, authorizationURL)
		if err != nil {
			return nil, err
		}
		return &auth.AuthorizationResult{
			Code:  result.Code,
			State: result.State,
			Iss:   result.Iss,
		}, nil
	})
}
