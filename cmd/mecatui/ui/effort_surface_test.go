package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func openedEffort(t *testing.T, current string, warning bool) (Model, *effortState) {
	t.Helper()
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.resolvedSessionModel.ReasoningEffort = current
	if warning {
		m.modelCatalog.models = []client.ModelInfo{{ID: m.resolvedSessionModel.ModelID, ProviderID: m.resolvedSessionModel.ProviderID}}
	}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyF7})
	m = mm.(Model)
	s, ok := m.modal.(*effortState)
	if !ok || cmd != nil || m.prompt.Focused() || m.deps.Session.(*fakeConv).forkCount != 0 {
		t.Fatal("open must install local surface, blur prompt, and perform no RPC")
	}
	return m, s
}

func effortOffer(m Model, width, height int) Model {
	style := m.deps.Theme.Style("askCard")
	m = applyAll(m, tea.WindowSizeMsg{Width: width + style.GetHorizontalFrameSize(), Height: 100})
	above, below := m.chrome()
	return applyAll(m, tea.WindowSizeMsg{Width: m.width, Height: height + style.GetVerticalFrameSize() + sumHeight(above) + sumHeight(below)})
}

func checkEffortBodyFits(t *testing.T, m Model) string {
	t.Helper()
	body := m.renderBody()
	if lipgloss.Height(body) > m.vp.Height() || lipgloss.Width(body) > m.width {
		t.Fatalf("picker outside parent: %dx%d > %dx%d\n%s", lipgloss.Width(body), lipgloss.Height(body), m.width, m.vp.Height(), body)
	}
	bounds := m.metrics.outerBounds
	if bounds.x0 < 0 || bounds.x1 > m.width || bounds.y0 < convTopRow(m) || bounds.y1 > convTopRow(m)+m.vp.Height() || bounds.x1-bounds.x0 > 128 {
		t.Fatalf("card bounds outside capped offer: %+v", bounds)
	}
	if len(m.hits.frame) != 0 {
		t.Fatal("effort must have no click regions")
	}
	return stripANSIstr(body)
}

