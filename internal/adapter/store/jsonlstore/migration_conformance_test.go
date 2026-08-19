package jsonlstore

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/storeconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestJSONLStoreSessionMigrationConformance runs the shared
// port.SessionMigrationStore conformance table (acquisition-gated mutation,
// cross-acquisition exclusion, ownership rejection, job checkpoint
// round-tripping) against the JSONL replay store, using the existing
// seedMigrationV1 helper (migration_crash_internal_test.go) to seed a v1
// migratable candidate.
func TestJSONLStoreSessionMigrationConformance(t *testing.T) {
	storeconformance.RunSessionMigration(t,
		func(t *testing.T) port.SessionStore {
			st, err := New(t.TempDir())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return st
		},
		func(t *testing.T, st port.SessionStore) session.SessionID {
			id := session.SessionID("conformance-legacy")
			seedMigrationV1(t, st.(*Store), id)
			return id
		},
	)
}
