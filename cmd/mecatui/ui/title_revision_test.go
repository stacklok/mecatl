package ui

import (
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestViewLeavesWindowTitleEmpty(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	if got := m.View().WindowTitle; got != "" {
		t.Fatalf("View().WindowTitle = %q, want empty: title output belongs to the controller", got)
	}
}

func TestTitleRevisionSnapshotRejectsDelayedLiveEvent(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	m, _, _ = m.onResolvedModelMsg(client.ResolvedModelMsg{
		SessionID: "active", Title: "snapshot", TitleProvenance: "operator", TitleRevision: 3,
	})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "delayed event", Provenance: "generated", Revision: 2})
	if m.sessionTitle != "snapshot" || m.sessionTitleRevision != 3 {
		t.Fatalf("title/revision = %q/%d, want snapshot/3", m.sessionTitle, m.sessionTitleRevision)
	}
}

func TestTitleRevisionSuppressesDuplicateAndLower(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "new", Provenance: "operator", Revision: 4})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "duplicate", Provenance: "generated", Revision: 4})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "lower", Provenance: "generated", Revision: 3})
	if m.sessionTitle != "new" || m.sessionTitleProvenance != "operator" || m.sessionTitleRevision != 4 {
		t.Fatalf("title/provenance/revision = %q/%q/%d, want new/operator/4", m.sessionTitle, m.sessionTitleProvenance, m.sessionTitleRevision)
	}
}

func TestTitleRevisionAcceptsLegacyOnlyBeforePositive(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "legacy", Provenance: "generated"})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "current", Provenance: "operator", Revision: 2})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "late legacy", Provenance: "generated"})
	if m.sessionTitle != "current" || m.sessionTitleProvenance != "operator" || m.sessionTitleRevision != 2 {
		t.Fatalf("title/provenance/revision = %q/%q/%d, want current/operator/2", m.sessionTitle, m.sessionTitleProvenance, m.sessionTitleRevision)
	}
}

func TestTitleRevisionRejectsRenameCompletionOlderThanLiveUpdate(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	mm, _ := m.renameTitle("optimistic")
	m = mm.(Model)
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "live", Provenance: "generated", Revision: 5})
	m, cmd := m.onTitleRenamed(client.SessionRenamedMsg{
		SessionID: "active", RequestToken: 1, Title: "rename", TitleProvenance: "operator", TitleRevision: 4,
	})
	if cmd != nil || m.sessionTitle != "live" || m.sessionTitleProvenance != "generated" || m.sessionTitleRevision != 5 {
		t.Fatalf("title/provenance/revision = %q/%q/%d, want live/generated/5", m.sessionTitle, m.sessionTitleProvenance, m.sessionTitleRevision)
	}
}
