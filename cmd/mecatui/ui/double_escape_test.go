package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func escapePress(repeat ...bool) tea.KeyPressMsg {
	msg := tea.KeyPressMsg{Code: tea.KeyEscape}
	if len(repeat) > 0 {
		msg.IsRepeat = repeat[0]
	}
	return msg
}

func escapeRelease() tea.KeyReleaseMsg {
	return tea.KeyReleaseMsg(tea.Key{Code: tea.KeyEscape})
}

func doubleEscapeDraft(t *testing.T) Model {
	t.Helper()
	m, _ := newQueueModel(t)
	m.prompt.Rewrite("keep this draft")
	m.prompt.Focus()
	return m
}

func enableEventTypes(m Model) Model {
	return applyAll(m, tea.KeyboardEnhancementsMsg{Flags: ansi.KittyReportEventTypes})
}

func pressReleasePress(m Model) Model {
	return applyAll(m, escapePress(), escapeRelease(), escapePress())
}

func TestADR_0303_DoubleEscape_Scenario1_ViewRequestsEventTypesAndTracksSupport(t *testing.T) {
	m := doubleEscapeDraft(t)
	if !m.View().KeyboardEnhancements.ReportEventTypes {
		t.Fatal("View did not request keyboard event-type reporting")
	}
	m = applyAll(m, tea.KeyboardEnhancementsMsg{})
	if m.keyboardEventTypes {
		t.Fatal("model recorded event-type support from an unsupported report")
	}
	m = enableEventTypes(m)
	if !m.keyboardEventTypes {
		t.Fatal("model did not record reported event-type support")
	}
}

func TestADR_0303_DoubleEscape_Scenario1_UnsupportedTerminalFailsClosed(t *testing.T) {
	m := doubleEscapeDraft(t)
	m = pressReleasePress(m)
	if m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("unsupported terminal armed or cleared: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
}

func TestADR_0303_DoubleEscape_Scenario1_FirstPressArmsExactExpiry(t *testing.T) {
	m := enableEventTypes(doubleEscapeDraft(t))
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("image")}}
	m.stagedPastes = map[string]string{"[Pasted text #1]": "large paste"}
	m.pendingPromptMedia = client.MediaResult{Descriptors: []string{"pending"}}
	status := m.statusMsg
	var after time.Duration
	var gen, calls int
	m.doubleEscapeTimer = func(d time.Duration, g int) tea.Cmd {
		after, gen, calls = d, g, calls+1
		return func() tea.Msg { return doubleEscapeExpiryMsg{gen: g} }
	}

	mm, cmd := m.Update(escapePress())
	m = mm.(Model)
	if !m.doubleEscapeArmed || m.doubleEscapeReleased || m.doubleEscapeGen != 1 || cmd == nil {
		t.Fatalf("first Escape arm = armed:%t released:%t gen:%d cmd:%v", m.doubleEscapeArmed, m.doubleEscapeReleased, m.doubleEscapeGen, cmd)
	}
	if calls != 1 || after != 500*time.Millisecond || gen != m.doubleEscapeGen {
		t.Fatalf("expiry scheduling = calls:%d after:%s gen:%d, want one 500ms command for gen %d", calls, after, gen, m.doubleEscapeGen)
	}
	if got, ok := cmd().(doubleEscapeExpiryMsg); !ok || got.gen != gen {
		t.Fatalf("expiry command message = %#v, want generation %d", got, gen)
	}
	if m.prompt.Value() != "keep this draft" || len(m.stagedMedia) != 1 || len(m.stagedPastes) != 1 || len(m.pendingPromptMedia.Descriptors) != 1 || m.statusMsg != status {
		t.Fatalf("first Escape mutated draft or status: text=%q media=%d pastes=%d pending=%d status=%q", m.prompt.Value(), len(m.stagedMedia), len(m.stagedPastes), len(m.pendingPromptMedia.Descriptors), m.statusMsg)
	}
}

