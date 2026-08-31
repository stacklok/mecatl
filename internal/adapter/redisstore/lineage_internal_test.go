package redisstore

import (
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLineageIndexCorruptionFailsLoud(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.testClient().HSet(t.Context(), lineageHashKey, "root", "not-json").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: "root", RootIncarnation: session.NewIncarnationID(), Limit: 1}); err == nil {
		t.Fatal("ReadSessionLineage accepted a corrupt index")
	}
}
