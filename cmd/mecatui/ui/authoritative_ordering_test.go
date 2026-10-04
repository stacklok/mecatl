package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestMecatuiAuthoritativeReload_Scenario2_SnapshotResultOrdering(t *testing.T) {
	for _, order := range []string{"snapshot first", "result first", "legacy missing ledger"} {
		t.Run(order, func(t *testing.T) {
			conv := &fakeConv{getSessionSnapshots: []client.SessionSnapshot{{MainUsagePresent: true, Usage: client.Usage{InputTokens: 40}}}}
			m := modeTestModel(t, conv)
			m.phase = phaseRunning
			m.usage = client.Usage{InputTokens: 30}
			m.contextTokens = 20
			m.reloadSeq = 1
			m.liveGen, m.reloadFeedGen = 7, 7
			result := client.ResultMsg{Stop: stopError, Error: "run failed", Usage: client.Usage{InputTokens: 10}}
			stale := snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, MainUsagePresent: true, Usage: client.Usage{InputTokens: 40}, ContextOccupancy: &client.ContextOccupancy{InputTokens: 5}}}
			switch order {
			case "snapshot first":
				// GetSession already counted the still-running run, but the UI has not received its terminal.
				m = applyAll(m, stale)
				if m.usage.InputTokens != 30 || m.contextTokens != 20 {
					t.Fatalf("in-flight snapshot installed uncertain counters: usage=%+v occupancy=%d", m.usage, m.contextTokens)
				}
				mm, reconcile := m.Update(result)
				m = mm.(Model)
				if reconcile == nil {
					t.Fatal("settled run did not schedule authoritative reconciliation")
				}
				var fetched bool
				for _, event := range flattenBatch(reconcile) {
					if reply, ok := event.(snapshotReply); ok {
						m = applyAll(m, reply)
						fetched = true
					}
				}
				if !fetched {
					t.Fatal("settled run did not fetch cumulative usage")
				}
			case "result first":
				m = applyAll(m, result)
				mm, retry := m.Update(stale)
				m = mm.(Model)
				if retry == nil || m.usage.InputTokens != 40 || m.contextTokens != 20 {
					t.Fatalf("stale snapshot did not fence result and occupancy: usage=%+v occupancy=%d retry=%t", m.usage, m.contextTokens, retry != nil)
				}
				m = applyAll(m, retry())
			case "legacy missing ledger":
				m.reloadPending, m.reloadSession = true, m.sessionID
				m = applyAll(m, liveMsg{gen: 7, msg: result})
				stale.msg.MainUsagePresent = false
				m = applyAll(m, stale)
				if m.usage.InputTokens != 40 {
					t.Fatalf("buffered result disappeared without main ledger: usage=%+v", m.usage)
				}
				return
			}
			if m.usage.InputTokens != 40 {
				t.Fatalf("run usage double-counted or lost: usage=%+v", m.usage)
			}
		})
	}
}