func TestADR_0303_DoubleEscape_Scenario1_PressReleasePressClearsThroughClearPrompt(t *testing.T) {
	m := enableEventTypes(doubleEscapeDraft(t))
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("image")}}
	m.stagedPastes = map[string]string{"[Pasted text #1]": "large paste"}
	m.pendingPromptMedia = client.MediaResult{Descriptors: []string{"pending"}}
	m = pressReleasePress(m)
	if m.doubleEscapeArmed || !m.prompt.Empty() || len(m.stagedMedia) != 0 || len(m.stagedPastes) != 0 || len(m.pendingPromptMedia.Descriptors) != 0 {
		t.Fatalf("release-qualified second Escape did not clear via clearPrompt: armed=%t text=%q media=%d pastes=%d pending=%d", m.doubleEscapeArmed, m.prompt.Value(), len(m.stagedMedia), len(m.stagedPastes), len(m.pendingPromptMedia.Descriptors))
	}

	attachmentOnly := enableEventTypes(doubleEscapeDraft(t))
	attachmentOnly.prompt.Reset()
	attachmentOnly.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("image")}}
	attachmentOnly = pressReleasePress(attachmentOnly)
	if len(attachmentOnly.stagedMedia) != 0 || attachmentOnly.doubleEscapeArmed {
		t.Fatalf("attachment-only draft was not cleared: media=%d armed=%t", len(attachmentOnly.stagedMedia), attachmentOnly.doubleEscapeArmed)
	}
}

