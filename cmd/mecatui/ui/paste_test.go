package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// newPastePathModel builds an idle Model rooted at ws (where the media file
// lives), so a pasted PATH resolves and stats against a real tree.
func newPastePathModel(t *testing.T, ws string, caps client.Capabilities) Model {
	t.Helper()
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := New(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   ws,
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	return applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
}

// TestPasteImagePathStages: pasting a single media FILE PATH stages it as an
// attachment ([Image #1]) instead of inserting the path literally.
func TestPasteImagePathStages(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "shot.png"), tinyPNG(t), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := newPastePathModel(t, ws, client.Capabilities{Image: true})

	mm, _ := m.Update(pasteMsg("shot.png"))
	m = mm.(Model)

	if len(m.stagedMedia) != 1 {
		t.Fatalf("staged = %d, want 1 (pasted media path)", len(m.stagedMedia))
	}
	if !strings.Contains(m.ta.Value(), "[Image #1]") {
		t.Errorf("input = %q, want the marker, not the literal path", m.ta.Value())
	}
	if strings.Contains(m.ta.Value(), "shot.png") {
		t.Errorf("input = %q, want the path replaced by the marker", m.ta.Value())
	}
}

// TestPasteNonImagePathLiteral: pasting a path that is NOT a media file stays
// literal text and stages nothing (fall-through to iteration-1 behaviour).
func TestPasteNonImagePathLiteral(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := newPastePathModel(t, ws, client.Capabilities{Image: true})

	mm, _ := m.Update(pasteMsg("notes.txt"))
	m = mm.(Model)

	if len(m.stagedMedia) != 0 {
		t.Fatalf("staged = %d, want 0 (a .txt path is not media)", len(m.stagedMedia))
	}
	if !strings.Contains(m.ta.Value(), "notes.txt") {
		t.Errorf("input = %q, want the literal path", m.ta.Value())
	}
}

// TestPasteImagePathCapGatedLiteral: a media path on an image-INCAPABLE server is
// NOT staged — StagePathMedia refuses, so onPaste falls through to literal text
// (no loud error, no marker).
func TestPasteImagePathCapGatedLiteral(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "shot.png"), tinyPNG(t), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := newPastePathModel(t, ws, client.Capabilities{Image: false})

	mm, _ := m.Update(pasteMsg("shot.png"))
	m = mm.(Model)

	if len(m.stagedMedia) != 0 {
		t.Fatalf("staged = %d, want 0 (cap-gated path falls through to literal)", len(m.stagedMedia))
	}
	if !strings.Contains(m.ta.Value(), "shot.png") {
		t.Errorf("input = %q, want the literal path on fall-through", m.ta.Value())
	}
	if strings.Contains(stripANSIstr(m.View().Content), "attach:") {
		t.Errorf("cap-gated path-paste should NOT raise a loud error (it falls through)")
	}
}

// pasteMsg is the bracketed-paste message Bubble Tea v2 emits on a paste — a
// struct with the pasted Content (NOT a KeyPressMsg), which is why it needs its
// own reducer case (onPaste) rather than passing through onKey.
func pasteMsg(s string) tea.PasteMsg { return tea.PasteMsg{Content: s} }

// TestPasteLandsInInputWhenIdle: a paste at idle inserts the clipboard text into
// the prompt textarea (proving onPaste forwards it; without the case the paste
// would fall through to update's no-op default and be dropped).
func TestPasteLandsInInputWhenIdle(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	if m.phase != phaseIdle {
		t.Fatalf("expected phaseIdle, got %d", m.phase)
	}

	mm, _ := m.Update(pasteMsg("hello pasted world"))
	m = mm.(Model)

	if got := m.ta.Value(); got != "hello pasted world" {
		t.Fatalf("input value = %q, want the pasted text", got)
	}
	if len(m.stagedMedia) != 0 {
		t.Fatalf("a normal text paste must stage no media, got %v", m.stagedMedia)
	}
}

func TestPasteRestoresModifyOtherKeysNewlines(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())

	mm, _ := m.Update(pasteMsg("first" + xtermModifyOtherKeysCtrlJ + "second"))
	m = mm.(Model)

	if got := m.ta.Value(); got != "first\nsecond" {
		t.Fatalf("input value = %q, want decoded newline", got)
	}
}

// TestPasteLandsInInputWhileRunning: a paste mid-run lands in the textarea (the
// input stays focused for compose/enqueue while running) WITHOUT enqueuing —
// there is no enter, so the phase stays running and nothing is staged.
func TestPasteLandsInInputWhileRunning(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")

	mm, _ := m.Update(pasteMsg("a follow-up draft"))
	m = mm.(Model)

	if m.phase != phaseRunning {
		t.Fatalf("paste must not change the running phase, got %d", m.phase)
	}
	if len(m.queued) != 0 {
		t.Fatalf("paste must not enqueue (no enter), got %v", m.queued)
	}
	if !strings.Contains(m.ta.Value(), "a follow-up draft") {
		t.Fatalf("input value = %q, want it to contain the pasted text", m.ta.Value())
	}
}