func TestMecatuiAuthoritativeReload_Scenario2_SnapshotLiveRace(t *testing.T) {
	opened := &reconnectLiveStreamer{}
	bound := modeTestModel(t, &fakeConv{})
	bound.deps.LiveStream = opened
	bound.liveReconGen = 1
	mm, fetch := bound.updateReconnectMsg(reconnectMsg{gen: 1, msg: client.LiveReconnectedMsg{}})
	bound = mm.(Model)
	defer bound.disarmLiveFeed()
	if opened.opens.Load() == 0 || fetch == nil || !bound.reloadPending {
		t.Fatalf("replacement feed not opened before snapshot fetch: opens=%d pending=%t fetch=%t", opened.opens.Load(), bound.reloadPending, fetch != nil)
	}

	m := titleModel(t, &titleRenamer{})
	m.liveGen = 7
	m.reloadFeedGen = 7
	m.reloadPending = true
	m.reloadSession = m.sessionID
	m.reloadSeq = 1
	m = applyAll(m, liveMsg{gen: 7, msg: client.SessionTitleMsg{Title: "new", Revision: 5}})
	m = applyAll(m, liveMsg{gen: 7, msg: client.DeliveryNoteMsg{ScheduleName: "job", FireID: "fire-1", Text: fencedDeliveryText("job", "fire-1")}})
	if len(m.conv.testBlocks()) != 0 {
		t.Fatal("feed delivery became visible before snapshot")
	}
	if m.sessionTitle == "new" {
		t.Fatal("feed event became visible before snapshot")
	}
	m = applyAll(m, snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, TitleMetadataPresent: true, Title: "snapshot", TitleRevision: 4}})
	if m.sessionTitle != "new" || m.sessionTitleRevision != 5 || len(m.conv.testBlocks()) != 1 {
		t.Fatalf("buffered feed event lost: title=%q/%d blocks=%d", m.sessionTitle, m.sessionTitleRevision, len(m.conv.testBlocks()))
	}
	m = applyAll(m, liveMsg{gen: 7, msg: client.SessionTitleMsg{Title: "late duplicate", Revision: 5}})
	m = applyAll(m, liveMsg{gen: 6, msg: client.SessionTitleMsg{Title: "retired", Revision: 9}})
	if m.sessionTitle != "new" {
		t.Fatalf("duplicate/retired event applied: %q", m.sessionTitle)
	}
	m.reloadPending = true
	m.reloadSeq = 2
	m.reloadFeedGen = 7
	m = applyAll(m, liveMsg{gen: 7, msg: client.SessionTitleMsg{Title: "duplicate in feed", Revision: 6}})
	m = applyAll(m, snapshotReply{seq: 2, session: m.sessionID, feedGen: 7, liveEpoch: m.reloadLiveEpoch, msg: client.ResolvedModelMsg{SessionID: m.sessionID, TitleMetadataPresent: true, Title: "included by snapshot", TitleRevision: 6}})
	if m.sessionTitle != "included by snapshot" {
		t.Fatalf("event already included by snapshot reapplied: %q", m.sessionTitle)
	}
	m.reloadPending = true
	m.reloadSeq = 3
	m.reloadFeedGen = 7
	m.deps.LiveStream = &reconnectLiveStreamer{}
	for i := 0; i < reloadEventBufferLimit; i++ {
		m = applyAll(m, liveMsg{gen: 7, msg: client.SessionTitleMsg{Title: "overflow", Revision: uint64(i + 6)}})
	}
	mm, cmd := m.Update(liveMsg{gen: 7, msg: client.SessionTitleMsg{Title: "overflow", Revision: 100}})
	m = mm.(Model)
	defer m.disarmReconnect()
	if cmd == nil || !m.reloadOverflow || !m.liveReconnecting || m.sessionTitle != "included by snapshot" {
		t.Fatalf("overflow did not immediately reconcile: reconnect=%t cmd=%t title=%q", m.liveReconnecting, cmd != nil, m.sessionTitle)
	}
	m = applyAll(m, snapshotReply{seq: 3, session: m.sessionID, feedGen: 7, liveEpoch: m.reloadLiveEpoch, msg: client.ResolvedModelMsg{SessionID: m.sessionID, TitleMetadataPresent: true, Title: "stale", TitleRevision: 4}})
	if m.sessionTitle != "included by snapshot" {
		t.Fatalf("abandoned snapshot after overflow replaced title: %q", m.sessionTitle)
	}

	disconnected := titleModel(t, &titleRenamer{})
	disconnected.deps.LiveStream = &reconnectLiveStreamer{}
	disconnected.liveGen, disconnected.reloadFeedGen = 7, 7
	disconnected.reloadSession, disconnected.reloadPending, disconnected.reloadSeq = disconnected.sessionID, true, 4
	mm, cmd = disconnected.Update(liveMsg{gen: 7, msg: client.StreamClosedMsg{}})
	disconnected = mm.(Model)
	defer disconnected.disarmReconnect()
	disconnected = applyAll(disconnected, snapshotReply{seq: 4, session: disconnected.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: disconnected.sessionID, TitleMetadataPresent: true, Title: "disconnected"}})
	if cmd == nil || !disconnected.liveReconnecting || disconnected.sessionTitle == "disconnected" {
		t.Fatalf("second disconnect accepted incomplete snapshot: reconnect=%t title=%q", disconnected.liveReconnecting, disconnected.sessionTitle)
	}
}

