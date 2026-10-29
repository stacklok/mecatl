package app

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func currentTestSourceStore(t *testing.T, trajectory learning.Trajectory) *memstore.Store {
	t.Helper()
	sess := session.New(trajectory.SessionID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.SeedHistory(trajectory.Messages); err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	return store
}

func currentTestTrajectory(id session.SessionID, workspace string, stop session.StopReason, usage session.Usage, messages []session.Message) learning.Trajectory {
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.SeedHistory(messages); err != nil {
		panic(err)
	}
	return learning.NewSessionTrajectory(sess, workspace, stop, usage)
}