func TestEffortSurfacePreferredCardStaysStableWhileNavigating(t *testing.T) {
	offers := []struct {
		name string
		rows int
	}{
		{"one-row", 1}, {"two-rows", 2}, {"three-rows", 3},
		{"four-rows", 4}, {"five-rows", 5}, {"six-rows", 6},
		{"roomy", 20},
	}
	for _, warning := range []bool{false, true} {
		for _, offer := range offers {
			t.Run(fmt.Sprintf("warning=%v/%s", warning, offer.name), func(t *testing.T) {
				m, s := openedEffort(t, "max", warning)
				height := offer.rows + 4 // title plus the three footer lines
				if warning {
					height++
				}
				m = effortOffer(m, 200, height)
				out := checkEffortBodyFits(t, m)
				bounds := m.metrics.outerBounds
				want := [2]int{bounds.x1 - bounds.x0, bounds.y1 - bounds.y0}
				if want[0] >= effortMaxOuterWidth {
					t.Fatalf("small picker retained the %d-cell card: %+v", effortMaxOuterWidth, bounds)
				}
				if warning && !strings.Contains(out, effortWarning) {
					t.Fatalf("warning did not fit readable width:\n%s", out)
				}
				for _, hint := range []string{"Reasoning effort", "move", "page", "first/last", "current  ·  auto = provider default"} {
					if !strings.Contains(out, hint) {
						t.Fatalf("title/footer missing %q:\n%s", hint, out)
					}
				}
				if offer.rows == 1 && (s.list.Height() != 1 || strings.Contains(out, effortRowText(effortAuto))) {
					t.Fatalf("max must start with the long auto row off-screen: height=%d\n%s", s.list.Height(), out)
				}
				if offer.name == "roomy" {
					if s.list.Height() < len(effortTiers) {
						t.Fatalf("roomy offer has only %d visible rows", s.list.Height())
					}
					for _, tier := range effortTiers {
						if !strings.Contains(out, effortRowText(tier)) {
							t.Fatalf("roomy offer omitted %q:\n%s", tier, out)
						}
					}
				}

				assertDimensions := func(msg tea.Msg) {
					t.Helper()
					_ = checkEffortBodyFits(t, m)
					got := m.metrics.outerBounds
					if dimensions := [2]int{got.x1 - got.x0, got.y1 - got.y0}; dimensions != want {
						t.Fatalf("%T changed card dimensions from %v to %v", msg, want, dimensions)
					}
				}
				for _, msg := range []tea.Msg{
					tea.KeyPressMsg{Code: tea.KeyUp}, tea.KeyPressMsg{Code: tea.KeyDown},
					tea.KeyPressMsg{Code: tea.KeyPgUp}, tea.KeyPressMsg{Code: tea.KeyPgDown},
				} {
					m = applyAll(m, msg)
					assertDimensions(msg)
				}
				m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				assertDimensions(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyHome})
				assertDimensions(tea.KeyPressMsg{Code: tea.KeyHome})
				out = checkEffortBodyFits(t, m)
				if offer.rows == 1 && !strings.Contains(out, effortRowText(effortAuto)) {
					t.Fatalf("auto row was clipped after becoming visible:\n%s", out)
				}
				for range 2 {
					m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
					assertDimensions(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
				}
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
				assertDimensions(tea.KeyPressMsg{Code: tea.KeyEnd})
				selected := s.list.CursorID()
				wheel := func(button tea.MouseButton) int {
					t.Helper()
					m = applyAll(m, tea.MouseWheelMsg{Button: button})
					assertDimensions(tea.MouseWheelMsg{Button: button})
					if s.list.CursorID() != selected {
						t.Fatalf("wheel changed selected tier from %q to %q", selected, s.list.CursorID())
					}
					return s.list.Offset()
				}
				if offer.rows < len(effortTiers) {
					bottom := s.list.Offset()
					if up := wheel(tea.MouseWheelUp); up >= bottom {
						t.Fatalf("wheel up did not move bounded window: %d -> %d", bottom, up)
					}
					for range 8 {
						_ = wheel(tea.MouseWheelUp)
					}
					top := s.list.Offset()
					if got := wheel(tea.MouseWheelUp); got != top {
						t.Fatalf("wheel up escaped top endpoint: %d -> %d", top, got)
					}
					for range 8 {
						_ = wheel(tea.MouseWheelDown)
					}
					bottom = s.list.Offset()
					if got := wheel(tea.MouseWheelDown); got != bottom {
						t.Fatalf("wheel down escaped bottom endpoint: %d -> %d", bottom, got)
					}
				} else {
					offset := s.list.Offset()
					for _, button := range []tea.MouseButton{tea.MouseWheelUp, tea.MouseWheelDown} {
						if got := wheel(button); got != offset {
							t.Fatalf("non-overflow wheel changed window: %d -> %d", offset, got)
						}
					}
				}
			})
		}
	}
}

