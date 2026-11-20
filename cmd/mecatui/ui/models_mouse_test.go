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

func modelsMouseModel(t *testing.T, inventory []client.ModelInfo) Model {
	t.Helper()
	m := newModelsModel(t, &fakeModels{models: inventory}, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.deps.NoAltScreen = false
	mm, cmd := m.runModels()
	return feedCmd(t, mm.(Model), cmd)
}

func modelsMouseControl(t *testing.T, m Model, primary bool) renderedHitRegion {
	t.Helper()
	s := modelsSurface(t, m)
	for _, hit := range m.hits.frame {
		if target, ok := s.hitControls[hit.id]; ok && target.selectModel == primary {
			return hit
		}
	}
	t.Fatalf("missing models control primary=%v", primary)
	return renderedHitRegion{}
}

func modelsMouseRow(t *testing.T, m Model, id string) renderedHitRegion {
	t.Helper()
	s := modelsSurface(t, m)
	for _, hit := range m.hits.frame {
		if s.hitItems[hit.id] == id {
			return hit
		}
	}
	t.Fatalf("missing model row %q", id)
	return renderedHitRegion{}
}

func modelsMouseInventory(n int) []client.ModelInfo {
	models := make([]client.ModelInfo, n)
	for i := range models {
		models[i] = client.ModelInfo{ProviderID: "provider", ID: fmt.Sprintf("model-%02d", i)}
	}
	return models
}

func TestModelsMouseUseMatchesKeysAndOffscreenSelection(t *testing.T) {
	for _, remap := range []bool{false, true} {
		for _, pointer := range []bool{false, true} {
			t.Run(fmt.Sprintf("remap=%v/pointer=%v", remap, pointer), func(t *testing.T) {
				m := modelsMouseModel(t, modelsMouseInventory(40))
				s := modelsSurface(t, m)
				choose := tea.KeyPressMsg{Code: tea.KeyEnter}
				if remap {
					// Dependencies are captured when opened, as in production.
					m.closeModal()
					m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"a"}, "Close": {"c"}})
					mm, cmd := m.runModels()
					m = feedCmd(t, mm.(Model), cmd)
					s = modelsSurface(t, m)
					choose = navigationTestKey("a")
				}
				m = effortOffer(m, 80, 15)
				view := stripANSIstr(m.View().Content)
				if remap && (!strings.Contains(view, "[a Use model]") || !strings.Contains(view, "[c Close]")) {
					t.Fatal("controls lost configured shortcuts")
				}
				selected := s.list.CursorID()
				before := m.vp.YOffset()
				for range 20 {
					m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				}
				_ = m.View()
				if s.list.CursorID() != selected || s.list.Offset() == 0 || m.vp.YOffset() != before || strings.Contains(stripANSIstr(m.View().Content), "▶") {
					t.Fatal("wheel must browse without selecting or leaking to conversation")
				}
				old := modelsMouseControl(t, m, true).id
				var cmd tea.Cmd
				if pointer {
					m, cmd = effortClick(m, modelsMouseControl(t, m, true), remap)
				} else {
					updated, c := m.Update(choose)
					m, cmd = updated.(Model), c
				}
				if m.modal != nil || m.phase != phaseConnecting || cmd == nil || m.createModelSelection != (client.ModelSelection{ProviderID: "provider", ModelID: "model-00"}) {
					t.Fatal("Use model and keyboard must act on selected offscreen model")
				}
				token := m.modelSwitchRequestToken
				updated, duplicate := m.Update(surfaceHitMsg{ID: old})
				m = updated.(Model)
				if duplicate != nil || m.modelSwitchRequestToken != token {
					t.Fatal("duplicate model switch")
				}
				m = feedModelSwitchBusiness(t, m, cmd)
				conv := m.deps.Session.(*fakeConv)
				if conv.carryoverCount != 1 {
					t.Fatal("both input paths must issue exactly one carryover")
				}
			})
		}
	}
}

