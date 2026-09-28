package subcred

import (
	"context"

	"github.com/stacklok/mecatl/internal/adapter/anthropicsub"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
)

// Provider identifiers under which grants are stored. They match the provider
// ids the rest of the host uses, so a grant is discoverable by provider name.
const (
	ProviderAnthropic   = "anthropic"
	ProviderOpenAICodex = "openai-codex"
)

// AnthropicGrants adapts the shared store to the Anthropic grant shape.
type AnthropicGrants struct{ Store *Store }

var _ anthropicsub.GrantStore = AnthropicGrants{}

// Load implements anthropicsub.GrantStore.
func (a AnthropicGrants) Load(ctx context.Context) (anthropicsub.OAuthTokens, error) {
	grant, err := a.Store.Load(ctx, ProviderAnthropic)
	if err != nil {
		return anthropicsub.OAuthTokens{}, err
	}
	return anthropicsub.OAuthTokens{
		AccessToken:  grant.AccessToken,
		RefreshToken: grant.RefreshToken,
		ExpiresAt:    grant.ExpiresAt,
		AuthorizedAt: grant.AuthorizedAt,
		AccountID:    grant.AccountID,
		Email:        grant.Email,
		OrgID:        grant.OrgID,
		OrgName:      grant.OrgName,
	}, nil
}

// Save implements anthropicsub.GrantStore.
func (a AnthropicGrants) Save(ctx context.Context, tokens anthropicsub.OAuthTokens) error {
	return a.Store.Save(ctx, FromAnthropic(tokens))
}

// FromAnthropic projects an Anthropic grant into the persisted shape.
func FromAnthropic(tokens anthropicsub.OAuthTokens) Grant {
	return Grant{
		Provider:     ProviderAnthropic,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
		AuthorizedAt: tokens.AuthorizedAt,
		AccountID:    tokens.AccountID,
		Email:        tokens.Email,
		OrgID:        tokens.OrgID,
		OrgName:      tokens.OrgName,
	}
}

// CodexGrants adapts the shared store to the Codex grant shape.
type CodexGrants struct{ Store *Store }

var _ openaicodex.TokenStore = CodexGrants{}

// Load implements openaicodex.TokenStore.
func (c CodexGrants) Load(ctx context.Context) (openaicodex.OAuthTokens, error) {
	grant, err := c.Store.Load(ctx, ProviderOpenAICodex)
	if err != nil {
		return openaicodex.OAuthTokens{}, err
	}
	return openaicodex.OAuthTokens{
		AccessToken:  grant.AccessToken,
		RefreshToken: grant.RefreshToken,
		AccountID:    grant.AccountID,
		ExpiresAt:    grant.ExpiresAt,
	}, nil
}

// Save implements openaicodex.TokenStore.
func (c CodexGrants) Save(ctx context.Context, tokens openaicodex.OAuthTokens) error {
	return c.Store.Save(ctx, FromCodex(tokens))
}

// FromCodex projects a Codex grant into the persisted shape.
func FromCodex(tokens openaicodex.OAuthTokens) Grant {
	return Grant{
		Provider:     ProviderOpenAICodex,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
		AccountID:    tokens.AccountID,
	}
}
