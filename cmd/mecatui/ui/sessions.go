package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type sessionsTab int

const (
	tabChats sessionsTab = iota
	tabScheduledRuns
	tabChildRuns
	tabOtherRuns
)

type sessionsView int

const (
	sessionsNone sessionsView = iota
	sessionsPanel
	sessionsTranscript
)

type sessionDetailsView struct {
	ID         string
	Title      string
	State      string
	Workspace  string
	CreatedAt  int64
	ModifiedAt int64
	ProviderID string
	ModelID    string
}

type sessionIDCopyResultMsg struct {
	id  string
	err error
}

type inventorySessionIDCopiedMsg struct {
	id  string
	err error
}

type sessionForkedMsg struct {
	sourceID   string
	newID      string
	snapshot   client.SessionSnapshot
	transcript client.SessionTranscript
	err        error
}

type sessionsState struct {
	view     sessionsView
	startup  bool // same /sessions renderer, with launch-only new/quit hints
	tab      sessionsTab
	loading  bool
	err      error
	sessions []client.SessionListItem
	filtered []client.SessionListItem
	handles  map[string]string
	filter   textinput.Model
	cursor   int
	selected client.SessionListItem
	inspect  bool
	loadErr  error

	renaming      bool
	renameInput   textinput.Model
	confirmDelete bool
	actionID      string
	actionLoading bool

	// Activity-replay fields are retained for the live-delivery catch-up and old
	// schedule replay machinery. /sessions never uses them as conversation truth.
	replayCh       chan tea.Msg
	replayStop     func()
	replayGen      uint64
	transcript     conversation
	receivedMsgs   int
	replayClosed   bool
	replayErr      error
	continueOnLoad bool
}

func (m Model) bindSessionID(id string) Model {
	m.sessionID = id
	m.sessionState = ""
	m.sessionCreatedAt = 0
	m.sessionModifiedAt = 0
	return m
}

func (m Model) sessionDetails() sessionDetailsView {
	return sessionDetailsView{
		ID: m.sessionID, Title: m.sessionTitle, State: m.sessionState,
		Workspace: m.activeWorkspace, CreatedAt: m.sessionCreatedAt,
		ModifiedAt: m.sessionModifiedAt, ProviderID: m.effectiveModel.ProviderID,
		ModelID: m.effectiveModel.ModelID,
	}
}

// ActiveSessionID returns the currently bound opaque session ID. The process
// entry point reads it only after Bubble Tea has restored the terminal.
func (m Model) ActiveSessionID() string { return m.sessionID }

func (m Model) sessionCopyTarget() string {
	if m.sessionID == "" || !utf8.ValidString(m.sessionID) {
		return ""
	}
	return m.sessionID
}

func safeSessionID(id string) string { return strconv.QuoteToASCII(id) }

func (m Model) openSessionDetails() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.sessionID == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("no active session")
		return m, nil
	}
	m.sessionDetailsOpen = true
	m.ta.Blur()
	if m.deps.Session != nil {
		return m, client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, m.sessionID)
	}
	return m, nil
}

func (m Model) onSessionDetailsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if !m.sessionDetailsOpen {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		m.sessionDetailsOpen = false
		return m, m.ta.Focus(), true
	}
	if msg.String() != "c" {
		return m, nil, true
	}
	id := m.sessionCopyTarget()
	if id == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("no active session ID to copy")
		return m, nil, true
	}
	if m.deps.Clipboard == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: clipboard unavailable")
		return m, nil, true
	}
	cb, ctx := m.deps.Clipboard, m.deps.Ctx
	return m, func() tea.Msg {
		err := cb.Write(ctx, "text/plain", []byte(id))
		return sessionIDCopyResultMsg{id: id, err: err}
	}, true
}

func (m Model) onSessionIDCopyResult(msg sessionIDCopyResultMsg) Model {
	if msg.id == "" || msg.id != m.sessionID {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: active session changed")
		return m
	}
	if msg.err != nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: " + sanitizeTerminal(msg.err.Error()))
		return m
	}
	m.statusMsg = m.deps.Theme.Style("success").Render("copied session ID")
	return m
}