func TestModelsMouseRowsAndContinuationsRemainSelectOnly(t *testing.T) {
	inventory := modelsMouseInventory(3)
	inventory[1].DisplayName = strings.Repeat("wrapped model 界 ", 12)
	m := effortOffer(modelsMouseModel(t, inventory), 30, 25)
	s := modelsSurface(t, m)
	for repeat := range 3 {
		_ = m.View()
		var rows []renderedHitRegion
		for _, hit := range m.hits.frame {
			if s.hitItems[hit.id] == modelIdentity(inventory[1]) {
				rows = append(rows, hit)
			}
		}
		if len(rows) < 3 {
			t.Fatal("fixture lacks wrapped model rows")
		}
		for _, row := range rows {
			_ = m.View()
			m, _ = effortClick(m, row, repeat%2 == 0)
			if s.list.CursorID() != modelIdentity(inventory[1]) || m.modal != s || m.phase != phaseIdle || !m.createModelSelection.IsZero() {
				t.Fatal("first/repeated/continuation row click must only select")
			}
		}
	}
	_ = m.View()
	m, _ = effortClick(m, modelsMouseControl(t, m, true), false)
	if m.createModelSelection.ModelID != inventory[1].ID || m.modal != nil {
		t.Fatal("explicit action did not use clicked model")
	}
}

func TestModelsMouseClearAndCloseKeyParity(t *testing.T) {
	for _, pointer := range []bool{false, true} {
		m := modelsMouseModel(t, modelsMouseInventory(3))
		m = typeFilter(t, m, "model-01")
		s := modelsSurface(t, m)
		_ = m.View()
		old := modelsMouseControl(t, m, false).id
		for step := range 2 {
			view := stripANSIstr(m.View().Content)
			label := "[esc Clear filter]"
			if step == 1 {
				label = "[esc Close]"
			}
			if !strings.Contains(view, label) {
				t.Fatalf("missing state-correct %s", label)
			}
			if pointer {
				m, _ = effortClick(m, modelsMouseControl(t, m, false), step == 0)
			} else {
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			}
			if step == 0 {
				if m.modal != s || s.filter.Value() != "" || len(s.filtered) != 3 {
					t.Fatal("first close action must clear filter")
				}
				m = applyAll(m, surfaceHitMsg{ID: old})
				if m.modal != s {
					t.Fatal("old Clear filter became Close")
				}
			}
		}
		if m.modal != nil || !m.prompt.Focused() {
			t.Fatal("Close must refocus prompt")
		}
	}
}

func TestModelsMouseStaleControls(t *testing.T) {
	for _, primary := range []bool{false, true} {
		for _, change := range []string{"row", "key", "cursor", "filter", "filter-direct", "refresh", "reorder", "remove", "loading", "error", "resize", "prepare", "compact", "replace", "reopen"} {
			t.Run(fmt.Sprintf("primary=%v/%s", primary, change), func(t *testing.T) {
				m := modelsMouseModel(t, modelsMouseInventory(3))
				s := modelsSurface(t, m)
				_ = m.View()
				old := modelsMouseControl(t, m, primary).id
				switch change {
				case "row":
					m, _ = effortClick(m, modelsMouseRow(t, m, modelIdentity(s.filtered[1])), false)
				case "key":
					m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
				case "cursor":
					s.list.SetCursor(1)
				case "filter":
					m = typeFilter(t, m, "model-01")
				case "filter-direct":
					s.filter.SetValue("model")
				case "refresh":
					m = applyAll(m, client.ModelsMsg{RequestToken: s.requestToken, Models: slices.Clone(s.catalog.models)})
				case "reorder":
					slices.Reverse(s.catalog.models)
				case "remove":
					s.catalog.models = s.catalog.models[1:]
					s.syncFilter()
				case "loading":
					s.loading = true
				case "error":
					s.err = errors.New("offline")
				case "resize":
					m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30})
				case "prepare":
					(&m).prepareSurfaceInput()
				case "compact":
					m = effortOffer(m, 80, 1)
					_ = m.View()
				case "replace":
					m.closeModal()
					mm, _ := m.runHelp()
					m = mm.(Model)
				case "reopen":
					m.closeModal()
					mm, _ := m.runModels()
					m = mm.(Model)
				}
				modal, filter, token := m.modal, s.filter.Value(), m.modelSwitchRequestToken
				updated, cmd := m.Update(surfaceHitMsg{ID: old})
				m = updated.(Model)
				if cmd != nil || m.modal != modal || s.filter.Value() != filter || m.modelSwitchRequestToken != token || m.phase != phaseIdle {
					t.Fatal("old control acted on changed state")
				}
			})
		}
	}
}

