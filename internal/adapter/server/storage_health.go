package server

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

// RetentionPolicy is the effective process-wide session retention policy. Zero
// values disable their respective limit or cadence.
type RetentionPolicy struct {
	// Version changes whenever the effective policy semantics change. When empty,
	// the server derives a stable version from the scalar limits.
	Version           string
	MainMaxAge        time.Duration
	MainMaxCount      int
	ChildMaxAge       time.Duration
	ChildMaxCount     int
	ScheduledMaxAge   time.Duration
	ScheduledMaxCount int
	SweepCadence      time.Duration
}

// StorageMaintenanceStatus is the content-free lifecycle of retention,
// migration, and cleanup. ActiveJob is a deterministic comma-separated set of
// closed job kinds (with counts for concurrent same-kind jobs).
type StorageMaintenanceStatus struct {
	LastSweep, NextSweep                   time.Time
	LastSweepAvailable, NextSweepAvailable bool
	ActiveJob, LastFailure                 string
}

// StorageMaintenanceState is a closed lifecycle vocabulary shared with the
// composition-owned health projection.
type StorageMaintenanceState string

const (
	// StorageMaintenanceStarted marks an active execution or durable reattachment.
	StorageMaintenanceStarted StorageMaintenanceState = "started"
	// StorageMaintenanceProgress refreshes an active job after a checkpoint.
	StorageMaintenanceProgress StorageMaintenanceState = "progress"
	// StorageMaintenanceCompleted removes a successfully terminal job.
	StorageMaintenanceCompleted StorageMaintenanceState = "completed"
	// StorageMaintenanceCancelled removes a forward-cancelled job.
	StorageMaintenanceCancelled StorageMaintenanceState = "cancelled"
	// StorageMaintenanceFailed records a sanitized failure; Resumable controls activity.
	StorageMaintenanceFailed StorageMaintenanceState = "failed"
)

// StorageMaintenanceEvent contains only closed kinds, opaque internal keys, and
// stable sanitized failures. It must never carry backend errors or paths.
type StorageMaintenanceEvent struct {
	Kind, Key string
	State     StorageMaintenanceState
	Failure   string
	Resumable bool
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
		s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: "health", Key: "storage-health", State: StorageMaintenanceFailed, Failure: "health: storage backend unavailable"})
		return StorageHealth{}, ErrStorageHealthBackend
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
