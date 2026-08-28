package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// pressMiddle drives a middle-click press through Update. The position is
// arbitrary: the primary-selection paste is position-independent (it goes to the
// prompt input wherever the pointer is, matching terminal convention).
func pressMiddle(m Model) (Model, tea.Cmd) {
	return pressMouse(m, tea.MouseMiddle, 5, 5)
}

// deliver runs cmd and feeds its message back through Update — the one-hop async
// round trip the program performs for the shell primary read.
func deliver(t *testing.T, m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command, got nil")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("command produced no message")
	}
	mm, next := m.Update(msg)
	return mm.(Model), next
}

// TestMiddleClickRequestsPrimaryRead: a middle-click returns a command that runs
// the shell primary-selection read (the fake backend records the call), and
// delivering its result inserts the selection into the prompt.
func TestMiddleClickRequestsPrimaryRead(t *testing.T) {
	cb := &fakeClipboard{primary: "from primary"}
	m, _ := newClipboardModel(t, client.Capabilities{}, cb)

	m, cmd := pressMiddle(m)
	m, _ = deliver(t, m, cmd)

	if cb.primaryCalls != 1 {
		t.Errorf("ReadPrimary calls = %d, want 1", cb.primaryCalls)
	}
	if !strings.Contains(m.prompt.Value(), "from primary") {
		t.Errorf("input = %q, want the primary selection inserted", m.prompt.Value())
	}
}

// TestPrimaryPasteInsertsViaPastePipeline: the retrieved text rides the SAME
// pipeline as a bracketed paste — a small selection lands literally, and a large
// one (>= pasteCharThreshold runes) STAGES behind a [Pasted text #N] placeholder
// (issue #45 parity) instead of flooding the buffer.
func TestPrimaryPasteInsertsViaPastePipeline(t *testing.T) {
	t.Run("small selection inserts literally", func(t *testing.T) {
		cb := &fakeClipboard{primary: "middle-clicked text"}
		m, _ := newClipboardModel(t, client.Capabilities{}, cb)

		m, cmd := pressMiddle(m)
		m, _ = deliver(t, m, cmd)

		if !strings.Contains(m.prompt.Value(), "middle-clicked text") {
			t.Errorf("input = %q, want the selection text", m.prompt.Value())
		}
		if len(m.stagedPastes) != 0 {
			t.Errorf("stagedPastes = %d, want 0 for a small selection", len(m.stagedPastes))
		}
	})

	t.Run("large selection stages as a placeholder", func(t *testing.T) {
		payload := strings.Repeat("x", pasteCharThreshold)
		cb := &fakeClipboard{primary: payload}
		m, _ := newClipboardModel(t, client.Capabilities{}, cb)

		m, cmd := pressMiddle(m)
		m, _ = deliver(t, m, cmd)

		if !strings.Contains(m.prompt.Value(), "[Pasted text #1]") {
			t.Fatalf("input = %q, want the [Pasted text #1] placeholder", m.prompt.Value())
		}
		if strings.Contains(m.prompt.Value(), payload) {
			t.Error("the raw payload entered the textarea; it must stage behind the placeholder")
		}
		if got := m.stagedPastes["[Pasted text #1]"]; got != payload {
			t.Errorf("staged payload = %d bytes, want the full %d-byte selection", len(got), len(payload))
		}
	})
}

// TestPrimaryClipboardMsgSelectionGate: an OSC52 clipboard response is consumed
// only for the PRIMARY selection ('p') — a system-clipboard response ('c') is
// ignored (the app never issues tea.ReadClipboard).
func TestPrimaryClipboardMsgSelectionGate(t *testing.T) {
	m, _ := newClipboardModel(t, client.Capabilities{}, nil)

	mm, _ := m.Update(tea.ClipboardMsg{Content: "system clipboard", Selection: 'c'})
	m = mm.(Model)
	if m.prompt.Value() != "" {
		t.Fatalf("a 'c' ClipboardMsg inserted %q, want it ignored", m.prompt.Value())
	}

	mm, _ = m.Update(tea.ClipboardMsg{Content: "primary selection", Selection: 'p'})
	m = mm.(Model)
	if !strings.Contains(m.prompt.Value(), "primary selection") {
		t.Errorf("input = %q, want the 'p' ClipboardMsg content inserted", m.prompt.Value())
	}
}

