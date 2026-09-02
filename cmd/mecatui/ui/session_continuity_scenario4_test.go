package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type fakeSessionTranscriptLoader struct {
	transcript client.SessionTranscript
	err        error
	calls      []string
}

func (f *fakeSessionTranscriptLoader) GetSessionTranscript(_ context.Context, id string) (client.SessionTranscript, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return client.SessionTranscript{}, f.err
	}
	return f.transcript, nil
}

func TestSessionContinuityUX_Scenario4_FamilyTabs(t *testing.T) {
	rows := []client.SessionListItem{
		{ID: "ordinary", Kind: client.SessionKindMain},
		{ID: "ordinary-schedule", Kind: client.SessionKindScheduled},
		{ID: "ordinary-child", Kind: client.SessionKindSubagent},
		{ID: "ordinary-member", Kind: client.SessionKindTeamMember},
		{ID: "legacy-unknown", Kind: client.SessionKindUnknown, Capabilities: client.SessionInventoryCapabilities{Inspect: true}},
	}
	cases := []struct {
		tab  sessionsTab
		want []string
	}{
		{tabChats, []string{"ordinary"}},
		{tabScheduledRuns, []string{"ordinary-schedule"}},
		{tabChildRuns, []string{"ordinary-child", "ordinary-member"}},
		{tabOtherRuns, []string{"legacy-unknown"}},
	}
	for _, tc := range cases {
		got := filterSessionsByTab(rows, tc.tab)
		if len(got) != len(tc.want) {
			t.Fatalf("tab %v rows = %v, want %v", tc.tab, got, tc.want)
		}
		for i := range got {
			if got[i].ID != tc.want[i] {
				t.Fatalf("tab %v row %d = %q, want %q", tc.tab, i, got[i].ID, tc.want[i])
			}
		}
	}
}

func TestSessionContinuityUX_Scenario4_SearchFields(t *testing.T) {
	row := client.SessionListItem{
		ID: "opaque-full-id", Title: "Fix Scheduler", ModelID: "GPT-5", Placement: client.Placement{Kind: "git", Label: "Repo"},
		Kind:         client.SessionKindTeamMember,
		Relationship: client.SessionRelationship{ParentSessionID: "parent-alpha", CallID: "call-beta", TeamID: "team-gamma", MemberName: "Reviewer"},
	}
	handle := sessionDisplayHandles([]client.SessionListItem{row})[row.ID]
	for _, query := range []string{"scheduler", "FULL-ID", handle, "gpt-5", "repo", "PARENT-ALPHA", "CALL-BETA", "TEAM-GAMMA", "reviewer"} {
		if got := filterSessions([]client.SessionListItem{row}, map[string]string{row.ID: handle}, query); len(got) != 1 {
			t.Errorf("query %q did not match all advertised fields", query)
		}
	}
}

func TestSessionContinuityUX_Scenario4_InspectionPreservesActiveChat(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  client.SessionListItem
	}{
		{name: "scheduled run", row: client.SessionListItem{ID: "scheduled-run", Kind: client.SessionKindScheduled, Capabilities: client.SessionInventoryCapabilities{Inspect: true}}},
		{name: "child run", row: client.SessionListItem{ID: "child-run", Kind: client.SessionKindSubagent, Capabilities: client.SessionInventoryCapabilities{Inspect: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{SessionID: tc.row.ID, Complete: true, Messages: []client.ConversationMessage{{Role: "assistant", Text: "inspection output"}}}}
			m := newScenario4Model(t, loader)
			m.sessionID = "active-chat"
			m.sessionTitle = "Active title"
			m.resolvedSessionModel = client.ResolvedModel{ProviderID: "provider", ModelID: "model"}
			m.caps = client.Capabilities{Teams: true}
			m.conv.addUser("active conversation")
			m.liveArmed = "active-chat"
			before := m
			ensureActiveSessions(&m).filtered = []client.SessionListItem{tc.row}
			mm, cmd, _ := m.chooseSession()
			m = mm.(Model)
			m = applyAll(m, cmd())
			if m.sessionID != before.sessionID || m.sessionTitle != before.sessionTitle || m.resolvedSessionModel != before.resolvedSessionModel || m.caps != before.caps || m.liveArmed != before.liveArmed || len(m.conv.blocks) != len(before.conv.blocks) {
				t.Fatal("inspection changed active chat identity, subscription, capabilities, model, or conversation")
			}
			mm, _, _ = m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			m = mm.(Model)
			if m.sessionID != "active-chat" || m.phase != phaseIdle {
				t.Fatalf("escape did not restore active chat: id=%q phase=%v", m.sessionID, m.phase)
			}
		})
	}
}

func TestInvariant_transcript_failure_never_enables_hidden_context(t *testing.T) {
	loader := &fakeSessionTranscriptLoader{err: errors.New("backend /secret/path owned by tenant-a")}
	m := newScenario4Model(t, loader)
	m.sessionID = "active-chat"
	row := client.SessionListItem{ID: "target-chat", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true}}
	ensureActiveSessions(&m).filtered = []client.SessionListItem{row}
	mm, cmd, _ := m.chooseSession()
	m = mm.(Model)
	m = applyAll(m, cmd())
	if m.phase != phaseReplay || m.sessionID != "active-chat" || m.prompt.Focused() {
		t.Fatalf("failure enabled or rebound chat: phase=%v id=%q focused=%v", m.phase, m.sessionID, m.prompt.Focused())
	}
	view := stripANSIstr(m.View().Content)
	if !strings.Contains(view, "Retry") || !strings.Contains(view, "Back") {
		t.Fatalf("failure must offer Retry and Back:\n%s", view)
	}
	if strings.Contains(view, "/secret/path") || strings.Contains(view, "tenant-a") {
		t.Fatalf("failure leaked backend or owner detail:\n%s", view)
	}
}

func TestSessionContinuityUX_Scenario4_ReasonCodes(t *testing.T) {
	m := newScenario4Model(t, &fakeSessionTranscriptLoader{})
	row := client.SessionListItem{ID: "opaque", Kind: client.SessionKindMain, ReasonCode: client.CapabilityReasonActiveElsewhere}
	ensureActiveSessions(&m).filtered = []client.SessionListItem{row}
	mm, cmd, handled := m.chooseSession()
	m = mm.(Model)
	if !handled || cmd != nil {
		t.Fatal("reason-coded unavailable row must stay in the picker without an RPC")
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "active elsewhere") || strings.Contains(got, "opaque") {
		t.Fatalf("reason-code message was not closed/sanitized: %q", got)
	}
}