func TestEffortSurfaceLongCapturedHintsCapAndResize(t *testing.T) {
	const offerHeight = 40
	longHint := strings.Repeat("q", 160)
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Up": {longHint}})
	mm, _ := m.runEffort()
	m = mm.(Model)
	s := m.modal.(*effortState)
	preferred := s.modalMaxOuterWidth()
	if preferred != effortMaxOuterWidth {
		t.Fatalf("long captured hint preferred width = %d, want cap %d", preferred, effortMaxOuterWidth)
	}
	checkLongHint := func(label string) {
		t.Helper()
		out := checkEffortBodyFits(t, m)
		if got := strings.Count(out, "q"); got != len(longHint) {
			t.Fatalf("%s long captured hint lost or duplicated cells: found %d of %d", label, got, len(longHint))
		}
		view := s.list.View()
		gutter := 0
		if len(view.Rows) > 0 {
			gutter = view.Rows[0].GutterCells
		}
		if !s.list.Valid() || len(view.Rows) == 0 || gutter != 2 || !strings.Contains(out, "Reasoning effort") {
			t.Fatalf("%s lost picker chrome or gutter: valid=%t rows=%d gutter=%d\n%s", label, s.list.Valid(), len(view.Rows), gutter, out)
		}
	}

	m = effortOffer(m, 200, offerHeight)
	checkLongHint("wide")
	wideBounds := m.metrics.outerBounds
	wideHeight := wideBounds.y1 - wideBounds.y0
	if wideBounds.x1-wideBounds.x0 != preferred {
		t.Fatalf("wide long-hint card bounds = %+v, want %d-cell cap", wideBounds, preferred)
	}

	m = effortOffer(m, 32, offerHeight)
	checkLongHint("narrow")
	narrowBounds := m.metrics.outerBounds
	if got, want := narrowBounds.x1-narrowBounds.x0, min(preferred, m.width); got != want {
		t.Fatalf("narrow outer width = %d, want min(preferred %d, parent %d) = %d", got, preferred, m.width, want)
	}
	if got := len(s.footerLines(narrowBounds.x1 - narrowBounds.x0 - m.deps.Theme.Style("askCard").GetHorizontalFrameSize())); got <= 3 {
		t.Fatalf("narrow long hint footer did not wrap: %d lines", got)
	}
	if got := s.modalMaxOuterWidth(); got != preferred {
		t.Fatalf("resize changed captured preferred width from %d to %d", preferred, got)
	}
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{Code: tea.KeyHome}, tea.KeyPressMsg{Code: tea.KeyEnd},
		tea.MouseWheelMsg{Button: tea.MouseWheelUp}, tea.MouseWheelMsg{Button: tea.MouseWheelDown},
	} {
		m = applyAll(m, msg)
		checkLongHint(fmt.Sprintf("narrow after %T", msg))
		if got := m.metrics.outerBounds; got != narrowBounds {
			t.Fatalf("%T changed narrow card bounds from %+v to %+v", msg, narrowBounds, got)
		}
	}

	m = effortOffer(m, 200, offerHeight)
	checkLongHint("wide after resize")
	if bounds := m.metrics.outerBounds; bounds.x1-bounds.x0 != preferred || bounds.y1-bounds.y0 != wideHeight {
		t.Fatalf("wide resize did not restore %d×%d card: %+v", preferred, wideHeight, bounds)
	}
}

func TestEffortSurfaceMeasuredGeometry(t *testing.T) {
	for _, warning := range []bool{false, true} {
		for _, dim := range [][2]int{{100, 20}, {32, 20}, {24, 30}, {200, 20}, {100, 7}, {100, 6}, {100, 5}, {5, 40}, {3, 80}} {
			t.Run(fmt.Sprintf("warning=%v/%dx%d", warning, dim[0], dim[1]), func(t *testing.T) {
				m, s := openedEffort(t, "max", warning)
				m = effortOffer(m, dim[0], dim[1])
				out := checkEffortBodyFits(t, m)
				if s.compact {
					if dim[0] >= 24 && dim[1] >= 6 {
						t.Fatal("usable offer incorrectly became close-only")
					}
					if strings.Contains(out, "apply") || strings.Contains(out, "move") {
						t.Fatal("compact hints advertise unavailable actions")
					}
					return
				}
				if !strings.Contains(out, "▶● m") {
					t.Fatalf("short/narrow open must reveal max: %s", out)
				}
				body, _ := s.Render(min(dim[0], 128-m.deps.Theme.Style("askCard").GetHorizontalFrameSize()), dim[1])
				if warning && !strings.Contains(strings.ReplaceAll(stripANSIstr(body), "\n", ""), "ignored") {
					t.Fatal("warning missing")
				}
				m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
				out = checkEffortBodyFits(t, m)
				if s.list.CursorID() != "auto" || !strings.Contains(out, "▶  a") {
					t.Fatal("keyboard did not reveal auto")
				}
			})
		}
	}
}

