package ui

import (
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

type sessionDetailsView struct {
	ID            string
	DebugTargetID string
	Title         string
	State         string
	Placement     client.Placement
	CreatedAt     int64
	ModifiedAt    int64
	ProviderID    string
	ModelID       string
}

type debugTargetSource interface {
	DebugTargetID() string
}

func (m Model) syncDebugTarget() Model {
	if source, ok := m.deps.Session.(debugTargetSource); ok {
		if target := source.DebugTargetID(); target != "" {
			m.deps.DebugTarget = target
		}
	}
	return m
}

type sessionIDCopyResultMsg struct {
	id          string
	debugTarget bool
	err         error
}

func (m *Model) newSessionsSurface(startup bool) *sessionsState {
	ti := textinput.New()
	ti.Placeholder = "search sessions…"
	ti.SetWidth(40)
	ti.Focus()

	m.sessionsActionRequestToken++
	state := &sessionsState{
		view:                          sessionsPanel,
		startup:                       startup,
		tab:                           tabChats,
		loading:                       true,
		loadState:                     sessionsInitialLoading,
		filter:                        ti,
		deps:                          m.surfaceDeps(),
		activeSessionID:               m.sessionID,
		pageRequestToken:              m.sessionsPageRequestToken,
		transcriptSurfaceRequestToken: m.sessionsTranscriptRequestToken,
		pager:                         m.deps.Sessions,
		transcripter:                  m.deps.Transcript,
		healthFetcher:                 m.deps.StorageHealth,
		migration:                     m.deps.Migration,
		cleanup:                       m.deps.Cleanup,
		forker:                        m.deps.Session,
		manager:                       m.deps.SessionManagement,
		clipboard:                     m.deps.Clipboard,
		actionRequestToken:            m.sessionsActionRequestToken,
	}
	m.modal = state
	return state
}

func sessionsSurface(m *Model) *sessionsState {
	s, _ := m.modal.(*sessionsState)
	return s
}

func (m Model) bindSessionID(id string) Model {
	if id != m.sessionID {
		m.compactPending = false
		m.compactRequestToken++
		if m.clearPending != nil && m.clearPending.sourceID != id {
			m.clearPending = nil
		}
		if m.authorization.controlCancel != nil {
			m.authorization.controlCancel()
		}
		m.authorization.stopFirstEventTimer()
		m.authorization = mcpAuthorizationState{}
		m.authorizationEvents = nil
	}
	m.sessionID = id
	m.sessionState = ""
	m.sessionCreatedAt = 0
	m.sessionModifiedAt = 0
	return m
}

func (m Model) sessionDetails() sessionDetailsView {
	return sessionDetailsView{
		ID: m.sessionID, DebugTargetID: m.deps.DebugTarget, Title: m.sessionTitle, State: m.sessionState,
		Placement: m.activePlacement, CreatedAt: m.sessionCreatedAt,
		ModifiedAt: m.sessionModifiedAt, ProviderID: m.resolvedSessionModel.ProviderID,
		ModelID: m.resolvedSessionModel.ModelID,
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
	m.prompt.Blur()
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
		return m, m.prompt.Focus(), true
	}
	debugTarget := msg.String() == "t" && m.deps.DebugTarget != ""
	if msg.String() != "c" && !debugTarget {
		return m, nil, true
	}
	id := m.sessionCopyTarget()
	label := "session ID"
	if debugTarget {
		id = m.deps.DebugTarget
		label = "debug target ID"
	}
	if id == "" || !utf8.ValidString(id) {
		m.statusMsg = m.deps.Theme.Style("warning").Render("no exact " + label + " to copy")
		return m, nil, true
	}
	if m.deps.Clipboard == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy " + label + ": clipboard unavailable")
		return m, nil, true
	}
	cb, ctx := m.deps.Clipboard, m.deps.Ctx
	return m, func() tea.Msg {
		err := cb.Write(ctx, "text/plain", []byte(id))
		return sessionIDCopyResultMsg{id: id, debugTarget: debugTarget, err: err}
	}, true
}

func (m Model) onSessionIDCopyResult(msg sessionIDCopyResultMsg) Model {
	current, label := m.sessionID, "session ID"
	if msg.debugTarget {
		current, label = m.deps.DebugTarget, "debug target ID"
	}
	if msg.id == "" || msg.id != current {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy " + label + ": active session changed")
		return m
	}
	if msg.err != nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy " + label + ": " + sanitizeTerminal(msg.err.Error()))
		return m
	}
	m.statusMsg = m.deps.Theme.Style("success").Render("copied " + label)
	return m
}

func formatSessionTimestamp(unixSec int64) string {
	if unixSec <= 0 {
		return unknownLabel
	}
	return time.Unix(unixSec, 0).UTC().Format(time.RFC3339)
}

