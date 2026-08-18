package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/stacklok/mecatl/engine/session"
)

const localStorageManagerKey = "local-embedded-operator"

// storageManagementPrincipalKey authorizes first, then derives the opaque job
// binding. The sole principal-less case is the explicitly wired local embedded
// server; remote deployments must carry an exact verified principal.
func (s *Service) storageManagementPrincipalKey(ctx context.Context) (*session.Principal, string, error) {
	if s.cfg.StorageManagementAuthorized == nil || !s.cfg.StorageManagementAuthorized(ctx) {
		return nil, "", ErrManagementUnauthorized
	}
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		if s.cfg.OwnershipEnforced {
			return nil, "", ErrManagementUnauthorized
		}
		return nil, localStorageManagerKey, nil
	}
	sum := sha256.Sum256([]byte(principal.Issuer + "\x00" + principal.Subject))
	return principal, hex.EncodeToString(sum[:16]), nil
}

func (s *Service) storageMaintenanceUpdate(event StorageMaintenanceEvent) {
	if s.cfg.StorageMaintenanceUpdate != nil {
		s.cfg.StorageMaintenanceUpdate(event)
	}
}