func TestMecatuiAuthoritativeReload_Scenario2_FailedFetchPreservesState(t *testing.T) {
	conv := &fakeConv{getSessionSnapshots: []client.SessionSnapshot{{TitleMetadataPresent: true, Title: "server", TitleRevision: 4, Mode: "plan"}}}
	m := modeTestModel(t, conv)
	m.sessionTitle, m.sessionTitleRevision, m.activeMode = "confirmed", 3, "default"
	m.usage = client.Usage{InputTokens: 10}
	m.liveGen, m.reloadFeedGen, m.reloadSeq = 7, 7, 1
	m.reloadPending, m.reloadSession = true, m.sessionID
	var delays []time.Duration
	m.reloadRetryTimer = func(delay time.Duration, msg snapshotRetryMsg) tea.Cmd {
		delays = append(delays, delay)
		return func() tea.Msg { return msg }
	}
	m = applyAll(m, liveMsg{gen: 7, msg: client.SessionTitleMsg{Title: "live", Revision: 5}})
	mm, retry := m.Update(snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, TitleMetadataPresent: true, Title: "partial", Mode: "ask", MainUsagePresent: true, Usage: client.Usage{InputTokens: 99}, Err: errors.New("fetch failed")}})
	m = mm.(Model)
	if retry == nil || len(delays) != 1 || delays[0] != 500*time.Millisecond || !strings.Contains(stripANSIstr(m.renderFooter()), "metadata not synchronized") {
		t.Fatalf("failure did not schedule bounded retry or display degraded state: cmd=%t delays=%v footer=%q", retry != nil, delays, m.idleFooterLeft())
	}
	if m.sessionTitle != "confirmed" || m.activeMode != "default" || m.usage.InputTokens != 10 || !m.reloadPending || len(m.reloadEvents) != 1 {
		t.Fatalf("failed fetch adopted partial state or released barrier: title=%q mode=%q usage=%+v pending=%t events=%d", m.sessionTitle, m.activeMode, m.usage, m.reloadPending, len(m.reloadEvents))
	}
	mm, fetch := m.Update(retry())
	m = mm.(Model)
	if fetch == nil || m.reloadSeq != 2 {
		t.Fatalf("retry timer did not issue a new fetch: cmd=%t seq=%d", fetch != nil, m.reloadSeq)
	}
	mm, retry2 := m.Update(snapshotReply{seq: 2, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, Title: "partial again", Err: errors.New("still unavailable")}})
	m = mm.(Model)
	if retry2 == nil || len(delays) != 2 || delays[1] != time.Second || m.sessionTitle != "confirmed" || !m.reloadPending {
		t.Fatalf("second fetch did not back off and preserve confirmed state: delays=%v title=%q pending=%t", delays, m.sessionTitle, m.reloadPending)
	}
	mm, fetch = m.Update(retry2())
	m = mm.(Model)
	if fetch == nil || m.reloadSeq != 3 {
		t.Fatalf("second retry did not fetch: cmd=%t seq=%d", fetch != nil, m.reloadSeq)
	}
	m = applyAll(m, liveMsg{gen: 7, msg: client.SessionTitleMsg{Title: "later", Revision: 6}})
	m = applyAll(m, fetch())
	if m.reloadPending || m.sessionTitle != "later" || m.sessionTitleRevision != 6 || m.activeMode != "plan" || strings.Contains(stripANSIstr(m.renderFooter()), "not synchronized") {
		t.Fatalf("success did not install snapshot then replay buffered events: title=%q/%d mode=%q pending=%t footer=%q", m.sessionTitle, m.sessionTitleRevision, m.activeMode, m.reloadPending, m.idleFooterLeft())
	}
	// A later failure must not duplicate its timer; a session switch cancels it.
	m.reloadPending, m.reloadSession, m.reloadFeedGen = true, m.sessionID, 7
	m.reloadSeq = 4
	mm, retry = m.Update(snapshotReply{seq: 4, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, Err: errors.New("again")}})
	m = mm.(Model)
	mm, duplicate := m.Update(snapshotReply{seq: 4, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, Err: errors.New("duplicate")}})
	m = mm.(Model)
	if retry == nil || duplicate != nil || len(delays) != 3 || delays[2] != 500*time.Millisecond || snapshotRetryDelay(100) != 30*time.Second {
		t.Fatalf("retry timer duplicated, not reset, or unbounded: delays=%v", delays)
	}
	m = m.resetSession()
	mm, stale := m.Update(retry())
	m = mm.(Model)
	if stale != nil || m.reloadPending || m.sessionTitle == "server" {
		t.Fatalf("old-session retry escaped reset: cmd=%t pending=%t title=%q", stale != nil, m.reloadPending, m.sessionTitle)
	}

	for _, exit := range []string{"disconnect", "quit"} {
		t.Run(exit, func(t *testing.T) {
			m := modeTestModel(t, &fakeConv{})
			m.liveGen, m.reloadFeedGen, m.reloadSeq = 7, 7, 1
			m.reloadPending, m.reloadSession = true, m.sessionID
			m.reloadRetryTimer = func(_ time.Duration, msg snapshotRetryMsg) tea.Cmd { return func() tea.Msg { return msg } }
			mm, retry := m.Update(snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, Err: errors.New("failed")}})
			m = mm.(Model)
			if retry == nil {
				t.Fatal("no retry to cancel")
			}
			if exit == "disconnect" {
				m.deps.LiveStream = &reconnectLiveStreamer{}
				mm, reconnect := m.Update(liveMsg{gen: 7, msg: client.StreamClosedMsg{}})
				m = mm.(Model)
				defer m.disarmReconnect()
				if reconnect == nil || !m.liveReconnecting {
					t.Fatal("second feed failure did not re-enter reconnection")
				}
			} else {
				mm, _ = m.quitNow()
				m = mm.(Model)
			}
			mm, fetch := m.Update(retry())
			m = mm.(Model)
			if fetch != nil || m.reloadPending {
				t.Fatalf("abandoned retry still fetched: cmd=%t pending=%t", fetch != nil, m.reloadPending)
			}
		})
	}
}

