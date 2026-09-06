package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

const titleGenerationExhausted = "exhausted"

func (m Model) onTitleRenamed(msg client.SessionRenamedMsg) (Model, tea.Cmd) {
	if msg.SessionID != m.sessionID || msg.RequestToken != m.titleRenameRequestToken {
		return m, nil
	}
	if msg.Err != nil {
		if m.deps.Session != nil {
			return m, client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, msg.SessionID)
		}
		return m, nil
	}
	m.sessionTitle = msg.Title
	m.sessionTitleProvenance = msg.TitleProvenance
	if sessions := sessionsSurface(&m); sessions != nil {
		for i := range sessions.sessions {
			if sessions.sessions[i].ID == msg.SessionID {
				sessions.sessions[i].Title = msg.Title
				sessions.sessions[i].TitleProvenance = msg.TitleProvenance
			}
		}
		sessions.syncFilter()
	}
	return m, nil
}

// onSessionTitle applies a current live session.title update. Live-reader generation
// guards keep events from a former session out of this reducer; snapshot refetches
// remain authoritative after reconnect and adoption.
func (m Model) onSessionTitle(msg client.SessionTitleMsg) Model {
	if msg.Title != "" {
		m.sessionTitle = msg.Title
	}
	m.sessionTitleProvenance = msg.Provenance
	if sessions := sessionsSurface(&m); sessions != nil {
		for i := range sessions.sessions {
			if sessions.sessions[i].ID == m.sessionID {
				sessions.sessions[i].Title = m.sessionTitle
				sessions.sessions[i].TitleProvenance = msg.Provenance
			}
		}
		sessions.syncFilter()
	}
	if msg.GenerationState == titleGenerationExhausted && msg.LatestAttempt.ID != "" {
		if msg.LatestAttempt.ID == m.titleFailedAttemptID {
			return m
		}
		m.titleFailedAttemptID = msg.LatestAttempt.ID
		m.conv.addNotice("Automatic title generation did not complete. The current title was kept; use /title <text> to set one.")
		m.refreshView()
	}
	return m
}

func (m Model) adoptTitle(title, provenance string) Model {
	if title != "" {
		m.sessionTitle = title
	}
	if provenance != "" {
		m.sessionTitleProvenance = provenance
	}
	return m
}

func titleProvenanceLabel(provenance string) string {
	switch strings.ToLower(provenance) {
	case "generated", "operator":
		return strings.ToLower(provenance)
	case "first-prompt":
		return "first prompt"
	default:
		// Older servers exposed a title without provenance; it may have been seeded
		// from a first prompt, but that cannot be known from the compatibility field.
		return "unknown"
	}
}
