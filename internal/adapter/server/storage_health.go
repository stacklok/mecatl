package server

import (
	"context"
	"errors"
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

// OwnerlessCutoverInventory is a content-free preflight for enabling caller
// ownership. Identifiers are bounded samples; counts cover the full available
// inventory. Availability is per family because custom stores may expose only
// one metadata seam.
type OwnerlessCutoverInventory struct {
	SessionsAvailable          bool
	SessionsUnavailableReason  string
	SessionCount               int
	SessionIDs                 []string
	SessionIDsTruncated        bool
	SchedulesAvailable         bool
	SchedulesUnavailableReason string
	ScheduleCount              int
	ScheduleNames              []string
	ScheduleNamesTruncated     bool
}

// StorageHealth is the authenticated, content-free management projection.
type StorageHealth struct {
	port.SessionStorageHealth
	Ownerless          OwnerlessCutoverInventory
	Policy             RetentionPolicy
	LastSweep          time.Time
	LastSweepAvailable bool
	NextSweep          time.Time
	NextSweepAvailable bool
	ActiveJob          string
	LastFailure        string
}

func implementsStorageHealth(store port.SessionStore) bool {
	if _, ok := store.(port.SessionStorageHealthProvider); ok {
		return true
	}
	_, ok := store.(port.SessionMetadataPager)
	return ok
}

const (
	ownerlessInventorySampleLimit = 100
	storageBackendUnsupported     = "backend_unsupported"
)

func ownerlessSessionInventory(ctx context.Context, pager port.SessionMetadataPager) (OwnerlessCutoverInventory, error) {
	const maxRestarts = 3
	for attempt := 0; attempt < maxRestarts; attempt++ {
		var inventory OwnerlessCutoverInventory
		var cursor *port.SessionMetadataCursor
		for {
			page, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 256, Cursor: cursor})
			if err != nil {
				if errors.Is(err, port.ErrSessionMetadataCursorRestart) {
					break
				}
				return OwnerlessCutoverInventory{}, err
			}
			for _, meta := range page.Sessions {
				if meta.Owner != nil {
					continue
				}
				inventory.SessionCount++
				if len(inventory.SessionIDs) < ownerlessInventorySampleLimit {
					inventory.SessionIDs = append(inventory.SessionIDs, string(meta.ID))
				}
			}
			if page.NextCursor == nil {
				inventory.SessionsAvailable = true
				inventory.SessionIDsTruncated = inventory.SessionCount > len(inventory.SessionIDs)
				return inventory, nil
			}
			// No-progress is a cursor that did not ADVANCE. An empty page is not
			// itself a stall: rows this walk already passed can be deleted under it
			// — retention and the child GC run concurrently — leaving a legitimate
			// empty page with an advancing cursor. Cursor KEY comparison, not struct
			// equality: SessionMetadataCursor embeds a time.Time, whose == compares
			// the monotonic reading and location pointer.
			if cursor != nil &&
				page.NextCursor.ModifiedAt.Equal(cursor.ModifiedAt) && page.NextCursor.ID == cursor.ID {
				return OwnerlessCutoverInventory{}, ErrStorageHealthBackend
			}
			next := *page.NextCursor
			cursor = &next
		}
	}
	return OwnerlessCutoverInventory{}, port.ErrSessionMetadataCursorRestart
}

func (s *Service) ownerlessCutoverInventory(ctx context.Context) (OwnerlessCutoverInventory, error) {
	var inventory OwnerlessCutoverInventory
	pager, ok := s.cfg.Store.(port.SessionMetadataPager)
	if !ok {
		inventory.SessionsUnavailableReason = storageBackendUnsupported
	} else {
		sessions, err := ownerlessSessionInventory(ctx, pager)
		if errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
			inventory.SessionsUnavailableReason = storageBackendUnsupported
		} else if err != nil {
			return OwnerlessCutoverInventory{}, err
		} else {
			inventory.SessionsAvailable = sessions.SessionsAvailable
			inventory.SessionCount = sessions.SessionCount
			inventory.SessionIDs = sessions.SessionIDs
			inventory.SessionIDsTruncated = sessions.SessionIDsTruncated
		}
	}

	if s.schedMgr == nil || s.schedMgr.schedStore == nil {
		inventory.SchedulesUnavailableReason = storageBackendUnsupported
		return inventory, nil
	}
	inventory.SchedulesAvailable = true
	schedules, err := s.schedMgr.schedStore.List(ctx)
	if errors.Is(err, port.ErrScheduleUnsupported) {
		inventory.SchedulesAvailable = false
		inventory.SchedulesUnavailableReason = storageBackendUnsupported
		return inventory, nil
	}
	if err != nil {
		return OwnerlessCutoverInventory{}, err
	}
	for _, schedule := range schedules {
		if schedule.Spec.Owner != nil {
			continue
		}
		inventory.ScheduleCount++
		if len(inventory.ScheduleNames) < ownerlessInventorySampleLimit {
			inventory.ScheduleNames = append(inventory.ScheduleNames, schedule.Spec.Name)
		}
	}
	inventory.ScheduleNamesTruncated = inventory.ScheduleCount > len(inventory.ScheduleNames)
	return inventory, nil
}

// StorageHealth returns aggregate storage measurements only after management
// authorization. Unsupported backends are represented as unavailable data,
// not measured zero and not an error.
func (s *Service) StorageHealth(ctx context.Context) (StorageHealth, error) {
	if s.cfg.StorageManagementAuthorized == nil || !s.cfg.StorageManagementAuthorized(ctx) {
		return StorageHealth{}, ErrManagementUnauthorized
	}
	status := StorageHealth{Policy: s.cfg.RetentionPolicy}
	inventory, err := s.ownerlessCutoverInventory(ctx)
	if err != nil {
		s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: "health", Key: "storage-health", State: StorageMaintenanceFailed, Failure: "health: storage backend unavailable"})
		return StorageHealth{}, ErrStorageHealthBackend
	}
	status.Ownerless = inventory
	provider, ok := s.cfg.Store.(port.SessionStorageHealthProvider)
	if !ok {
		status.UnavailableReason = storageBackendUnsupported
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