func TestEffortSurfaceExactlyOneRow(t *testing.T) {
	for _, tier := range []string{"auto", "medium", "max"} {
		t.Run(tier, func(t *testing.T) {
			m, s := openedEffort(t, effortValue(tier), false)
			m = effortOffer(m, 100, 5) // title + three footer rows + exactly one tier
			out := checkEffortBodyFits(t, m)
			if s.compact || s.list.Height() != 1 || !strings.Contains(out, "▶● "+tier) || !strings.Contains(out, "more") {
				t.Fatalf("one-row reveal lost tier/overflow: compact=%v height=%d\n%s", s.compact, s.list.Height(), out)
			}
			beforeConversation := m.vp.YOffset()
			wheel := tea.MouseWheelDown
			if tier == "max" {
				wheel = tea.MouseWheelUp
			}
			mm, _ := m.Update(tea.MouseWheelMsg{Button: wheel})
			m = mm.(Model)
			offset := s.list.Offset()
			out = checkEffortBodyFits(t, m)
			if s.list.CursorID() != tier || offset == effortCursorFor(effortValue(tier)) || s.list.Offset() != offset || strings.Contains(out, "▶") || m.vp.YOffset() != beforeConversation {
				t.Fatalf("wheel must move only window and survive Render: cursor=%s offset=%d\n%s", s.list.CursorID(), offset, out)
			}
			// Enter may apply the selection outside the manually wheeled window.
			mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			if cmd == nil || m.modal != nil || m.phase != phaseConnecting || m.createModelSelection.ReasoningEffort != effortValue(tier) || !m.prompt.Focused() {
				t.Fatal("one-row Enter must directly apply selected tier with legacy connecting focus")
			}
			if s.takeSurfaceIntent() != nil {
				t.Fatal("selection intent was not drained once")
			}

			m, s = openedEffort(t, effortValue(tier), false)
			m = effortOffer(m, 100, 5)
			_ = checkEffortBodyFits(t, m)
			mm, _ = m.Update(tea.MouseWheelMsg{Button: wheel})
			m = mm.(Model)
			m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
			out = checkEffortBodyFits(t, m)
			if !strings.Contains(out, "▶") || !strings.Contains(out, s.list.CursorID()) {
				t.Fatal("keyboard must re-reveal selection")
			}
		})
	}
}

func TestEffortSurfaceRemappedNavigationAndWheelIsolation(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.keys = navigationTestKeys()
	mm, _ := m.runEffort()
	m = effortOffer(mm.(Model), 100, 7)
	s := m.modal.(*effortState)
	out := checkEffortBodyFits(t, m)
	for _, hint := range []string{"u/d move", "p/n page", "t/b first/last"} {
		if !strings.Contains(out, hint) {
			t.Fatalf("captured remap hint missing %q: %s", hint, out)
		}
	}
	for _, step := range []struct{ key, want string }{{"u", "auto"}, {"d", "low"}, {"u", "auto"}, {"n", "medium"}, {"p", "low"}, {"p", "auto"}, {"b", "max"}, {"d", "max"}, {"t", "auto"}} {
		m = pressEffortKey(t, m, navigationTestKey(step.key))
		out = checkEffortBodyFits(t, m)
		if s.list.CursorID() != step.want || !strings.Contains(out, "▶") {
			t.Fatalf("%s selected %s, want %s: %s", step.key, s.list.CursorID(), step.want, out)
		}
	}
	m.prompt.Rewrite("hidden draft")
	before := m.vp.YOffset()
	for _, msg := range []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelRight}, tea.MouseWheelMsg{Button: tea.MouseWheelLeft}, tea.KeyPressMsg{Code: 'x', Text: "x"}} {
		offset := s.list.Offset()
		mm, _ = m.Update(msg)
		m = mm.(Model)
		_ = checkEffortBodyFits(t, m)
		if s.list.CursorID() != "auto" || m.vp.YOffset() != before || m.prompt.Value() != "hidden draft" {
			t.Fatal("input leaked or moved selection")
		}
		if wheel, ok := msg.(tea.MouseWheelMsg); ok && wheel.Button != tea.MouseWheelDown && s.list.Offset() != offset {
			t.Fatal("horizontal wheel moved window")
		}
	}
	m = pressEffortKey(t, m, navigationTestKey("u"))
	if out = checkEffortBodyFits(t, m); !strings.Contains(out, "▶● auto") {
		t.Fatal("clamped keyboard move must re-reveal auto")
	}
}

