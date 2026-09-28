package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type selectionTestSurface struct{ body string }

type selectionTestFillSurface struct{ selectionTestSurface }

func (*selectionTestFillSurface) modalPlacement() modalPlacement { return modalPlacementFill }

func (s *selectionTestSurface) Render(int, int) (string, []ClickableRegion) { return s.body, nil }
func (*selectionTestSurface) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	return nil, true, false
}
func (*selectionTestSurface) HandleMsg(tea.Msg) (tea.Cmd, bool, bool) { return nil, false, false }
func (*selectionTestSurface) HandleWheel(tea.MouseWheelMsg) (tea.Cmd, bool) {
	return nil, true
}
func (*selectionTestSurface) Close() {}

func selectBodyMarker(t *testing.T, m Model, marker string) (Model, tea.Cmd) {
	t.Helper()
	_ = m.View()
	for line, rendered := range strings.Split(m.bodyFrame.body, "\n") {
		plain := stripANSIstr(rendered)
		if at := strings.Index(plain, marker); at >= 0 {
			col := lipgloss.Width(plain[:at])
			width := lipgloss.Width(marker)
			x, y := m.bodyFrame.origin.x+col, m.bodyFrame.origin.y+line
			m, _ = pressMouse(m, tea.MouseLeft, x, y)
			m, _ = motionMouse(m, x+width, y)
			return releaseMouse(m, x+width, y)
		}
	}
	t.Fatalf("body does not contain %q: %q", marker, stripANSIstr(m.bodyFrame.body))
	return m, nil
}

func TestBodySelectionIsRootOwnedForSurfaceAndLegacyOverlay(t *testing.T) {
	m, _ := selModel(t)
	m.prompt.Rewrite("hidden prompt")
	m.prompt.SelectAll()
	m.sel = selection{active: true, anchorL: 0, headL: 1}
	m.modal = &selectionTestSurface{body: "trivial modal marker"}

	m, cmd := selectBodyMarker(t, m, "modal marker")
	if got := m.bodyFrame.selectedText(); got != "modal marker" {
		t.Fatalf("modal selection = %q", got)
	}
	if payload, ok := osc52Payload(collectLeaves(cmd)); !ok || payload != "modal marker" {
		t.Fatalf("release payload = %q, ok=%v", payload, ok)
	}
	if !m.prompt.HasSelection() {
		t.Fatal("completed prompt selection was not preserved behind surface")
	}
	if !strings.Contains(m.View().Content, m.deps.Theme.Style("selection").Render("modal marker")) {
		t.Fatal("selected modal text is not visibly highlighted")
	}

	m, cmd = pressMouse(m, tea.MouseRight, 0, 0)
	if payload, ok := osc52Payload(collectLeaves(cmd)); !ok || payload != "modal marker" {
		t.Fatalf("right-click payload = %q, ok=%v", payload, ok)
	}
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if payload, ok := osc52Payload(collectLeaves(cmd)); !ok || payload != "modal marker" {
		t.Fatalf("CopySelection payload = %q, ok=%v", payload, ok)
	}
	m, cmd = pressMouse(m, tea.MouseMiddle, 0, 0)
	if cmd != nil || m.prompt.Value() != "hidden prompt" {
		t.Fatal("middle-click reached hidden prompt")
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal == nil || m.bodyFrame.selectedText() != "" {
		t.Fatal("first Escape did not clear body selection before surface action")
	}
	beforeHidden := m.sel
	m, _ = motionMouse(m, 0, 0)
	m, cmd = releaseMouse(m, 0, 0)
	if cmd != nil || m.sel != beforeHidden {
		t.Fatal("body motion/release mutated hidden conversation selection")
	}
	m, cmd = pressMouse(m, tea.MouseRight, 0, 0)
	if cmd != nil {
		t.Fatal("right-click fell through to hidden prompt or conversation selection")
	}

	m.modal = nil
	m.showHelp = true
	m, _ = selectBodyMarker(t, m, "Prompting")
	if got := m.bodyFrame.selectedText(); got != "Prompting" {
		t.Fatalf("legacy help selection = %q", got)
	}
	m.vp.SetContent(strings.Repeat("hidden conversation\n", 100))
	m.vp.SetYOffset(10)
	beforeOffset := m.vp.YOffset()
	mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mm.(Model)
	if m.vp.YOffset() != beforeOffset {
		t.Fatal("wheel over a body owner scrolled the hidden conversation")
	}
}

func TestBodySelectionFatalAndCaptureGates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noMouse    bool
		noAlt      bool
		wantSelect bool
	}{
		{name: "captured", wantSelect: true},
		{name: "no mouse", noMouse: true},
		{name: "inline", noAlt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := selModel(t)
			m.deps.NoMouse, m.deps.NoAltScreen = tc.noMouse, tc.noAlt
			m.phase, m.fatalErr = phaseFatal, "fatal marker"
			m, cmd := selectBodyMarker(t, m, "fatal marker")
			if got := m.bodyFrame.selectedText(); (got != "") != tc.wantSelect {
				t.Fatalf("fatal selection = %q, want active=%v", got, tc.wantSelect)
			}
			if !tc.wantSelect && cmd != nil {
				t.Fatal("uncaptured posture created copy command")
			}
		})
	}
}

