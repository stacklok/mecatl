package redisstore

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func lineageHashHas(t *testing.T, st *Store, hash, field string) bool {
	t.Helper()
	found, err := st.testClient().HExists(t.Context(), hash, field).Result()
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestLineageIndexCorruptionFailsLoud(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	incarnation := session.NewIncarnationID()
	partition := redisLineageRecordPartition("root")
	if err := st.testClient().HSet(t.Context(), partition, redisLineageKey("root", string(incarnation)), "not-json").Err(); err != nil {
		t.Fatal(err)
	}
	mr.ZAdd(redisLineageOrderPartition(partition), 0, redisLineageOrderMember(port.SessionLineageRecord{ID: "root", Incarnation: string(incarnation), State: port.SessionLineageRetained}, false))
	if _, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: "root", RootIncarnation: incarnation, Limit: 1}); err == nil {
		t.Fatal("ReadSessionLineage accepted a corrupt index")
	}
}

func TestReadSessionLineageDoesNotReadUnrelatedPartitions(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	if err := st.Save(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	if err := st.testClient().HSet(t.Context(), redisLineageRecordPartition("unrelated"), "bad", "not-json").Err(); err != nil {
		t.Fatal(err)
	}
	result, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || result.Records[0].ID != root.ID {
		t.Fatalf("records=%+v", result.Records)
	}
}

func TestOldLineageNamespaceIsIgnoredUntouched(t *testing.T) {
	mr := miniredis.RunT(t)
	const oldKey = "mecatl:session-lineage:v1"
	mr.HSet(oldKey, "poison", `{"bad":"old"}`)
	before := mr.HGet(oldKey, "poison")
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	child, err := session.NewSubagent("child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(2, 0), root.ID, root.Incarnation(), "call")
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*session.Session{root, child} {
		if err := st.Save(t.Context(), current); err != nil {
			t.Fatal(err)
		}
	}
	query := port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10}
	result, err := st.ReadSessionLineage(t.Context(), query)
	if err != nil || len(result.Records) != 2 || result.Records[0].ID != root.ID || result.Records[1].ID != child.ID {
		t.Fatalf("current root and direct child=%+v err=%v", result.Records, err)
	}
	if got := mr.HGet(oldKey, "poison"); got != before {
		t.Fatalf("old lineage changed: got %q want %q", got, before)
	}
}

func TestTargetedSaveRemovesOldReparentEdgeAndRetiresOldIncarnation(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	env := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}
	parents := []*session.Session{
		session.New("parent-a", session.ModeDefault, env, session.Limits{}, time.Unix(1, 0)),
		session.New("parent-b", session.ModeDefault, env, session.Limits{}, time.Unix(2, 0)),
	}
	child, err := session.NewSubagent("child", session.ModeDefault, env, session.Limits{}, time.Unix(3, 0), parents[0].ID, parents[0].Incarnation(), "call")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append(parents, child) {
		if err := st.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	oldIncarnation := child.Incarnation()
	child.Relationship.ParentSessionID = parents[1].ID
	child.Relationship.ParentIncarnation = parents[1].Incarnation()
	if err := st.Save(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	field := redisLineageKey(child.ID, string(oldIncarnation))
	if lineageHashHas(t, st, redisLineageEdgePartition(parents[0].ID, parents[0].Incarnation()), field) {
		t.Fatal("reparent retained the old direct edge")
	}
	if !lineageHashHas(t, st, redisLineageEdgePartition(parents[1].ID, parents[1].Incarnation()), field) {
		t.Fatal("reparent did not materialize the new direct edge")
	}

	if err := st.Delete(t.Context(), child.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := session.NewSubagent(child.ID, session.ModeDefault, env, session.Limits{}, time.Unix(4, 0), parents[0].ID, parents[0].Incarnation(), "replacement-call")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	oldQuery := port.SessionLineageQuery{RootID: parents[1].ID, RootIncarnation: parents[1].Incarnation(), RecordID: child.ID, RecordIncarnation: oldIncarnation, Limit: 1}
	old, err := st.ReadSessionLineage(t.Context(), oldQuery)
	if err != nil || len(old.Records) != 1 || old.Records[0].State != port.SessionLineagePruned {
		t.Fatalf("old incarnation edge was not retired: %+v, %v", old, err)
	}
	newQuery := port.SessionLineageQuery{RootID: parents[0].ID, RootIncarnation: parents[0].Incarnation(), RecordID: replacement.ID, RecordIncarnation: replacement.Incarnation(), Limit: 1}
	current, err := st.ReadSessionLineage(t.Context(), newQuery)
	if err != nil || len(current.Records) != 1 || current.Records[0].State != port.SessionLineageRetained {
		t.Fatalf("replacement incarnation edge missing: %+v, %v", current, err)
	}
}
