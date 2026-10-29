package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// Exercise the real modal routing, not the picker's private key handler.
func TestConnectRoutedConfirmationPinsChoiceAndBack(t *testing.T) {
	m := newConnectModel([]ConnectTarget{{Target: "first:443"}, {Target: "second:443"}})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 32})
	mm, load := m.runConnect()
	m = applyAll(mm.(Model), load())
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Code: tea.KeyEnter})
	intent, ok := m.ConnectRestartIntent()
	if !ok || intent.Target != "first:443" {
		t.Fatalf("confirmation/Back changed choice: %+v, %v", intent, ok)
	}
}

func TestConnectRoutedResizeBeforeEnterIsCloseOnly(t *testing.T) {
	m := newConnectModel([]ConnectTarget{{Target: "first:443"}})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 32})
	mm, load := m.runConnect()
	m = applyAll(mm.(Model), load())
	_ = m.View()
	m = applyAll(m, tea.WindowSizeMsg{Width: 8, Height: 3}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if intent, ok := m.ConnectRestartIntent(); ok {
		t.Fatalf("hidden choice activated: %+v", intent)
	}
}

func connectInventory(n int) []ConnectTarget {
	targets := make([]ConnectTarget, n)
	for i := range targets {
		targets[i] = ConnectTarget{Target: fmt.Sprintf("remote-%02d:443", i), Issuer: strings.Repeat("issuer value ", 5), ClientID: "public-client", Audience: "api"}
	}
	return targets
}

func assertConnectSelectedRendered(t *testing.T, m Model, target ConnectTarget) string {
	t.Helper()
	body := m.renderBody()
	s := m.modal.(*connectState)
	label := target.Target
	if label == "" {
		label = "OIDC sign-in for a new target…"
	}
	if plain := stripANSIstr(body); !strings.Contains(plain, "▶ "+label) || strings.Count(plain, "▶") != 1 {
		t.Fatalf("selected target %q not uniquely revealed with its marker:\n%s", label, plain)
	}
	rows := []string{"▶ " + label}
	if target.Target != "" {
		metadata := target.Issuer + " · " + target.ClientID + " · " + target.Audience
		for _, line := range strings.Split(ansi.Hardwrap(metadata, s.offer[0]-2, true), "\n") {
			rows = append(rows, "  "+line)
		}
	}
	for _, row := range rows {
		selected := strings.TrimSuffix(m.deps.Theme.Style("spinner").Render(row), "\x1b[m")
		if selected == strings.TrimSuffix(m.deps.Theme.Style("muted").Render(row), "\x1b[m") {
			t.Fatal("fixture cannot distinguish selected/unselected styling")
		}
		if !strings.Contains(body, selected) {
			t.Fatalf("selected row/continuation missing accent %q:\n%s", row, body)
		}
	}
	return stripANSIstr(body)
}

