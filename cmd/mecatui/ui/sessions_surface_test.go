package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestSessionsNextTabCyclesVisibleTabsWhenDraftsAreHidden(t *testing.T) {
	st := newSessionsPanelState()
	st.activityInventory = false
	visible := []sessionsTab{tabChats, tabScheduledRuns, tabChildRuns, tabOtherRuns}
	for i, want := range visible {
		st.tab = want
		st.nextTab()
		if got := st.tab; got != visible[(i+1)%len(visible)] {
			t.Fatalf("nextTab from %v = %v, want %v", want, got, visible[(i+1)%len(visible)])
		}
	}
}
func TestSessionsSurfaceConsumesPickerKeysAndMessages(t *testing.T) {
	st := newSessionsPanelState()
	st.loading = false
	st.loadState = sessionsComplete
	st.sessions = []client.SessionListItem{{ID: "one", Title: "one", Kind: client.SessionKindMain}}
	st.syncFilter()

	if _, handled, closed := st.HandleKey(tea.KeyPressMsg{Text: "o"}); !handled || closed {
		t.Fatalf("filter key handled=%v closed=%v, want consumed open surface", handled, closed)
	}
	if st.filter.Value() != "o" {
		t.Fatalf("filter = %q, want picker-local mutation", st.filter.Value())
	}

	_, handled, closed := st.HandleMsg(client.SessionsListedMsg{Sessions: []client.SessionListItem{{ID: "two", Title: "two", Kind: client.SessionKindMain}}})
	if !handled || closed {
		t.Fatalf("list message handled=%v closed=%v, want consumed", handled, closed)
	}
	if len(st.sessions) != 1 || st.sessions[0].ID != "two" {
		t.Fatalf("list result was not adopted by surface: %+v", st.sessions)
	}
}

func TestSessionsTranscriptEscapeReturnsToPickerAndHintsIdleOnce(t *testing.T) {
	st := newSessionsPanelState()
	st.deps.keys = defaultKeys()
	st.view = sessionsTranscript
	st.transcript.addUser("history")
	st.transcriptRend = newRenderer(testTheme(), defaultHelpKeys())
	requestToken := st.transcriptRequestToken

	cmd, handled, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !handled || closed || cmd != nil {
		t.Fatalf("escape handled=%v closed=%v cmd=%v", handled, closed, cmd != nil)
	}
	if st.view != sessionsPanel || !st.transcript.isEmpty() || st.transcriptRend != nil || st.transcriptRequestToken != requestToken+1 {
		t.Fatalf("transcript teardown: view=%v empty=%v renderer=%p requestToken=%d", st.view, st.transcript.isEmpty(), st.transcriptRend, st.transcriptRequestToken)
	}
	intent, ok := st.takeSurfaceIntent().(sessionsPhaseIntent)
	if !ok || intent.phase != sessionsIntentPhaseIdle {
		t.Fatalf("first intent = %#v, want idle phase", intent)
	}
	if got := st.takeSurfaceIntent(); got != nil {
		t.Fatalf("second intent = %#v, want consumed", got)
	}
}

func TestSessionsTranscriptEscapeResetsRendererForNextSession(t *testing.T) {
	st := newSessionsPanelState()
	st.deps = surfaceDeps{theme: testTheme(), marks: defaultHelpKeys()}
	st.deps.keys = defaultKeys()
	st.view = sessionsTranscript
	st.inspect = true
	st.selected = client.SessionListItem{ID: "first"}
	st.transcriptRequestToken = 1

	first := sessionTranscriptLoadedMsg{
		sessionID:    "first",
		requestToken: 1,
		transcript:   client.SessionTranscript{SessionID: "first", Complete: true, Messages: []client.ConversationMessage{{Role: "assistant", Text: "first transcript"}}},
	}
	st.HandleMsg(first)
	firstView, _ := st.Render(100, 30)
	if !strings.Contains(stripANSIstr(firstView), "first transcript") {
		t.Fatalf("first transcript did not render: %q", stripANSIstr(firstView))
	}

	st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	st.selected = client.SessionListItem{ID: "second"}
	st.inspect = true
	st.view = sessionsTranscript
	st.transcriptRequestToken++
	second := sessionTranscriptLoadedMsg{
		sessionID:    "second",
		requestToken: st.transcriptRequestToken,
		transcript:   client.SessionTranscript{SessionID: "second", Complete: true, Messages: []client.ConversationMessage{{Role: "assistant", Text: "second transcript"}}},
	}
	st.HandleMsg(second)
	secondView, _ := st.Render(100, 30)
	plain := stripANSIstr(secondView)
	if !strings.Contains(plain, "second transcript") || strings.Contains(plain, "first transcript") {
		t.Fatalf("second transcript reused the first renderer cache: %q", plain)
	}
}

