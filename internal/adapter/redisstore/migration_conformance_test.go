package redisstore

import (
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/adapter/storeconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestRedisStoreSessionMigrationConformance runs the shared
// port.SessionMigrationStore conformance table (acquisition-gated mutation,
// cross-acquisition exclusion, ownership rejection, job checkpoint
// round-tripping) against the Redis-backed store.
//
// The legacy row is seeded directly into miniredis (no metadata_entry field)
// BEFORE the store is constructed, not inside the seed hook: New scans for
// existing session keys at open time to decide whether the metadata index
// starts "ready" (an empty store, nothing to migrate) or "stale" (legacy
// data present); seeding after construction would leave the index wrongly
// marked ready, and InspectSessionMigration would then report
// Available=false. jsonlstore has no such construction-time state and seeds
// in its own seed hook instead (migration_conformance_test.go) — this
// asymmetry is a genuine adapter difference, not a suite flaw.
func TestRedisStoreSessionMigrationConformance(t *testing.T) {
	const id session.SessionID = "conformance-legacy"
	sess := session.New(id, session.ModeAccept, "/work", session.Limits{}, time.Unix(1, 0))
	blob, err := sessnap.Marshal(sess)
	if err != nil {
		t.Fatalf("sessnap.Marshal: %v", err)
	}

	newStore := func(t *testing.T) port.SessionStore {
		t.Helper()
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatalf("miniredis: %v", err)
		}
		t.Cleanup(mr.Close)
		mr.HSet(sessionKey(id), fieldBlob, string(blob), fieldMtime, "1")
		st, err := New(mr.Addr())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
	seed := func(_ *testing.T, _ port.SessionStore) session.SessionID {
		return id
	}
	storeconformance.RunSessionMigration(t, newStore, seed)
}
