package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestDraftAwareSessionInventory_Scenario3_DraftActionsRemainExplicitAndNonDestructive(t *testing.T) {
	draft := client.SessionListItem{
		ID: "draft", Kind: client.SessionKindMain, UsageState: client.SessionActivityDraft,
		Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Delete: true},
	}
	st := sessionsState{tab: tabDrafts, activityInventory: true, sessions: []client.SessionListItem{draft}, filtered: []client.SessionListItem{draft}, actionID: draft.ID}
	if !st.filtered[0].Capabilities.PublicChat || !st.filtered[0].Capabilities.Delete {
		t.Fatal("draft lost its explicit continue/delete actions")
	}
	if _, _, _ = st.handleDeleted(client.SessionDeletedMsg{SessionID: "other"}); len(st.sessions) != 1 {
		t.Fatal("an unrelated action mutated the draft inventory")
	}
}

func TestDraftAwareSessionInventory_Scenario3_LocalDraftGroupingRenderingAndPaging(t *testing.T) {
	rows := []client.SessionListItem{
		{ID: "active", Kind: client.SessionKindMain, UsageState: client.SessionActivityActive},
		{ID: "draft", Kind: client.SessionKindMain, UsageState: client.SessionActivityDraft},
		{ID: "unknown", Kind: client.SessionKindMain},
		{ID: "scheduled", Kind: client.SessionKindScheduled},
		{ID: "child", Kind: client.SessionKindSubagent},
		{ID: "other", Kind: client.SessionKindUnknown},
	}
	if got := filterSessionsByTabWithActivity(rows, tabChats, true); len(got) != 2 || got[0].ID != "active" || got[1].ID != "unknown" {
		t.Fatalf("feature Chats = %+v", got)
	}
	if got := filterSessionsByTabWithActivity(rows, tabScheduledRuns, true); len(got) != 1 || got[0].ID != "scheduled" {
		t.Fatalf("scheduled grouping = %+v", got)
	}
	if got := filterSessionsByTabWithActivity(rows, tabChildRuns, true); len(got) != 1 || got[0].ID != "child" {
		t.Fatalf("child grouping = %+v", got)
	}
	if got := filterSessionsByTabWithActivity(rows, tabOtherRuns, true); len(got) != 1 || got[0].ID != "other" {
		t.Fatalf("other grouping = %+v", got)
	}
	if got := filterSessionsByTabWithActivity(rows, tabDrafts, true); len(got) != 1 || got[0].ID != "draft" {
		t.Fatalf("Drafts = %+v", got)
	}
	if got := filterSessionsByTabWithActivity(rows, tabChats, false); len(got) != 3 {
		t.Fatalf("legacy Chats = %+v", got)
	}
	st := sessionsState{tab: tabDrafts, activityInventory: true, sessions: rows, filtered: rows[1:2], handles: map[string]string{}, deps: surfaceDeps{caps: client.Capabilities{StorageHealth: true}}}
	if !strings.Contains(sessionsTabBar(testTheme(), st.tab, true, true), "Maintenance") {
		t.Fatal("storage-health category disappeared from the inventory tabs")
	}
	rendered := stripANSIstr(renderSessionsPanel(testTheme(), st, client.Capabilities{}, helpKeys{}, 100, 30))
	if !strings.Contains(rendered, "New — no messages") {
		t.Fatalf("draft rendering missing empty-state label:\n%s", rendered)
	}
	merged := mergeSessionPages(rows[:1], rows[1:3], false)
	if len(merged) != 3 || merged[2].ID != "unknown" {
		t.Fatalf("later page discarded received rows: %+v", merged)
	}
}
