package ui

// These fixtures retain the concise state setup used by sessions tests while
// exercising the production surface initializer for its ambient wiring.
func newSessionsPanelState() sessionsState {
	var m Model
	return *m.newSessionsSurface(false)
}

func setActiveSessions(m *Model, state sessionsState) {
	initialized := m.newSessionsSurface(state.startup)
	deps := initialized.deps
	pageRequestToken := initialized.pageRequestToken
	transcriptSurfaceRequestToken := initialized.transcriptSurfaceRequestToken
	pager := initialized.pager
	transcripter := initialized.transcripter
	healthFetcher := initialized.healthFetcher
	cleanup := initialized.cleanup
	forker := initialized.forker
	manager := initialized.manager
	clipboard := initialized.clipboard
	actionRequestToken := initialized.actionRequestToken
	*initialized = state
	initialized.deps = deps
	initialized.activeSessionID = m.sessionID
	initialized.pageRequestToken = pageRequestToken
	initialized.transcriptSurfaceRequestToken = transcriptSurfaceRequestToken
	initialized.pager = pager
	initialized.transcripter = transcripter
	initialized.healthFetcher = healthFetcher
	initialized.cleanup = cleanup
	initialized.forker = forker
	initialized.manager = manager
	initialized.clipboard = clipboard
	initialized.actionRequestToken = actionRequestToken
}

func ensureActiveSessions(m *Model) *sessionsState {
	if m.modal == nil {
		setActiveSessions(m, newSessionsPanelState())
	}
	return sessionsSurface(m)
}
