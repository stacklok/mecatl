package jsonlstore

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionStorageContinuity_Scenario6_HealthIsBoundedAndHonest(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, s := range []*session.Session{
		session.New("main", session.ModeDefault, "/secret/main", session.Limits{}, time.Unix(1, 0)),
	} {
		if err := st.Save(context.Background(), s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	if _, err := st.PageSessionMetadata(context.Background(), port.SessionMetadataPageRequest{Limit: 1}); err != nil {
		t.Fatalf("build catalog: %v", err)
	}

	work := map[inventoryWorkKind]int{}
	st.inventoryWorkObserver = func(kind inventoryWorkKind) { work[kind]++ }
	health, err := st.SessionStorageHealth(context.Background())
	if err != nil {
		t.Fatalf("SessionStorageHealth: %v", err)
	}
	if !health.Available || !health.CurrentBytesAvailable || health.CurrentBytes == 0 {
		t.Fatalf("current bytes = %+v, want available non-zero measurement", health)
	}
	if health.SessionCount != 1 || health.V2Count != 1 || health.MainCount != 1 || health.FileCount < 1 {
		t.Fatalf("health counts = %+v, want one v2 main session and at least one file", health)
	}
	if health.ReclaimableBytesAvailable {
		t.Fatalf("reclaimable bytes unexpectedly claimed available without a maintenance plan: %+v", health)
	}
	if work[inventoryWorkSnapshotRead] != 0 || work[inventoryWorkRebuild] != 0 {
		t.Fatalf("health traversed authoritative snapshots: work=%v", work)
	}
}