// TestPrimaryPasteEmptyNoOp: an empty or whitespace-only primary selection is a
// silent no-op — no insertion, no status (statusline noise for a paste miss is
// worse than nothing). Same for a generic backend error.
func TestPrimaryPasteEmptyNoOp(t *testing.T) {
	for name, cb := range map[string]*fakeClipboard{
		"empty":           {primary: ""},
		"whitespace-only": {primary: " \n\t "},
		"empty sentinel":  {primaryErr: client.ErrEmptyClipboard},
		"backend error":   {primaryErr: errors.New("wl-paste exploded")},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := newClipboardModel(t, client.Capabilities{}, cb)
			before, beforeStatus := m.prompt.Value(), m.statusMsg

			m, cmd := pressMiddle(m)
			m, next := deliver(t, m, cmd)

			if m.prompt.Value() != before {
				t.Errorf("input changed %q → %q, want untouched", before, m.prompt.Value())
			}
			if m.statusMsg != beforeStatus {
				t.Errorf("statusMsg = %q, want unchanged (silent no-op)", m.statusMsg)
			}
			if next != nil {
				t.Error("a no-op delivery returned a follow-up command, want none (stateless)")
			}
		})
	}
}

// TestMiddleClickGatedUnderOverlay: a middle-click while an overlay/modal owns
// the screen (help, the permission modal) starts NO read — same gate as a
// bracketed paste.
func TestMiddleClickGatedUnderOverlay(t *testing.T) {
	cb := &fakeClipboard{primary: "should not appear"}
	m, _ := newClipboardModel(t, client.Capabilities{}, cb)

	m.showHelp = true
	mm, cmd := pressMiddle(m)
	if cmd != nil {
		t.Error("middle-click under the help overlay returned a command, want nil")
	}
	m = mm
	m.showHelp = false
	m.phase = phaseAwaitingApproval
	_, cmd = pressMiddle(m)
	if cmd != nil {
		t.Error("middle-click under the approval modal returned a command, want nil")
	}
	if cb.primaryCalls != 0 {
		t.Errorf("ReadPrimary calls = %d, want 0 while gated", cb.primaryCalls)
	}
}

// TestMiddleClickDoesNotAdvanceClickCount: a middle-click lives outside the
// multi-click sequence (mirroring TestRightClickDoesNotAdvanceClickCount) — it
// must NOT advance clickCount/clickGen, and it leaves an active drag selection
// exactly as it found it.
func TestMiddleClickDoesNotAdvanceClickCount(t *testing.T) {
	m, _, y := convModel(t, "hello world here")

	// Build a real selection via press+drag, then re-arm the count to a known
	// value, exactly like the right-click twin.
	m, _ = pressMouse(m, tea.MouseLeft, 0, y)
	m, _ = motionMouse(m, 5, y) // select "hello"
	if !m.sel.active || m.sel.empty() {
		t.Fatal("precondition: press+drag should leave a non-empty selection")
	}
	m.clickCount = 1
	m.clickGen = 7
	beforeCount, beforeGen, beforeSel := m.clickCount, m.clickGen, m.sel

	m, cmd := pressMouse(m, tea.MouseMiddle, 40, y)

	if m.clickCount != beforeCount {
		t.Errorf("middle-click advanced clickCount %d → %d, want it unchanged", beforeCount, m.clickCount)
	}
	if m.clickGen != beforeGen {
		t.Errorf("middle-click bumped clickGen %d → %d, want it unchanged", beforeGen, m.clickGen)
	}
	if m.sel != beforeSel {
		t.Errorf("middle-click perturbed the selection %+v → %+v, want it unchanged", beforeSel, m.sel)
	}
	if cmd == nil {
		t.Error("middle-click at idle should still return the primary-read command")
	}
}