func TestSessionsTranscriptCloseReopenSameIDDropsStaleRequestToken(t *testing.T) {
	loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{
		SessionID: "same", Complete: true,
		Messages: []client.ConversationMessage{{Role: "assistant", Text: "stale transcript"}},
	}}
	st := newSessionsPanelState()
	st.deps.ctx = context.Background()
	st.transcripter = loader
	st.selected = client.SessionListItem{ID: "same"}
	st.inspect = true
	st.view = sessionsTranscript
	firstCmd := st.loadTranscriptCmd()
	stale := firstCmd().(sessionTranscriptLoadedMsg)
	firstRequestToken := stale.requestToken

	st.closeTranscript()
	st.selected = client.SessionListItem{ID: "same"}
	st.inspect = true
	st.view = sessionsTranscript
	loader.transcript.Messages[0].Text = "fresh transcript"
	freshCmd := st.loadTranscriptCmd()
	fresh := freshCmd().(sessionTranscriptLoadedMsg)
	if fresh.requestToken <= firstRequestToken {
		t.Fatalf("reopen requestToken=%d, first=%d", fresh.requestToken, firstRequestToken)
	}

	st.HandleMsg(stale)
	if !st.transcript.isEmpty() || !st.loading {
		t.Fatalf("stale same-ID response changed reopened load: empty=%v loading=%v", st.transcript.isEmpty(), st.loading)
	}
	st.HandleMsg(fresh)
	if st.loading || len(st.transcript.blocks) != 1 || st.transcript.blocks[0].raw != "fresh transcript" {
		t.Fatalf("fresh response not accepted: loading=%v blocks=%+v", st.loading, st.transcript.blocks)
	}
}

func TestSessionsTranscriptEscapeRetainsModalAndRestoresModelPhase(t *testing.T) {
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true})
	st := newSessionsPanelState()
	st.deps = (&m).surfaceDeps()
	st.view = sessionsTranscript
	m.modal = &st
	m.phase = phaseReplay

	updated, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if !handled || m.phase != phaseIdle || m.modal == nil {
		t.Fatalf("escape handled=%v phase=%v modal=%T", handled, m.phase, m.modal)
	}
	if sessionsSurface(&m).view != sessionsPanel {
		t.Fatalf("view = %v, want picker", sessionsSurface(&m).view)
	}
}

func TestClosedSessionsSurfaceDropsStaleResponsesAndRoutesModelActionResults(t *testing.T) {
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true})
	st := newSessionsPanelState()
	st.deps = (&m).surfaceDeps()
	st.view = sessionsTranscript
	st.selected = client.SessionListItem{ID: "old"}
	m.modal = &st
	m.modal.Close()
	m.modal = nil

	for _, msg := range []tea.Msg{
		client.SessionInventoryPageMsg{Page: client.SessionInventoryPage{Sessions: []client.SessionListItem{{ID: "old"}}}},
		sessionTranscriptLoadedMsg{sessionID: "old", requestToken: st.transcriptRequestToken, transcript: client.SessionTranscript{SessionID: "old", Complete: true}},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
		if m.modal != nil || !m.conv.isEmpty() {
			t.Fatalf("stale %T mutated closed model: modal=%T empty=%v", msg, m.modal, m.conv.isEmpty())
		}
	}

	updated, cmd := m.Update(inventorySessionIDCopiedMsg{id: "old"})
	m = updated.(Model)
	if cmd == nil || !strings.Contains(stripANSIstr(m.statusMsg), "copied exact session ID") {
		t.Fatalf("Model-owned copy result was not routed: cmd=%v status=%q", cmd != nil, stripANSIstr(m.statusMsg))
	}
}