func TestConnectRoutedWheelConfirmationAndNavigation(t *testing.T) {
	targets := connectInventory(24)
	m := newConnectModel(targets)
	m.keys.Up = key.NewBinding(key.WithKeys("k"))
	m.keys.Down = key.NewBinding(key.WithKeys("j"))
	m.keys.ScrollD = key.NewBinding(key.WithKeys("f"))
	m.keys.ScrollTop = key.NewBinding(key.WithKeys("g"))
	m.keys.ScrollBottom = key.NewBinding(key.WithKeys("G"))
	m = applyAll(m, tea.WindowSizeMsg{Width: 64, Height: 32})
	m = openTestConnect(m, "", "", "")
	s := m.modal.(*connectState)
	m.vp.SetContent(strings.Repeat("hidden conversation\n", 100))
	m.vp.SetYOffset(8)
	transcript := m.vp.YOffset()
	_ = m.View()
	width, height := lipgloss.Size(m.renderBody())
	selected := s.list.CursorID()
	for range 5 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	if s.list.Offset() == 0 || s.list.CursorID() != selected || m.vp.YOffset() != transcript {
		t.Fatal("wheel selected or leaked to conversation")
	}
	if w, h := lipgloss.Size(m.renderBody()); w != width || h != height {
		t.Fatalf("wheel resized card: %dx%d -> %dx%d", width, height, w, h)
	}
	scrolled := s.list.Offset()
	m = applyAll(m, tea.WindowSizeMsg{Width: 6, Height: 3}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.confirm != nil || s.list.Offset() != scrolled || s.list.CursorID() != selected {
		t.Fatal("compact input changed list")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 64, Height: 32})
	_ = m.View()
	if s.list.Offset() != scrolled || s.list.CursorID() != selected {
		t.Fatal("compact restoration lost browsing window")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	if s.list.Cursor() != 1 {
		t.Fatal("remapped navigation did not select")
	}
	assertConnectSelectedRendered(t, m, targets[1])
	m = applyAll(m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if s.list.Cursor() <= 1 {
		t.Fatal("physical page did not advance")
	}
	page := assertConnectSelectedRendered(t, m, targets[s.list.Cursor()])
	if !strings.Contains(page, "↑ ") || !strings.Contains(page, "↓ ") || strings.Count(page, "more") != 2 {
		t.Fatalf("middle page lost overflow indicators:\n%s", page)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 'G', Text: "G"})
	if s.list.CursorID() != "add" {
		t.Fatal("last choice unreachable")
	}
	end := assertConnectSelectedRendered(t, m, ConnectTarget{})
	if !strings.Contains(end, "↑ ") || strings.Contains(end, "↓ ") || strings.Count(end, "more") != 1 {
		t.Fatalf("last page has incorrect overflow indicators:\n%s", end)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 'k', Text: "k"})
	selected, offset := s.list.CursorID(), s.list.Offset()
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	pending := *s.confirm
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'g', Text: "g"}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: 10}} {
		m = applyAll(m, msg)
	}
	if *s.confirm != pending || s.list.CursorID() != selected || s.list.Offset() != offset {
		t.Fatal("confirmation mutated origin or pending intent")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.confirm != nil || m.modal != s || s.list.CursorID() != selected || s.list.Offset() != offset {
		t.Fatal("Back changed list")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if s.list.Cursor() != 0 || s.list.Offset() != 0 {
		t.Fatal("top did not reveal selection")
	}
	for i := 0; i < 24; i++ {
		if s.list.CursorID() != fmt.Sprintf("target:remote-%02d:443", i) {
			t.Fatalf("choice %d unreachable", i)
		}
		assertConnectSelectedRendered(t, m, targets[i])
		m = applyAll(m, tea.KeyPressMsg{Code: 'j', Text: "j"})
		if w, h := lipgloss.Size(m.renderBody()); w != width || h != height {
			t.Fatalf("selection resized card: %dx%d -> %dx%d", width, height, w, h)
		}
	}
	if s.list.CursorID() != "add" {
		t.Fatal("sign-in unreachable")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal != nil {
		t.Fatal("second Escape did not close")
	}
}

func TestConnectGeometrySanitizationAndRestoration(t *testing.T) {
	raw := "raw\tremote\x1b[2J:443"
	m := openTestConnect(newConnectModel([]ConnectTarget{{Target: raw, Issuer: "issuer\t界\n" + strings.Repeat("界", 90), ClientID: "\x07client", Audience: "api"}}), "", "", "")
	s := m.modal.(*connectState)
	for _, size := range [][2]int{{128, 30}, {32, 12}, {7, 20}, {1, 1}, {0, 0}, {80, 30}} {
		body, hits := s.Render(size[0], size[1])
		if hits != nil {
			t.Fatal("connect publishes click regions")
		}
		if lipgloss.Width(body) > size[0] || lipgloss.Height(body) > size[1] && body != "" {
			t.Fatalf("offer %v exceeded by %dx%d: %q", size, lipgloss.Width(body), lipgloss.Height(body), body)
		}
		if strings.ContainsAny(stripANSIstr(body), "\t\x1b\x07") {
			t.Fatalf("unsafe metadata: %q", body)
		}
	}
	if s.list.CursorID() != "target:"+raw {
		t.Fatal("sanitized label replaced raw identity")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.confirm == nil || s.confirm.Target != raw {
		t.Fatal("confirmation lost raw target")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 6, Height: 3}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.connectIntent != nil || s.confirm == nil {
		t.Fatal("compact confirmation activated or lost pending choice")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 40}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if intent, ok := m.ConnectRestartIntent(); !ok || intent.Target != raw {
		t.Fatalf("restored confirmation = %+v, %v", intent, ok)
	}
}

type connectContextCapture struct{ ctx context.Context }

func (f *connectContextCapture) ListConnectTargets(ctx context.Context) ([]ConnectTarget, error) {
	f.ctx = ctx
	return nil, nil
}

func TestConnectRequestCancellationReplacementAndRootMessages(t *testing.T) {
	m := newConnectModel(nil)
	controller := &connectContextCapture{}
	m.deps.Connect = controller
	mm, load := m.openConnect()
	m = mm.(Model)
	first := m.modal.(*connectState)
	old := load().(connectTargetsMsg)
	// A newer request on the same instance also invalidates the prior receipt.
	load = first.listCmd()
	m = applyAll(m, old)
	if !first.loading {
		t.Fatal("old request accepted")
	}
	current := load().(connectTargetsMsg)
	current.targets = []ConnectTarget{{Target: "fresh"}}
	// Root lifecycle updates must pass through before the pending load completes.
	m.sessionID = "session-1"
	for i, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 5, Height: 3}} {
		m = applyAll(m, size, client.SessionTitleMsg{Title: fmt.Sprintf("root update %d", i), Revision: uint64(i + 1)})
		_ = m.View()
		if m.sessionTitle != fmt.Sprintf("root update %d", i) || !first.loading || len(first.items) != 0 || first.compact != (i == 1) {
			t.Fatalf("pending load swallowed lifecycle update or changed state at geometry %+v", size)
		}
	}
	m = applyAll(m, current)
	m.deps.Server = "fresh"
	mm, _ = m.reduceLiveAuthRecovery(client.AuthSessionExpired)
	m = mm.(Model)
	if m.modal == first || controller.ctx.Err() == nil || len(m.hits.frame) != 0 {
		t.Fatal("idle auth recovery did not close/cancel old modal")
	}
	m = applyAll(m, current)
	if !m.modal.(*connectState).loading {
		t.Fatal("replacement accepted old result")
	}
	m.closeModal()
}

type connectCloseProbe struct {
	hitDispatchSurface
	closed bool
}

func (s *connectCloseProbe) Close() { s.closed = true }

func TestConnectIdleAuthRecoveryClosesPreviousModal(t *testing.T) {
	m := newConnectModel(nil)
	previous := &connectCloseProbe{hitDispatchSurface: hitDispatchSurface{deps: (&m).surfaceDeps()}}
	m.modal = previous
	_ = m.View()
	if len(m.hits.frame) == 0 {
		t.Fatal("fixture has no published hits")
	}
	mm, _ := m.Update(client.StreamErrMsg{AuthReason: client.AuthRejected})
	m = mm.(Model)
	if !previous.closed || len(m.hits.frame) != 0 || *m.metrics != (renderedSurfaceMetrics{}) {
		t.Fatal("recovery retained old lifetime/hit geometry")
	}
	if _, ok := m.modal.(*connectState); !ok {
		t.Fatalf("recovery modal=%T", m.modal)
	}
}

func TestConnectPostSessionReadyUsesRecoverySurface(t *testing.T) {
	m := newConnectModel([]ConnectTarget{{Target: "same:443"}})
	m.deps.Session = &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m.deps.ConnectOpen = true
	m.deps.ConnectReason = client.AuthSessionExpired
	m.deps.ConnectTarget = "same:443"
	m.deps.ConnectResumeSessionID = "resume-1"
	m.phase = phaseConnecting
	mm, cmd := m.Update(client.SessionReadyMsg{SessionID: "ready-1"})
	m = mm.(Model)
	s, ok := m.modal.(*connectState)
	if !ok || !s.loading || s.failedTarget != "same:443" {
		t.Fatal("post-ready path did not install recovery")
	}
	requests := 0
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		switch msg := next().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case connectTargetsMsg:
			requests++
			m = applyAll(m, msg)
		}
	}
	if requests != 1 {
		t.Fatalf("post-ready loads=%d", requests)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if intent, ok := m.ConnectRestartIntent(); !ok || intent.Action != Reauthenticate || intent.ResumeSessionID != "resume-1" {
		t.Fatalf("post-ready intent=%+v, %v", intent, ok)
	}
}