func TestMecatuiAuthoritativeReload_Scenario2_StaleResponses(t *testing.T) {
	conv := &fakeConv{getSessionSnapshots: []client.SessionSnapshot{{TitleMetadataPresent: true, Title: "older", Mode: "default"}, {TitleMetadataPresent: true, Title: "newer", Mode: "plan"}}}
	ordered := modeTestModel(t, conv)
	first := (&ordered).refreshSessionCmd()
	second := (&ordered).refreshSessionCmd()
	oldReply, newReply := first(), second()
	ordered = applyAll(ordered, newReply, oldReply)
	if ordered.sessionTitle != "newer" || ordered.activeMode != "plan" {
		t.Fatalf("out-of-order GetSession replies = %q/%q", ordered.sessionTitle, ordered.activeMode)
	}

	// A cumulative snapshot started before a run result cannot replace that result.
	ordered.usage = client.Usage{InputTokens: 10}
	staleUsage := (&ordered).refreshSessionCmd()
	ordered = applyAll(ordered, client.ResultMsg{Usage: client.Usage{InputTokens: 5}})
	before := ordered.usage
	mm, retry := ordered.Update(staleUsage())
	ordered = mm.(Model)
	if ordered.usage != before || retry == nil {
		t.Fatalf("live result rolled back or not reconciled: usage=%+v before=%+v retry=%t", ordered.usage, before, retry != nil)
	}

	m := titleModel(t, &titleRenamer{})
	m.reloadSeq = 2
	m = applyAll(m, snapshotReply{seq: 2, session: m.sessionID, msg: client.ResolvedModelMsg{SessionID: m.sessionID, Mode: "plan", TitleMetadataPresent: true, Title: "fresh", TitleRevision: 4}})
	m = applyAll(m, snapshotReply{seq: 1, session: m.sessionID, msg: client.ResolvedModelMsg{SessionID: m.sessionID, Mode: "default", TitleMetadataPresent: true, Title: "old", TitleRevision: 9}})
	if m.sessionTitle != "fresh" || m.activeMode != "plan" {
		t.Fatalf("old snapshot replaced newer: %q/%q", m.sessionTitle, m.activeMode)
	}
	m.reloadSeq = 3
	m = applyAll(m, snapshotReply{seq: 3, session: "former", msg: client.ResolvedModelMsg{SessionID: "former", Mode: "default", TitleMetadataPresent: true, Title: "wrong session"}})
	if m.sessionTitle != "fresh" || m.activeMode != "plan" {
		t.Fatalf("old session response replaced bound state: %q/%q", m.sessionTitle, m.activeMode)
	}
	m.pendingMode = "ask"
	m = applyAll(m, client.ModeChangedMsg{SessionID: m.sessionID, Requested: "plan", Mode: "plan"})
	if m.pendingMode != "ask" {
		t.Fatalf("old mode completion cleared new intent: %q", m.pendingMode)
	}
	m.pendingMode = "plan"
	m.modeIntentSeq = 2
	m = applyAll(m, modeReply{session: m.sessionID, intent: 1, msg: client.ModeChangedMsg{SessionID: m.sessionID, Requested: "plan", Mode: "plan"}})
	if m.pendingMode != "plan" || m.activeMode != "plan" {
		t.Fatalf("old same-mode completion changed newer intent: active=%q pending=%q", m.activeMode, m.pendingMode)
	}
	m.pendingMode = "ask"
	m.reloadSeq = 3
	m = applyAll(m, snapshotReply{seq: 3, session: m.sessionID, modeIntent: 1, msg: client.ResolvedModelMsg{SessionID: m.sessionID, Mode: "default", State: "awaiting"}})
	if m.activeMode != "plan" || m.pendingMode != "ask" || m.sessionState != "awaiting" {
		t.Fatalf("stale snapshot mode changed newer intent: active=%q pending=%q state=%q", m.activeMode, m.pendingMode, m.sessionState)
	}
	m.titleRenameRequestToken = 2
	m = applyAll(m, client.SessionRenamedMsg{SessionID: m.sessionID, RequestToken: 1, Title: "old rename", TitleRevision: 10})
	if m.sessionTitle != "fresh" {
		t.Fatalf("old rename completion applied: %q", m.sessionTitle)
	}
	m.sessionTitle = "pending operator rename"
	m.reloadSeq = 4
	m = applyAll(m, snapshotReply{seq: 4, session: m.sessionID, titleIntent: 1, modeIntent: m.modeIntentSeq, msg: client.ResolvedModelMsg{SessionID: m.sessionID, TitleMetadataPresent: true, Title: "pre-rename snapshot", TitleRevision: 5}})
	if m.sessionTitle != "pending operator rename" {
		t.Fatalf("pre-intent snapshot cleared rename intent: %q", m.sessionTitle)
	}
}

