package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func connectMouseHit(t *testing.T, m Model, action connectMouseAction, id string) renderedHitRegion {
	t.Helper()
	s := m.modal.(*connectState)
	hit, ok := currentFrameHit(m, func(region renderedHitRegion) bool {
		target, ok := s.hitTargets[region.id]
		return ok && target.action == action && (action != connectRow || target.id == id)
	})
	if !ok {
		t.Fatalf("missing connect hit in %d current-frame regions: action=%d id=%s", len(m.hits.frame), action, id)
	}
	return hit
}

func connectMouseModel(targets []ConnectTarget, reason client.AuthReason, failed, resume string) Model {
	m := newConnectModel(targets)
	m.deps.NoAltScreen = false
	return openTestConnect(m, reason, failed, resume)
}

func TestConnectMouseActionsAndRecovery(t *testing.T) {
	for _, reason := range []client.AuthReason{"", client.AuthNotEnrolled, client.AuthSessionExpired, client.AuthCredentialUnusable, client.AuthTargetChanged, client.AuthCredentialCleanup, client.AuthStorageUnavailable, client.AuthAnonymousRejected, client.AuthRejected} {
		for _, selected := range []string{"failed", "other", "add"} {
			if reason == client.AuthRejected && selected != "other" {
				continue
			}
			for _, pointer := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/pointer=%v", reason, selected, pointer), func(t *testing.T) {
					m := newConnectModel([]ConnectTarget{{Target: "failed"}, {Target: "other"}})
					m.deps.NoAltScreen = false
					m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"a"}, "Close": {"c"}})
					m = openTestConnect(m, reason, "failed", "resume-1")
					s := m.modal.(*connectState)
					id := "target:" + selected
					want := ConnectRestartIntent{Target: selected, Action: ConnectSaved}
					switch selected {
					case "add":
						id, want = "add", ConnectRestartIntent{Action: AddTarget}
					case "failed":
						switch reason {
						case client.AuthNotEnrolled, client.AuthSessionExpired, client.AuthCredentialUnusable, client.AuthTargetChanged:
							want.Action, want.ResumeSessionID = Reauthenticate, "resume-1"
						case client.AuthCredentialCleanup, client.AuthStorageUnavailable:
							want.Action, want.ResumeSessionID = RetryAfterCleanup, "resume-1"
						}
					}
					_ = m.View()
					m, _ = clickFrameHit(m, connectMouseHit(t, m, connectRow, id), true)
					if s.list.CursorID() != id || s.confirm != nil || m.connectIntent != nil {
						t.Fatal("row must select only")
					}
					activate := func() tea.Cmd {
						view := stripANSIstr(m.View().Content)
						if !strings.Contains(view, "[a "+connectActionLabel(want.Action)+"]") {
							t.Fatal("action lost configured shortcut or operation label")
						}
						if pointer {
							updated, cmd := clickFrameHit(m, connectMouseHit(t, m, connectPrimary, ""), true)
							m = updated
							return cmd
						}
						updated, cmd := m.Update(navigationTestKey("a"))
						m = updated.(Model)
						return cmd
					}
					if cmd := activate(); cmd != nil || s.confirm == nil || *s.confirm != want || m.connectIntent != nil {
						t.Fatal("primary must only confirm the selected operation")
					}
					_ = m.View()
					old := connectMouseHit(t, m, connectPrimary, "").id
					m, _ = clickFrameHit(m, connectMouseHit(t, m, connectBack, ""), false)
					if s.confirm != nil || s.list.CursorID() != id || m.modal != s {
						t.Fatal("Back lost picker selection")
					}
					m = applyAll(m, surfaceHitMsg{ID: old})
					if s.confirm != nil {
						t.Fatal("old confirmation repurposed as picker action")
					}
					activate()
					_ = m.View()
					old = connectMouseHit(t, m, connectPrimary, "").id
					cmd := activate()
					if got, ok := m.ConnectRestartIntent(); !ok || got != want || cmd == nil || m.modal != nil {
						t.Fatalf("restart = %+v, %v; want %+v", got, ok, want)
					}
					if _, ok := cmd().(tea.QuitMsg); !ok {
						t.Fatal("confirmation did not request quit")
					}
					updated, duplicate := m.Update(surfaceHitMsg{ID: old})
					if duplicate != nil || updated.(Model).connectIntent == nil {
						t.Fatal("duplicate control resubmitted")
					}
				})
			}
		}
	}
}

