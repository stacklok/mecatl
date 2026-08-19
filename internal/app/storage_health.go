package app

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

// storageMaintenanceState is Build-owned, process-local observability over
// retention plus server-owned durable/resumable maintenance jobs. Keys and
// failures are already sanitized before entering this state.
type storageMaintenanceState struct {
	mu     sync.RWMutex
	status server.StorageMaintenanceStatus
	active map[string]string // opaque lifecycle key -> closed job kind
}

func (s *storageMaintenanceState) beginSweep() {
	s.start("retention_sweep", "retention_sweep")
}

func (s *storageMaintenanceState) finishSweep(now time.Time, cadence time.Duration) {
	s.finishSweepAttempt(now, cadence, "", true)
}

func (s *storageMaintenanceState) failSweep(now time.Time, cadence time.Duration) {
	s.finishSweepAttempt(now, cadence, "retention sweep failed", false)
}

func (s *storageMaintenanceState) disableSweep() {
	s.settleStoppedSweep("retention sweep unavailable")
}

func (s *storageMaintenanceState) stopSweepSchedule() {
	s.settleStoppedSweep("")
}

func (s *storageMaintenanceState) settleStoppedSweep(failure string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.active, maintenanceKey("retention_sweep", "retention_sweep"))
	if failure != "" {
		s.status.LastFailure = failure
	}
	s.status.NextSweep = time.Time{}
	s.status.NextSweepAvailable = false
	s.refreshActiveLocked()
	s.mu.Unlock()
}

func (s *storageMaintenanceState) finishSweepAttempt(now time.Time, cadence time.Duration, failure string, completed bool) {
	if s == nil {
		return
	}
	s.finish("retention_sweep", "retention_sweep", failure)
	s.mu.Lock()
	if completed {
		s.status.LastSweep = now
		s.status.LastSweepAvailable = true
	}
	if cadence > 0 {
		s.status.NextSweep = now.Add(cadence)
		s.status.NextSweepAvailable = true
	} else {
		s.status.NextSweep = time.Time{}
		s.status.NextSweepAvailable = false
	}
	s.mu.Unlock()
}

func (s *storageMaintenanceState) start(kind, key string) {
	if s == nil || kind == "" || key == "" {
		return
	}
	s.mu.Lock()
	if s.active == nil {
		s.active = make(map[string]string)
	}
	s.active[maintenanceKey(kind, key)] = kind
	s.refreshActiveLocked()
	s.mu.Unlock()
}

func (s *storageMaintenanceState) finish(kind, key, failure string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.active, maintenanceKey(kind, key))
	if failure != "" {
		s.status.LastFailure = failure
	}
	s.refreshActiveLocked()
	s.mu.Unlock()
}

func maintenanceKey(kind, key string) string {
	return kind + "\x00" + key
}

func (s *storageMaintenanceState) refreshActiveLocked() {
	counts := make(map[string]int)
	for _, kind := range s.active {
		counts[kind]++
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		if counts[kind] == 1 {
			parts = append(parts, kind)
		} else {
			parts = append(parts, fmt.Sprintf("%s(%d)", kind, counts[kind]))
		}
	}
	s.status.ActiveJob = strings.Join(parts, ",")
}

func (s *storageMaintenanceState) update(event server.StorageMaintenanceEvent) {
	switch event.State {
	case server.StorageMaintenanceStarted, server.StorageMaintenanceProgress:
		s.start(event.Kind, event.Key)
	case server.StorageMaintenanceCompleted, server.StorageMaintenanceCancelled:
		s.finish(event.Kind, event.Key, event.Failure)
	case server.StorageMaintenanceFailed:
		// A failed resumable migration remains active; terminal cleanup failures do not.
		if event.Resumable {
			if s == nil || event.Kind == "" || event.Key == "" {
				return
			}
			s.mu.Lock()
			if s.active == nil {
				s.active = make(map[string]string)
			}
			s.active[maintenanceKey(event.Kind, event.Key)] = event.Kind
			s.status.LastFailure = event.Failure
			s.refreshActiveLocked()
			s.mu.Unlock()
		} else {
			s.finish(event.Kind, event.Key, event.Failure)
		}
	}
}

func (s *storageMaintenanceState) snapshot() server.StorageMaintenanceStatus {
	if s == nil {
		return server.StorageMaintenanceStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}