func TestEffortSurfaceCapturedInputsAndFreshReopen(t *testing.T) {
	m, s := openedEffort(t, "medium", true)
	m = effortOffer(m, 100, 20)
	m.keys = navigationTestKeys()
	m.resolvedSessionModel.ReasoningEffort = "max"
	m.modelCatalog.models[0].Reasoning = true
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	out := checkEffortBodyFits(t, m)
	if s.list.CursorID() != "high" || !strings.Contains(out, " ● medium") || !strings.Contains(out, "▶  high") || !strings.Contains(out, "no reasoning support") || !strings.Contains(out, "↑/↓ move") {
		t.Fatalf("open inputs refreshed or markers conflated: %s", out)
	}
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("Escape must close/refocus")
	}
	mm, _ := m.runEffort()
	m = mm.(Model)
	fresh := m.modal.(*effortState)
	out = checkEffortBodyFits(t, m)
	if fresh == s || fresh.list.CursorID() != "max" || strings.Contains(out, "no reasoning support") || !strings.Contains(out, "u/d move") {
		t.Fatal("reopen did not capture fresh inputs")
	}
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m.resolvedSessionModel.ReasoningEffort = "unknown"
	mm, _ = m.runEffort()
	m = mm.(Model)
	out = checkEffortBodyFits(t, m)
	if m.modal.(*effortState).list.CursorID() != "auto" || strings.Contains(out, "▶●") {
		t.Fatal("unknown current invented a marker")
	}
	for _, row := range m.modal.(*effortState).list.View().Rows {
		if row.StatusCells[0] != "" {
			t.Fatal("unknown current marked another tier")
		}
	}
}

func TestEffortSurfaceWheelDoesNotScrollHiddenTranscript(t *testing.T) {
	for _, geometry := range []string{"normal", "one-row", "compact", "zero-width", "zero-height"} {
		t.Run(geometry, func(t *testing.T) {
			m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
			m = effortOffer(m, 100, 20)
			m.conv.addUser("scrollable source")
			m.conv.startAssistant()
			m.conv.appendAssistant(strings.Repeat("underlying conversation\n\n", 100))
			m.refreshView()
			m.vp.SetYOffset(9)
			m.conversationView.observe(m.vp)
			for _, button := range []tea.MouseButton{tea.MouseWheelUp, tea.MouseWheelDown} {
				before := m.vp.YOffset()
				m = applyAll(m, tea.MouseWheelMsg{Button: button})
				if got := m.vp.YOffset(); button == tea.MouseWheelUp && got >= before || button == tea.MouseWheelDown && got <= before {
					t.Fatalf("control wheel must scroll unowned transcript: %d -> %d", before, got)
				}
			}
			m.resolvedSessionModel.ReasoningEffort = "max"
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF7})
			s := m.modal.(*effortState)
			switch geometry {
			case "one-row":
				m = effortOffer(m, 100, 5)
			case "compact":
				m = effortOffer(m, 100, 4)
			case "zero-width":
				m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 30})
			case "zero-height":
				m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 0})
			}
			before := m.vp.YOffset()
			if before <= 0 || m.vp.AtBottom() {
				t.Fatal("isolation fixture must be away from both scroll edges")
			}
			for _, button := range []tea.MouseButton{tea.MouseWheelUp, tea.MouseWheelDown} {
				m = applyAll(m, tea.MouseWheelMsg{Button: button})
				_ = m.View()
				if m.vp.YOffset() != before || s.list.CursorID() != "max" {
					t.Fatal("modal wheel leaked to transcript or moved selection")
				}
			}
		})
	}
}

