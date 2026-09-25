package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatuikeymap "github.com/stacklok/mecatl/cmd/mecatui/keymap"
)

func historyModel(t *testing.T, entries ...string) Model {
	t.Helper()
	m, _ := newQueueModel(t)
	for _, entry := range entries {
		m.promptHistory.record(entry)
	}
	return m
}

func historyKey(t *testing.T, m Model, code rune) Model {
	t.Helper()
	msg := tea.KeyPressMsg{Code: code}
	if code >= 32 && code <= 126 {
		msg.Text = string(code)
	}
	mm, _ := m.Update(msg)
	return mm.(Model)
}

func TestMecatuiPromptHistory_Scenario1_PreviousTraversesAndClamps(t *testing.T) {
	m := historyModel(t, "one", "two")
	m = historyKey(t, m, tea.KeyUp)
	if got := m.prompt.Value(); got != "two" {
		t.Fatalf("first previous = %q", got)
	}
	m = historyKey(t, m, tea.KeyUp)
	m = historyKey(t, m, tea.KeyUp)
	if got := m.prompt.Value(); got != "one" {
		t.Fatalf("clamped previous = %q", got)
	}
}

func TestMecatuiPromptHistory_Scenario1_NextRestoresSavedDraft(t *testing.T) {
	m := historyModel(t, "one", "two")
	m.prompt.Rewrite("draft\nexact")
	m = historyKey(t, m, tea.KeyUp)
	m = historyKey(t, m, tea.KeyDown)
	m = historyKey(t, m, tea.KeyDown)
	if got := m.prompt.Value(); got != "draft\nexact" {
		t.Fatalf("restored draft = %q", got)
	}
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png"}}
	m = historyKey(t, m, tea.KeyUp)
	if got := m.prompt.Value(); got != "draft\nexact" {
		t.Fatalf("media-owned draft browsed to %q", got)
	}
	for name, stage := range map[string]func(*Model){
		"staged paste": func(m *Model) { m.stagedPastes = map[string]string{"[Pasted text #1]": "body"} },
		"pending media": func(m *Model) {
			m.pendingPromptMedia = client.MediaResult{Descriptors: []string{"pending"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			blocked := historyModel(t, "history")
			blocked.prompt.Rewrite("owned draft")
			stage(&blocked)
			blocked = historyKey(t, blocked, tea.KeyUp)
			if blocked.prompt.Value() != "owned draft" || blocked.promptHistory.index != 0 {
				t.Fatalf("staged state allowed history browsing: text=%q index=%d", blocked.prompt.Value(), blocked.promptHistory.index)
			}
		})
	}
}

func TestMecatuiPromptHistory_Scenario1_MultilineAndSelectionStayEditorOwned(t *testing.T) {
	t.Run("logical rows", func(t *testing.T) {
		m := historyModel(t, "history")
		m.prompt.Rewrite("first line\nsecond line")
		beforeLine := m.prompt.Line()
		m = historyKey(t, m, tea.KeyUp)
		if m.prompt.Line() >= beforeLine || m.promptHistory.index != 0 {
			t.Fatalf("interior Up did not move the editor: line=%d before=%d history=%d", m.prompt.Line(), beforeLine, m.promptHistory.index)
		}
	})
	t.Run("soft-wrapped rows", func(t *testing.T) {
		m := historyModel(t, "history")
		m.prompt.SetWidth(8)
		m.prompt.Rewrite("a long single logical line that wraps")
		beforeColumn := m.prompt.Column()
		m = historyKey(t, m, tea.KeyUp)
		if m.prompt.Column() >= beforeColumn || m.promptHistory.index != 0 {
			t.Fatalf("soft-wrap Up did not move the editor: column=%d before=%d history=%d", m.prompt.Column(), beforeColumn, m.promptHistory.index)
		}
	})
	t.Run("shift selection", func(t *testing.T) {
		m := historyModel(t, "history")
		m.prompt.Rewrite("first\nsecond")
		mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
		m = mm.(Model)
		if !m.prompt.HasSelection() || m.promptHistory.index != 0 || m.prompt.Value() != "first\nsecond" {
			t.Fatalf("Shift+Up ownership: selected=%t history=%d text=%q", m.prompt.HasSelection(), m.promptHistory.index, m.prompt.Value())
		}
	})
	t.Run("readline ctrl p and n", func(t *testing.T) {
		m := historyModel(t, "history")
		m.prompt.Rewrite("first\nsecond")
		start := m.prompt.Line()
		mm, _ := m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
		m = mm.(Model)
		if m.prompt.Line() >= start || m.promptHistory.index != 0 {
			t.Fatalf("ctrl+p ownership: line=%d start=%d history=%d", m.prompt.Line(), start, m.promptHistory.index)
		}
		mm, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
		m = mm.(Model)
		if m.prompt.Line() != start || m.promptHistory.index != 0 {
			t.Fatalf("ctrl+n ownership: line=%d want=%d history=%d", m.prompt.Line(), start, m.promptHistory.index)
		}
	})
}

func TestMecatuiPromptHistory_Scenario1_EditDetachesWithoutLosingDraft(t *testing.T) {
	assertDetached := func(t *testing.T, m Model) {
		t.Helper()
		if m.promptHistory.index != 0 {
			t.Fatalf("mutation left history browsing attached at index %d", m.promptHistory.index)
		}
		before := m.prompt.Value()
		m = historyKey(t, m, tea.KeyDown)
		if m.prompt.Value() != before {
			t.Fatalf("detached content %q replaced by %q", before, m.prompt.Value())
		}
	}
	assertDetachedOnNext := func(t *testing.T, m Model) {
		t.Helper()
		before := m.prompt.Value()
		m = historyKey(t, m, tea.KeyDown)
		if m.promptHistory.index != 0 || m.prompt.Value() != before {
			t.Fatalf("next history action did not detach mutation: index=%d before=%q after=%q", m.promptHistory.index, before, m.prompt.Value())
		}
	}
	browsing := func(t *testing.T) Model {
		t.Helper()
		m := historyModel(t, "old")
		m.prompt.Rewrite("saved")
		m = historyKey(t, m, tea.KeyUp)
		return m
	}

	t.Run("insertion", func(t *testing.T) {
		assertDetached(t, historyKey(t, browsing(t), 'x'))
	})
	t.Run("deletion", func(t *testing.T) {
		assertDetached(t, historyKey(t, browsing(t), tea.KeyBackspace))
	})
	t.Run("newline", func(t *testing.T) {
		m := browsing(t)
		mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
		assertDetached(t, mm.(Model))
	})
	t.Run("paste", func(t *testing.T) {
		m := browsing(t)
		mm, _ := m.Update(tea.PasteMsg{Content: " pasted"})
		assertDetached(t, mm.(Model))
	})
	t.Run("clear", func(t *testing.T) {
		m := browsing(t)
		mm, _ := m.clearPrompt()
		assertDetached(t, mm.(Model))
	})
	t.Run("palette completion", func(t *testing.T) {
		m := historyModel(t, "/old")
		m = historyKey(t, m, tea.KeyUp)
		m.palette = paletteState{open: true, filtered: []client.Command{{Name: "clear"}}}
		m.palette.syncList()
		assertDetachedOnNext(t, m.paletteComplete())
	})
	t.Run("mention completion", func(t *testing.T) {
		m := historyModel(t, "inspect @ol")
		m = historyKey(t, m, tea.KeyUp)
		m.mention = mentionState{open: true, matches: []string{"old.txt"}}
		m.mention.syncList()
		assertDetachedOnNext(t, m.mentionComplete())
	})
	t.Run("byte-identical host rewrite", func(t *testing.T) {
		m := browsing(t)
		m.prompt.Rewrite(m.prompt.Value())
		assertDetachedOnNext(t, m)
	})
	t.Run("MCP insertion", func(t *testing.T) {
		m := browsing(t)
		mm, _ := m.insertIntoInput("loaded prompt", "loaded")
		assertDetachedOnNext(t, mm.(Model))
	})
}

func TestMecatuiPromptHistory_Scenario2_CommittedMessagesEnterOnceAndBound(t *testing.T) {
	m, _ := newQueueModel(t)
	m.prompt.Rewrite("ordinary")
	mm, _ := m.submitPrompt()
	m = mm.(Model)
	if len(m.promptHistory.entries) != 1 || m.promptHistory.entries[0] != "ordinary" {
		t.Fatalf("ordinary prepared send history = %q", m.promptHistory.entries)
	}
	for i := 0; i < 100; i++ {
		m.promptHistory.record(fmt.Sprintf("p%d", i))
	}
	if len(m.promptHistory.entries) != 100 || m.promptHistory.entries[0] != "p0" {
		t.Fatalf("bounded history = %#v", m.promptHistory.entries[:2])
	}
	m.steer = &steerState{Sends: []steerQueuedSend{{ID: "steer-1", Text: "steer"}}}
	mm, _ = m.applySteerEcho(client.SteerEchoMsg{Text: "steer", MessageID: "steer-1"})
	m = mm.(Model)
	if got := m.promptHistory.entries[len(m.promptHistory.entries)-1]; got != "steer" {
		t.Fatalf("echo record = %q", got)
	}
	queued, _ := newQueueModel(t)
	queued.queued = []string{"queued one", "queued two"}
	queued.queuedHistory = []string{"queued one", "queued two"}
	mm, _ = queued.popAndSubmit()
	queued = mm.(Model)
	if got := queued.promptHistory.entries; len(got) != 1 || got[0] != "queued one"+queueMergeSep+"queued two" {
		t.Fatalf("merged queue history = %q", got)
	}
}

func TestMecatuiPromptHistory_Scenario2_NonOperatorAndNonTextInputExcluded(t *testing.T) {
	m, _ := newQueueModel(t)
	m.promptHistory.record("")
	m.promptHistory.record("   ")
	if len(m.promptHistory.entries) != 0 {
		t.Fatalf("empty entries recorded: %q", m.promptHistory.entries)
	}
	m.prompt.Rewrite("/help")
	mm, _ := m.submitPrompt()
	if len(mm.(Model).promptHistory.entries) != 0 {
		t.Fatal("builtin entered history")
	}
	mediaOnly, _ := newQueueModel(t)
	part, _, err := client.StageClipboardImage("image/png", []byte("pixels"), client.Capabilities{Image: true})
	if err != nil {
		t.Fatal(err)
	}
	mediaOnly.pendingPromptMedia = client.MediaResult{Parts: append(mediaOnly.pendingPromptMedia.Parts, part)}
	mm, _ = mediaOnly.submitPrompt()
	if len(mm.(Model).promptHistory.entries) != 0 {
		t.Fatal("media-only submission entered text history")
	}

	t.Run("later failure retains ordinary send", func(t *testing.T) {
		ordinary, _ := newQueueModel(t)
		ordinary.prompt.Rewrite("keep after failure")
		mm, _ := ordinary.submitPrompt()
		ordinary = mm.(Model)
		mm, _ = ordinary.Update(client.ResultMsg{Stop: stopError, Error: "failed"})
		if got := mm.(Model).promptHistory.entries; len(got) != 1 || got[0] != "keep after failure" {
			t.Fatalf("later failure changed history: %q", got)
		}
	})
	t.Run("diagnostics ordinary and queued", func(t *testing.T) {
		ordinary, _ := newQueueModel(t)
		ordinary.prompt.Rewrite("generated diagnostics")
		mm, _ := ordinary.submitDiagnosticsReport()
		if len(mm.(Model).promptHistory.entries) != 0 {
			t.Fatal("ordinary generated diagnostics entered history")
		}

		queued, _ := newQueueModel(t)
		queued.phase = phaseRunning
		queued.prompt.Rewrite("generated diagnostics")
		mm, _ = queued.submitDiagnosticsReport()
		queued = mm.(Model)
		if len(queued.queued) != 1 || len(queued.queuedHistory) != 0 {
			t.Fatalf("generated diagnostics queue provenance lost: queue=%q history=%q", queued.queued, queued.queuedHistory)
		}
		queued.phase = phaseIdle
		mm, _ = queued.popAndSubmit()
		if len(mm.(Model).promptHistory.entries) != 0 {
			t.Fatal("drained generated diagnostics entered history")
		}

		mixed, _ := newQueueModel(t)
		mixed.phase = phaseRunning
		mixed.prompt.Rewrite("generated diagnostics")
		mm, _ = mixed.submitDiagnosticsReport()
		mixed = mm.(Model)
		mixed.prompt.Rewrite("operator follow-up")
		mm, _ = mixed.enqueuePrompt()
		mixed = mm.(Model)
		mixed.phase = phaseIdle
		mm, _ = mixed.popAndSubmit()
		if got := mm.(Model).promptHistory.entries; len(got) != 1 || got[0] != "operator follow-up" {
			t.Fatalf("mixed queue history = %q, want only operator text", got)
		}
	})
	t.Run("edited pending-mode queue becomes operator text", func(t *testing.T) {
		m, _ := newQueueModel(t)
		m.queued = []string{"generated diagnostics", "operator follow-up"}
		m.queuedHistory = []string{"operator follow-up"}
		m.pendingMode = "plan"
		mm, _ := m.popAndSubmit()
		m = mm.(Model)
		m.prompt.InsertText(" edited")
		m.pendingMode = ""
		mm, _ = m.submitPrompt()
		got := mm.(Model).promptHistory.entries
		want := "generated diagnostics" + queueMergeSep + "operator follow-up edited"
		if len(got) != 1 || got[0] != want {
			t.Fatalf("edited pending-mode history = %q, want %q", got, want)
		}
	})
	t.Run("diagnostics steer and retractions", func(t *testing.T) {
		steer, _ := newSteerModel(t, true)
		steer = startRunning(t, steer, "initial")
		steer.promptHistory = promptHistoryState{}
		steer.prompt.Rewrite("generated diagnostics")
		mm, _ := steer.submitDiagnosticsReport()
		steer = mm.(Model)
		if steer.steer == nil || len(steer.steer.Sends) != 1 || !steer.steer.Sends[0].Synthetic {
			t.Fatalf("generated steer provenance lost: %+v", steer.steer)
		}
		mm, _ = steer.applySteerEcho(client.SteerEchoMsg{Text: "generated diagnostics", MessageID: steer.steer.watermarkID()})
		if len(mm.(Model).promptHistory.entries) != 0 {
			t.Fatal("generated steer echo entered history")
		}

		mixedSteer := historyModel(t)
		mixedSteer.steer = &steerState{Sends: []steerQueuedSend{
			{ID: "steer-1", Text: "generated", Synthetic: true},
			{ID: "steer-2", Text: "operator steer"},
		}}
		mm, _ = mixedSteer.applySteerEcho(client.SteerEchoMsg{Text: "generated" + queueMergeSep + "operator steer", MessageID: "steer-2"})
		if got := mm.(Model).promptHistory.entries; len(got) != 1 || got[0] != "operator steer" {
			t.Fatalf("mixed steer history = %q, want only operator text", got)
		}

		retracted, _ := newQueueModel(t)
		retracted.phase = phaseRunning
		retracted.prompt.Rewrite("retracted follow-up")
		mm, _ = retracted.enqueuePrompt()
		retracted = mm.(Model)
		mm, _ = retracted.editBackQueue()
		if len(mm.(Model).promptHistory.entries) != 0 {
			t.Fatal("retracted queue entered history")
		}

		retractedSteer := historyModel(t, "existing")
		retractedSteer.steer = &steerState{Phase: steerSent, Sends: []steerQueuedSend{{ID: "steer-1", Text: "retracted steer"}}}
		mm, _ = retractedSteer.applySteerOutcome(client.SteerOutcomeMsg{Outcome: client.SteerRetracted, MessageID: "steer-1"})
		if got := mm.(Model).promptHistory.entries; len(got) != 1 || got[0] != "existing" {
			t.Fatalf("retracted steer changed history: %q", got)
		}
	})
	t.Run("plan continuation bypasses history", func(t *testing.T) {
		plan, _ := newQueueModel(t)
		mm, _ := plan.submitProceedPrompt()
		if len(mm.promptHistory.entries) != 0 {
			t.Fatal("plan continuation entered history")
		}
	})
}

func TestMecatuiPromptHistory_Scenario2_RecallNeverReattachesMedia(t *testing.T) {
	m, _ := newQueueModel(t)
	m.caps.Image = true
	m.prompt.Rewrite("inspect [Image #1]")
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("pixels")}}
	mm, _ := m.submitPrompt()
	m = mm.(Model)
	if got := m.promptHistory.entries; len(got) != 1 || got[0] != "inspect" {
		t.Fatalf("mixed-media prepared history = %q, want text only", got)
	}
	if len(m.stagedMedia) != 0 || len(m.pendingPromptMedia.Parts) != 0 {
		t.Fatal("submitted media remained staged")
	}
	m = historyKey(t, m, tea.KeyUp)
	if m.prompt.Value() != "inspect" || len(m.stagedMedia) != 0 || len(m.pendingPromptMedia.Parts) != 0 {
		t.Fatal("recall restored media state")
	}
}

