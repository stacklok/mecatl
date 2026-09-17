package cliconfig

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/subcred"
)

// StoredSubscription describes a persisted sign-in without exposing its
// tokens. Account identifies the signed-in identity for display.
type StoredSubscription struct {
	Account string
}

// StoredSubscriptions reports the subscription sign-ins present on this host,
// keyed by provider id. It never returns token material.
//
// A host with no provisioned credential store, or no sign-in, yields an empty
// map rather than an error: not being signed in is a normal state that status
// output must render, not fail on.
func StoredSubscriptions(ctx context.Context) map[string]StoredSubscription {
	stored := map[string]StoredSubscription{}
	backing, _, err := clientauth.OpenExistingCredentialStore(ctx, filepath.Join(xdg.ConfigHome, "mecatl"))
	if err != nil {
		return stored
	}
	defer func() { _ = backing.Close() }()
	store, err := subcred.New(backing)
	if err != nil {
		return stored
	}
	for _, provider := range []string{subcred.ProviderAnthropic, subcred.ProviderOpenAICodex} {
		grant, loadErr := store.Load(ctx, provider)
		if loadErr != nil {
			if !errors.Is(loadErr, subcred.ErrNoGrant) {
				continue
			}
			continue
		}
		account := grant.Email
		if account == "" {
			account = grant.AccountID
		}
		stored[provider] = StoredSubscription{Account: account}
	}
	return stored
}