func TestEffortSurfaceResizeRevealsSelection(t *testing.T) {
	for _, transition := range []string{"wide", "compact", "zero-width", "zero-height"} {
		t.Run(transition, func(t *testing.T) {
			m, s := openedEffort(t, "max", false)
			m = effortOffer(m, 100, 20)
			_ = checkEffortBodyFits(t, m)
			switch transition {
			case "compact":
				m = effortOffer(m, 100, 4)
				_ = m.View()
			case "zero-width":
				m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 20})
				_ = m.View()
			case "zero-height":
				m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 0})
				_ = m.View()
			}
			m = effortOffer(m, 100, 5)
			out := checkEffortBodyFits(t, m)
			if s.list.Height() != 1 || s.list.CursorID() != "max" || !strings.Contains(out, "▶● max") {
				t.Fatalf("changed offer must reveal selected max in sole row: %s", out)
			}
			m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
			out = checkEffortBodyFits(t, m)
			offset := s.list.Offset()
			if strings.Contains(out, "▶") || offset != 4 {
				t.Fatal("manual wheel did not hide selection")
			}
			if again := checkEffortBodyFits(t, m); again != out || s.list.Offset() != offset {
				t.Fatal("unchanged offer re-revealed manually hidden selection")
			}
		})
	}
}

func TestEffortSurfaceZeroAndCompactInvalidateActivation(t *testing.T) {
	for _, offer := range [][2]int{{0, 20}, {100, 0}, {100, 4}, {3, 80}, {1, 1}} {
		t.Run(fmt.Sprint(offer), func(t *testing.T) {
			m, s := openedEffort(t, "max", false)
			_ = m.View()
			if offer[0] == 0 || offer[1] == 0 {
				m = applyAll(m, tea.WindowSizeMsg{Width: offer[0], Height: offer[1]})
			} else {
				m = effortOffer(m, offer[0], offer[1])
			}
			// No intervening View: Model must invalidate stale usable geometry itself.
			for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyHome}, tea.MouseWheelMsg{Button: tea.MouseWheelUp}} {
				mm, cmd := m.Update(msg)
				m = mm.(Model)
				if cmd != nil || m.modal != s || !s.compact || m.phase != phaseIdle || s.list.CursorID() != "max" {
					t.Fatal("compact/zero input navigated or activated stale geometry")
				}
			}
			body := m.renderBody()
			if offer[0] == 0 || offer[1] == 0 {
				if body != "" || s.list.Valid() {
					t.Fatal("zero offer retained usable list or body")
				}
			} else {
				checkEffortBodyFits(t, m)
			}
			m = effortOffer(m, 100, 20)
			m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
			if s.compact || s.list.CursorID() != "auto" {
				t.Fatal("restored offer did not enable keyboard")
			}
			if offer[0] == 0 || offer[1] == 0 {
				m = applyAll(m, tea.WindowSizeMsg{Width: offer[0], Height: offer[1]})
			} else {
				m = effortOffer(m, offer[0], offer[1])
			}
			m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
			if m.modal != nil || !m.prompt.Focused() {
				t.Fatal("compact/zero Escape must close/refocus")
			}
		})
	}
}

