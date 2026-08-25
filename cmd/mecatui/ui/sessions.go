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
		adopter:                       m.deps.Adoption,
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

func (m Model) openSessions() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Sessions == nil || m.deps.Transcript == nil {
		return m, nil
	}
	m.ta.Blur()
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

func (m Model) adoptionBindings() client.AdoptionBindings {
	provider, model := m.effectiveModel.ProviderID, m.effectiveModel.ModelID
	if provider == "" && model == "" {
		provider, model = m.deps.InitialModel.ProviderID, m.deps.InitialModel.ModelID
	}
	return client.AdoptionBindings{
		Workspace: m.activeWorkspace, EnvironmentKind: "local", EnvironmentID: m.activeWorkspace,
		ProviderID: provider, ModelID: model,
	}
}

func (m Model) adoptionPreflightCmd(row client.SessionListItem) tea.Cmd {
	if row.Kind != client.SessionKindUnknown || m.deps.Adoption == nil {
		return nil
	}
	bindings := m.adoptionBindings()
	if !completeAdoptionBindings(bindings) {
		return nil
	}
	return client.PreflightSessionAdoptionCmd(m.deps.Ctx, m.deps.Adoption, row.ID, bindings)
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
	m.sessionState = row.State
	m.sessionCreatedAt = row.CreatedAt
	m.sessionModifiedAt = row.ModifiedAt
	m.activeWorkspace = row.Workspace
	m.conv = loaded
	m.restartedThisRun = true
	m.closeModal()
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