// gateClosers are the two modal owners the delivery-time gate tests close AFTER
// the middle-click press but BEFORE the async result lands: the help overlay and
// the permission-approval modal (the same pair TestMiddleClickGatedUnderOverlay
// pins at PRESS time).
var gateClosers = map[string]func(m *Model){
	"help overlay":   func(m *Model) { m.showHelp = true },
	"approval modal": func(m *Model) { m.phase = phaseAwaitingApproval },
}

// TestPrimaryReadDeliveryGatedUnderOverlay: the overlay/phase gate re-applies at
// DELIVERY time, not only at press time — a shell primary read that lands after a
// modal opened is dropped, not leaked behind it (the insertPrimaryPaste→onPaste
// pipeline's gate). The payload is deliberately SMALL: a gate bypass would insert
// it straight into the textarea (a large payload would stage instead, and a
// value-only assertion could pass vacuously).
//
// The textarea is RE-FOCUSED after the gate closes (the agents-overlay gate-test
// pattern): a blurred textarea silently no-ops a forwarded paste, which would MASK
// a missing gate — re-focusing makes the gate the SOLE line of defence.
func TestPrimaryReadDeliveryGatedUnderOverlay(t *testing.T) {
	for name, closeGate := range gateClosers {
		t.Run(name, func(t *testing.T) {
			cb := &fakeClipboard{primary: "late leak"}
			m, _ := newClipboardModel(t, client.Capabilities{}, cb)

			m, cmd := pressMiddle(m) // gate OPEN at press time: the read starts
			if cmd == nil {
				t.Fatal("middle-click at idle returned no command, want the primary read")
			}
			closeGate(&m)
			_ = m.prompt.Focus() // defeat the blur masking — exercise the gate, not the blur.

			msg := cmd()
			mm, next := m.Update(msg)
			m = mm.(Model)

			if cb.primaryCalls != 1 {
				t.Fatalf("ReadPrimary calls = %d, want 1 (the read ran; the GATE must drop its result)", cb.primaryCalls)
			}
			if got := m.prompt.Value(); got != "" {
				t.Errorf("late primary read leaked into the input behind the modal: %q", got)
			}
			if len(m.stagedPastes) != 0 {
				t.Errorf("stagedPastes = %d, want 0 (the store must stay untouched)", len(m.stagedPastes))
			}
			if next != nil {
				t.Error("a gated delivery returned a follow-up command, want none")
			}
		})
	}
}

// TestPrimaryClipboardMsgDeliveryGatedUnderOverlay: the OSC52 twin of the
// delivery-time gate — a late ClipboardMsg{Selection: 'p'} (the terminal answering
// a primary read issued before the modal opened) is dropped, not leaked behind it.
// Small payload + the re-focus defeat, for the same reasons as the shell variant.
func TestPrimaryClipboardMsgDeliveryGatedUnderOverlay(t *testing.T) {
	for name, closeGate := range gateClosers {
		t.Run(name, func(t *testing.T) {
			m, _ := newClipboardModel(t, client.Capabilities{}, nil)

			m, cmd := pressMiddle(m) // gate OPEN at press time: the OSC52 query is issued
			if cmd == nil {
				t.Fatal("middle-click at idle returned no command, want the OSC52 primary read")
			}
			closeGate(&m)
			_ = m.prompt.Focus() // defeat the blur masking — exercise the gate, not the blur.

			mm, next := m.Update(tea.ClipboardMsg{Content: "late osc52 leak", Selection: 'p'})
			m = mm.(Model)

			if got := m.prompt.Value(); got != "" {
				t.Errorf("late OSC52 response leaked into the input behind the modal: %q", got)
			}
			if len(m.stagedPastes) != 0 {
				t.Errorf("stagedPastes = %d, want 0 (the store must stay untouched)", len(m.stagedPastes))
			}
			if next != nil {
				t.Error("a gated delivery returned a follow-up command, want none")
			}
		})
	}
}