func TestMecatuiPromptHistory_Scenario2_LifecycleFollowsLogicalConversation(t *testing.T) {
	assertHistory := func(t *testing.T, m Model, want int) {
		t.Helper()
		if len(m.promptHistory.entries) != want {
			t.Fatalf("history entries = %q, want %d", m.promptHistory.entries, want)
		}
	}

	t.Run("clear replacement resets", func(t *testing.T) {
		m := historyModel(t, "discard")
		source := m.sessionID
		m.phase = phaseConnecting
		m.clearPending = &clearHandoff{sourceID: source, token: 1}
		mm, _ := m.Update(clearSessionReadyMsg{oldID: source, token: 1, ready: client.SessionReadyMsg{SessionID: "clear-target"}})
		assertHistory(t, mm.(Model), 0)
	})
	t.Run("session adoption resets", func(t *testing.T) {
		m := historyModel(t, "discard")
		loaded := conversation{}
		loaded.addUser("stored transcript prompt must not hydrate history")
		mm, _, handled := m.adoptAuthoritativeTranscript(client.SessionListItem{ID: "adopted", State: "idle"}, loaded)
		if !handled {
			t.Fatal("session adoption was not handled")
		}
		assertHistory(t, mm.(Model), 0)
	})
	t.Run("successful model handoff preserves", func(t *testing.T) {
		m := historyModel(t, "keep")
		source := m.sessionID
		m.phase = phaseConnecting
		m.modelSwitchRequestToken = 1
		mm, _ := m.Update(modelSwitchReadyMsg{
			token: 1, sourceID: source,
			ready:      client.SessionReadyMsg{SessionID: "model-target"},
			transcript: client.SessionTranscript{SessionID: "model-target", Complete: true},
		})
		assertHistory(t, mm.(Model), 1)
	})
	t.Run("successful effort handoff preserves", func(t *testing.T) {
		m := historyModel(t, "keep")
		source := m.sessionID
		m.phase = phaseConnecting
		m.modelSwitchRequestToken = 1
		mm, _ := m.Update(effortHandoffReadyMsg{
			token: 1, sourceID: source, targetID: "effort-target",
			snapshot: client.SessionSnapshot{State: "idle"},
		})
		assertHistory(t, mm.(Model), 1)
	})
	t.Run("successful worktree handoff preserves", func(t *testing.T) {
		m := historyModel(t, "keep")
		source := m.sessionID
		m.phase = phaseConnecting
		mm, _ := m.Update(worktreeSwitchReadyMsg{oldID: source, ready: client.SessionReadyMsg{SessionID: "worktree-target"}})
		assertHistory(t, mm.(Model), 1)
	})
	t.Run("failed and stale handoffs preserve", func(t *testing.T) {
		failed := historyModel(t, "keep")
		source := failed.sessionID
		failed.phase = phaseConnecting
		failed.modelSwitchRequestToken = 2
		mm, _ := failed.Update(modelSwitchFailedMsg{token: 2, sourceID: source, model: "model", err: errors.New("failed")})
		assertHistory(t, mm.(Model), 1)

		stale := historyModel(t, "keep")
		stale.modelSwitchRequestToken = 2
		mm, _ = stale.Update(modelSwitchReadyMsg{token: 1, sourceID: stale.sessionID, ready: client.SessionReadyMsg{SessionID: "stale"}})
		assertHistory(t, mm.(Model), 1)

		worktree := historyModel(t, "keep")
		worktree.phase = phaseConnecting
		mm, _ = worktree.Update(worktreeSwitchFailedMsg{sourceID: worktree.sessionID, err: errors.New("failed")})
		assertHistory(t, mm.(Model), 1)
	})
	t.Run("reconnect reducer preserves", func(t *testing.T) {
		m := historyModel(t, "keep")
		mm, _ := m.updateReconnectMsg(reconnectMsg{gen: m.liveReconGen, msg: client.LiveReconnectingMsg{Attempt: 1}})
		got := mm.(Model)
		if !got.liveReconnecting {
			t.Fatal("reconnect reducer did not enter reconnecting state")
		}
		assertHistory(t, got, 1)
		mm, _ = got.updateReconnectMsg(reconnectMsg{gen: got.liveReconGen, msg: client.UserPromptMsg{Text: "delivery replay prompt"}})
		assertHistory(t, mm.(Model), 1)
	})
}