func formatSessionTimestamp(unixSec int64) string {
	if unixSec <= 0 {
		return "unknown"
	}
	return time.Unix(unixSec, 0).UTC().Format(time.RFC3339)
}

func renderSessionDetails(th theme.Theme, details sessionDetailsView, hk helpKeys, width, height int) string {
	unknown := func(value string) string {
		if value == "" {
			return "unknown"
		}
		return sanitizeTerminal(value)
	}
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Active session") + "\n\n")
	b.WriteString("ID: " + indentWrap(safeSessionID(details.ID), cardTextWidth(width)) + "\n")
	b.WriteString("Title: " + unknown(details.Title) + "\n")
	b.WriteString("State: " + unknown(details.State) + "\n")
	b.WriteString("Workspace: " + unknown(details.Workspace) + "\n")
	b.WriteString("Created: " + formatSessionTimestamp(details.CreatedAt) + "\n")
	b.WriteString("Modified: " + formatSessionTimestamp(details.ModifiedAt) + "\n")
	b.WriteString("Provider: " + unknown(details.ProviderID) + "\n")
	b.WriteString("Model: " + unknown(details.ModelID) + "\n\n")
	b.WriteString(th.Style("muted").Render("c: copy exact ID  " + hk.closeOnly + ": close"))
	return centerCard(th, b.String(), width, height)
}

func newSessionsPanelState() sessionsState {
	ti := textinput.New()
	ti.Placeholder = "search sessions…"
	ti.SetWidth(40)
	ti.Focus()
	return sessionsState{view: sessionsPanel, tab: tabChats, loading: true, filter: ti}
}

func (m Model) openSessions() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Sessions == nil || m.deps.Transcript == nil {
		return m, nil
	}
	m.ta.Blur()
	m.sessions = newSessionsPanelState()
	return m, tea.Batch(client.ListSessionsCmd(m.deps.Ctx, m.deps.Sessions), textinput.Blink)
}

func (m Model) closeSessions() (tea.Model, tea.Cmd) {
	m.sessions = sessionsState{}
	cmd := m.ta.Focus()
	return m, cmd
}

func (m Model) onSessionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.sessions.view == sessionsNone || m.sessions.view == sessionsTranscript {
		return m, nil, false
	}
	if m.sessions.renaming {
		return m.onSessionRenameKey(msg)
	}
	if m.sessions.confirmDelete {
		return m.onSessionDeleteConfirmKey(msg)
	}
	if m.sessions.actionLoading {
		return m, nil, true
	}
	if mm, cmd, handled := m.onStartupSessionsKey(msg); handled {
		return mm, cmd, true
	}
	if key.Matches(msg, m.keys.NextTab) {
		m = m.switchSessionsTab()
		return m, nil, true
	}
	if mm, cmd, handled := m.onSessionsNavigationKey(msg); handled {
		return mm, cmd, true
	}
	if mm, cmd, handled := m.onSessionsActionKey(msg); handled {
		return mm, cmd, true
	}
	var cmd tea.Cmd
	m.sessions.filter, cmd = m.sessions.filter.Update(msg)
	m = m.syncSessionsFilter()
	return m, cmd, true
}

func (m Model) onStartupSessionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if !m.browsingStartupSessions {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		return m, tea.Quit, true
	}
	if msg.String() != "n" {
		return m, nil, false
	}
	if !m.modelsReconciled {
		m.statusMsg = m.deps.Theme.Style("muted").Render("loading model defaults…")
		return m, nil, true
	}
	m.sessions = sessionsState{}
	m.phase = phaseConnecting
	return m, m.createSessionCmd(), true
}

