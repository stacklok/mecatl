package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const localStorageManagerKey = "local-embedded-operator"
const maintenancePlanLeaseSuffix = "-maintenance-plan-probe"

// storageManagementPrincipalKey authorizes first, then derives the opaque job
// binding. The sole principal-less case is the explicitly wired local embedded
// server; remote deployments must carry an exact verified principal.
func (s *Service) storageManagementPrincipalKey(ctx context.Context) (string, error) {
	if s.cfg.StorageManagementAuthorized == nil || !s.cfg.StorageManagementAuthorized(ctx) {
		return "", ErrManagementUnauthorized
	}
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		if s.cfg.OwnershipEnforced {
			return "", ErrManagementUnauthorized
		}
		return localStorageManagerKey, nil
	}
	sum := sha256.Sum256([]byte(principal.Issuer + "\x00" + principal.Subject))
	return hex.EncodeToString(sum[:16]), nil
}

// maintenanceMutationAvailable is the capability truth for destructive storage
// maintenance. A genuinely process-private store may rely on its process-local
// liveness registry and family locks; every shareable store requires a configured
// lease backend that has not reported unsupported. Authorization is orthogonal.
func (s *Service) maintenanceMutationAvailable() bool {
	if s.cfg.LocalStorageMaintenanceSingleWriter {
		return true
	}
	if s.cfg.SessionLease == nil {
		return false
	}
	s.mu.Lock()
	disabled := s.leaseDisabled
	s.mu.Unlock()
	return !disabled
}

// MaintenanceMutationAvailable reports whether destructive maintenance can
// currently obtain the required process/cross-process exclusion. Composition
// consults it before starting automatic retention and again before each sweep.
func (s *Service) MaintenanceMutationAvailable() bool {
	return s.maintenanceMutationAvailable()
}

// probeMaintenanceMutationLease reports whether id is leased at this instant.
// It is used only by read-only cleanup planning. A shareable store has no lease
// inspection operation, so the fallback is a bounded, sequential trial acquire
// followed immediately by a cancel-detached bounded release. The result is an
// instant-in-time classification; apply still reacquires and revalidates.
func (s *Service) probeMaintenanceMutationLease(ctx context.Context, id session.SessionID) (bool, error) {
	if s.cfg.LocalStorageMaintenanceSingleWriter {
		return false, nil
	}
	if !s.maintenanceMutationAvailable() {
		return false, ErrMaintenanceExclusionUnavailable
	}
	s.mu.Lock()
	_, selfHeld := s.heldLeases[id]
	s.mu.Unlock()
	if selfHeld {
		return true, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, leaseAcquireTimeout)
	lease, err := s.cfg.SessionLease.Acquire(probeCtx, id, s.cfg.LeaseOwner+maintenancePlanLeaseSuffix)
	cancel()
	switch {
	case errors.Is(err, port.ErrLeaseHeld):
		return true, nil
	case errors.Is(err, port.ErrLeaseUnsupported):
		s.mu.Lock()
		s.leaseDisabled = true
		s.mu.Unlock()
		return false, ErrMaintenanceExclusionUnavailable
	case err != nil:
		return false, err
	}
	releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), leaseAcquireTimeout)
	err = s.cfg.SessionLease.Release(releaseCtx, lease)
	releaseCancel()
	if err != nil {
		return false, err
	}
	return false, nil
}

// acquireMaintenanceMutationLease obtains the exclusion required for one full
// family rewrite/deletion. The caller holds runEntryMu and defers the returned
// release until the backend mutation has completed.
func (s *Service) acquireMaintenanceMutationLease(ctx context.Context, id session.SessionID) (func(), error) {
	if !s.maintenanceMutationAvailable() {
		return func() {}, ErrMaintenanceExclusionUnavailable
	}
	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return func() {}, err
	}
	if s.cfg.LocalStorageMaintenanceSingleWriter {
		return release, nil
	}
	s.mu.Lock()
	_, held := s.heldLeases[id]
	disabled := s.leaseDisabled
	s.mu.Unlock()
	if !held || disabled {
		release()
		return func() {}, ErrMaintenanceExclusionUnavailable
	}
	return release, nil
}

func (s *Service) storageMaintenanceUpdate(event StorageMaintenanceEvent) {
	if s.cfg.StorageMaintenanceUpdate != nil {
		s.cfg.StorageMaintenanceUpdate(event)
	}
}
