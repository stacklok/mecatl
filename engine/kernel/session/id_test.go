package session_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	resourceid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

func TestSessionIDAliasAndResourceKey(t *testing.T) {
	id := session.NewSessionID()
	direct, err := sessionid.ParseSessionID(id.String())
	mustBatch(t, err)
	parsed, err := session.ParseSessionID(direct.String())
	mustBatch(t, err)
	generic, err := resourceid.Parse(id.String())
	mustBatch(t, err)
	if parsed != id || id.ResourceID() != generic || generic.Kind() != "SES" {
		t.Fatal("typed Session identity changed during conversion")
	}
	if (session.SessionID{}).ResourceID() != (resourceid.ID{}) {
		t.Fatal("zero conversion must stay zero")
	}

	store := mem.New()
	h := newLifecycleHistory(t, store)
	s, err := session.New(h, id, testConfig())
	mustBatch(t, err)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	input := batchInput(t, b, run, []byte("retained input"))
	usage, err := b.AddUsage(&run, nil, "fixture", session.UsageReport{State: session.UsageReportActive})
	mustBatch(t, err)
	proposed := captureCommit(t, b)
	if proposed.SessionID() != id || run.SessionID() != id || input.RunID().SessionID() != id || usage.SessionID() != id {
		t.Fatal("authoring lost typed Session scope")
	}
	head := submitBatch(t, s, b)
	observation, err := store.Observe(t.Context(), generic, 1)
	mustBatch(t, err)
	if observation.Head != head || observation.Commit != head {
		t.Fatal("Session was not stored under the same generic resource key")
	}
	page, err := h.Read(t.Context(), generic, history.ReadRequest{Through: head, MaxCommits: 1})
	mustBatch(t, err)
	if len(page.Commits) != 1 || page.Commits[0].Commit.HistoryID == nil || *page.Commits[0].Commit.HistoryID != generic {
		t.Fatal("root envelope allocated a different identity")
	}
	loaded, err := session.Load(t.Context(), h, direct, testConfig())
	mustBatch(t, err)
	for _, view := range []session.Snapshot{s.Snapshot(), loaded.Snapshot()} {
		created, ok := view.RecordAt(0).Created()
		if !ok || created.SessionID() != id || view.SessionID() != id || view.Head() != head || view.RunAt(0).ID() != run {
			t.Fatal("accepted/reloaded views lost Session identity")
		}
		gotInput, ok := view.RecordAt(2).UserInput()
		if !ok || gotInput.ID() != input || gotInput.ID().RunID().SessionID() != id {
			t.Fatal("accepted/reloaded input lost scoped identity")
		}
		gotUsage, ok := view.RecordAt(3).Usage()
		if !ok || gotUsage.ID() != usage || gotUsage.ID().SessionID() != id {
			t.Fatal("accepted/reloaded accounting lost scoped identity")
		}
	}
}