func (m Model) onSessionsNavigationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Close):
		if m.sessions.filter.Value() != "" {
			m.sessions.filter.SetValue("")
			m = m.syncSessionsFilter()
			return m, nil, true
		}
		mm, cmd := m.closeSessions()
		return mm, cmd, true
	case msg.String() == keyMenuUp:
		if m.sessions.cursor > 0 {
			m.sessions.cursor--
		}
		return m, nil, true
	case msg.String() == keyMenuDown:
		if m.sessions.cursor < len(m.sessions.filtered)-1 {
			m.sessions.cursor++
		}
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollTop):
		m.sessions.cursor = 0
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollBottom):
		m.sessions.cursor = clampModelsCursor(len(m.sessions.filtered)-1, len(m.sessions.filtered))
		return m, nil, true
	case key.Matches(msg, m.keys.Choose):
		return m.chooseSession()
	default:
		return m, nil, false
	}
}

func (m Model) onSessionsActionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "y":
		return m.copyInventorySessionID()
	case "v":
		return m.viewInventorySession()
	case "f":
		return m.forkInventorySession()
	case "r":
		return m.openSessionRename()
	case "d":
		return m.openSessionDelete()
	default:
		return m, nil, false
	}
}

func (m Model) syncSessionsFilter() Model {
	tabbed := filterSessionsByTab(m.sessions.sessions, m.sessions.tab)
	m.sessions.handles = sessionDisplayHandles(tabbed)
	m.sessions.filtered = filterSessions(tabbed, m.sessions.handles, m.sessions.filter.Value())
	if m.sessions.cursor >= len(m.sessions.filtered) {
		m.sessions.cursor = 0
	}
	return m
}

func filterSessionsByTab(sessions []client.SessionListItem, tab sessionsTab) []client.SessionListItem {
	out := make([]client.SessionListItem, 0, len(sessions))
	for _, s := range sessions {
		keep := false
		switch tab {
		case tabChats:
			keep = s.Kind == client.SessionKindMain
		case tabScheduledRuns:
			keep = s.Kind == client.SessionKindScheduled
		case tabChildRuns:
			keep = s.Kind == client.SessionKindSubagent || s.Kind == client.SessionKindParallelBranch || s.Kind == client.SessionKindTeamMember
		case tabOtherRuns:
			keep = s.Kind != client.SessionKindMain && s.Kind != client.SessionKindScheduled &&
				s.Kind != client.SessionKindSubagent && s.Kind != client.SessionKindParallelBranch && s.Kind != client.SessionKindTeamMember
		}
		if keep {
			out = append(out, s)
		}
	}
	return out
}

func filterSessions(sessions []client.SessionListItem, handles map[string]string, q string) []client.SessionListItem {
	if q == "" {
		return sessions
	}
	needle := strings.ToLower(q)
	out := make([]client.SessionListItem, 0, len(sessions))
	for _, s := range sessions {
		fields := []string{
			s.Title, s.ID, handles[s.ID], s.ModelID, s.Workspace,
			s.Relationship.ParentSessionID, s.Relationship.CallID,
			s.Relationship.ScheduleName, s.Relationship.OriginSessionID,
			s.Relationship.TeamID, s.Relationship.MemberName,
		}
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field), needle) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func sessionDigest(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// sessionDisplayHandles derives terminal-safe lowercase-hex handles and expands
// only colliding prefixes. The map is display-only; callers retain the full ID.
func sessionDisplayHandles(rows []client.SessionListItem) map[string]string {
	const minimum = 8
	digests := make(map[string]string, len(rows))
	lengths := make(map[string]int, len(rows))
	for _, row := range rows {
		digests[row.ID] = sessionDigest(row.ID)
		lengths[row.ID] = minimum
	}
	for {
		groups := make(map[string][]string, len(rows))
		for id, digest := range digests {
			groups[digest[:lengths[id]]] = append(groups[digest[:lengths[id]]], id)
		}
		changed := false
		for _, ids := range groups {
			if len(ids) < 2 {
				continue
			}
			for _, id := range ids {
				if lengths[id] < len(digests[id]) {
					lengths[id]++
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = digests[row.ID][:lengths[row.ID]]
	}
	return out
}

func (m Model) selectedInventorySession() (client.SessionListItem, bool) {
	if m.sessions.cursor < 0 || m.sessions.cursor >= len(m.sessions.filtered) {
		return client.SessionListItem{}, false
	}
	return m.sessions.filtered[m.sessions.cursor], true
}

func (m Model) copyInventorySessionID() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.CopyID {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.CopyID))
		return m, nil, true
	}
	if row.ID == "" || !utf8.ValidString(row.ID) {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: invalid ID")
		return m, nil, true
	}
	id, cb, ctx := row.ID, m.deps.Clipboard, m.deps.Ctx
	return m, func() tea.Msg {
		if cb != nil {
			return inventorySessionIDCopiedMsg{id: id, err: cb.Write(ctx, "text/plain", []byte(id))}
		}
		return inventorySessionIDCopiedMsg{id: id}
	}, true
}

func (m Model) viewInventorySession() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.ViewTranscript {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.ViewTranscript))
		return m, nil, true
	}
	return m.loadSessionTranscript(row, true)
}

