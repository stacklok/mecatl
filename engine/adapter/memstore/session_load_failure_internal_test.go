package memstore

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLoadClassifiesSnapshotRestoreFailure(t *testing.T) {
	const id = session.SessionID("corrupt")
	st := New()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "r1"}, session.Limits{}, time.Unix(1, 0))
	if err := st.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	st.mu.Lock()
	snap := st.sessions[id]
	snap.EnvironmentRef = session.EnvironmentRef{}
	st.sessions[id] = snap
	st.mu.Unlock()

	_, err := st.Load(t.Context(), id)
	if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureSnapshot {
		t.Fatalf("Load restore class = %s, want snapshot: %v", got, err)
	}
}