func TestMecatuiAuthoritativeReload_Scenario2_LiveOnlyResultDuringFetch(t *testing.T) {
	conv := &fakeConv{getSessionSnapshots: []client.SessionSnapshot{{MainUsagePresent: true, Usage: client.Usage{InputTokens: 100}}}}
	m := modeTestModel(t, conv)
	m.phase, m.liveGen, m.reloadFeedGen = phaseRunning, 7, 7
	m.reloadPending, m.reloadSession, m.reloadSeq = true, m.sessionID, 1
	m.usage = client.Usage{InputTokens: 30}
	terminal := client.ResultMsg{Stop: stopError, Error: "run failed", Usage: client.Usage{InputTokens: 10}, RetryDispositionPresent: true, RetryDisposition: client.RetryDispositionRetryable}
	m = applyAll(m, liveMsg{gen: 7, msg: terminal})
	if m.phase != phaseRunning || m.usage.InputTokens != 30 {
		t.Fatalf("terminal exposed before snapshot: phase=%v usage=%+v", m.phase, m.usage)
	}
	mm, cmd := m.Update(snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, MainUsagePresent: true, Usage: client.Usage{InputTokens: 100}}})
	m = mm.(Model)
	if m.phase != phaseIdle || m.usage.InputTokens != 30 || !m.reloadNeedRefresh && cmd == nil {
		t.Fatalf("live-only terminal lost or usage duplicated: phase=%v usage=%+v cmd=%t", m.phase, m.usage, cmd != nil)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "/retry") || len(m.conv.testBlocks()) != 1 || !strings.Contains(testCardText(m.conv.testBlocks()[0]), "run failed") {
		t.Fatalf("terminal error/retry feedback lost: status=%q blocks=%v", m.statusMsg, m.conv.testBlocks())
	}
	var reconciled bool
	for _, event := range flattenBatch(cmd) {
		if reply, ok := event.(snapshotReply); ok {
			m = applyAll(m, reply)
			reconciled = true
		}
	}
	if !reconciled || m.usage.InputTokens != 100 {
		t.Fatalf("no authoritative cumulative reconciliation: fetched=%t usage=%+v", reconciled, m.usage)
	}

	// A second metadata request can start after the result was buffered. Its
	// snapshot may already include that result, even though its epoch matches.
	m = modeTestModel(t, conv)
	m.phase, m.liveGen, m.reloadFeedGen = phaseRunning, 7, 7
	m.reloadPending, m.reloadSession, m.reloadSeq = true, m.sessionID, 1
	m.usage = client.Usage{InputTokens: 30}
	m = applyAll(m, liveMsg{gen: 7, msg: terminal})
	m = applyAll(m, snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, liveEpoch: m.reloadLiveEpoch, msg: client.ResolvedModelMsg{SessionID: m.sessionID, MainUsagePresent: true, Usage: client.Usage{InputTokens: 100}}})
	if m.usage.InputTokens != 30 || m.phase != phaseIdle {
		t.Fatalf("matching epoch duplicated buffered result: usage=%+v phase=%v", m.usage, m.phase)
	}

	m = modeTestModel(t, conv)
	m.phase, m.liveGen, m.reloadFeedGen = phaseRunning, 7, 7
	m.reloadPending, m.reloadSession, m.reloadSeq = true, m.sessionID, 1
	m = applyAll(m, liveMsg{gen: 7, msg: terminal}, client.SessionTitleMsg{Title: "current", Revision: 3})
	m = applyAll(m, snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, TitleMetadataPresent: true, Title: "stale", TitleRevision: 2}})
	if m.sessionTitle != "current" {
		t.Fatalf("result replay rolled back newer title: %q", m.sessionTitle)
	}
}