func TestModelsMouseGeometryStyleAndIsolation(t *testing.T) {
	for _, width := range []int{18, 80} {
		for _, filtered := range []bool{false, true} {
			for _, end := range []bool{false, true} {
				m := modelsMouseModel(t, modelsMouseInventory(4))
				if filtered {
					m = typeFilter(t, m, "model")
				}
				m = effortOffer(m, width, 30)
				s := modelsSurface(t, m)
				view := m.View().Content
				primary, back := modelsMouseControl(t, m, true), modelsMouseControl(t, m, false)
				if s.rowBudget == 0 || (width == 18 && primary.rect.y0 == back.rect.y0) {
					t.Fatal("controls must wrap before budgeting useful body")
				}
				backLabel := "[esc Close]"
				if filtered {
					backLabel = "[esc Clear filter]"
				}
				for _, control := range []struct {
					hit   renderedHitRegion
					label string
				}{{primary, "[enter Use model]"}, {back, backLabel}} {
					x, y := m.metrics.localToGlobal(control.hit.rect.x0, control.hit.rect.y0)
					line := strings.Split(stripANSIstr(view), "\n")[y]
					if ansi.Cut(line, x, x+control.hit.rect.x1-control.hit.rect.x0) != control.label || !strings.Contains(view, m.deps.Theme.Style("viewport").Bold(true).Render(control.label)) {
						t.Fatal("full label hit/style differs from rendered button")
					}
					x, y = m.metrics.localToGlobal(control.hit.rect.x1, control.hit.rect.y0)
					updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
					m = updated.(Model)
					if cmd != nil || m.modal != s || s.filter.Value() != map[bool]string{false: "", true: "model"}[filtered] {
						t.Fatal("button gap activated")
					}
				}
				if width == 80 && !strings.Contains(view, m.deps.Theme.Style("muted").Render("type to filter · ↑/↓/pgup move · ctrl+g set global default")) {
					t.Fatal("navigation and keyboard-only global default must be separate muted help")
				}
				if strings.Contains(stripANSIstr(view), "enter use") || strings.Contains(stripANSIstr(view), "clear filter / close") {
					t.Fatal("duplicate action hints")
				}
				_ = m.View()
				m, _ = effortClick(m, modelsMouseControl(t, m, false), end)
				if filtered && (m.modal != s || s.filter.Value() != "") || !filtered && m.modal != nil {
					t.Fatal("button edge did not share close/filter behavior")
				}
			}
		}
	}
}

func TestModelsMouseCompactPreservesBrowsingAndDropsOptionalHelp(t *testing.T) {
	m := effortOffer(modelsMouseModel(t, modelsMouseInventory(40)), 80, 12)
	s := modelsSurface(t, m)
	s.list.SetCursor(20)
	view := stripANSIstr(m.View().Content)
	if s.rowBudget == 0 || strings.Contains(view, "reason =") || !strings.Contains(view, "[enter Use model]") {
		t.Fatalf("optional help must yield before useful body and controls: budget=%d\n%s", s.rowBudget, view)
	}
	selected, offset := s.list.CursorID(), s.list.Offset()
	m = effortOffer(m, 80, 1)
	_ = m.View()
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyHome}, tea.MouseWheelMsg{Button: tea.MouseWheelUp}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.rowBudget != 0 || s.list.CursorID() != selected || s.list.Offset() != offset || m.modal != s {
		t.Fatal("compact input changed browsing state or activated")
	}
	m = effortOffer(m, 80, 12)
	_ = m.View()
	if s.rowBudget == 0 || s.list.CursorID() != selected {
		t.Fatal("compact restoration lost selection")
	}
	m, _ = effortClick(m, modelsMouseControl(t, m, true), true)
	if m.modal != nil || m.createModelSelection.ModelID != "model-20" {
		t.Fatal("restored primary must apply the preserved selection")
	}
}

