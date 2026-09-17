package cliconfig

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/internal/adapter/anthropicsub"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
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
	if cfg == nil || cfg.AnthropicKey != "" {
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
	grants := subcred.AnthropicGrants{Store: store}
	if _, err := grants.Load(ctx); err != nil {
		_ = backing.Close()
		if errors.Is(err, subcred.ErrNoGrant) {
			return nil
		}
		return err
	}

	renewable, err := anthropicsub.NewRenewable(grants, nil, nil)
	if err != nil {
		_ = backing.Close()
		return err
	}
	// The store handle outlives this call: the renewer persists each rotation
	// through it for the lifetime of the process.
	cfg.AnthropicSubscription = renewable
	return nil
}
