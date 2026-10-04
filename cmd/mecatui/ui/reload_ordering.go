package ui

import (
	"time"

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

type snapshotRetryMsg struct {
	seq     uint64
	session string
	feedGen uint64
}

// Use the live reconnect loop's 500ms-to-30s bounded exponential schedule.
func snapshotRetryDelay(attempt int) time.Duration {
	delay := 500 * time.Millisecond
	for i := 1; i < attempt && delay < 30*time.Second; i++ {
		delay *= 2
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func (m Model) snapshotRetryCmd(reply snapshotRetryMsg) tea.Cmd {
	delay := snapshotRetryDelay(m.reloadRetryAttempt)
	if m.reloadRetryTimer != nil {
		return m.reloadRetryTimer(delay, reply)
	}
	return func() tea.Msg {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			return reply
		case <-m.deps.Ctx.Done():
			return nil
		}
	}
}

func (m Model) onSnapshotRetry(msg snapshotRetryMsg) (tea.Model, tea.Cmd) {
	if !m.reloadPending || !m.reloadRetryScheduled || msg.seq != m.reloadSeq || msg.session != m.sessionID || msg.feedGen != m.liveGen || msg.session != m.reloadSession {
		return m, nil
	}
	m.reloadRetryScheduled = false
	return m, (&m).refreshSessionCmd()
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
	m.reloadRetryScheduled = false
	reply := snapshotReply{seq: m.reloadSeq, session: m.sessionID, feedGen: m.reloadFeedGen, liveEpoch: m.reloadLiveEpoch, modeIntent: m.modeIntentSeq, titleIntent: m.titleRenameRequestToken}
	return func() tea.Msg {
		reply.msg = cmd().(client.ResolvedModelMsg)
		return reply
	}
}

func (m Model) onSnapshotReply(reply snapshotReply) (tea.Model, tea.Cmd) {
	if m.ignoreSnapshotReply(reply) {
		return m, nil
	}
	if reply.msg.Err != nil {
		return m.handleSnapshotFailure(reply)
	}
	if m.reloadRequiresReconnect(reply) {
		m.resetIncompleteReload()
		return m, (&m).startReconnect(nil)
	}
	bufferedResults := reloadBufferedResults(m.reloadEvents)
	// A result in the replacement feed can be the only terminal fact. Install
	// just the title baseline, then replay the result exactly once against the
	// previously confirmed usage and refetch its cumulative total after settlement.
	if bufferedResults > 0 {
		m.applySnapshotTitleAfterBufferedResults(reply, bufferedResults)
	} else if reply.liveEpoch != m.reloadLiveEpoch {
		if m.phase != phaseRunning && m.phase != phaseAwaitingApproval && m.deps.Session != nil {
			return m, (&m).refreshSessionCmd()
		}
		m.reloadNeedRefresh = true
		return m, nil
	} else {
		// The server can commit the run before its terminal reaches this UI.
		// Neither its ledger nor its occupancy can be ordered against that result
		// until the run settles and a fresh GetSession completes.
		return m.applyCurrentSnapshot(reply, m.phase == phaseRunning || m.phase == phaseAwaitingApproval)
	}
	m.reloadApplied = reply.seq
	return m.replayReloadEvents(nil, !reply.msg.MainUsagePresent)
}

func (m Model) ignoreSnapshotReply(reply snapshotReply) bool {
	return reply.session != m.sessionID || reply.msg.SessionID != m.sessionID || reply.seq != m.reloadSeq || reply.seq <= m.reloadApplied
}

func (m Model) handleSnapshotFailure(reply snapshotReply) (tea.Model, tea.Cmd) {
	if !m.reloadPending || m.reloadRetryScheduled {
		return m, nil
	}
	if m.reloadRequiresReconnect(reply) {
		m.resetFailedReload()
		return m, (&m).startReconnect(nil)
	}
	m.reloadRetryAttempt++
	m.reloadRetryScheduled = true
	return m, m.snapshotRetryCmd(snapshotRetryMsg{seq: reply.seq, session: reply.session, feedGen: reply.feedGen})
}

func (m Model) reloadRequiresReconnect(reply snapshotReply) bool {
	return m.reloadPending && (reply.feedGen != m.liveGen || m.reloadOverflow || reply.session != m.reloadSession)
}

func (m *Model) resetFailedReload() {
	m.reloadPending = false
	m.reloadEvents = nil
	m.disarmLiveFeed()
}

func (m *Model) resetIncompleteReload() {
	m.reloadPending = false
	m.reloadEvents = nil
	m.reloadOverflow = false
	m.reloadNeedRefresh = false
	m.disarmLiveFeed()
}

func reloadBufferedResults(events []tea.Msg) uint64 {
	var count uint64
	for _, event := range events {
		if _, ok := event.(client.ResultMsg); ok {
			count++
		}
	}
	return count
}

func (m *Model) applySnapshotTitleAfterBufferedResults(reply snapshotReply, bufferedResults uint64) {
	m.reloadNeedRefresh = true
	// If something besides these buffered results changed the projection during
	// fetch, leave even the title for the next authoritative fetch.
	if reply.liveEpoch+bufferedResults == m.reloadLiveEpoch && reply.msg.TitleMetadataPresent && reply.titleIntent == m.titleRenameRequestToken {
		m.sessionTitle = reply.msg.Title
		m.sessionTitleProvenance = reply.msg.TitleProvenance
		m.sessionTitleRevision = reply.msg.TitleRevision
	}
}

func (m Model) applyCurrentSnapshot(reply snapshotReply, deferCounters bool) (tea.Model, tea.Cmd) {
	m.reloadNeedRefresh = deferCounters
	if deferCounters {
		reply.msg.MainUsagePresent = false
		reply.msg.ContextOccupancy = nil
	} else {
		m.reloadMainUsagePresent = reply.msg.MainUsagePresent
	}
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
	return m.replayReloadEvents(cmd, false)
}

func (m *Model) refreshAfterSettledRun() tea.Cmd {
	if !m.reloadNeedRefresh || m.deps.Session == nil || m.sessionID == "" || m.phase != phaseIdle {
		return nil
	}
	m.reloadNeedRefresh = false
	return m.refreshSessionCmd()
}

func (m Model) replayReloadEvents(cmd tea.Cmd, legacyUsage bool) (tea.Model, tea.Cmd) {
	m.reloadRetryAttempt = 0
	m.reloadRetryScheduled = false
	if m.reloadPending {
		m.reloadPending = false
		for _, event := range m.reloadEvents {
			switch title := event.(type) {
			case client.SessionTitleMsg:
				m = m.onSessionTitle(title)
			default:
				// A present cumulative ledger may already contain this result.
				// Without that ledger, retain the incremental legacy fallback.
				if result, ok := event.(client.ResultMsg); ok && !legacyUsage {
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
