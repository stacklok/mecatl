package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

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
