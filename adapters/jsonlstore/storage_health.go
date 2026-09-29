package jsonlstore

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

var _ port.SessionStorageHealthProvider = (*Store)(nil)

// SessionStorageHealth reports content-free aggregate health from the current
// derivative inventory catalog and cheap filesystem metadata. A missing or
// stale catalog is reported unavailable; health inspection never rebuilds it.
func (st *Store) SessionStorageHealth(ctx context.Context) (port.SessionStorageHealth, error) {
	st.inventoryMu.Lock()
	defer st.inventoryMu.Unlock()

	var health port.SessionStorageHealth
	err := st.withInventoryCatalogLock(ctx, func() error {
		fingerprint, err := st.inventoryFingerprint()
		if err != nil {
			return err
		}
		catalog, ready := st.readInventoryManifest(fingerprint)
		if !ready {
			health.UnavailableReason = "metadata_index_unavailable"
			return nil
		}
		scope, ok := catalog.Scopes[inventoryGlobalScope]
		if !ok {
			health.UnavailableReason = "metadata_index_unavailable"
			return nil
		}
		rows, err := st.readInventoryScope(scope)
		if err != nil || !validInventoryRows(rows) {
			health.UnavailableReason = "metadata_index_unavailable"
			return nil
		}
		health.Available = true
		health.SessionCount = int64(len(rows))
		for range rows {
			st.observeInventoryWork(inventoryWorkCatalogRow)
		}
		for _, row := range rows {
			if row.State == "" {
				health.CorruptCount++
				continue
			}
			switch row.Kind {
			case session.SessionKindMain:
				health.MainCount++
			case session.SessionKindScheduled:
				health.ScheduledCount++
			case session.SessionKindSubagent, session.SessionKindParallelBranch, session.SessionKindTeamMember:
				health.ChildCount++
			case session.SessionKindUnknown, "":
				health.UnknownCount++
			default:
				health.CorruptCount++
			}
		}
		return st.measureStorageFiles(&health)
	})
	if err != nil {
		return port.SessionStorageHealth{}, fmt.Errorf("jsonlstore: measure storage health: %w", err)
	}
	return health, nil
}

func (st *Store) measureStorageFiles(health *port.SessionStorageHealth) error {
	for _, dir := range []string{st.resolver.dir, st.resolver.canonicalDir()} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !isSessionStorageFile(name) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			health.FileCount++
			health.CurrentBytes += info.Size()
			switch {
			case strings.HasSuffix(name, sessionFileSuffix):
				health.V1Count++
			case strings.HasSuffix(name, currentSnapshotSuffix):
				health.V2Count++
			}
		}
	}
	health.CurrentBytesAvailable = true
	// Reclaimable bytes require a generation-bound maintenance plan. No such job
	// is implemented yet, so availability remains false rather than fabricating 0.
	return nil
}

func isSessionStorageFile(name string) bool {
	return strings.HasSuffix(name, currentSnapshotSuffix) || strings.HasSuffix(name, sessionFileSuffix) ||
		strings.HasSuffix(name, toolsFileSuffix) || strings.HasSuffix(name, eventsFileSuffix)
}