func (m Model) forkInventorySession() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.Fork || m.deps.Session == nil || m.deps.Transcript == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.Fork))
		return m, nil, true
	}
	m.sessions.actionLoading = true
	m.sessions.actionID = row.ID
	session, transcript, ctx := m.deps.Session, m.deps.Transcript, m.deps.Ctx
	return m, func() tea.Msg {
		newID, err := session.ForkSession(ctx, row.ID, "")
		if err != nil {
			return sessionForkedMsg{sourceID: row.ID, err: err}
		}
		snapshot, err := session.GetSession(ctx, newID)
		if err != nil {
			return sessionForkedMsg{sourceID: row.ID, newID: newID, err: err}
		}
		loaded, err := transcript.GetSessionTranscript(ctx, newID)
		if err != nil || !loaded.Complete || loaded.SessionID != newID {
			if err == nil {
				err = errIncompleteTranscript
			}
			return sessionForkedMsg{sourceID: row.ID, newID: newID, err: err}
		}
		return sessionForkedMsg{sourceID: row.ID, newID: newID, snapshot: snapshot, transcript: loaded}
	}, true
}

func (m Model) openSessionRename() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.Rename || m.deps.SessionManagement == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.Rename))
		return m, nil, true
	}
	input := textinput.New()
	input.SetWidth(50)
	input.SetValue(row.Title)
	input.Focus()
	m.sessions.renaming = true
	m.sessions.renameInput = input
	m.sessions.actionID = row.ID
	return m, textinput.Blink, true
}

func (m Model) onSessionRenameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, m.keys.Close) {
		m.sessions.renaming = false
		m.sessions.actionID = ""
		return m, nil, true
	}
	if key.Matches(msg, m.keys.Choose) {
		id, title := m.sessions.actionID, m.sessions.renameInput.Value()
		m.sessions.renaming = false
		m.sessions.actionLoading = true
		return m, client.RenameSessionCmd(m.deps.Ctx, m.deps.SessionManagement, id, title), true
	}
	var cmd tea.Cmd
	m.sessions.renameInput, cmd = m.sessions.renameInput.Update(msg)
	return m, cmd, true
}

func (m Model) openSessionDelete() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.Delete || m.deps.SessionManagement == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.Delete))
		return m, nil, true
	}
	if row.ID == m.sessionID && m.sessionID != "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("cannot delete the current chat — switch to another chat first")
		return m, nil, true
	}
	m.sessions.confirmDelete = true
	m.sessions.actionID = row.ID
	return m, nil, true
}

func (m Model) onSessionDeleteConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, m.keys.Close) || msg.String() == "n" {
		m.sessions.confirmDelete = false
		m.sessions.actionID = ""
		return m, nil, true
	}
	if msg.String() != "y" && !key.Matches(msg, m.keys.Choose) {
		return m, nil, true
	}
	id := m.sessions.actionID
	m.sessions.confirmDelete = false
	m.sessions.actionLoading = true
	return m, client.DeleteSessionCmd(m.deps.Ctx, m.deps.SessionManagement, id), true
}