func TestModelsMouseAvailabilityBindingsAndCompact(t *testing.T) {
	for _, state := range []string{"loading", "error", "empty", "nomatch", "disabled-choose", "disabled-close", "no-mouse", "compact"} {
		t.Run(state, func(t *testing.T) {
			m := modelsMouseModel(t, modelsMouseInventory(4))
			s := modelsSurface(t, m)
			switch state {
			case "loading":
				s.loading = true
			case "error":
				s.err = errors.New("offline")
			case "empty":
				s.catalog.models = nil
				s.syncFilter()
			case "nomatch":
				m = typeFilter(t, m, "absent")
			case "disabled-choose":
				s.deps.keys.Choose.SetEnabled(false)
			case "disabled-close":
				s.deps.keys.Close.SetEnabled(false)
			case "no-mouse":
				m.deps.NoMouse = true
			case "compact":
				m = effortOffer(m, 80, 1)
			}
			_ = m.View()
			if state == "compact" {
				if len(m.hits.frame) != 0 {
					t.Fatal("compact view published hits")
				}
			} else if state != "no-mouse" {
				for _, target := range s.hitControls {
					if target.selectModel && state != "disabled-close" {
						t.Fatal("unavailable Use model published")
					}
					if !target.selectModel && state == "disabled-close" {
						t.Fatal("disabled Close published")
					}
				}
			}
			if state == "disabled-close" {
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
				if m.modal != s {
					t.Fatal("disabled Close key acted")
				}
				return
			}
			if state == "no-mouse" {
				m, _ = effortClick(m, modelsMouseControl(t, m, true), false)
				m, _ = effortClick(m, modelsMouseControl(t, m, false), true)
				m, _ = effortClick(m, modelsMouseRow(t, m, modelIdentity(s.filtered[1])), false)
				if m.modal != s || s.list.CursorID() != modelIdentity(s.filtered[0]) || m.View().MouseMode != tea.MouseModeNone {
					t.Fatal("no-mouse pointer input acted")
				}
				// No mouse affects pointer routing, not keyboard semantics.
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				if m.modal != nil || m.phase != phaseConnecting {
					t.Fatal("no-mouse blocked keyboard")
				}
				return
			}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if m.modal != s || m.phase != phaseIdle {
				t.Fatal("unavailable key action applied")
			}
			if state == "compact" {
				m = effortOffer(m, 80, 20)
				_ = m.View()
				if s.rowBudget == 0 || s.list.CursorID() != modelIdentity(s.filtered[0]) {
					t.Fatal("compact restoration lost selection")
				}
			} else {
				_ = m.View()
				m, _ = effortClick(m, modelsMouseControl(t, m, false), true)
				if state == "nomatch" && (m.modal != s || s.filter.Value() != "") || state != "nomatch" && m.modal != nil {
					t.Fatal("availability Close/Clear filter failed")
				}
			}
		})
	}
}

