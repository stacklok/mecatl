package app

import (
	"sync"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

// storageMaintenanceState is Build-owned, process-local observability for the
// already-existing retention sweeper. It contains no session identifiers.
type storageMaintenanceState struct {
	mu     sync.RWMutex
	status server.StorageMaintenanceStatus
}

func (s *storageMaintenanceState) beginSweep() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.status.ActiveJob = "retention_sweep"
	s.mu.Unlock()
}

func (s *storageMaintenanceState) finishSweep(now time.Time, cadence time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.status.ActiveJob = ""
	s.status.LastSweep = now
	s.status.LastSweepAvailable = true
	if cadence > 0 {
		s.status.NextSweep = now.Add(cadence)
		s.status.NextSweepAvailable = true
	} else {
		s.status.NextSweep = time.Time{}
		s.status.NextSweepAvailable = false
	}
	s.mu.Unlock()
}

func (s *storageMaintenanceState) snapshot() server.StorageMaintenanceStatus {
	if s == nil {
		return server.StorageMaintenanceStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}