func TestMecatuiAuthoritativeReload_Scenario2_StreamSettlesWithoutResult(t *testing.T) {
	for _, terminal := range []struct {
		name string
		msg  any
	}{
		{"closed", client.StreamClosedMsg{}},
		{"error", client.StreamErrMsg{Err: errors.New("transport failed")}},
	} {
		t.Run(terminal.name, func(t *testing.T) {
			conv := &fakeConv{getSessionSnapshots: []client.SessionSnapshot{{TitleMetadataPresent: true, Title: "settled", TitleRevision: 1}}}
			m := modeTestModel(t, conv)
			m.phase, m.liveGen, m.reloadFeedGen = phaseRunning, 7, 7
			m.reloadPending, m.reloadSession, m.reloadSeq = true, m.sessionID, 1
			m = applyAll(m, liveMsg{gen: 7, msg: client.SessionTitleMsg{Title: "live", Revision: 2}})
			m.reloadLiveEpoch++ // a current turn update while the fetch was in flight
			m = applyAll(m, snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, TitleMetadataPresent: true, Title: "old", TitleRevision: 1}})
			if !m.reloadNeedRefresh || !m.reloadPending {
				t.Fatal("in-flight run did not defer metadata reconciliation")
			}
			mm, cmd := m.Update(terminal.msg)
			m = mm.(Model)
			if m.phase != phaseIdle || cmd == nil {
				t.Fatalf("stream settled without refetch: phase=%v cmd=%t", m.phase, cmd != nil)
			}
			var fetched bool
			for _, event := range flattenBatch(cmd) {
				if reply, ok := event.(snapshotReply); ok {
					m = applyAll(m, reply)
					fetched = true
				}
			}
			if !fetched || m.reloadPending || m.sessionTitle != "live" {
				t.Fatalf("barrier stuck after stream settled: fetched=%t pending=%t title=%q", fetched, m.reloadPending, m.sessionTitle)
			}
		})
	}
}

func TestMecatuiAuthoritativeReload_Scenario2_InitialBindingFeedBeforeHeal(t *testing.T) {
	conv := &fakeConv{getSessionSnapshots: []client.SessionSnapshot{{TitleMetadataPresent: true, Title: "snapshot", TitleRevision: 1}}}
	feed := &reconnectLiveStreamer{}
	m := newTestModelFromDeps(Deps{Session: conv, LiveStream: feed, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), NoAltScreen: true})
	mm, cmd := m.Update(client.SessionReadyMsg{SessionID: "fresh"})
	m = mm.(Model)
	defer m.disarmLiveFeed()
	if cmd == nil || feed.opens.Load() != 1 || !m.reloadPending {
		t.Fatalf("binding did not establish feed barrier before fetch: opens=%d pending=%t", feed.opens.Load(), m.reloadPending)
	}
	m = applyAll(m, liveMsg{gen: m.liveGen, msg: client.SessionTitleMsg{Title: "newer", Revision: 2}})
	if m.sessionTitle == "newer" {
		t.Fatal("initial feed event exposed before heal snapshot")
	}
	var fetched bool
	for _, event := range flattenBatch(cmd) {
		if reply, ok := event.(snapshotReply); ok {
			m = applyAll(m, reply)
			fetched = true
		}
	}
	if !fetched || m.sessionTitle != "newer" || m.reloadPending {
		t.Fatalf("initial heal lost live title: fetched=%t title=%q pending=%t", fetched, m.sessionTitle, m.reloadPending)
	}
}