func TestMecatuiPromptHistory_Scenario3_EditBackPrecedesHistory(t *testing.T) {
	m := historyModel(t, "history")
	m.queued = []string{"queued"}
	m = historyKey(t, m, tea.KeyUp)
	if got := m.prompt.Value(); got != "queued" {
		t.Fatalf("EditBack precedence = %q", got)
	}
	m.prompt.Reset()
	m.queued = []string{"queued"}
	m = historyKey(t, m, tea.KeyDown)
	if !m.prompt.Empty() || len(m.queued) != 1 {
		t.Fatal("next bypassed editable queue")
	}

	steer := historyModel(t, "history")
	steer.phase = phaseRunning
	steer.steer = &steerState{Phase: steerPending, Sends: []steerQueuedSend{{ID: "steer-1", Draft: "pending steer", Text: "pending steer"}}}
	steer = historyKey(t, steer, tea.KeyDown)
	if !steer.prompt.Empty() || steer.steer == nil || steer.promptHistory.index != 0 {
		t.Fatal("next bypassed pending steer")
	}
	steer = historyKey(t, steer, tea.KeyUp)
	if steer.prompt.Value() != "pending steer" || steer.steer != nil || steer.promptHistory.index != 0 {
		t.Fatalf("pending steer did not precede history: prompt=%q steer=%+v history=%d", steer.prompt.Value(), steer.steer, steer.promptHistory.index)
	}
}