// TestPasteIgnoredWhileHelpOpen: the help overlay owns the keyboard, so a paste
// is dropped — it must not leak into the input behind the modal. Asserts the
// value stays empty AND help stays open (a meaningful drop, not a coincidence).
func TestPasteIgnoredWhileHelpOpen(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	// "?" on an empty prompt opens the help overlay.
	mm, _ := m.Update(qmark())
	m = mm.(Model)
	if !m.showHelp {
		t.Fatalf("help overlay should be open after '?'")
	}

	mm, _ = m.Update(pasteMsg("leak attempt"))
	m = mm.(Model)

	if got := m.ta.Value(); got != "" {
		t.Fatalf("paste leaked into input behind help overlay: %q", got)
	}
	if !m.showHelp {
		t.Fatalf("help overlay should still be open after an ignored paste")
	}
}

// TestPasteIgnoredDuringApproval: the permission modal owns the keyboard, so a
// paste is dropped. Asserts the value stays empty AND the phase stays awaiting
// approval (so the paste neither leaked into the input nor resolved the modal).
func TestPasteIgnoredDuringApproval(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = applyAll(m, client.PermissionAskMsg{
		AskID: "ask-1", Tool: "Write", Args: `{"path":"note.txt"}`, Reason: "approval required",
	})
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("expected phaseAwaitingApproval, got %d", m.phase)
	}

	mm, _ := m.Update(pasteMsg("leak attempt"))
	m = mm.(Model)

	if got := m.ta.Value(); got != "" {
		t.Fatalf("paste leaked into input behind the approval modal: %q", got)
	}
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("paste must not resolve the modal, phase=%d", m.phase)
	}
}

// TestPasteIgnoredWhileMCPOverlayOpen: an open MCP overlay owns the keyboard, so
// a paste is dropped. Asserts the value stays empty AND the overlay stays open.
func TestPasteIgnoredWhileMCPOverlayOpen(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePanelMCP())
	m = openOverlay(t, m, ctrlKey('o'))
	if m.mcp.view == mcpNone {
		t.Fatalf("MCP overlay should be open after ctrl+o")
	}

	mm, _ := m.Update(pasteMsg("leak attempt"))
	m = mm.(Model)

	if got := m.ta.Value(); got != "" {
		t.Fatalf("paste leaked into input behind the MCP overlay: %q", got)
	}
	if m.mcp.view == mcpNone {
		t.Fatalf("MCP overlay should still be open after an ignored paste")
	}
}

// TestPasteIgnoredWhileAgentsOverlayOpen: an open agent-team overlay owns the
// keyboard (idle-only), so a paste is dropped. Asserts the value stays empty AND
// the overlay stays open — this pins the `|| m.team.view != teamNone` term in
// onPaste's gate.
//
// The textarea is deliberately RE-FOCUSED after the overlay opens (openTeam
// blurs it, and a blurred textarea silently no-ops a forwarded paste — which would
// MASK a missing gate term). Re-focusing makes the agents gate term the SOLE line
// of defence, so deleting `|| m.team.view != teamNone` from onPaste genuinely
// fails this test (the focused textarea would otherwise insert the runes). This is
// the defense-in-depth the gate provides: drop the paste while the overlay owns the
// keyboard regardless of the textarea's focus state.
func TestPasteIgnoredWhileAgentsOverlayOpen(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
	})
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	if m.team.view == teamNone {
		t.Fatalf("agents overlay should be open after ctrl+a")
	}
	_ = m.ta.Focus() // defeat the blur masking — exercise the gate, not the blur.

	mm, _ = m.Update(pasteMsg("leak attempt"))
	m = mm.(Model)

	if got := m.ta.Value(); got != "" {
		t.Fatalf("paste leaked into input behind the agents overlay: %q", got)
	}
	if m.team.view == teamNone {
		t.Fatalf("agents overlay should still be open after an ignored paste")
	}
}

// TestPasteIgnoredWhileConnecting: a fresh model is in phaseConnecting (before
// SessionReadyMsg), which is NOT an input-accepting phase — a paste there is
// dropped. This pins onPaste's second guard (the phaseIdle/phaseRunning
// restriction); removing it would let a paste land before the session is ready.
func TestPasteIgnoredWhileConnecting(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := New(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       aztec(),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	if m.phase != phaseConnecting {
		t.Fatalf("fresh model should be phaseConnecting, got %d", m.phase)
	}

	mm, _ := m.Update(pasteMsg("too early"))
	m = mm.(Model)

	if got := m.ta.Value(); got != "" {
		t.Fatalf("paste landed before the session was ready: %q", got)
	}
	if m.phase != phaseConnecting {
		t.Fatalf("paste must not change the connecting phase, got %d", m.phase)
	}
}

// TestPasteSyncsPalette: pasting a "/<prefix>" line runs through afterInputEdit —
// the same funnel typed input uses — so the slash palette opens and filters on
// the pasted prefix. Proves the paste did not bypass the palette re-sync.
func TestPasteSyncsPalette(t *testing.T) {
	m := newPaletteModel(t, sampleCommands())

	mm, _ := m.Update(pasteMsg("/re"))
	m = mm.(Model)

	if !m.palette.open {
		t.Fatalf("palette should open after pasting a '/' command prefix")
	}
	if got := m.ta.Value(); got != "/re" {
		t.Fatalf("input value = %q, want the pasted '/re'", got)
	}
	// "/re" filters the seeded workspace rows to review + refactor.
	if len(m.palette.filtered) != 2 {
		t.Fatalf("palette filtered = %d, want 2 (review, refactor) after pasting '/re'", len(m.palette.filtered))
	}
}