type connectCountingController struct {
	calls   int
	targets []ConnectTarget
}

func (f *connectCountingController) ListConnectTargets(context.Context) ([]ConnectTarget, error) {
	f.calls++
	return f.targets, nil
}

func TestConnectAuthRecoveryRoutesExactlyOneMetadataLoad(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("live=%v", live), func(t *testing.T) {
			controller := &connectCountingController{targets: []ConnectTarget{{Target: "failed:443"}, {Target: "other:443"}}}
			m := newConnectModel(nil)
			m.deps.Connect, m.deps.Server = controller, "failed:443"
			m.phase = phaseConnecting
			var event tea.Msg = client.ConnectErrMsg{AuthReason: client.AuthNotEnrolled}
			if live {
				m.phase, m.sessionID = phaseIdle, "live-session"
				event = liveMsg{gen: m.liveGen, msg: client.StreamErrMsg{AuthReason: client.AuthSessionExpired}}
			}
			mm, cmd := m.Update(event)
			m = mm.(Model)
			s, ok := m.modal.(*connectState)
			if !ok || !s.loading || controller.calls != 0 {
				t.Fatal("auth event did not schedule a pending metadata load")
			}
			receipts := 0
			queue := []tea.Cmd{cmd}
			for len(queue) > 0 {
				next := queue[0]
				queue = queue[1:]
				if next == nil {
					continue
				}
				msg := next()
				if batch, ok := msg.(tea.BatchMsg); ok {
					queue = append(queue, batch...)
					continue
				}
				if _, ok := msg.(connectTargetsMsg); ok {
					receipts++
				}
				mm, followup := m.Update(msg)
				m = mm.(Model)
				queue = append(queue, followup)
			}
			if controller.calls != 1 || receipts != 1 || s.loading || len(s.targets) != 2 || len(s.items) != 3 {
				t.Fatalf("metadata load calls=%d receipts=%d loading=%v targets=%+v choices=%+v", controller.calls, receipts, s.loading, s.targets, s.items)
			}
			if s.list.CursorID() != "target:failed:443" || s.items[1].ID != "target:other:443" || s.items[2].ID != "add" {
				t.Fatalf("loaded choices = %+v", s.items)
			}
			out := stripANSIstr(m.View().Content)
			for _, label := range []string{"▶ failed:443", "other:443", "OIDC sign-in for a new target…"} {
				if !strings.Contains(out, label) {
					t.Fatalf("loaded choice %q not rendered:\n%s", label, out)
				}
			}
			if live && (m.sessionID != "live-session" || s.resumeSessionID != "live-session") {
				t.Fatal("live recovery lost resume candidate")
			}
			m.closeModal()
		})
	}
}