func TestMecatuiPromptHistory_Scenario3_VisibleOwnersConsumeArrows(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		own   func(*Model)
	}{
		{name: "slash palette", entry: "/", own: func(m *Model) {
			m.palette = paletteState{open: true, filtered: []client.Command{{Name: "clear"}}}
			m.palette.syncList()
		}},
		{name: "mention", entry: "@", own: func(m *Model) {
			m.mention = mentionState{open: true, matches: []string{"file.txt"}}
			m.mention.syncList()
		}},
		{name: "help", entry: "history", own: func(m *Model) { m.showHelp = true; m.prompt.Blur() }},
		{name: "approval", entry: "history", own: func(m *Model) { m.phase = phaseAwaitingApproval }},
		{name: "effort overlay", entry: "history", own: func(m *Model) { m.effort.view = effortPanel; m.prompt.Blur() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := historyModel(t, tc.entry)
			m = historyKey(t, m, tea.KeyUp)
			tc.own(&m)
			before := m.promptHistory
			beforeValue := m.prompt.Value()
			m = historyKey(t, m, tea.KeyUp)
			m = historyKey(t, m, tea.KeyDown)
			if m.promptHistory.index != before.index || m.promptHistory.draft != before.draft || m.prompt.Value() != beforeValue {
				t.Fatalf("owner changed history traversal: before=%+v after=%+v text=%q", before, m.promptHistory, m.prompt.Value())
			}
		})
	}
}

