package ui

func ensureActiveSessions(m *Model) *sessionsState {
	if m.modal == nil {
		setActiveSessions(m, newSessionsPanelState())
	}
	return sessionsSurface(m)
}
