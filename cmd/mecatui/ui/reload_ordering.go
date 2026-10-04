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
	if reply.session != m.sessionID || reply.msg.SessionID != m.sessionID || reply.seq != m.reloadSeq || reply.seq <= m.reloadApplied {
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
	bufferedResults := 0
	for _, event := range m.reloadEvents {
		if _, ok := event.(client.ResultMsg); ok {
			bufferedResults++
		}
	}
	// A result in the replacement feed can be the only terminal fact. Install
	// just the title baseline, then replay the result exactly once against the
	// previously confirmed usage and refetch its cumulative total after settlement.
	if bufferedResults > 0 {
		m.reloadNeedRefresh = true
		// If something besides these buffered results changed the projection
		// during fetch, leave even the title for the next authoritative fetch.
		if reply.liveEpoch+uint64(bufferedResults) == m.reloadLiveEpoch && reply.msg.TitleMetadataPresent && reply.titleIntent == m.titleRenameRequestToken {
			m.sessionTitle = reply.msg.Title
			m.sessionTitleProvenance = reply.msg.TitleProvenance
			m.sessionTitleRevision = reply.msg.TitleRevision
		}
	} else if reply.liveEpoch != m.reloadLiveEpoch {
		if m.phase != phaseRunning && m.phase != phaseAwaitingApproval && m.deps.Session != nil {
			cmd := (&m).refreshSessionCmd()
			return m, cmd
		}
		m.reloadNeedRefresh = true
		return m, nil
	} else {
		m.reloadNeedRefresh = false
		// A newer operator intent must remain pending after the fetch.
		if reply.modeIntent != m.modeIntentSeq {
			reply.msg.Mode = ""
		}
		if reply.titleIntent != m.titleRenameRequestToken {
			reply.msg.TitleMetadataPresent = false
		}
		var cmd tea.Cmd
		m, cmd, _ = m.onResolvedModelMsg(reply.msg)
		m.reloadApplied = reply.seq
		return m.replayReloadEvents(cmd)
	}
	m.reloadApplied = reply.seq
	return m.replayReloadEvents(nil)
}

func (m *Model) refreshAfterSettledRun() tea.Cmd {
	if !m.reloadNeedRefresh || m.deps.Session == nil || m.sessionID == "" || m.phase != phaseIdle {
		return nil
	}
	m.reloadNeedRefresh = false
	return m.refreshSessionCmd()
}

func (m Model) replayReloadEvents(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if m.reloadPending {
		m.reloadPending = false
		for _, event := range m.reloadEvents {
			switch title := event.(type) {
			case client.SessionTitleMsg:
				m = m.onSessionTitle(title)
			default:
				// A result must settle the run, but its incremental usage may
				// already be in GetSession or Converse. The follow-up snapshot
				// supplies the cumulative ledger exactly once.
				if result, ok := event.(client.ResultMsg); ok {
					result.Usage = client.Usage{}
					event = result
				}
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