// TestRapidDoubleMiddleClickInsertsBothInOrder: two middle-clicks before either
// read delivers — both results insert, in delivery order, with no panic and no
// cross-talk (the path is stateless: nothing is parked per request, so concurrent
// in-flight reads cannot corrupt each other).
func TestRapidDoubleMiddleClickInsertsBothInOrder(t *testing.T) {
	cb := &fakeClipboard{primary: "alpha"}
	m, _ := newClipboardModel(t, client.Capabilities{}, cb)

	m, cmd1 := pressMiddle(m)
	m, cmd2 := pressMiddle(m) // second press before the first result lands
	if cmd1 == nil || cmd2 == nil {
		t.Fatal("both middle-clicks should return a primary-read command")
	}

	// The commands read at EXECUTION time; re-script the selection between them so
	// the two deliveries are distinguishable and order is assertable.
	msg1 := cmd1()
	cb.primary = "bravo"
	msg2 := cmd2()

	m, _ = deliver(t, m, func() tea.Msg { return msg1 })
	m, _ = deliver(t, m, func() tea.Msg { return msg2 })

	if cb.primaryCalls != 2 {
		t.Errorf("ReadPrimary calls = %d, want 2", cb.primaryCalls)
	}
	val := m.prompt.Value()
	i, j := strings.Index(val, "alpha"), strings.Index(val, "bravo")
	if i < 0 || j < 0 {
		t.Fatalf("input = %q, want BOTH selections inserted", val)
	}
	if i > j {
		t.Errorf("input = %q, want the deliveries inserted in order (alpha before bravo)", val)
	}
}

// TestMiddleClickNoBackendFallsBackToOSC52: with no shell backend
// (ErrNoClipboardTool) the delivery's follow-up command is the OSC52 primary
// read, and a terminal that answers it (ClipboardMsg{Selection: 'p'}) gets its
// content inserted. Stateless: nothing is parked between the query and the
// (possibly never-arriving) response.
func TestMiddleClickNoBackendFallsBackToOSC52(t *testing.T) {
	cb := &fakeClipboard{primaryErr: client.ErrNoClipboardTool}
	m, _ := newClipboardModel(t, client.Capabilities{}, cb)

	m, cmd := pressMiddle(m)
	m, next := deliver(t, m, cmd)

	if next == nil {
		t.Fatal("no-backend delivery returned no follow-up command, want the OSC52 primary read")
	}
	if got := next(); got != tea.ReadPrimaryClipboard() {
		t.Fatalf("follow-up command produced %T, want the OSC52 primary-read message", got)
	}
	// The terminal answers the query: the response inserts.
	mm, _ := m.Update(tea.ClipboardMsg{Content: "osc52 primary", Selection: 'p'})
	m = mm.(Model)
	if !strings.Contains(m.prompt.Value(), "osc52 primary") {
		t.Errorf("input = %q, want the OSC52 response inserted", m.prompt.Value())
	}
}

// TestMiddleClickNilClipboardFallsBackToOSC52: a nil Clipboard collaborator (the
// inject-time disable) skips the shell hop entirely — the middle-click command IS
// the OSC52 primary read.
func TestMiddleClickNilClipboardFallsBackToOSC52(t *testing.T) {
	m, _ := newClipboardModel(t, client.Capabilities{}, nil)

	_, cmd := pressMiddle(m)
	if cmd == nil {
		t.Fatal("middle-click with a nil Clipboard returned no command, want the OSC52 primary read")
	}
	if got := cmd(); got != tea.ReadPrimaryClipboard() {
		t.Fatalf("command produced %T, want the OSC52 primary-read message", got)
	}
}
