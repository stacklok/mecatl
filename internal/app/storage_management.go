package app

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func foldOperatorStorageManagement(cfg Config) (Config, error) {
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok {
		return cfg, validateStorageManagement(cfg)
	}
	configured, err := res.OperatorStorageManagement()
	if err != nil {
		return cfg, fmt.Errorf("invalid operator storage management config: %w", err)
	}
	if configured != nil {
		cfg.StorageManagementPrincipals = make([]session.Principal, 0, len(configured.Principals))
		for _, principal := range configured.Principals {
			cfg.StorageManagementPrincipals = append(cfg.StorageManagementPrincipals, session.Principal{
				Issuer: principal.Issuer, Subject: principal.Subject,
			})
		}
	}
	return cfg, validateStorageManagement(cfg)
}

func validateStorageManagement(cfg Config) error {
	if cfg.LocalStorageManagement && cfg.OwnershipEnforced {
		return fmt.Errorf("local embedded storage management cannot be combined with OIDC ownership enforcement")
	}
	for i, principal := range cfg.StorageManagementPrincipals {
		if principal.Issuer == "" || principal.Subject == "" {
			return fmt.Errorf("storage management principal %d requires issuer and subject", i)
		}
	}
	return nil
}

func storageManagementAuthorizer(cfg Config) func(context.Context) bool {
	if cfg.LocalStorageManagement && !cfg.OwnershipEnforced {
		return func(ctx context.Context) bool {
			return session.PrincipalFromContext(ctx) == nil
		}
	}
	if !cfg.OwnershipEnforced || len(cfg.StorageManagementPrincipals) == 0 {
		return nil
	}
	principals := append([]session.Principal(nil), cfg.StorageManagementPrincipals...)
	return func(ctx context.Context) bool {
		caller := session.PrincipalFromContext(ctx)
		if caller == nil {
			return false
		}
		for i := range principals {
			if principals[i].Issuer == caller.Issuer && principals[i].Subject == caller.Subject {
				return true
			}
		}
		return false
	}
}
