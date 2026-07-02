package redisstore_test

import (
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/scheduleconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// TestRedisScheduleStoreConformance runs the shared ScheduleStore conformance
// table against the Redis-backed schedule store over an in-process miniredis
// (fully offline). The store shares the session store's *redis.Client; the
// factory wires a fresh miniredis per subtest so the at-most-once proof is
// isolated. The port methods take `now` as an explicit argument, so the suite
// controls time directly (no injected clock to move).
func TestRedisScheduleStoreConformance(t *testing.T) {
	scheduleconformance.Run(t, func(*testing.T) port.ScheduleStore {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatalf("miniredis: %v", err)
		}
		t.Cleanup(mr.Close)
		st, err := redisstore.New(mr.Addr())
		if err != nil {
			t.Fatalf("redisstore.New: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st.ScheduleStore()
	})
}