func TestEffortSurfaceDisabledActionsNotAdvertised(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.keys.Choose.SetEnabled(false)
	m.keys.ScrollU = key.NewBinding()
	m.keys.ScrollD = key.NewBinding()
	m.keys.ScrollTop.SetEnabled(false)
	mm, _ := m.runEffort()
	m = mm.(Model)
	out := checkEffortBodyFits(t, m)
	if strings.Contains(out, "apply") || strings.Contains(out, " page") || strings.Contains(out, "first") || !strings.Contains(out, "end last") {
		t.Fatal("hints advertise disabled actions")
	}
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.phase != phaseIdle || m.modal == nil {
		t.Fatal("disabled Enter activated")
	}
}

func TestEffortSurfaceSubsetWheelWindowSurvivesRender(t *testing.T) {
	for capacity := 2; capacity <= 5; capacity++ {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			m, s := openedEffort(t, "auto", false)
			m = effortOffer(m, 100, capacity+4)
			_ = checkEffortBodyFits(t, m)
			before := m.vp.YOffset()
			for _, button := range []tea.MouseButton{tea.MouseWheelDown, tea.MouseWheelDown, tea.MouseWheelDown, tea.MouseWheelDown, tea.MouseWheelUp, tea.MouseWheelUp} {
				m = applyAll(m, tea.MouseWheelMsg{Button: button})
				out := checkEffortBodyFits(t, m)
				offset := s.list.Offset()
				if again := checkEffortBodyFits(t, m); again != out || s.list.Offset() != offset || s.list.CursorID() != "auto" || m.vp.YOffset() != before {
					t.Fatal("ordinary Render changed manual window or wheel changed selection/conversation")
				}
			}
			m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
			if out := checkEffortBodyFits(t, m); !strings.Contains(out, "▶  max") {
				t.Fatal("keyboard did not reveal max")
			}
			m = effortOffer(m, 32, 20)
			m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
			if out := checkEffortBodyFits(t, m); !strings.Contains(out, "▶● auto") {
				t.Fatal("navigation did not reveal after reflow")
			}
		})
	}
}

func TestEffortSurfaceRemappedActivationAndClosePrecedence(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"a"}, "Close": {"c"}, "Down": {"c"}})
	mm, _ := m.runEffort()
	m = mm.(Model)
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.phase != phaseIdle || m.modal == nil {
		t.Fatal("old Enter activated remapped picker")
	}
	m = pressEffortKey(t, m, navigationTestKey("c"))
	if m.modal != nil || !m.prompt.Focused() || m.deps.Session.(*fakeConv).forkCount != 0 {
		t.Fatal("Close must precede overlapping navigation")
	}
	mm, _ = m.runEffort()
	m = mm.(Model)
	mm, cmd := m.Update(navigationTestKey("a"))
	m = mm.(Model)
	if m.modal != nil || m.phase != phaseConnecting || cmd == nil {
		t.Fatal("remapped Choose must apply directly")
	}
}

func TestEffortSurfaceResizeBeforeViewRejectsEnter(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF7})
	m = mm.(Model)
	_ = m.View()
	mm, _ = m.Update(tea.WindowSizeMsg{Width: 12, Height: 4})
	m = mm.(Model)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseIdle || m.prompt.Focused() || cmd != nil {
		t.Fatalf("resize before View activated stale picker: phase=%v focused=%v cmd=%v", m.phase, m.prompt.Focused(), cmd != nil)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if !m.prompt.Focused() {
		t.Fatal("compact Escape must refocus prompt")
	}
}

func TestEffortSurfaceShortMaxFitsParent(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.resolvedSessionModel.ReasoningEffort = "max"
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 14})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF7})
	m = mm.(Model)
	body := m.renderBody()
	if lipgloss.Height(body) > m.vp.Height() || lipgloss.Width(body) > m.width {
		t.Fatalf("picker exceeds parent: %dx%d > %dx%d", lipgloss.Width(body), lipgloss.Height(body), m.width, m.vp.Height())
	}
}