func TestBodySelectionCaptureGatesAcrossOwnerKinds(t *testing.T) {
	owners := []struct {
		name   string
		marker string
		setup  func(*Model)
	}{
		{name: "legacy overlay", marker: "Prompting", setup: func(m *Model) { m.showHelp = true }},
		{name: "card modal", marker: "card marker", setup: func(m *Model) { m.modal = &selectionTestSurface{body: "card marker"} }},
		{name: "fill modal", marker: "fill marker", setup: func(m *Model) { m.modal = &selectionTestFillSurface{selectionTestSurface{body: "fill marker"}} }},
	}
	for _, owner := range owners {
		for _, gate := range []struct {
			name    string
			noMouse bool
			noAlt   bool
		}{
			{name: "no mouse", noMouse: true},
			{name: "inline", noAlt: true},
		} {
			t.Run(owner.name+"/"+gate.name, func(t *testing.T) {
				m, _ := selModel(t)
				owner.setup(&m)
				m.deps.NoMouse, m.deps.NoAltScreen = gate.noMouse, gate.noAlt
				m, cmd := selectBodyMarker(t, m, owner.marker)
				if m.bodyFrame.selectedText() != "" || cmd != nil {
					t.Fatal("uncaptured posture created an in-app body selection")
				}
			})
		}
	}
}

func TestBodySelectionFatalDebugHeaderAndReplay(t *testing.T) {
	m, _ := selModel(t)
	m.deps.DebugTarget = "remote"
	m.phase, m.fatalErr = phaseFatal, "debug fatal marker"
	m, cmd := selectBodyMarker(t, m, "fatal marker")
	if payload, ok := osc52Payload(collectLeaves(cmd)); !ok || payload != "fatal marker" {
		t.Fatalf("debug fatal payload = %q, ok=%v", payload, ok)
	}

	m, _ = selModel(t)
	m.phase = phaseReplay
	m.vp.SetContent("stored replay marker")
	m.rend.invalidateVPView()
	m, cmd = selectBodyMarker(t, m, "replay marker")
	if payload, ok := osc52Payload(collectLeaves(cmd)); !ok || payload != "replay marker" {
		t.Fatalf("replay payload = %q, ok=%v", payload, ok)
	}
}

func TestBodySelectionAuthorizationCopyPrecedence(t *testing.T) {
	controller := &mcpAuthorizationControllerFake{}
	clipboard := &fakeClipboard{}
	m := New(Deps{
		Theme:            theme.New("aztec", theme.AztecPalette()),
		MCPAuthorization: controller,
		Clipboard:        clipboard,
	})
	m.deps.NoAltScreen = false
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"CopySelection": {"alt+c"}})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	m, _ = selectBodyMarker(t, m, "Copy Link")

	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModAlt})
	if payload, ok := osc52Payload(collectLeaves(cmd)); !ok || payload != "Copy Link" {
		t.Fatalf("selected authorization copy = %q, ok=%v", payload, ok)
	}
	if controller.presentation != 0 {
		t.Fatal("authorization Copy Link action ran instead of copying body selection")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	_, cmd = pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModAlt})
	if cmd == nil {
		t.Fatal("authorization Copy Link action disappeared with no body selection")
	}
	_ = cmd()
	if controller.presentation != 1 {
		t.Fatalf("authorization presentations = %d, want 1", controller.presentation)
	}
}

func TestBodySelectionInvalidatesOnFrameIdentity(t *testing.T) {
	m, _ := selModel(t)
	first := &selectionTestSurface{body: "same marker"}
	m.modal = first
	m, _ = selectBodyMarker(t, m, "marker")

	first.body = "changed marker"
	_ = m.View()
	if m.bodyFrame.selectedText() != "" {
		t.Fatal("body change retained stale selection")
	}
	m, _ = selectBodyMarker(t, m, "marker")
	m.modal = &selectionTestSurface{body: "changed marker"}
	_ = m.View()
	if m.bodyFrame.selectedText() != "" {
		t.Fatal("new modal instance with identical text retained selection")
	}
	m, _ = selectBodyMarker(t, m, "marker")
	m.width++
	_ = m.View()
	if m.bodyFrame.selectedText() != "" {
		t.Fatal("geometry change retained stale selection")
	}

	m, _ = selectBodyMarker(t, m, "marker")
	m.closeModal()
	if m.bodyFrame.owner.valid() || m.bodyFrame.selectedText() != "" {
		t.Fatal("closing a body owner did not clear its render frame")
	}

	m.modal = &selectionTestSurface{body: "old marker"}
	m, _ = selectBodyMarker(t, m, "old marker")
	m.modal = &selectionTestSurface{body: "new marker"}
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if _, ok := osc52Payload(collectLeaves(cmd)); ok || m.bodyFrame.owner.valid() {
		t.Fatal("keyboard copy reused a stale owner frame before the next render")
	}
}

func TestApprovalButtonHitPrecedesBodySelection(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "sess-hit:1:shell", Tool: "Shell", Args: `{"command":"echo marker"}`})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	_ = m.View()
	if len(m.hits.frame) == 0 {
		t.Fatal("approval rendered no button hits")
	}
	hit := m.hits.frame[0].rect
	x, y := m.metrics.localToGlobal(hit.x0, hit.y0)
	m, _ = pressMouse(m, tea.MouseLeft, x, y)
	if m.bodyFrame.selection.active {
		t.Fatal("button click started text selection")
	}
}