func TestConnectMouseContinuationSelectOnly(t *testing.T) {
	targets := []ConnectTarget{{Target: "first"}, {Target: "second", Issuer: strings.Repeat("issuer 界 ", 8), ClientID: "public"}}
	m := offerCardBody(connectMouseModel(targets, "", "", ""), 30, 26)
	s := m.modal.(*connectState)
	for repeat := range 3 {
		_ = m.View()
		var rows []renderedHitRegion
		for _, hit := range m.hits.frame {
			if target := s.hitTargets[hit.id]; target.action == connectRow && target.id == "target:second" {
				rows = append(rows, hit)
			}
		}
		if len(rows) < 3 {
			t.Fatal("fixture has no wrapped metadata")
		}
		for i := range rows {
			_ = m.View()
			m, _ = clickFrameHit(m, rows[i], repeat%2 == 0)
			if s.list.CursorID() != "target:second" || s.confirm != nil || m.connectIntent != nil {
				t.Fatal("first/repeated/continuation click activated or selected wrong target")
			}
		}
	}
}

func TestConnectMouseWheelOffscreenAndBack(t *testing.T) {
	m := offerCardBody(connectMouseModel(connectInventory(30), "", "", ""), 64, 12)
	s := m.modal.(*connectState)
	before := m.vp.YOffset()
	for range 10 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	view := stripANSIstr(m.View().Content)
	if strings.Contains(view, "▶") || s.list.CursorID() != "target:remote-00:443" || s.list.Offset() == 0 || m.vp.YOffset() != before {
		t.Fatal("wheel must browse without selecting or leaking")
	}
	offset := s.list.Offset()
	m, _ = clickFrameHit(m, connectMouseHit(t, m, connectPrimary, ""), false)
	if s.confirm == nil || s.confirm.Target != "remote-00:443" {
		t.Fatal("primary did not confirm offscreen selection")
	}
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	_ = m.View()
	m, _ = clickFrameHit(m, connectMouseHit(t, m, connectBack, ""), true)
	_ = m.View()
	if s.confirm != nil || s.list.Offset() != offset || m.vp.YOffset() != before {
		t.Fatal("confirmation wheel or Back changed window")
	}
}

func TestConnectMouseStaleControls(t *testing.T) {
	for _, confirmation := range []bool{false, true} {
		actions := []connectMouseAction{connectPrimary, connectBack}
		if !confirmation {
			actions = append(actions, connectRow)
		}
		for _, change := range []string{"row", "key", "cursor", "resize", "prepare", "compact", "replace", "reopen", "refresh", "reorder", "remove", "reason", "resume", "failed-target", "confirmation", "operation"} {
			for _, action := range actions {
				t.Run(fmt.Sprintf("confirm=%v/%s/%d", confirmation, change, action), func(t *testing.T) {
					m := connectMouseModel([]ConnectTarget{{Target: "first"}, {Target: "second"}}, client.AuthSessionExpired, "first", "resume")
					s := m.modal.(*connectState)
					if confirmation {
						m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
					}
					_ = m.View()
					old := connectMouseHit(t, m, action, "target:second").id
					switch change {
					case "row":
						if confirmation {
							m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
						} else {
							m, _ = clickFrameHit(m, connectMouseHit(t, m, connectRow, "target:second"), false)
						}
					case "key":
						m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
					case "cursor":
						s.list.SetCursor(1)
					case "resize":
						m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30})
					case "prepare":
						(&m).prepareSurfaceInput()
					case "compact":
						m = offerCardBody(m, 80, 1)
						_ = m.View()
					case "replace":
						m.closeModal()
						updated, _ := m.runHelp()
						m = updated.(Model)
					case "reopen":
						m.closeModal()
						m = openTestConnect(m, "", "", "")
					case "refresh":
						_ = s.listCmd()
					case "reorder":
						slices.Reverse(s.targets)
						s.syncItems()
					case "remove":
						s.targets = s.targets[1:]
						s.syncItems()
					case "reason":
						s.reason = client.AuthCredentialCleanup
					case "resume":
						s.resumeSessionID = "other-resume"
					case "failed-target":
						s.failedTarget = "second"
					case "confirmation":
						if confirmation {
							s.confirm = nil
						} else {
							intent, _ := s.selectedIntent()
							s.confirm = &intent
						}
					case "operation":
						if confirmation {
							s.confirm.ResumeSessionID = "new-owner"
						} else {
							s.reason = client.AuthRejected
						}
					}
					modal, pending, selected := m.modal, s.confirm, s.list.CursorID()
					updated, cmd := m.Update(surfaceHitMsg{ID: old})
					m = updated.(Model)
					if cmd != nil || m.modal != modal || s.confirm != pending || s.list.CursorID() != selected || m.connectIntent != nil {
						t.Fatal("stale control acted")
					}
				})
			}
		}
	}
}

