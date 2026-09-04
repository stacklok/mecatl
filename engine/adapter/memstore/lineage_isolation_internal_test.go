package memstore

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestReadSessionLineageExactEdgeRequiresPartitionMembership(t *testing.T) {
	st := New()
	root := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	child, err := session.NewSubagent("child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(2, 0), root.ID, root.Incarnation(), "call")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*session.Session{root, child} {
		if err := st.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	query := port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), RecordID: child.ID, RecordIncarnation: child.Incarnation(), Limit: 1}
	if got, err := st.ReadSessionLineage(t.Context(), query); err != nil || len(got.Records) != 1 {
		t.Fatalf("intact exact edge = %+v, %v", got, err)
	}

	st.mu.Lock()
	delete(st.lineageEdges[lineageRecordKey(root.ID, string(root.Incarnation()))], lineageRecordKey(child.ID, string(child.Incarnation())))
	st.mu.Unlock()
	if got, err := st.ReadSessionLineage(t.Context(), query); err != nil || len(got.Records) != 0 {
		t.Fatalf("record relationship substituted for missing edge membership: %+v, %v", got, err)
	}
}

func TestReadSessionLineageTouchesOnlyAddressedPartitions(t *testing.T) {
	st := New()
	root := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	other := session.New("unrelated", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(2, 0))
	child, err := session.NewSubagent("child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(3, 0), root.ID, root.Incarnation(), "call")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*session.Session{root, other, child} {
		if err := st.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	var reads []string
	st.lineageReadObserver = func(partition string) { reads = append(reads, partition) }
	result, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 2 {
		t.Fatalf("records=%+v", result.Records)
	}
	want := []string{"records:root", "edges:" + lineageRecordKey(root.ID, string(root.Incarnation()))}
	if len(reads) != len(want) || reads[0] != want[0] || reads[1] != want[1] {
		t.Fatalf("partition reads=%q want=%q", reads, want)
	}
}
