package server

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

// RetentionPolicy is the effective process-wide session retention policy. Zero
// values disable their respective limit or cadence.
type RetentionPolicy struct {
	MainMaxAge        time.Duration
	MainMaxCount      int
	ChildMaxAge       time.Duration
	ChildMaxCount     int
	ScheduledMaxAge   time.Duration
	ScheduledMaxCount int
	SweepCadence      time.Duration
}

// StorageMaintenanceStatus is the content-free lifecycle of implemented
// automatic retention sweeps. Empty fields are honest placeholders for job
// systems and failure persistence that are not implemented yet.
type StorageMaintenanceStatus struct {
	LastSweep, NextSweep                   time.Time
	LastSweepAvailable, NextSweepAvailable bool
	ActiveJob, LastFailure                 string
}

// StorageHealth is the authenticated, content-free management projection.
type StorageHealth struct {
	port.SessionStorageHealth
	Policy             RetentionPolicy
	LastSweep          time.Time
	LastSweepAvailable bool
	NextSweep          time.Time
	NextSweepAvailable bool
	ActiveJob          string
	LastFailure        string
}

func implementsStorageHealth(store port.SessionStore) bool {
	_, ok := store.(port.SessionStorageHealthProvider)
	return ok
}

// StorageHealth returns aggregate storage measurements only after management
// authorization. Unsupported backends are represented as unavailable data,
// not measured zero and not an error.
func (s *Service) StorageHealth(ctx context.Context) (StorageHealth, error) {
	if s.cfg.StorageManagementAuthorized == nil || !s.cfg.StorageManagementAuthorized(ctx) {
		return StorageHealth{}, ErrManagementUnauthorized
	}
	status := StorageHealth{Policy: s.cfg.RetentionPolicy}
	provider, ok := s.cfg.Store.(port.SessionStorageHealthProvider)
	if !ok {
		status.UnavailableReason = "backend_unsupported"
		return status, nil
	}
	health, err := provider.SessionStorageHealth(ctx)
	if err != nil {
		return StorageHealth{}, err
	}
	status.SessionStorageHealth = health
	if s.cfg.StorageMaintenanceStatus != nil {
		maintenance := s.cfg.StorageMaintenanceStatus()
		status.LastSweep, status.LastSweepAvailable = maintenance.LastSweep, maintenance.LastSweepAvailable
		status.NextSweep, status.NextSweepAvailable = maintenance.NextSweep, maintenance.NextSweepAvailable
		status.ActiveJob, status.LastFailure = maintenance.ActiveJob, maintenance.LastFailure
	}
	return status, nil
}