func TestConnectMouseGeometryAndStyles(t *testing.T) {
	for _, width := range []int{24, 80} {
		for _, confirmation := range []bool{false, true} {
			for _, end := range []bool{false, true} {
				m := offerCardBody(connectMouseModel([]ConnectTarget{{Target: "first"}, {Target: "second"}}, "", "", ""), width, 20)
				s := m.modal.(*connectState)
				if confirmation {
					m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				}
				view := m.View().Content
				primary := connectMouseHit(t, m, connectPrimary, "")
				back := connectMouseHit(t, m, connectBack, "")
				if width == 24 && primary.rect.y0 == back.rect.y0 {
					t.Fatal("controls must wrap")
				}
				backLabel := "[esc Close]"
				if confirmation {
					backLabel = "[esc Back]"
				}
				for _, control := range []struct {
					hit   renderedHitRegion
					label string
				}{{primary, "[enter connect]"}, {back, backLabel}} {
					assertFrameHitLabel(t, view, *m.metrics, control.hit, control.label)
					if !strings.Contains(view, m.deps.Theme.Style("viewport").Bold(true).Render(control.label)) {
						t.Fatal("enabled control must use bold normal theme style")
					}
				}
				if !confirmation && width == 80 && !strings.Contains(view, m.deps.Theme.Style("muted").Render("↑/↓ select · pgup/pgdn page · home/end first/last")) {
					t.Fatal("navigation must be separate and muted")
				}
				for _, point := range []cellPoint{{x: primary.rect.x1, y: primary.rect.y0}, {x: back.rect.x1, y: back.rect.y0}, {x: 0, y: 0}, {x: -1, y: primary.rect.y0}, {x: 0, y: -1}} {
					x, y := m.metrics.localToGlobal(point.x, point.y)
					pending := s.confirm
					updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
					m = updated.(Model)
					if cmd != nil || m.modal != s || s.confirm != pending || s.list.Cursor() != 0 {
						t.Fatal("gap/chrome/border/padding acted")
					}
				}
				m, cmd := clickFrameHit(m, primary, end)
				if confirmation {
					if cmd == nil || m.connectIntent == nil || m.modal != nil {
						t.Fatal("confirmation edge did not submit")
					}
				} else if s.confirm == nil || cmd != nil {
					t.Fatal("primary edge did not confirm")
				}
			}
		}
	}
}

func TestConnectMouseAvailability(t *testing.T) {
	for _, state := range []string{"loading", "failed", "rejected", "choose-disabled", "close-disabled", "both-disabled", "no-mouse"} {
		t.Run(state, func(t *testing.T) {
			m := connectMouseModel([]ConnectTarget{{Target: "first"}}, "", "", "")
			s := m.modal.(*connectState)
			switch state {
			case "loading":
				_ = s.listCmd()
			case "failed":
				_ = s.listCmd()
				m = applyAll(m, connectTargetsMsg{owner: s, request: s.request, err: errors.New("private failure")})
			case "rejected":
				s.reason, s.failedTarget = client.AuthRejected, "first"
				s.syncItems()
			case "choose-disabled":
				s.deps.keys.Choose.SetEnabled(false)
			case "close-disabled":
				s.deps.keys.Close.SetEnabled(false)
			case "both-disabled":
				s.deps.keys.Choose.SetEnabled(false)
				s.deps.keys.Close.SetEnabled(false)
			case "no-mouse":
				m.deps.NoMouse = true
			}
			view := m.View()
			primary := state == "close-disabled" || state == "no-mouse"
			if strings.Contains(stripANSIstr(view.Content), "[enter connect]") != primary {
				t.Fatal("incorrect primary availability")
			}
			for _, target := range s.hitTargets {
				if !primary && target.action == connectPrimary || (state == "loading" || state == "failed" || state == "rejected") && target.action == connectRow {
					t.Fatal("unavailable target exposes hits")
				}
				if (state == "close-disabled" || state == "both-disabled") && target.action == connectBack {
					t.Fatal("disabled Close exposes hit")
				}
			}
			if state == "no-mouse" {
				if view.MouseMode != tea.MouseModeNone {
					t.Fatal("no-mouse mode lost")
				}
				for _, action := range []connectMouseAction{connectRow, connectPrimary, connectBack} {
					m, _ = clickFrameHit(m, connectMouseHit(t, m, action, "add"), false)
				}
				if s.confirm != nil || s.list.Cursor() != 0 || m.modal != s {
					t.Fatal("no-mouse click acted")
				}
			} else if !primary {
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				if s.confirm != nil {
					t.Fatal("unavailable keyboard action acted")
				}
			}
			if state == "close-disabled" || state == "both-disabled" {
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
				if m.modal != s || strings.Contains(stripANSIstr(view.Content), "Close]") {
					t.Fatal("disabled Close acted or advertised")
				}
				m = offerCardBody(m, 80, 1)
				if view := stripANSIstr(m.View().Content); !s.compact || len(m.hits.frame) != 0 || !strings.Contains(view, "terminal too small") {
					t.Fatal("compact advertises disabled Close")
				}
			}
		})
	}
}