func TestADR_0303_DoubleEscape_Scenario1_RepeatBeforeReleaseCannotClear(t *testing.T) {
	m := enableEventTypes(doubleEscapeDraft(t))
	m = applyAll(m, escapePress(true))
	if m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("repeat armed from rest: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
	m = applyAll(m, escapePress(), escapePress(true), escapePress())
	if !m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("repeat or press-before-release confirmed gesture: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
	m = applyAll(m, escapeRelease(), escapePress(true))
	if m.prompt.Empty() {
		t.Fatal("repeat after release confirmed gesture")
	}
	m = applyAll(m, escapePress())
	if !m.prompt.Empty() {
		t.Fatal("non-repeat press after release did not confirm gesture")
	}
}

func TestADR_0303_DoubleEscape_Scenario1_InterveningKeyDisarmsAndRoutesNormally(t *testing.T) {
	m := enableEventTypes(doubleEscapeDraft(t))
	m = applyAll(m, escapePress(), escapeRelease(), tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.doubleEscapeArmed || m.prompt.Value() != "keep this draftx" {
		t.Fatalf("intervening key did not disarm and route: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
	m = applyAll(m, escapePress())
	if !m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("Escape after intervening key was not fresh first press: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
}

func TestADR_0303_DoubleEscape_Scenario1_ExactExpiryAndGenerationGuard(t *testing.T) {
	m := enableEventTypes(doubleEscapeDraft(t))
	var after time.Duration
	m.doubleEscapeTimer = func(d time.Duration, gen int) tea.Cmd {
		after = d
		return func() tea.Msg { return doubleEscapeExpiryMsg{gen: gen} }
	}
	mm, cmd := m.Update(escapePress())
	m = mm.(Model)
	first := m.doubleEscapeGen
	if after != doubleEscapeWindow || cmd == nil {
		t.Fatalf("expiry command = %v after %s, want exact %s", cmd, after, doubleEscapeWindow)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	mm, _ = m.Update(escapePress())
	m = mm.(Model)
	second := m.doubleEscapeGen
	m = applyAll(m, doubleEscapeExpiryMsg{gen: first})
	if !m.doubleEscapeArmed || second == first {
		t.Fatalf("stale expiry disarmed rearmed gesture: armed=%t first=%d second=%d", m.doubleEscapeArmed, first, second)
	}
	m = applyAll(m, doubleEscapeExpiryMsg{gen: second})
	if m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("matching expiry did not silently disarm: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
}

func TestADR_0303_DoubleEscape_Scenario1_OwnersConsumeAndDisarm(t *testing.T) {
	assertFreshNext := func(t *testing.T, m Model) {
		t.Helper()
		m.phase = phaseIdle
		m.showHelp = false
		m.closeModal()
		m.prompt.Focus()
		m = applyAll(m, escapePress())
		if !m.doubleEscapeArmed || m.prompt.Empty() {
			t.Fatalf("next idle Escape was not fresh: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
		}
	}
	t.Run("selection", func(t *testing.T) {
		m := enableEventTypes(doubleEscapeDraft(t))
		m.doubleEscapeArmed, m.doubleEscapeReleased = true, true
		m.prompt.SelectAll()
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed || m.prompt.HasSelection() || m.prompt.Empty() {
			t.Fatalf("selection did not consume/disarm: armed=%t selected=%t", m.doubleEscapeArmed, m.prompt.HasSelection())
		}
		assertFreshNext(t, m)
	})
	t.Run("help", func(t *testing.T) {
		m := enableEventTypes(doubleEscapeDraft(t))
		m.doubleEscapeArmed, m.doubleEscapeReleased, m.showHelp = true, true, true
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed || m.showHelp || m.prompt.Empty() {
			t.Fatalf("help did not consume/disarm: armed=%t open=%t", m.doubleEscapeArmed, m.showHelp)
		}
		assertFreshNext(t, m)
	})
	t.Run("approval", func(t *testing.T) {
		m := enableEventTypes(bashAskModel(t, `{"command":"true"}`))
		m.doubleEscapeArmed, m.doubleEscapeReleased = true, true
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed {
			t.Fatal("approval Escape did not disarm")
		}
		m.prompt.Rewrite("keep")
		assertFreshNext(t, m)
	})
	t.Run("running cancel", func(t *testing.T) {
		m, _ := newQueueModel(t)
		m = enableEventTypes(startRunning(t, m, "first"))
		m.prompt.Rewrite("keep")
		m.doubleEscapeArmed, m.doubleEscapeReleased = true, true
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed || m.prompt.Value() != "keep" || m.statusMsg != "cancelling…" {
			t.Fatalf("running cancel did not consume/disarm: armed=%t text=%q status=%q", m.doubleEscapeArmed, m.prompt.Value(), m.statusMsg)
		}
		assertFreshNext(t, m)
	})
}

func TestADR_0303_DoubleEscape_Scenario1_AllEscapeOwnersSuppressGesture(t *testing.T) {
	base := func() Model {
		m := enableEventTypes(doubleEscapeDraft(t))
		m.doubleEscapeArmed, m.doubleEscapeReleased = true, true
		return m
	}
	owners := []struct {
		name string
		set  func(Model) Model
	}{
		{"conversation selection", func(m Model) Model { m.sel.active = true; return m }},
		{"prompt selection", func(m Model) Model { m.prompt.SelectAll(); return m }},
		{"palette", func(m Model) Model { m.palette.open = true; return m }},
		{"mention", func(m Model) Model { m.mention.open = true; return m }},
		{"paused queue", func(m Model) Model { m.queuePaused = "paused"; return m }},
		{"session details", func(m Model) Model { m.sessionDetailsOpen = true; return m }},
		{"help", func(m Model) Model { m.showHelp = true; return m }},
		{"modal", func(m Model) Model { m.modal = bashAskModel(t, `{"command":"true"}`).modal; return m }},
		{"team", func(m Model) Model { m.team.view = teamRoster; return m }},
		{"agents inventory", func(m Model) Model { m.agentsInv.view = agentsInvPanel; return m }},
		{"user model", func(m Model) Model { m.userModel.view = userModelPanel; return m }},
		{"reflections", func(m Model) Model { m.reflections.view = reflectionsList; return m }},
		{"dream", func(m Model) Model { m.dream.view = dreamGenerating; return m }},
		{"connect", func(m Model) Model { m.connect.open = true; return m }},
		{"effort", func(m Model) Model { m.effort.view = effortPanel; return m }},
		{"worktrees", func(m Model) Model { m.worktrees.view = worktreesPanel; return m }},
		{"schedule", func(m Model) Model { m.schedule.view = schedulePanel; return m }},
		{"running turn", func(m Model) Model { m.phase = phaseRunning; return m }},
	}
	for _, tc := range owners {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.set(base())
			if m.doubleEscapeEligible() {
				t.Fatal("owner left idle double-Escape gesture eligible")
			}
			m = applyAll(m, escapePress())
			if m.doubleEscapeArmed || m.prompt.Empty() {
				t.Fatalf("owner failed to suppress gesture: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
			}
		})
	}
}

func TestADR_0303_DoubleEscape_Scenario1_ClearPromptAlternativeRemainsAccessible(t *testing.T) {
	m := enableEventTypes(doubleEscapeDraft(t))
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Cancel": {"ctrl+f34"}, "ClearPrompt": {"ctrl+f33"}})
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png"}}
	m = pressReleasePress(m)
	if !m.prompt.Empty() || len(m.stagedMedia) != 0 {
		t.Fatalf("physical gesture followed remapping: text=%q media=%d", m.prompt.Value(), len(m.stagedMedia))
	}
	m.prompt.Rewrite("clear through alternative")
	m.stagedMedia = map[string]stagedAttachment{"[Image #2]": {mime: "image/png"}}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF33, Mod: tea.ModCtrl})
	if !m.prompt.Empty() || len(m.stagedMedia) != 0 {
		t.Fatalf("remapped ClearPrompt stopped working: text=%q media=%d", m.prompt.Value(), len(m.stagedMedia))
	}
}

func TestADR_0303_DoubleEscape_Scenario1_FirstPressAndCompletionHaveNoNewStatus(t *testing.T) {
	m := enableEventTypes(doubleEscapeDraft(t))
	m.statusMsg = "existing status"
	m = applyAll(m, escapePress())
	if m.statusMsg != "existing status" {
		t.Fatalf("first press changed status: %q", m.statusMsg)
	}
	m = applyAll(m, escapeRelease(), escapePress())
	if m.statusMsg != "existing status" {
		t.Fatalf("completion changed status: %q", m.statusMsg)
	}
}

func TestADR_0303_DoubleEscape_Scenario2_LiveHelpExplainsRequirementAndAlternative(t *testing.T) {
	body := stripANSIstr(m_helpBody(allOnCaps()))
	for _, want := range []string{"esc esc", "500ms", "enhanced key-event support required", "first press is silent", "release", "repeat", "not remappable", "staged attachments", "large-paste", "pending media", "owners take precedence", "ctrl+u", "ClearPrompt"} {
		if !strings.Contains(body, want) {
			t.Errorf("live help missing %q:\n%s", want, body)
		}
	}
}

func TestADR_0303_DoubleEscape_Scenario2_DocumentationNamesSafetyContract(t *testing.T) {
	for _, name := range []string{"../../../docs/tui.md", "../../../user-docs/mecatui/keybindings.md"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, want := range []string{"500ms", "enhanced key-event support", "not remappable", "first press is silent", "key release", "key repeat", "staged attachments", "large-paste", "pending media", "owners take precedence", "ClearPrompt", "ctrl+u"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing safety-contract term %q", name, want)
			}
		}
	}
}

func TestADR_0303_DoubleEscape_Scenario2_HelpGoldenChangesAreScoped(t *testing.T) {
	matches, err := filepath.Glob("testdata/help_*.golden")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("help golden scope = %v, want exactly embedded and all-on", matches)
	}
	for _, name := range matches {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "enhanced key-event support required") {
			t.Errorf("%s does not document the double-Escape safety requirement", name)
		}
	}
}
