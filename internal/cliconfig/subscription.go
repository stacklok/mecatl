package cliconfig

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/internal/adapter/anthropicsub"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/subcred"
	"github.com/stacklok/mecatl/internal/app"
)

// AttachSubscriptionCredentials binds a stored subscription sign-in to the
// config so inference can use it.
//
// It is deliberately separate from ApplyResolved: reading a grant touches the
// host credential store, while that projection is pure. A command root calls
// this once, after resolving API keys.
//
// An explicitly resolved API key wins and short-circuits the lookup, so an
// operator who supplied a key keeps that billing identity and the credential
// store is not opened at all. A missing grant is the normal unsigned-in state
// and is not an error.
func AttachSubscriptionCredentials(ctx context.Context, cfg *app.Config) error {
	if cfg == nil {
		return nil
	}
	// An API key for every subscription-capable provider means there is
	// nothing a grant could contribute, so the credential store is not opened
	// at all.
	if cfg.AnthropicKey != "" && cfg.OpenAICodexCredential.Configured() {
		return nil
	}
	root := filepath.Join(xdg.ConfigHome, "mecatl")

	// Only an already-provisioned store is opened. Attaching a credential must
	// never pin a custody backend or create key material as a side effect of
	// starting up; that decision belongs to an explicit login.
	backing, _, err := clientauth.OpenExistingCredentialStore(ctx, root)
	if err != nil {
		// No provisioned store means nobody has signed in on this host.
		return nil
	}
	store, err := subcred.New(backing)
	if err != nil {
		_ = backing.Close()
		return err
	}

	// The store handle outlives this call: each renewer persists its rotations
	// through it for the lifetime of the process.
	attached := false
	if cfg.AnthropicKey == "" {
		grants := subcred.AnthropicGrants{Store: store}
		if _, loadErr := grants.Load(ctx); loadErr == nil {
			renewable, newErr := anthropicsub.NewRenewable(grants, nil, nil)
			if newErr != nil {
				_ = backing.Close()
				return newErr
			}
			cfg.AnthropicSubscription = renewable
			attached = true
		} else if !errors.Is(loadErr, subcred.ErrNoGrant) {
			_ = backing.Close()
			return loadErr
		}
	}
	if !cfg.OpenAICodexCredential.Configured() {
		grants := subcred.CodexGrants{Store: store}
		if _, loadErr := grants.Load(ctx); loadErr == nil {
			renewable, newErr := openaicodex.NewRenewableCredential(grants, nil, nil)
			if newErr != nil {
				_ = backing.Close()
				return newErr
			}
			cfg.OpenAICodexSubscription = renewable
			attached = true
		} else if !errors.Is(loadErr, subcred.ErrNoGrant) {
			_ = backing.Close()
			return loadErr
		}
	}
	if !attached {
		_ = backing.Close()
	}
	return nil
}

// SubscriptionShadowedBy reports the credential that will be used for a
// subscription-capable provider when a sign-in is also stored, or an empty
// string when the sign-in is what takes effect.
//
// It exists so a login is never silently inert: an operator who exported an
// API key otherwise signs in successfully and observes no change.
func SubscriptionShadowedBy(keys ResolvedCredentials, provider string) string {
	switch provider {
	case subcred.ProviderAnthropic:
		if keys.Anthropic != "" {
			return "an Anthropic API key"
		}
	case subcred.ProviderOpenAICodex:
		if keys.HasOpenAICodex() {
			return "a manual Codex token in auth.yaml"
		}
	}
	return ""
}