func (m Model) chooseSession() (tea.Model, tea.Cmd, bool) {
	if m.sessions.cursor < 0 || m.sessions.cursor >= len(m.sessions.filtered) {
		return m, nil, false
	}
	chosen := m.sessions.filtered[m.sessions.cursor]
	if chosen.ID == m.sessionID {
		m.statusMsg = m.deps.Theme.Style("muted").Render("this is the current chat")
		return m, nil, true
	}
	inspect := !chosen.Capabilities.PublicChat && chosen.Capabilities.Inspect
	if !chosen.Capabilities.PublicChat && !chosen.Capabilities.Inspect {
		reason := chosen.Reasons.PublicChat
		if reason == "" {
			reason = chosen.Reasons.Inspect
		}
		if reason == "" {
			reason = chosen.ReasonCode // compatibility with older servers/tests.
		}
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(reason))
		return m, nil, true
	}
	return m.loadSessionTranscript(chosen, inspect)
}

func capabilityReasonText(reason client.CapabilityReason) string {
	switch reason {
	case client.CapabilityReasonInspectOnlyKind:
		return "this run is available for inspection only"
	case client.CapabilityReasonAwaitingApproval:
		return "this chat is awaiting approval"
	case client.CapabilityReasonActiveElsewhere:
		return "this chat is active elsewhere"
	case client.CapabilityReasonTranscriptUnavailable:
		return "the authoritative transcript is unavailable"
	case client.CapabilityReasonEnvironmentUnavailable:
		return "the chat environment is unavailable"
	case client.CapabilityReasonStorageUnsupported:
		return "this action is unsupported by session storage"
	default:
		return "this action is unavailable"
	}
}

func (m Model) loadSessionTranscript(row client.SessionListItem, inspect bool) (tea.Model, tea.Cmd, bool) {
	if m.deps.Transcript == nil {
		return m, nil, false
	}
	m.sessions.selected = row
	m.sessions.inspect = inspect
	m.sessions.loadErr = nil
	m.sessions.loading = true
	m.sessions.transcript = conversation{}
	m.sessions.view = sessionsTranscript
	m.phase = phaseReplay
	m.ta.Blur()
	return m, client.GetSessionTranscriptCmd(m.deps.Ctx, m.deps.Transcript, row.ID), true
}

func (m Model) updateSessionsMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if mm, cmd, handled := m.updateSessionActionMsg(msg); handled {
		return mm, cmd, true
	}
	switch sm := msg.(type) {
	case client.SessionsListedMsg:
		m.sessions.loading = false
		selectedID := ""
		if m.sessions.cursor >= 0 && m.sessions.cursor < len(m.sessions.filtered) {
			selectedID = m.sessions.filtered[m.sessions.cursor].ID
		}
		if sm.Err != nil {
			m.sessions.err = sm.Err
			m.sessions.sessions = nil
			m.sessions.filtered = nil
			return m, nil, true
		}
		m.sessions.err = nil
		m.sessions.sessions = sm.Sessions
		if m.sessions.actionID != "" {
			selectedID = m.sessions.actionID
		}
		m = m.syncSessionsFilter()
		for i := range m.sessions.filtered {
			if m.sessions.filtered[i].ID == selectedID {
				m.sessions.cursor = i
				break
			}
		}
		m.sessions.actionID = ""
		return m, nil, true
	case client.SessionTranscriptMsg:
		if m.sessions.view != sessionsTranscript || sm.SessionID != m.sessions.selected.ID {
			return m, nil, true
		}
		m.sessions.loading = false
		if sm.Err != nil || !sm.Transcript.Complete || sm.Transcript.SessionID != sm.SessionID {
			m.sessions.loadErr = sm.Err
			if m.sessions.loadErr == nil {
				m.sessions.loadErr = errIncompleteTranscript
			}
			m.refreshView()
			return m, nil, true
		}
		m.sessions.transcript = conversationFromTranscript(sm.Transcript.Messages)
		if m.sessions.inspect {
			m.refreshView()
			return m, nil, true
		}
		return m.adoptAuthoritativeTranscript()
	default:
		return m, nil, false
	}
}