func TestModelsMouseStaleRows(t *testing.T) {
	for _, change := range []string{"row", "key", "filter", "refresh", "reorder", "remove", "loading", "error", "wheel", "clear"} {
		t.Run(change, func(t *testing.T) {
			inventory := modelsMouseInventory(4)
			m := modelsMouseModel(t, inventory)
			if change == "clear" {
				m = typeFilter(t, m, "model")
			}
			s := modelsSurface(t, m)
			_ = m.View()
			old := modelsMouseRow(t, m, modelIdentity(inventory[2])).id
			switch change {
			case "row":
				m, _ = effortClick(m, modelsMouseRow(t, m, modelIdentity(inventory[1])), false)
			case "key":
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
			case "filter":
				m = typeFilter(t, m, "model-0")
			case "refresh", "reorder", "remove":
				models := slices.Clone(inventory)
				switch change {
				case "reorder":
					slices.Reverse(models)
				case "remove":
					models = models[:2]
				}
				m = applyAll(m, client.ModelsMsg{RequestToken: s.requestToken, Models: models})
			case "loading":
				// The existing pending-state seam has no separate loading message.
				s.loading = true
			case "error":
				m = applyAll(m, client.ModelsMsg{RequestToken: s.requestToken, Err: errors.New("offline")})
			case "wheel":
				m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			case "clear":
				m, _ = effortClick(m, modelsMouseControl(t, m, false), false)
			}
			selected := s.list.CursorID()
			updated, cmd := m.Update(surfaceHitMsg{ID: old})
			m = updated.(Model)
			if cmd != nil || s.list.CursorID() != selected || m.modal != s || m.phase != phaseIdle {
				t.Fatal("old row handle selected after frame changed or was consumed")
			}
			if len(s.hitItems) != 0 || len(s.hitControls) != 0 {
				t.Fatal("input/catalog change retained frame-local targets")
			}
		})
	}
}

func TestModelsMouseUntrustedChromeGeometry(t *testing.T) {
	for _, field := range []string{"provider", "state", "hint", "provenance", "error"} {
		t.Run(field, func(t *testing.T) {
			m := modelsMouseModel(t, modelsMouseInventory(3))
			s := modelsSurface(t, m)
			inventory := modelsMouseInventory(3)
			status := client.ProviderStatus{ProviderID: "provider", State: "unreachable", Hint: "check configuration"}
			const multiline = "remote\n\nstatus"
			switch field {
			case "provider":
				status.ProviderID = multiline
			case "state":
				status.State = multiline
			case "hint":
				status.Hint = multiline
			case "provenance":
				m.resolvedSessionModel = client.ResolvedModel{ProviderID: "provider", ModelID: "model-00"}
				inventory[0].DisplayName = multiline
			case "error":
				m = applyAll(m, client.ModelsMsg{RequestToken: s.requestToken, Err: errors.New(multiline)})
			}
			if field != "error" {
				m = applyAll(m, client.ModelsMsg{RequestToken: s.requestToken, Models: inventory, Statuses: []client.ProviderStatus{status}})
			}
			m = effortOffer(m, 100, 35)
			view := stripANSIstr(m.View().Content)
			primary := field != "error"
			label := "[enter Use model]"
			if !primary {
				label = "[esc Close]"
			}
			x, y := -1, -1
			for row, line := range strings.Split(view, "\n") {
				if i := strings.Index(line, label); i >= 0 {
					x, y = ansi.StringWidth(line[:i]), row
				}
			}
			if x < 0 {
				t.Fatalf("missing visible action: %s", view)
			}
			hit := modelsMouseControl(t, m, primary)
			hx, hy := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
			if hx != x || hy != y {
				t.Fatalf("visible label at %d,%d but hit starts %d,%d", x, y, hx, hy)
			}
			// Status and the blank row above the footer must never activate it.
			for _, missY := range []int{y - 2, y - 1} {
				_ = m.View()
				updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: missY})
				m = updated.(Model)
				if cmd != nil || m.modal != s || m.phase != phaseIdle {
					t.Fatal("status/gap click activated action")
				}
			}
			_ = m.View()
			updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m = updated.(Model)
			if m.modal != nil || cmd == nil {
				t.Fatal("visible action did not act")
			}
			if primary {
				if m.createModelSelection.ModelID != "model-00" {
					t.Fatal("visible Use model selected wrong model")
				}
				token := m.modelSwitchRequestToken
				updated, duplicate := m.Update(surfaceHitMsg{ID: hit.id})
				m = updated.(Model)
				if duplicate != nil || m.modelSwitchRequestToken != token {
					t.Fatal("action submitted twice")
				}
				m = feedModelSwitchBusiness(t, m, cmd)
				if m.deps.Session.(*fakeConv).carryoverCount != 1 {
					t.Fatal("visible Use model must select exactly once")
				}
			}
		})
	}
}

