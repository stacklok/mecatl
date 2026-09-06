package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func escapePress(repeat ...bool) tea.KeyPressMsg {
	msg := tea.KeyPressMsg{Code: tea.KeyEscape}
	if len(repeat) > 0 {
		msg.IsRepeat = repeat[0]
	}
	return msg
}

func doubleEscapeDraft(t *testing.T) Model {
	t.Helper()
	m, _ := newQueueModel(t)
	m.prompt.Rewrite("keep this draft")
	m.prompt.Focus()
	return m
}

func TestADR_0025_DoubleEscape_Scenario1_FirstEscapeArmsWithoutMutation(t *testing.T) {
	m := doubleEscapeDraft(t)
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("image")}}
	m.stagedPastes = map[string]string{"[Pasted text #1]": "large paste"}
	m.pendingPromptMedia = client.MediaResult{Descriptors: []string{"pending"}}
	status := m.statusMsg

	mm, cmd := m.Update(escapePress())
	m = mm.(Model)
	if !m.doubleEscapeArmed || m.doubleEscapeGen != 1 || cmd == nil {
		t.Fatalf("first Escape did not arm one expiry: armed=%t gen=%d cmd=%v", m.doubleEscapeArmed, m.doubleEscapeGen, cmd)
	}
	if m.prompt.Value() != "keep this draft" || len(m.stagedMedia) != 1 || len(m.stagedPastes) != 1 || len(m.pendingPromptMedia.Descriptors) != 1 || m.statusMsg != status {
		t.Fatalf("first Escape mutated draft or status: text=%q media=%d pastes=%d pending=%d status=%q", m.prompt.Value(), len(m.stagedMedia), len(m.stagedPastes), len(m.pendingPromptMedia.Descriptors), m.statusMsg)
	}
}

func TestADR_0025_DoubleEscape_Scenario1_SecondEscapeClearsThroughClearPrompt(t *testing.T) {
	m := doubleEscapeDraft(t)
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("image")}}
	m.stagedPastes = map[string]string{"[Pasted text #1]": "large paste"}
	m.pendingPromptMedia = client.MediaResult{Descriptors: []string{"pending"}}
	m = applyAll(m, escapePress(), escapePress())
	if m.doubleEscapeArmed || !m.prompt.Empty() || len(m.stagedMedia) != 0 || len(m.stagedPastes) != 0 || len(m.pendingPromptMedia.Descriptors) != 0 {
		t.Fatalf("second Escape did not clear via draft funnel: armed=%t text=%q media=%d pastes=%d pending=%d", m.doubleEscapeArmed, m.prompt.Value(), len(m.stagedMedia), len(m.stagedPastes), len(m.pendingPromptMedia.Descriptors))
	}

	attachmentOnly := doubleEscapeDraft(t)
	attachmentOnly.prompt.Reset()
	attachmentOnly.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("image")}}
	attachmentOnly = applyAll(attachmentOnly, escapePress(), escapePress())
	if len(attachmentOnly.stagedMedia) != 0 || attachmentOnly.doubleEscapeArmed {
		t.Fatalf("attachment-only draft was not cleared: media=%d armed=%t", len(attachmentOnly.stagedMedia), attachmentOnly.doubleEscapeArmed)
	}
}