func (m Model) updateSessionActionMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch sm := msg.(type) {
	case inventorySessionIDCopiedMsg:
		if sm.err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: " + sanitizeTerminal(sm.err.Error()))
			return m, nil, true
		}
		m.statusMsg = m.deps.Theme.Style("success").Render("copied exact session ID " + safeSessionID(sm.id))
		return m, tea.SetClipboard(sm.id), true
	case client.SessionRenamedMsg:
		m.sessions.actionLoading = false
		if sm.Err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not rename session: " + sanitizeTerminal(sm.Err.Error()))
			return m, nil, true
		}
		for i := range m.sessions.sessions {
			if m.sessions.sessions[i].ID == sm.SessionID {
				m.sessions.sessions[i].Title = sm.Title
				m.sessions.sessions[i].TitleProvenance = sm.TitleProvenance
			}
		}
		if sm.SessionID == m.sessionID {
			m.sessionTitle = sm.Title
		}
		m = m.syncSessionsFilter()
		m.statusMsg = m.deps.Theme.Style("success").Render("renamed session")
		if m.deps.Sessions != nil {
			return m, client.ListSessionsCmd(m.deps.Ctx, m.deps.Sessions), true
		}
		return m, nil, true
	case client.SessionDeletedMsg:
		m.sessions.actionLoading = false
		if sm.Err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not delete session: " + sanitizeTerminal(sm.Err.Error()))
			return m, nil, true
		}
		kept := m.sessions.sessions[:0]
		for _, row := range m.sessions.sessions {
			if row.ID != sm.SessionID {
				kept = append(kept, row)
			}
		}
		m.sessions.sessions = kept
		m.sessions.actionID = ""
		m = m.syncSessionsFilter()
		m.statusMsg = m.deps.Theme.Style("success").Render("deleted session")
		return m, nil, true
	case sessionForkedMsg:
		m.sessions.actionLoading = false
		if sm.sourceID != m.sessions.actionID {
			return m, nil, true
		}
		if sm.err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not fork session: " + sanitizeTerminal(sm.err.Error()))
			return m, nil, true
		}
		m.sessions.selected = client.SessionListItem{
			ID: sm.newID, Title: sm.snapshot.Title, TitleProvenance: sm.snapshot.TitleProvenance,
			State: sm.snapshot.State, Workspace: sm.snapshot.Workspace, CreatedAt: sm.snapshot.CreatedAt,
			Kind: sm.transcript.Kind, Relationship: sm.transcript.Relationship,
		}
		m.sessions.transcript = conversationFromTranscript(sm.transcript.Messages)
		m.sessions.inspect = false
		return m.adoptAuthoritativeTranscript()
	default:
		return m, nil, false
	}
}

var errIncompleteTranscript = &sessionTranscriptError{"authoritative transcript incomplete"}

type sessionTranscriptError struct{ text string }

func (e *sessionTranscriptError) Error() string { return e.text }

func conversationFromTranscript(messages []client.ConversationMessage) conversation {
	var out conversation
	for _, message := range messages {
		switch message.Role {
		case "user":
			if media := mediaDescriptors(message.Parts); len(media) > 0 {
				out.addUserWithMedia(message.Text, media)
			} else {
				out.addUser(message.Text)
			}
		case "assistant":
			out.appendAssistant(message.Text)
			for _, call := range message.ToolCalls {
				out.addTool(call.ID, call.Name, call.Args)
			}
		case "tool":
			if message.ToolResult != nil {
				out.resolveTool(message.ToolResult.CallID, message.ToolResult.Content, message.ToolResult.IsError, message.ToolResult.Blocks...)
			}
		}
	}
	return out
}

func (m Model) adoptAuthoritativeTranscript() (tea.Model, tea.Cmd, bool) {
	row := m.sessions.selected
	loaded := m.sessions.transcript
	m = m.endRun("")
	m = m.resetSession()
	m = m.bindSessionID(row.ID)
	m.sessionTitle = row.Title
	m.sessionState = row.State
	m.sessionCreatedAt = row.CreatedAt
	m.sessionModifiedAt = row.ModifiedAt
	m.activeWorkspace = row.Workspace
	m.conv = loaded
	m.restartedThisRun = true
	m.sessions = sessionsState{}
	m.browsingStartupSessions = false
	m.phase = phaseIdle
	m.stuck = true
	m.statusMsg = "continuing chat " + sanitizeTerminal(row.Title) + " — type to add a turn"
	cmd := m.ta.Focus()
	m.refreshView()
	if m.deps.Session != nil {
		cmd = tea.Batch(cmd, client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, row.ID))
	}
	cmd = tea.Batch(cmd, (&m).armLiveFeed())
	return m, cmd, true
}