func TestConnectMouseCompactAndOptionalHelp(t *testing.T) {
	m := offerCardBody(connectMouseModel(connectInventory(30), "", "", ""), 80, 3)
	s := m.modal.(*connectState)
	s.list.SetCursor(5)
	if view := stripANSIstr(m.View().Content); s.compact || !strings.Contains(view, "remote-05") || strings.Contains(view, "first/last") {
		t.Fatal("optional help displaced body")
	}
	for _, offer := range [][2]int{{80, 1}, {12, 20}} {
		m = offerCardBody(m, offer[0], offer[1])
		_ = m.View()
		if !s.compact || len(m.hits.frame) != 0 {
			t.Fatal("compact exposes hits")
		}
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyHome}, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		if s.confirm != nil || s.list.Cursor() != 5 {
			t.Fatal("compact input moved or activated")
		}
	}
	m = offerCardBody(m, 80, 20)
	_ = m.View()
	if s.compact || s.list.Cursor() != 5 {
		t.Fatal("restoration lost selection")
	}
	_ = connectMouseHit(t, m, connectRow, "target:remote-05:443")
}

func TestConnectMousePartialContinuationAndInertIndicators(t *testing.T) {
	m := offerCardBody(connectMouseModel([]ConnectTarget{{Target: "first"}, {Target: "before"}, {Target: "second", Issuer: strings.Repeat("issuer 界 ", 80)}, {Target: "third"}}, "", "", ""), 30, 10)
	s := m.modal.(*connectState)
	_ = m.View()
	for range 6 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	view := stripANSIstr(m.View().Content)
	if strings.Contains(view, "second") || !strings.Contains(view, "issuer") || !strings.Contains(view, "↑ ") || !strings.Contains(view, "↓ ") {
		t.Fatalf("fixture must show only partial metadata with indicators:\n%s", view)
	}
	hit := connectMouseHit(t, m, connectRow, "target:second")
	for _, point := range []cellPoint{{x: hit.rect.x1, y: hit.rect.y0}, {x: 0, y: hit.rect.y0 - 1}} {
		x, y := m.metrics.localToGlobal(point.x, point.y)
		m = applyAll(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		if s.list.Cursor() != 0 || s.confirm != nil {
			t.Fatal("indicator or trailing row padding selected")
		}
	}
	m, _ = clickFrameHit(m, hit, true)
	if s.list.CursorID() != "target:second" || s.confirm != nil || m.connectIntent != nil {
		t.Fatal("partial continuation must select by stable identity only")
	}
}

func TestConnectMouseRefreshAndLateReceipts(t *testing.T) {
	m := connectMouseModel([]ConnectTarget{{Target: "first"}, {Target: "second"}}, "", "", "")
	s := m.modal.(*connectState)
	_ = m.View()
	oldRow := connectMouseHit(t, m, connectRow, "target:second").id
	oldPrimary := connectMouseHit(t, m, connectPrimary, "").id
	old := s.listCmd()().(connectTargetsMsg)
	fresh := s.listCmd()().(connectTargetsMsg)
	fresh.targets = []ConnectTarget{{Target: "second"}, {Target: "first"}}
	m = applyAll(m, old)
	if !s.loading {
		t.Fatal("old request completed current load")
	}
	m = applyAll(m, fresh, surfaceHitMsg{ID: oldRow}, surfaceHitMsg{ID: oldPrimary})
	if s.loading || s.list.CursorID() != "target:first" || s.confirm != nil {
		t.Fatal("refresh lost stable selection or reused old controls")
	}
	_ = m.View()
	m, _ = clickFrameHit(m, connectMouseHit(t, m, connectRow, "target:second"), false)
	if s.list.CursorID() != "target:second" || s.list.Cursor() != 0 || s.confirm != nil {
		t.Fatal("fresh reordered row used old index or activated")
	}
	_ = s.listCmd()
	request := s.request
	m.closeModal()
	m = openTestConnect(m, "", "", "")
	replacement := m.modal.(*connectState)
	m = applyAll(m, connectTargetsMsg{owner: s, request: request, targets: []ConnectTarget{{Target: "late"}}})
	if m.modal != replacement || replacement.list.CursorID() == "target:late" || s.cancel == nil {
		t.Fatal("late closed-owner receipt changed reopened picker")
	}
}

func TestConnectMouseConfirmationConfiguredControls(t *testing.T) {
	for _, disabled := range []string{"", "Choose", "Close"} {
		m := newConnectModel([]ConnectTarget{{Target: "first"}})
		m.deps.NoAltScreen = false
		m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"ctrl+f17"}, "Close": {"ctrl+f16"}})
		m = openTestConnect(m, "", "", "")
		m = applyAll(m, navigationTestKey("ctrl+f17"))
		s := m.modal.(*connectState)
		if s.confirm == nil {
			t.Fatal("remapped Choose did not confirm")
		}
		switch disabled {
		case "Choose":
			s.deps.keys.Choose.SetEnabled(false)
		case "Close":
			s.deps.keys.Close.SetEnabled(false)
		}
		view := m.View().Content
		for _, control := range []struct {
			name, label string
			action      connectMouseAction
		}{{"Choose", "[ctrl+f17 connect]", connectPrimary}, {"Close", "[ctrl+f16 Back]", connectBack}} {
			if strings.Contains(stripANSIstr(view), control.label) != (disabled != control.name) {
				t.Fatal("confirmation advertises incorrect configured availability")
			}
			if disabled != control.name {
				hit := connectMouseHit(t, m, control.action, "")
				if hit.rect.x1-hit.rect.x0 != ansi.StringWidth(control.label) {
					t.Fatal("hit must include entire configured shortcut and brackets")
				}
			}
		}
		if disabled != "" {
			pending := s.confirm
			m = applyAll(m, navigationTestKey(map[string]string{"Choose": "ctrl+f17", "Close": "ctrl+f16"}[disabled]))
			if s.confirm != pending || m.modal != s || m.connectIntent != nil {
				t.Fatal("disabled configured confirmation key acted")
			}
		}
	}
}

