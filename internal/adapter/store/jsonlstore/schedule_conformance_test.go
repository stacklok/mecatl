package jsonlstore_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/scheduleconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestJSONLStoreScheduleConformance runs the shared ScheduleStore conformance
// table against the JSONL-backed schedule store (the schedule-store half of
// the dual-path contract: the same suite the in-memory reference
// memschedulestore passes). The store shares the session store's dir + mutex;
// the factory wires a fresh temp dir per subtest so the at-most-once proof is
// isolated. The port methods take `now` as an explicit argument, so the suite
// controls time directly (no injected clock to move).
func TestJSONLStoreScheduleConformance(t *testing.T) {
	scheduleconformance.Run(t, func(*testing.T) port.ScheduleStore {
		st, err := jsonlstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		return st.ScheduleStore()
	})
}
