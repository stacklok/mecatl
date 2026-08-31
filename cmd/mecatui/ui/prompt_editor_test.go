package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestClearPromptClearsOnlyDraftState(t *testing.T) {
	m, _ := newQueueModel(t)
	m = applyAll(m, pasteMsg(largePasteText()))
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("image")}}
	m.nextMediaN = 1
	m.queued = []string{"queued follow-up"}
	m.queuedMedia = client.MediaResult{Descriptors: []string{"queued media"}}
	m.pendingPromptMedia = client.MediaResult{Descriptors: []string{"draft media"}}
	m.prompt.Focus()

	mm, _ := m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.prompt.Empty() || len(m.stagedMedia) != 0 || len(m.stagedPastes) != 0 || m.nextMediaN != 1 || m.nextPasteN != 1 {
		t.Fatalf("ClearPrompt left draft state: text=%q media=%d pastes=%d next=(%d,%d)", m.prompt.Value(), len(m.stagedMedia), len(m.stagedPastes), m.nextMediaN, m.nextPasteN)
	}
	if len(m.pendingPromptMedia.Descriptors) != 0 {
		t.Fatalf("ClearPrompt left pending draft media: %+v", m.pendingPromptMedia)
	}
	if !m.prompt.Focused() {
		t.Fatal("ClearPrompt lost prompt focus")
	}
	if got := m.queued; len(got) != 1 || got[0] != "queued follow-up" || len(m.queuedMedia.Descriptors) != 1 {
		t.Fatalf("ClearPrompt changed queued follow-up state: queue=%q media=%+v", got, m.queuedMedia)
	}
}

func TestClearPromptWorksWhileRunning(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = applyAll(m, pasteMsg(largePasteText()))
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("image")}}
	m.nextMediaN = 1
	m.pendingPromptMedia = client.MediaResult{Descriptors: []string{"draft media"}}
	m.prompt.Focus()
	m.queued = []string{"queued follow-up"}
	m.queuedMedia = client.MediaResult{Descriptors: []string{"queued media"}}

	mm, _ := m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m = mm.(Model)
	if m.phase != phaseRunning || !m.prompt.Empty() || len(m.stagedMedia) != 0 || len(m.stagedPastes) != 0 || m.nextMediaN != 1 || m.nextPasteN != 1 || len(m.pendingPromptMedia.Descriptors) != 0 {
		t.Fatalf("ClearPrompt while running left draft state: phase=%d text=%q media=%d pastes=%d next=(%d,%d) pending=%+v", m.phase, m.prompt.Value(), len(m.stagedMedia), len(m.stagedPastes), m.nextMediaN, m.nextPasteN, m.pendingPromptMedia)
	}
	if !m.prompt.Focused() {
		t.Fatal("ClearPrompt while running lost prompt focus")
	}
	if len(m.queued) != 1 || len(m.queuedMedia.Descriptors) != 1 {
		t.Fatalf("ClearPrompt cleared queued follow-up: %q", m.queued)
	}
}

func TestEscapePreservesDraftIdleAndCancelsRunningDirectly(t *testing.T) {
	t.Run("idle draft remains", func(t *testing.T) {
		m, _ := newQueueModel(t)
		m.prompt.Rewrite("keep this draft")
		mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		m = mm.(Model)
		if got := m.prompt.Value(); got != "keep this draft" {
			t.Fatalf("idle Escape cleared draft: %q", got)
		}
	})

	t.Run("selection clears first", func(t *testing.T) {
		m, _ := newQueueModel(t)
		m.prompt.Rewrite("keep this draft")
		m.prompt.SelectAll()
		mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		m = mm.(Model)
		if m.prompt.HasSelection() || m.prompt.Value() != "keep this draft" {
			t.Fatalf("Escape selection precedence = text %q selected %t", m.prompt.Value(), m.prompt.HasSelection())
		}
	})

	t.Run("running sends cancel without clearing composition", func(t *testing.T) {
		m, conv := newQueueModel(t)
		m = startRunning(t, m, "first")
		m.prompt.Rewrite("keep this draft")
		m.queued = []string{"queued follow-up"}
		mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		m = mm.(Model)
		runBatchLeaves(cmd)
		if m.prompt.Value() != "keep this draft" || len(m.queued) != 1 {
			t.Fatalf("running Escape changed composition: text=%q queue=%q", m.prompt.Value(), m.queued)
		}
		frames := conv.send.frames()
		if len(frames) == 0 || frames[len(frames)-1].GetCancel() == nil {
			t.Fatalf("running Escape did not send Cancel: %#v", frames)
		}
	})
}

func TestDynamicPromptRelayoutPreservesConversationSelection(t *testing.T) {
	m, _ := selModel(t)
	m = applyAll(m, tea.WindowSizeMsg{Width: 24, Height: 30})
	m, _ = pressMouse(m, tea.MouseLeft, 0, convTopRow(m))
	m, _ = motionMouse(m, 3, convTopRow(m))
	m, _ = releaseMouse(m, 3, convTopRow(m))
	if !m.sel.active {
		t.Fatal("precondition: conversation selection did not activate")
	}
	beforeSelection := m.sel
	beforeOffset := m.vp.YOffset()
	// Drive an ordinary multiline paste through the real root reducer, then type
	// enough soft-wrapped text through that same reducer to stay beyond the cap.
	mm, _ := m.Update(pasteMsg("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine"))
	m = mm.(Model)
	for _, r := range strings.Repeat(" wrap", 20) {
		mm, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	if m.prompt.Height() != 8 {
		t.Fatalf("prompt height = %d, want cap 8", m.prompt.Height())
	}
	if !m.sel.active || m.sel.anchorL != beforeSelection.anchorL || m.sel.anchorC != beforeSelection.anchorC || m.sel.headL != beforeSelection.headL || m.sel.headC != beforeSelection.headC {
		t.Fatalf("prompt reflow changed conversation selection: got %+v, want %+v", m.sel, beforeSelection)
	}
	if m.vp.YOffset() != beforeOffset {
		t.Fatalf("prompt reflow changed conversation offset: got %d, want %d", m.vp.YOffset(), beforeOffset)
	}
}