func (m Model) closeSessionsTranscript() (tea.Model, tea.Cmd) {
	if m.sessions.replayStop != nil {
		m.sessions.replayStop()
	}
	m.sessions.selected = client.SessionListItem{}
	m.sessions.inspect = false
	m.sessions.loading = false
	m.sessions.loadErr = nil
	m.sessions.transcript = conversation{}
	m.sessions.view = sessionsPanel
	m.phase = phaseIdle
	m.statusMsg = ""
	m.stuck = true
	m.refreshView()
	return m, nil
}

func (m Model) switchSessionsTab() Model {
	m.sessions.tab = (m.sessions.tab + 1) % 4
	return m.syncSessionsFilter()
}

func stateBadge(state string) string {
	switch state {
	case "running":
		return "▶"
	case "completed":
		return "✓"
	case teamStopReasonCancelled, "failed":
		return "✗"
	case "awaiting":
		return "⏸"
	default:
		return "·"
	}
}

func relativeTime(unixSec int64) string {
	if unixSec <= 0 {
		return "—"
	}
	d := time.Since(time.Unix(unixSec, 0))
	switch {
	case d >= 24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	case d >= time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	default:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	}
}

func renderSessionsOverlay(th theme.Theme, st sessionsState, caps client.Capabilities, sessionID, vpContent string, hk helpKeys, width, height int) string {
	if st.view == sessionsTranscript {
		return renderSessionsTranscript(th, st, sessionID, vpContent, hk, width, height)
	}
	return renderSessionsPanel(th, st, caps, hk, width, height, sessionID)
}

func sessionsTabBar(th theme.Theme, tab sessionsTab) string {
	labels := []string{"Chats", "Scheduled runs", "Child runs", "Other"}
	var parts []string
	for i, label := range labels {
		prefix := "  "
		style := th.Style("muted")
		if int(tab) == i {
			prefix = "▸ "
			style = th.Style("askTitle")
		}
		parts = append(parts, style.Render(prefix+label))
	}
	return strings.Join(parts, th.Style("muted").Render("  "))
}

func renderSessionsPanel(th theme.Theme, st sessionsState, _ client.Capabilities, hk helpKeys, _, _ int, currentID ...string) string {
	current := ""
	if len(currentID) > 0 {
		current = currentID[0]
	}
	var b strings.Builder
	b.WriteString(sessionsTabBar(th, st.tab) + "\n\n")
	if rendered, ok := renderSessionsPanelState(th, st, hk); ok {
		b.WriteString(rendered)
		return b.String()
	}
	renderSessionRows(&b, th, st, current)
	b.WriteString("\n" + th.Style("muted").Render(sessionActionsHint(st, hk)))
	return b.String()
}

func renderSessionsPanelState(th theme.Theme, st sessionsState, hk helpKeys) (string, bool) {
	switch {
	case st.renaming:
		return th.Style("askTitle").Render("Rename session") + "\n\n" +
			st.renameInput.View() + "\n\n" +
			th.Style("muted").Render(hk.choose+": save  "+hk.closeOnly+": cancel"), true
	case st.confirmDelete:
		return th.Style("errorText").Render("Permanently delete session "+safeSessionID(st.actionID)+"?") + "\n\n" +
			th.Style("muted").Render("y/"+hk.choose+": delete  "+hk.closeOnly+": cancel"), true
	case st.loading || st.actionLoading:
		return th.Style("muted").Render("loading…"), true
	case st.err != nil:
		hint := hk.closeOnly + ": close"
		if st.startup {
			hint = sessionsPanelHint(hk, true)
		}
		return th.Style("errorText").Render("could not list sessions") + "\n" + th.Style("muted").Render(hint), true
	case len(st.filtered) == 0:
		message := "no " + []string{"chats", "scheduled runs", "child runs", "other sessions"}[st.tab] + " found"
		if st.filter.Value() != "" {
			message = "no matches — clear search to see all"
		}
		return th.Style("muted").Render(message) + "\n" + th.Style("muted").Render(sessionsPanelHint(hk, st.startup)), true
	default:
		return "", false
	}
}