func TestConnectMouseCompactConfirmationBack(t *testing.T) {
	m := connectMouseModel(connectInventory(20), "", "", "")
	s := m.modal.(*connectState)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	pending := *s.confirm
	m = offerCardBody(m, 80, 1)
	if view := stripANSIstr(m.View().Content); !s.compact || len(m.hits.frame) != 0 || !strings.Contains(view, "esc back") {
		t.Fatal("compact confirmation must advertise Back without active controls")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.confirm == nil || *s.confirm != pending || m.connectIntent != nil {
		t.Fatal("compact confirmation activated or lost pinned intent")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.confirm != nil || m.modal != s {
		t.Fatal("compact Back must return to picker, not close")
	}
}

func TestConnectMouseGoldens(t *testing.T) {
	for _, state := range []string{"picker", "recovery_confirmation", "wrapped", "confirmation_wrapped", "loading", "unavailable", "failed", "compact"} {
		t.Run(state, func(t *testing.T) {
			m := connectMouseModel([]ConnectTarget{{Target: "remote.example:443", Issuer: "https://issuer.example", ClientID: "public-client", Audience: "mecatui"}, {Target: "other.example:443", Issuer: "https://issuer.example", ClientID: "public-client", Audience: "api"}}, "", "", "")
			s := m.modal.(*connectState)
			m = offerCardBody(m, 80, 18)
			if state == "recovery_confirmation" {
				s.reason, s.failedTarget, s.resumeSessionID = client.AuthCredentialCleanup, "remote.example:443", "resume-1"
			}
			if strings.Contains(state, "confirmation") {
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if strings.Contains(state, "wrapped") {
				m = offerCardBody(m, 24, 18)
			}
			if state == "loading" {
				_ = s.listCmd()
			}
			if state == "unavailable" {
				s.reason, s.failedTarget = client.AuthRejected, "remote.example:443"
				s.syncItems()
			}
			if state == "failed" {
				_ = s.listCmd()
				m = applyAll(m, connectTargetsMsg{owner: s, request: s.request, err: errors.New("offline")})
			}
			if state == "compact" {
				m = offerCardBody(m, 80, 1)
			}
			m = goldenStatusFrame(t, m)
			_ = m.View()
			if strings.Contains(state, "wrapped") {
				primary := connectMouseHit(t, m, connectPrimary, "")
				back := connectMouseHit(t, m, connectBack, "")
				if s.compact || primary.rect.y0 == back.rect.y0 {
					t.Fatal("wrapped golden must retain body and wrap controls")
				}
			}
			compareGolden(t, "connect_"+state+".golden", stripANSI([]byte(m.View().Content)))
		})
	}
}