func renderSessionDetails(th theme.Theme, details sessionDetailsView, hk helpKeys, width, height int) string {
	unknown := func(value string) string {
		if value == "" {
			return unknownLabel
		}
		return sanitizeTerminal(value)
	}
	budget := cardTextWidth(width)
	row := func(label, value string) string { return wrapCardText(label+unknown(value), budget) }
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Active session") + "\n\n")
	b.WriteString(wrapCardText("ID: "+safeSessionID(details.ID), budget) + "\n")
	if details.DebugTargetID != "" {
		b.WriteString(wrapCardText("Debug target ID: "+safeSessionID(details.DebugTargetID), budget) + "\n")
	}
	b.WriteString(row("Title: ", details.Title) + "\n")
	b.WriteString(row("State: ", details.State) + "\n")
	b.WriteString(row("Placement: ", details.Placement.Label) + "\n")
	b.WriteString("Created: " + formatSessionTimestamp(details.CreatedAt) + "\n")
	b.WriteString("Modified: " + formatSessionTimestamp(details.ModifiedAt) + "\n")
	b.WriteString(row("Provider: ", details.ProviderID) + "\n")
	b.WriteString(row("Model: ", details.ModelID) + "\n\n")
	copyHelp := "c: copy exact ID"
	if details.DebugTargetID != "" {
		copyHelp += "  t: copy exact target ID"
	}
	b.WriteString(th.Style("muted").Render(copyHelp + "  " + hk.closeOnly + ": close"))
	return centerCard(th, b.String(), width, height)
}

func (m Model) openSessions() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Sessions == nil || m.deps.Transcript == nil {
		return m, nil
	}
	m.prompt.Blur()
	state := (&m).newSessionsSurface(false)
	pageCmd := state.beginPage("")
	cmds := []tea.Cmd{pageCmd, textinput.Blink}
	if m.caps.StorageHealth && m.deps.StorageHealth != nil {
		cmds = append(cmds, loadStorageHealthCmd(m.deps.Ctx, m.deps.StorageHealth))
	}
	if m.maintenanceMigrationJobID != "" && m.caps.StorageMigration && m.deps.Migration != nil {
		cmds = append(cmds, migrationStatusCmd(m.deps.Ctx, m.deps.Migration, m.maintenanceMigrationJobID))
	}
	if m.maintenanceCleanupJobID != "" && m.caps.StorageCleanup && m.deps.Cleanup != nil {
		cmds = append(cmds, cleanupStatusCmd(m.deps.Ctx, m.deps.Cleanup, m.maintenanceCleanupJobID))
	}
	return m, tea.Batch(cmds...)
}

func (m Model) chooseSession() (tea.Model, tea.Cmd, bool) {
	s := sessionsSurface(&m)
	if s == nil {
		return m, nil, false
	}
	cmd, handled, _ := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled {
		return m, nil, false
	}
	mm, intentCmd, _ := m.applySurfaceIntent(s.takeSurfaceIntent())
	return mm, tea.Batch(cmd, intentCmd), true
}

func (m Model) loadSessionTranscript(row client.SessionListItem, inspect bool) (tea.Model, tea.Cmd, bool) {
	if m.deps.Transcript == nil {
		return m, nil, false
	}
	var state *sessionsState
	if sessionsSurface(&m) == nil {
		state = (&m).newSessionsSurface(false)
	} else {
		state = sessionsSurface(&m)
	}
	cmd := state.openTranscript(row, inspect)
	mm, _, _ := m.applySurfaceIntent(state.takeSurfaceIntent())
	m = mm.(Model)
	return m, cmd, true
}

func (m Model) adoptAuthoritativeTranscript(row client.SessionListItem, loaded conversation) (tea.Model, tea.Cmd, bool) {
	m = m.endRun("")
	m = m.resetSession()
	m = m.bindSessionID(row.ID)
	m.sessionTitle = row.Title
	m.sessionTitleProvenance = row.TitleProvenance
	m.sessionTitleRevision = row.TitleRevision
	m.sessionState = row.State
	m.sessionCreatedAt = row.CreatedAt
	m.sessionModifiedAt = row.ModifiedAt
	m.activePlacement = row.Placement
	m.conv = loaded
	m.restartedThisRun = true
	m.closeModal()
	m.browsingStartupSessions = false
	m.phase = phaseIdle
	m.statusMsg = "continuing chat " + sanitizeTerminal(row.Title) + " — type to add a turn"
	cmd := m.prompt.Focus()
	if contextCmd := m.refreshStatusContextCmd(); contextCmd != nil {
		cmd = tea.Batch(cmd, contextCmd)
	}
	m.refreshView()
	if m.deps.Session != nil {
		cmd = tea.Batch(cmd, client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, row.ID))
	}
	cmd = tea.Batch(cmd, (&m).armLiveFeed())
	return m, cmd, true
}