func TestMecatuiPromptHistory_Scenario3_KeymapAndHelpStayLive(t *testing.T) {
	resolved, err := mecatuikeymap.Parse(map[string][]string{"HistoryNext": {"ctrl+f19"}})
	if err != nil || mecatuikeymap.Validate(resolved) != nil {
		t.Fatalf("HistoryNext parse/validate failed: parse=%v validate=%v", err, mecatuikeymap.Validate(resolved))
	}
	collision, err := mecatuikeymap.Parse(map[string][]string{"ScrollD": {"down"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mecatuikeymap.Validate(collision); err == nil || !strings.Contains(err.Error(), "ScrollD") || !strings.Contains(err.Error(), "HistoryNext") || !strings.Contains(err.Error(), "down") {
		t.Fatalf("effective-default collision = %v, want ScrollD/HistoryNext/down", err)
	}

	km := applyKeyOverrides(defaultKeys(), resolved.ByAction)
	if got := km.HistoryNext.Keys(); len(got) != 1 || got[0] != "ctrl+f19" {
		t.Fatalf("HistoryNext override = %v", got)
	}
	m, _ := newQueueModel(t)
	body := helpBody(m.deps.Theme, client.Capabilities{}, keyMarkings(km))
	if !strings.Contains(body, "ctrl+f19") {
		t.Fatal("live HistoryNext absent from help")
	}
}