func TestConnectFramedOffersStayBounded(t *testing.T) {
	for _, reason := range []client.AuthReason{"", client.AuthRejected, client.AuthNotEnrolled, client.AuthStorageUnavailable} {
		m := newConnectModel(connectInventory(12))
		m = openTestConnect(m, reason, "remote-00:443", "resume-1")
		s := m.modal.(*connectState)
		for _, size := range [][2]int{{160, 40}, {42, 24}, {20, 12}, {3, 3}, {0, 0}, {120, 40}} {
			m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			body := m.renderBody()
			if lipgloss.Width(body) > size[0] || body != "" && lipgloss.Height(body) > m.vp.Height() {
				t.Fatalf("reason=%q offer=%v body=%dx%d vp=%d", reason, size, lipgloss.Width(body), lipgloss.Height(body), m.vp.Height())
			}
			if s.compact {
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter})
				if s.confirm != nil || m.connectIntent != nil {
					t.Fatal("compact chrome allowed activation")
				}
			}
		}
		if s.compact {
			t.Fatal("large offer did not restore browsing")
		}
		if s.modalMaxOuterWidth() > 128 {
			t.Fatal("card exceeds shared ceiling")
		}
	}
}

func TestConnectIdentityReconciliation(t *testing.T) {
	m := openTestConnect(newConnectModel([]ConnectTarget{{Target: "a"}, {Target: "b"}}), "", "", "")
	s := m.modal.(*connectState)
	s.list.SetCursor(1)
	s.targets[0], s.targets[1] = s.targets[1], s.targets[0]
	s.syncItems()
	if s.list.CursorID() != "target:b" || s.list.Cursor() != 0 {
		t.Fatal("reordering changed target identity")
	}
}