func TestSessionsTranscriptModalCloseReopenSameIDDropsOldSurfaceRequestToken(t *testing.T) {
	loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{
		SessionID: "same", Complete: true,
		Messages: []client.ConversationMessage{{Role: "assistant", Text: "old transcript"}},
	}}
	m := newTestModelFromDeps(Deps{Transcript: loader, Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true})
	row := client.SessionListItem{ID: "same"}

	updated, oldCmd, ok := m.loadSessionTranscript(row, false)
	if !ok {
		t.Fatal("initial transcript request was not started")
	}
	m = updated.(Model)
	old := oldCmd().(sessionTranscriptLoadedMsg)

	updated, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	updated, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	loader.transcript.Messages[0].Text = "new transcript"
	updated, newCmd, ok := m.loadSessionTranscript(row, false)
	if !ok {
		t.Fatal("reopened transcript request was not started")
	}
	m = updated.(Model)
	fresh := newCmd().(sessionTranscriptLoadedMsg)
	if old.surfaceRequestToken == fresh.surfaceRequestToken {
		t.Fatalf("reopened request reused surfaceRequestToken %d", old.surfaceRequestToken)
	}

	updated, _ = m.Update(old)
	m = updated.(Model)
	if !m.conv.isEmpty() || !sessionsSurface(&m).transcript.isEmpty() || !sessionsSurface(&m).loading {
		t.Fatalf("old response adopted after modal reopen: conversation=%+v transcript=%+v loading=%v", m.conv, sessionsSurface(&m).transcript, sessionsSurface(&m).loading)
	}
}

func TestSessionsSurfaceCloseIsConsumed(t *testing.T) {
	st := newSessionsPanelState()
	st.loading = false
	st.loadState = sessionsComplete
	st.deps.keys = defaultKeys()
	if _, handled, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape}); !handled || !closed {
		t.Fatalf("escape handled=%v closed=%v, want surface close", handled, closed)
	}
}

func TestSessionDeleteConfirmationNamesTargetAndConsequence(t *testing.T) {
	st := newSessionsPanelState()
	st.confirmDelete = true
	st.actionID = "session-123"
	out := stripANSIstr(renderSessionsPanel(testTheme(), st, client.Capabilities{}, defaultHelpKeys(), 100, 30))
	for _, want := range []string{"Permanently delete session \"session-123\"? This cannot be undone.", "y/enter: delete", "esc: cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("session delete confirmation missing %q:\n%s", want, out)
		}
	}
}

func TestSessionsSurfaceEscapeClosesMaintenanceSubviewFirst(t *testing.T) {
	st := newSessionsPanelState()
	st.deps.keys = defaultKeys()
	st.tab, st.maintenance, st.maintenanceErr = tabStorageHealth, maintenanceCleanupPlan, true
	if _, handled, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape}); !handled || closed {
		t.Fatalf("escape handled=%v closed=%v, want maintenance-only close", handled, closed)
	}
	if st.maintenance != maintenanceNone || st.maintenanceErr {
		t.Fatalf("maintenance remained open: %+v", st)
	}
}

func TestSessionsSurfaceStartupEscapeRecordsModelKeyIntent(t *testing.T) {
	st := newSessionsPanelState()
	st.deps.keys = defaultKeys()
	st.startup = true
	msg := tea.KeyPressMsg{Code: tea.KeyEscape}
	cmd, handled, closed := st.HandleKey(msg)
	if !handled || closed || cmd != nil {
		t.Fatalf("startup escape handled=%v closed=%v cmd=%v", handled, closed, cmd != nil)
	}
	intent, ok := st.takeSurfaceIntent().(sessionsStartupQuitIntent)
	if !ok {
		t.Fatalf("intent = %#v, want startup quit intent", intent)
	}
}

func TestSessionsSurfaceSemanticFormsStayStateOwned(t *testing.T) {
	for name, set := range map[string]func(*sessionsState){
		"rename": func(st *sessionsState) { st.renaming = true },
		"delete": func(st *sessionsState) { st.confirmDelete = true },
	} {
		t.Run(name, func(t *testing.T) {
			st := newSessionsPanelState()
			st.deps.keys = defaultKeys()
			st.filter.SetValue("keep")
			set(&st)
			msg := tea.KeyPressMsg{Code: 'x', Text: "x"}
			cmd, handled, closed := st.HandleKey(msg)
			if !handled || closed || cmd != nil || st.filter.Value() != "keep" {
				t.Fatalf("form key handled=%v closed=%v filter=%q cmd=%v", handled, closed, st.filter.Value(), cmd != nil)
			}
			if intent := st.takeSurfaceIntent(); intent != nil {
				t.Fatalf("intent = %#v, want no model key intent", intent)
			}
		})
	}
}

