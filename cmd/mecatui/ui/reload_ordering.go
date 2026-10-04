package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

const reloadEventBufferLimit = 64

type snapshotReply struct {
	seq         uint64
	session     string
	feedGen     uint64
	liveEpoch   uint64
	modeIntent  uint64
	titleIntent uint64
	msg         client.ResolvedModelMsg
}

type modeReply struct {
	session string
	intent  uint64
	msg     client.ModeChangedMsg
}

func (m Model) setModeIntentCmd(mode string) tea.Cmd {
	cmd := client.SetModeCmd(m.deps.Ctx, m.deps.Session, m.sessionID, mode)
	return func() tea.Msg {
		return modeReply{session: m.sessionID, intent: m.modeIntentSeq, msg: cmd().(client.ModeChangedMsg)}
	}
}

// refreshSessionCmd orders all UI-issued GetSession responses by request, not arrival.
func (m *Model) refreshSessionCmd() tea.Cmd {
	return m.refreshSessionWithCmd(client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, m.sessionID))
}

func (m *Model) refreshSessionWithCmd(cmd tea.Cmd) tea.Cmd {
	m.reloadSeq++
	reply := snapshotReply{seq: m.reloadSeq, session: m.sessionID, feedGen: m.reloadFeedGen, liveEpoch: m.reloadLiveEpoch, modeIntent: m.modeIntentSeq, titleIntent: m.titleRenameRequestToken}
	return func() tea.Msg {
		reply.msg = cmd().(client.ResolvedModelMsg)
		return reply
	}
}

func (m Model) onSnapshotReply(reply snapshotReply) (tea.Model, tea.Cmd) {
	if reply.session != m.sessionID || reply.seq != m.reloadSeq || reply.seq <= m.reloadApplied {
		return m, nil
	}
	if reply.msg.Err != nil {
		// Retain the feed barrier for the failed-fetch retry owner (task 04).
		return m, nil
	}
	if m.reloadPending && (reply.feedGen != m.liveGen || m.reloadOverflow || reply.session != m.reloadSession) {
		m.reloadPending = false
		m.reloadEvents = nil
		m.reloadOverflow = false
		m.reloadNeedRefresh = false
		m.disarmLiveFeed()
		cmd := (&m).startReconnect(nil)
		return m, cmd
	}
	// A live result cannot be ordered against cumulative snapshot usage or
	// occupancy. Refetch after it settles instead of duplicating an increment.
	if reply.liveEpoch != m.reloadLiveEpoch {
		m.reloadNeedRefresh = true
		if m.phase != phaseRunning && m.phase != phaseAwaitingApproval && m.deps.Session != nil {
			m.reloadNeedRefresh = false
			cmd := (&m).refreshSessionCmd()
			return m, cmd
		}
		return m, nil
	}
	// A newer operator mode request must remain pending after the fetch.
	if reply.modeIntent != m.modeIntentSeq {
		reply.msg.Mode = ""
	}
	if reply.titleIntent != m.titleRenameRequestToken {
		reply.msg.TitleMetadataPresent = false
	}
	m.reloadApplied = reply.seq
	var cmd tea.Cmd
	m, cmd, _ = m.onResolvedModelMsg(reply.msg)
	if m.reloadPending {
		m.reloadPending = false
		for _, event := range m.reloadEvents {
			switch title := event.(type) {
			case client.SessionTitleMsg:
				m = m.onSessionTitle(title)
			default:
				var eventCmd tea.Cmd
				var reduced tea.Model
				reduced, eventCmd = m.updateStreamEvent(event)
				m = reduced.(Model)
				cmd = tea.Batch(cmd, eventCmd)
			}
		}
		m.reloadEvents = nil
	}
	return m, cmd
}
