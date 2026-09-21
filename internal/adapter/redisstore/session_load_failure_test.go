package redisstore_test

import (
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

func TestLoadClassifiesRetrievalAndSnapshotFailures(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sess := newTestSession(t, "classified")
	if err := st.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	mr.HSet("mecatl:store:v2:session:classified", "blob", "not-a-snapshot")
	_, err = st.Load(t.Context(), sess.ID)
	if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureSnapshot {
		t.Fatalf("decode class = %s, want snapshot: %v", got, err)
	}

	other := newTestSession(t, "different")
	blob, err := sessnap.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	mr.HSet("mecatl:store:v2:session:classified", "blob", string(blob))
	_, err = st.Load(t.Context(), sess.ID)
	if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureSnapshot {
		t.Fatalf("identity mismatch class = %s, want snapshot: %v", got, err)
	}

	mr.Close()
	_, err = st.Load(t.Context(), sess.ID)
	if got := port.ClassifySessionLoadFailure(err); got != port.SessionLoadFailureStore {
		t.Fatalf("transport class = %s, want store: %v", got, err)
	}
}