func TestSessionsModelKeyDispatchesThroughSurfaceIntent(t *testing.T) {
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true})
	st := newSessionsPanelState()
	st.deps = (&m).surfaceDeps()
	st.loading = false
	st.sessions = []client.SessionListItem{{ID: "one", Title: "old", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Rename: true}}}
	st.syncFilter()
	m.modal = &st
	m.deps.SessionManagement = &fakeSessionManager{}

	updated, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = updated.(Model)
	if !handled || !sessionsSurface(&m).renaming {
		t.Fatalf("model key dispatch: handled=%v renaming=%v", handled, sessionsSurface(&m).renaming)
	}
	if got := sessionsSurface(&m).takeSurfaceIntent(); got != nil {
		t.Fatalf("model key intent was not drained: %#v", got)
	}
}

func TestOverlayRoutesStartupSessionEscapeDirectly(t *testing.T) {
	m := progressiveSessionsModel(&progressiveSessionPager{})
	updated, cmd, handled := m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !handled || cmd == nil {
		t.Fatalf("startup escape handled=%v cmd=%v", handled, cmd != nil)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("startup escape cmd = %T, want tea.QuitMsg", cmd())
	}
	if updated.(Model).modal == nil {
		t.Fatal("startup quit path must not close the modal first")
	}
}

func TestSessionsSurfaceDropsStalePaginationRequestTokenInPlace(t *testing.T) {
	st := newSessionsPanelState()
	st.deps.ctx = context.Background()
	st.pager = &progressiveSessionPager{}
	st.sessions = []client.SessionListItem{{ID: "kept", Kind: client.SessionKindMain}}
	st.syncFilter()
	st.beginPage("older")
	oldRequestToken := st.pageRequestToken
	st.beginPage("newer")
	newRequestToken := st.pageRequestToken
	if newRequestToken <= oldRequestToken {
		t.Fatalf("new requestToken=%d, old=%d", newRequestToken, oldRequestToken)
	}

	cmd, handled, closed := st.HandleMsg(client.SessionInventoryPageMsg{
		RequestToken: oldRequestToken,
		Cursor:       "older",
		Page: client.SessionInventoryPage{
			Sessions:   []client.SessionListItem{{ID: "stale", Kind: client.SessionKindMain}},
			NextCursor: "stale-next",
		},
	})
	if !handled || closed || cmd != nil {
		t.Fatalf("stale page handled=%v closed=%v followup=%v", handled, closed, cmd != nil)
	}
	if len(st.sessions) != 1 || st.sessions[0].ID != "kept" || st.nextCursor != "newer" || st.loadState != sessionsLoadingMore || st.pageRequestToken != newRequestToken {
		t.Fatalf("stale page mutated active pagination: rows=%+v cursor=%q state=%v requestToken=%d", st.sessions, st.nextCursor, st.loadState, st.pageRequestToken)
	}
	if intent := st.takeSurfaceIntent(); intent != nil {
		t.Fatalf("stale page scheduled root effects: %#v", intent)
	}
}

func TestClosedSessionsSurfaceDropsStalePageResponse(t *testing.T) {
	pager := &progressiveSessionPager{}
	m := progressiveSessionsModel(pager)
	st := sessionsSurface(&m)
	if st == nil || st.pageCtx == nil {
		t.Fatal("startup picker did not begin pagination")
	}
	requestToken := st.pageRequestToken
	m.modal.Close()
	m.modal = nil

	updated, _ := m.Update(client.SessionInventoryPageMsg{RequestToken: requestToken, Err: context.Canceled})
	m = updated.(Model)
	if m.modal != nil {
		t.Fatalf("stale page response resurrected modal: %T", m.modal)
	}
}

func TestSyntheticUserPromptReplay_Scenario2_TranscriptRendersNoticeNotUserBubble(t *testing.T) {
	st := newSessionsPanelState()
	st.applyReplayEvent(client.UserPromptMsg{Text: "harness continuation", Synthetic: true})
	st.applyReplayEvent(client.UserPromptMsg{Text: "operator prompt", Parts: []client.ContentBlock{{Kind: "image", MimeType: "image/png"}}})

	if len(st.transcript.blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(st.transcript.blocks))
	}
	notice, user := st.transcript.blocks[0], st.transcript.blocks[1]
	if notice.kind != blockNotice || notice.raw != "harness continuation" {
		t.Fatalf("synthetic replay block = %+v, want persistent notice", notice)
	}
	if user.kind != blockUser || user.raw != "operator prompt" || len(user.media) != 1 || user.media[0] != "image/png (inline)" {
		t.Fatalf("genuine replay block = %+v, want user bubble with media descriptor", user)
	}
}
