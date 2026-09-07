package jsonlstore

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLoadClassifiesRetrievalAndSnapshotFailures(t *testing.T) {
	newStored := func(t *testing.T) (*Store, string) {
		t.Helper()
		st, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		sess := session.New("classified", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
		if err := st.Save(t.Context(), sess); err != nil {
			t.Fatal(err)
		}
		return st, st.resolver.currentSnapshotPath(sess.ID)
	}

	t.Run("snapshot", func(t *testing.T) {
		st, path := newStored(t)
		if err := os.WriteFile(path, []byte(`{"v":"broken"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := st.Load(t.Context(), "classified")
		if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureSnapshot {
			t.Fatalf("class = %s, want snapshot: %v", got, err)
		}
	})

	t.Run("store", func(t *testing.T) {
		st, path := newStored(t)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := st.Load(t.Context(), "classified")
		if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureStore {
			t.Fatalf("class = %s, want store: %v", got, err)
		}
		if errors.Is(err, port.ErrSessionNotFound) {
			t.Fatalf("retrieval failure became not-found: %v", err)
		}
	})
}