func TestModelsEmptyProviderStatusTerminalSafety(t *testing.T) {
	for _, field := range []string{"safe", "provider", "state", "hint"} {
		t.Run(field, func(t *testing.T) {
			m := modelsMouseModel(t, nil)
			s := modelsSurface(t, m)
			status := client.ProviderStatus{ProviderID: "remote-provider", State: "custom-state", Hint: "safe remediation"}
			const attack = "\x1b]52;c;YXR0YWNr\x07\x1b[31m"
			switch field {
			case "provider":
				status.ProviderID += attack
			case "state":
				status.State += attack
			case "hint":
				status.Hint += attack
			}
			m = applyAll(m, client.ModelsMsg{RequestToken: s.requestToken, Statuses: []client.ProviderStatus{status}})
			raw := m.View().Content
			// Check raw output before stripping legitimate theme SGR sequences.
			if strings.Contains(raw, "\x1b]52;") || strings.Contains(raw, "\x07") || strings.Contains(raw, "\x1b[31m") {
				t.Fatal("server OSC/ANSI controls escaped into real View")
			}
			plain := stripANSIstr(raw)
			for _, safe := range []string{"remote-provider", "custom-state", "safe remediation"} {
				if !strings.Contains(plain, safe) {
					t.Fatalf("lost safe provider text %q", safe)
				}
			}
			hit := modelsMouseControl(t, m, false)
			x, y := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
			line := strings.Split(plain, "\n")[y]
			if ansi.Cut(line, x, x+hit.rect.x1-hit.rect.x0) != "[esc Close]" {
				t.Fatal("empty-inventory Close hit differs from visible label")
			}
			updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m = updated.(Model)
			if cmd == nil || m.modal != nil || !m.prompt.Focused() {
				t.Fatal("empty-inventory Close must close and refocus prompt")
			}
			updated, duplicate := m.Update(surfaceHitMsg{ID: hit.id})
			if duplicate != nil || updated.(Model).modal != nil {
				t.Fatal("consumed empty-inventory Close acted twice")
			}
		})
	}
}

func TestModelsMouseAdditionalGoldens(t *testing.T) {
	for _, state := range []string{"wrapped", "compact", "loading", "error", "disabled_bindings"} {
		t.Run(state, func(t *testing.T) {
			m := modelsMouseModel(t, modelsMouseInventory(6))
			s := modelsSurface(t, m)
			switch state {
			case "wrapped":
				m = typeFilter(t, m, "model")
				m = effortOffer(m, 24, 20)
			case "compact":
				m = effortOffer(m, 80, 1)
			case "loading":
				s.loading = true
				s.filtered = nil
			case "error":
				s.err = errors.New("catalog unavailable")
				s.filtered = nil
			case "disabled_bindings":
				s.deps.keys.Choose.SetEnabled(false)
				s.deps.keys.Close.SetEnabled(false)
			}
			m = goldenStatusFrame(t, m)
			_ = m.View()
			if state == "wrapped" {
				if s.rowBudget == 0 || modelsMouseControl(t, m, true).rect.y0 == modelsMouseControl(t, m, false).rect.y0 {
					t.Fatal("golden must wrap controls while retaining body")
				}
			}
			compareGolden(t, "models_"+state+".golden", stripANSI([]byte(m.View().Content)))
		})
	}
}