func renderSessionRows(b *strings.Builder, th theme.Theme, st sessionsState, current string) {
	for i, s := range st.filtered {
		marker := "  "
		if i == st.cursor {
			marker = "▶ "
		}
		label := s.Title
		if label == "" {
			label = "untitled"
		}
		line := marker + stateBadge(s.State) + " " + relativeTime(s.ModifiedAt) + " " + strconv.Itoa(int(s.Turns)) + "t " + sanitizeTerminal(label)
		if handle := st.handles[s.ID]; handle != "" {
			line += "  #" + handle
		}
		if s.ModelID != "" {
			line += "  (" + sanitizeTerminal(s.ModelID) + ")"
		}
		if s.ID == current {
			line += "  [current]"
		}
		if s.Kind == client.SessionKindTeamMember && s.Relationship.MemberName != "" {
			line += "  [member " + sanitizeTerminal(s.Relationship.MemberName) + "]"
		}
		if i == st.cursor {
			line = th.Style("accent").Render(line)
		}
		b.WriteString(line + "\n")
	}
}

func sessionActionsHint(st sessionsState, hk helpKeys) string {
	selected := client.SessionListItem{}
	if st.cursor >= 0 && st.cursor < len(st.filtered) {
		selected = st.filtered[st.cursor]
	}
	action := "unavailable"
	if selected.Capabilities.PublicChat {
		action = "continue"
	} else if selected.Capabilities.Inspect {
		action = "inspect"
	}
	actions := []string{hk.choose + ": " + action}
	for _, action := range []struct {
		enabled bool
		label   string
	}{
		{selected.Capabilities.CopyID, "y: copy ID"},
		{selected.Capabilities.ViewTranscript, "v: view"},
		{selected.Capabilities.Fork, "f: fork"},
		{selected.Capabilities.Rename, "r: rename"},
		{selected.Capabilities.Delete, "d: delete"},
	} {
		if action.enabled {
			actions = append(actions, action.label)
		}
	}
	return strings.Join(actions, "  ") + "  " + sessionsPanelHint(hk, st.startup)
}

func sessionsPanelHint(hk helpKeys, startup bool) string {
	if startup {
		return hk.nextTab + ": switch  n: new chat  " + hk.closeOnly + ": quit"
	}
	return hk.nextTab + ": switch  " + hk.closeOnly + ": close"
}

func renderSessionsTranscript(th theme.Theme, st sessionsState, _ string, vpContent string, hk helpKeys, _, _ int) string {
	var b strings.Builder
	label := st.selected.Title
	if label == "" {
		label = "session"
	}
	if st.loading {
		b.WriteString(th.Style("title").Render("loading authoritative transcript") + "\n\n")
		b.WriteString(th.Style("muted").Render("Loading " + sanitizeTerminal(label) + "…"))
		b.WriteString("\n" + th.Style("muted").Render(hk.closeOnly+": Back"))
		return b.String()
	}
	if st.loadErr != nil {
		b.WriteString(th.Style("title").Render("transcript unavailable") + "\n\n")
		b.WriteString(th.Style("errorText").Render("The authoritative transcript could not be loaded. Continuation remains disabled."))
		b.WriteString("\n" + th.Style("muted").Render("r: Retry  "+hk.closeOnly+": Back"))
		return b.String()
	}
	b.WriteString(th.Style("muted").Render("Inspecting "+sanitizeTerminal(label)+" · read-only") + "\n")
	if st.transcript.isEmpty() {
		b.WriteString(th.Style("muted").Render("(empty authoritative transcript)"))
	} else {
		b.WriteString(vpContent)
	}
	b.WriteString(th.Style("muted").Render(hk.closeOnly + ": Back"))
	return b.String()
}