func TestADR_0025_DoubleEscape_Scenario1_HeldEscapeRepeatDoesNotArmOrClear(t *testing.T) {
	m := doubleEscapeDraft(t)
	m = applyAll(m, escapePress(true))
	if m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("repeat armed or cleared from rest: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
	m = applyAll(m, escapePress(), escapePress(true))
	if !m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("repeat counted as confirmation: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
}

func TestADR_0025_DoubleEscape_Scenario1_InterveningKeyDisarmsAndRoutesNormally(t *testing.T) {
	m := doubleEscapeDraft(t)
	m = applyAll(m, escapePress(), tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.doubleEscapeArmed || m.prompt.Value() != "keep this draftx" {
		t.Fatalf("intervening key did not disarm and route: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
	m = applyAll(m, escapePress())
	if !m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("Escape after intervening key was not fresh first press: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
}

func TestADR_0025_DoubleEscape_Scenario1_GenerationGuardsExpiry(t *testing.T) {
	m := doubleEscapeDraft(t)
	m = applyAll(m, escapePress())
	first := m.doubleEscapeGen
	m = applyAll(m, tea.KeyPressMsg{Code: 'x', Text: "x"}, escapePress())
	second := m.doubleEscapeGen
	m = applyAll(m, doubleEscapeExpiryMsg{gen: first})
	if !m.doubleEscapeArmed || second == first {
		t.Fatalf("stale expiry disarmed rearmed gesture: armed=%t first=%d second=%d", m.doubleEscapeArmed, first, second)
	}
	m = applyAll(m, doubleEscapeExpiryMsg{gen: second})
	if m.doubleEscapeArmed {
		t.Fatal("matching expiry did not disarm gesture")
	}
}

func TestADR_0025_DoubleEscape_Scenario1_OwnersConsumeAndDisarm(t *testing.T) {
	assertFreshNext := func(t *testing.T, m Model) {
		t.Helper()
		m.phase = phaseIdle
		m.showHelp = false
		m.closeModal()
		m.prompt.Focus()
		m = applyAll(m, escapePress())
		if !m.doubleEscapeArmed || m.prompt.Empty() {
			t.Fatalf("next idle Escape was not a fresh first press: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
		}
	}
	t.Run("selection", func(t *testing.T) {
		m := doubleEscapeDraft(t)
		m.doubleEscapeArmed = true
		m.prompt.SelectAll()
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed || m.prompt.HasSelection() || m.prompt.Empty() {
			t.Fatalf("selection did not consume/disarm: armed=%t selected=%t text=%q", m.doubleEscapeArmed, m.prompt.HasSelection(), m.prompt.Value())
		}
		assertFreshNext(t, m)
	})
	t.Run("palette", func(t *testing.T) {
		m := doubleEscapeDraft(t)
		m.doubleEscapeArmed = true
		m.palette.open = true
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed || m.palette.open || m.prompt.Empty() {
			t.Fatalf("palette did not consume/disarm: armed=%t open=%t", m.doubleEscapeArmed, m.palette.open)
		}
		assertFreshNext(t, m)
	})
	t.Run("mention", func(t *testing.T) {
		m := doubleEscapeDraft(t)
		m.doubleEscapeArmed = true
		m.mention = mentionState{open: true, matches: []string{"file.go"}}
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed || m.mention.open || m.prompt.Empty() {
			t.Fatalf("mention did not consume/disarm: armed=%t open=%t", m.doubleEscapeArmed, m.mention.open)
		}
		assertFreshNext(t, m)
	})
	t.Run("help overlay", func(t *testing.T) {
		m := doubleEscapeDraft(t)
		m.doubleEscapeArmed = true
		m.showHelp = true
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed || m.showHelp || m.prompt.Empty() {
			t.Fatalf("help did not consume/disarm: armed=%t open=%t", m.doubleEscapeArmed, m.showHelp)
		}
		assertFreshNext(t, m)
	})
	t.Run("approval modal", func(t *testing.T) {
		m := bashAskModel(t, `{"command":"true"}`)
		m.doubleEscapeArmed = true
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed {
			t.Fatal("approval Escape did not disarm gesture")
		}
		m.prompt.Rewrite("keep")
		assertFreshNext(t, m)
	})
	t.Run("running cancel", func(t *testing.T) {
		m, _ := newQueueModel(t)
		m = startRunning(t, m, "first")
		m.prompt.Rewrite("keep")
		m.doubleEscapeArmed = true
		m = applyAll(m, escapePress())
		if m.doubleEscapeArmed || m.prompt.Value() != "keep" || m.statusMsg != "cancelling…" {
			t.Fatalf("running cancel did not consume/disarm: armed=%t text=%q status=%q", m.doubleEscapeArmed, m.prompt.Value(), m.statusMsg)
		}
		assertFreshNext(t, m)
	})
}

func TestADR_0025_DoubleEscape_Scenario1_ClearPromptAlternativeRemainsAccessible(t *testing.T) {
	m := doubleEscapeDraft(t)
	m.keys = applyKeyOverrides(m.keys, map[string][]string{
		"Cancel":      {"ctrl+f34"},
		"ClearPrompt": {"ctrl+f33"},
	})
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png"}}
	m = applyAll(m, escapePress(), escapePress())
	if !m.prompt.Empty() || len(m.stagedMedia) != 0 {
		t.Fatalf("physical gesture followed remapping: text=%q media=%d", m.prompt.Value(), len(m.stagedMedia))
	}

	m.prompt.Rewrite("clear through alternative")
	m.stagedMedia = map[string]stagedAttachment{"[Image #2]": {mime: "image/png"}}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF33, Mod: tea.ModCtrl})
	if !m.prompt.Empty() || len(m.stagedMedia) != 0 {
		t.Fatalf("remapped ClearPrompt alternative stopped working: text=%q media=%d", m.prompt.Value(), len(m.stagedMedia))
	}
}

func TestADR_0025_DoubleEscape_Scenario1_FirstPressAndCompletionHaveNoNewStatus(t *testing.T) {
	m := doubleEscapeDraft(t)
	m.statusMsg = "existing status"
	m = applyAll(m, escapePress())
	if m.statusMsg != "existing status" {
		t.Fatalf("first press changed status: %q", m.statusMsg)
	}
	m = applyAll(m, escapePress())
	if m.statusMsg != "existing status" {
		t.Fatalf("completion changed status: %q", m.statusMsg)
	}
}

func TestADR_0025_DoubleEscape_Scenario1_IsDeterministicWithoutWallClock(t *testing.T) {
	m := doubleEscapeDraft(t)
	m = applyAll(m, escapePress())
	gen := m.doubleEscapeGen
	m = applyAll(m, doubleEscapeExpiryMsg{gen: gen})
	if m.doubleEscapeArmed || m.prompt.Empty() {
		t.Fatalf("message-driven expiry was not deterministic: armed=%t text=%q", m.doubleEscapeArmed, m.prompt.Value())
	}
}

func TestADR_0025_DoubleEscape_Scenario2_LiveHelpExplainsGestureAndAlternative(t *testing.T) {
	body := stripANSIstr(m_helpBody(allOnCaps()))
	for _, want := range []string{"esc esc", "within 500ms", "physical", "not remappable", "ctrl+u", "ClearPrompt"} {
		if !strings.Contains(body, want) {
			t.Errorf("live help missing %q:\n%s", want, body)
		}
	}
}

func TestADR_0025_DoubleEscape_Scenario2_DocumentationNamesCompatibilityContract(t *testing.T) {
	for _, name := range []string{"../../../docs/tui.md", "../../../user-docs/mecatui/keybindings.md"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, want := range []string{"500ms", "physical", "not remappable", "first press", "key repeat", "staged attachments", "large-paste", "pending media", "selection", "ClearPrompt", "ctrl+u"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing compatibility term %q", name, want)
			}
		}
	}
}

func TestADR_0025_DoubleEscape_Scenario2_HelpGoldenChangesAreScoped(t *testing.T) {
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
		if !strings.Contains(string(body), "esc esc") {
			t.Errorf("%s does not document double Escape", name)
		}
	}
}
