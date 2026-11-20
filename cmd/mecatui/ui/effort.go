package ui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// effort.go is the /effort picker — a tiny SELECTING overlay (cursor + enter) for
// the reasoning-effort tier. Enter on a tier applies DIRECTLY via a
// FORK-RESUME: the server forks the session's conversation onto a peer
// session at the new effort, so the transcript SURVIVES — no confirm step (the
// switch is non-destructive, so it is never a teardown warning), no wipe. It is
// much simpler than /models: a FIXED enum, no filter; the only RPC is the fork
// itself. Like /models it renders purely from client state (no proto in ui) and is
// idle-only / esc-dismissable.
//
// # Vocab + the auto token
//
// The neutral picker enum (in order) is auto, low, medium, high, xhigh, max. "auto"
// asks for the provider default and is sent as the explicit "auto" token, so a fork
// from a session at another level resets it instead of inheriting it (an empty
// effort would). effortLabel maps the empty (unset) value back to "auto" for
// display. The LIVE display reads m.resolvedSessionModel.ReasoningEffort (the
// level the server applies), so the picker only ever sends the operator's REQUEST.
//
// # Per-model levels
//
// When the server reports per-model effort levels (Capabilities.ModelEffortLevels),
// the picker offers auto plus the levels the session's model accepts, and says
// why when that is fewer than all five (effortChoicesFor). Without the feature it
// offers every tier.

// effortAuto is the picker label for the UNSET tier; it maps to the empty string on
// the wire and in persisted state (see the package header). Named once so the label
// list, effortValue, and effortLabel share one spelling.
const effortAuto = "auto"

const (
	effortMaxOuterWidth  = 128
	effortRowGutterWidth = 3
	effortWarning        = "this model has no reasoning support — a tier will be ignored"
	effortNoLevels       = "This model has no effort levels"
	effortUnknownLevels  = "This model's effort levels are unknown; the provider may refuse a level"
)

// effortTiers is the neutral reasoning-effort vocabulary in picker order. "auto" is
// the unset sentinel (⇒ ""); the rest are the real tiers the server understands.
// FIXED order, locked by a test so the picker layout is stable.
//
// AUTHORITY: the canonical neutral vocabulary lives in composition —
// internal/app/reasoning_effort.go (validReasoningEfforts / NormalizeReasoningEffort).
// This list is the TUI's display copy (the ui imports no engine/internal package); it
// must stay in sync with that set. The server normalises/clamps whatever the picker
// sends, so a drift here degrades gracefully (an unknown token fail-softs to unset),
// but keep the two aligned.
var effortTiers = []string{effortAuto, "low", "medium", "high", "xhigh", "max"}

// effortChoices is what the picker offers for the session's model: the tiers, in
// order with auto first, and an optional explanation shown under them.
type effortChoices struct {
	tiers   []string
	note    string
	warning bool // render note as a warning rather than muted text
}

// effortState owns the picker for one open lifetime; session effects stay on Model.
type effortState struct {
	deps       surfaceDeps
	list       bounded.List
	choices    effortChoices
	intent     surfaceIntent
	compact    bool   // view cache: refreshed from every Render offer
	offer      [2]int // view cache: last Render geometry, for selection reveal on resize
	hitTargets map[HitID]effortHit
}

type effortHit struct {
	tier         string
	apply, close bool
}

type effortSelectIntent struct{ effort string }

func (effortSelectIntent) isSurfaceIntent() {}

func (s *effortState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func (s *effortState) modalMaxOuterWidth() int {
	frame := s.deps.theme.Style("askCard").GetHorizontalFrameSize()
	width := ansi.StringWidth("Reasoning effort")
	for _, tier := range s.choices.tiers {
		width = max(width, effortRowGutterWidth+ansi.StringWidth(effortRowText(tier)))
	}
	for _, line := range s.footerLines(effortMaxOuterWidth - frame) {
		width = max(width, ansi.StringWidth(line))
	}
	width = max(width, ansi.StringWidth(s.choices.note))
	return min(effortMaxOuterWidth, width+frame)
}
func (s *effortState) modalFrame() bool { return !s.compact }
func (s *effortState) Close()           { s.intent, s.hitTargets = nil, nil }

func (s *effortState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	hit, ok := msg.(surfaceHitMsg)
	if !ok {
		return nil, false, false
	}
	target, current := s.hitTargets[hit.ID]
	s.hitTargets = nil
	if !current || s.compact {
		return nil, true, false
	}
	if target.close {
		return nil, true, true
	}
	if target.apply {
		if target.tier == s.list.CursorID() {
			s.apply()
		}
	} else {
		for i, tier := range s.choices.tiers {
			if tier == target.tier {
				s.list.SetCursor(i)
				break
			}
		}
	}
	return nil, true, false
}

func (s *effortState) apply() {
	if !s.compact && s.list.Valid() && s.deps.keys.Choose.Enabled() {
		s.intent = effortSelectIntent{effort: s.list.CursorID()}
	}
}

func (s *effortState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if key.Matches(msg, s.deps.keys.Close) {
		return nil, true, true
	}
	if s.compact || !s.list.Valid() {
		return nil, true, false
	}
	if move, ok := boundedNavigationMove(msg, s.deps.keys); ok {
		s.list.Move(move)
	} else if key.Matches(msg, s.deps.keys.Choose) {
		s.apply()
	}
	return nil, true, false
}

func (s *effortState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if !s.compact && s.list.Valid() {
		if move, ok := boundedWheelMove(msg); ok {
			s.list.Scroll(move)
		}
	}
	return nil, true
}

// effortLabel maps an effort value to its picker label: the empty (unset) value
// displays as auto, every other value is itself. Used to mark the current row and
// to find the cursor's initial position.
func effortLabel(value string) string {
	if value == "" {
		return effortAuto
	}
	return value
}

// openEffort opens the picker, positioning the cursor on the CURRENT effective effort
// (resolved by the server, m.resolvedSessionModel.ReasoningEffort) so the current tier is
// pre-selected. Only callable while idle and when model selection is available (the
// effort is a per-session server setting that only matters with a selectable model);
// returns the model unchanged otherwise. Unlike /models it fires no RPC (the enum is
// fixed and client-owned).
func (m Model) openEffort() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || !m.caps.ModelSelection {
		return m, nil
	}
	s := &effortState{deps: (&m).surfaceDeps(), choices: m.effortChoicesFor(), compact: true}
	items := make([]bounded.ListItem, len(s.choices.tiers))
	for i, tier := range s.choices.tiers {
		items[i] = bounded.ListItem{ID: tier, Text: effortRowText(tier)}
		if tier == effortLabel(m.resolvedSessionModel.ReasoningEffort) {
			items[i].StatusCells[0] = "●"
		}
	}
	s.list.SetItems(items)
	s.list.SetCursor(effortCursorFor(s.choices.tiers, m.resolvedSessionModel.ReasoningEffort))
	m.modal = s
	m.prompt.Blur()
	return m, nil
}

// effortCursorFor returns the index of the tier whose label matches the current
// (effective) effort value, or 0 (auto) when none matches (an unknown/unset effort
// lands on auto, the safe default).
func effortCursorFor(tiers []string, current string) int {
	want := effortLabel(current)
	for i, tier := range tiers {
		if tier == want {
			return i
		}
	}
	return 0
}

// effortChoicesFor decides what the picker offers for the session's model. With
// per-model levels it offers auto plus the levels the model accepts: a model known
// to take none offers auto alone and says so, a model whose levels are unknown
// offers every tier and says the provider may refuse one, and a requested level
// the model doesn't accept is named. Without the feature, or before the model is
// in the inventory, it offers every tier without a claim.
func (m Model) effortChoicesFor() effortChoices {
	if !m.caps.ModelEffortLevels {
		choices := effortChoices{tiers: effortTiers}
		if m.currentModelNoReasoning() {
			choices.note, choices.warning = effortWarning, true
		}
		return choices
	}
	info, found := m.currentModelInfo()
	switch {
	case !found:
		return effortChoices{tiers: effortTiers}
	case !info.EffortLevelsKnown:
		return effortChoices{tiers: effortTiers, note: effortUnknownLevels}
	}
	choices := effortChoices{tiers: []string{effortAuto}}
	for _, tier := range effortTiers[1:] {
		if slices.Contains(info.EffortLevels, tier) {
			choices.tiers = append(choices.tiers, tier)
		}
	}
	requested := m.resolvedSessionModel.RequestedReasoningEffort
	switch {
	case len(choices.tiers) == 1:
		choices.note, choices.warning = effortNoLevels, true
	case requested != "" && requested != effortAuto && !slices.Contains(choices.tiers, requested):
		choices.note = "This model doesn't accept " + terminaltext.SanitizeSingleLine(requested) + "; requests use the provider default"
		choices.warning = true
	}
	return choices
}

// currentModelInfo finds the session's effective model in the loaded inventory.
func (m Model) currentModelInfo() (client.ModelInfo, bool) {
	id, pid := m.resolvedSessionModel.ModelID, m.resolvedSessionModel.ProviderID
	if id == "" {
		return client.ModelInfo{}, false
	}
	for _, mi := range m.modelCatalog.models {
		if mi.ID == id && mi.ProviderID == pid {
			return mi, true
		}
	}
	return client.ModelInfo{}, false
}

// currentModelNoReasoning reports whether the CURRENT effective model is KNOWN (from
// the loaded /models inventory) to NOT support reasoning — so a picked effort tier
// would be DROPPED by the server's capability gate. It is the TUI half of
// that gate's acknowledgement (UX): the picker warns up front so a user who restarts
// the session for an effort they can't get is not left guessing (the server-side
// degrade only logs). It is CONSERVATIVE — true ONLY when the model is found in the
// inventory with Reasoning==false; an unknown/unloaded model returns false (fail-open,
// matching the server's unknown=capable posture), so the warning never cries wolf.
func (m Model) currentModelNoReasoning() bool {
	id, pid := m.resolvedSessionModel.ModelID, m.resolvedSessionModel.ProviderID
	if id == "" {
		return false // no resolved model yet → say nothing
	}
	for _, mi := range m.modelCatalog.models {
		if mi.ID == id && mi.ProviderID == pid {
			return !mi.Reasoning
		}
	}
	return false // not in the inventory → unknown → fail-open (no warning)
}

// effortSelection builds the ModelSelection for a restart that changes ONLY the
// reasoning effort: it carries the CURRENT session's provider/model (the effective
// model the server resolved, falling back to the pending createModelSelection) so the model
// is preserved across the restart, with the new effort applied. When no model is
// known yet (older server / mid-connect) it carries the bare effort over the
// server-default provider — meaningful on its own (effort rides the
// server-default provider), so this is not a zero selection.
func (m Model) effortSelection(effort string) client.ModelSelection {
	sel := client.ModelSelection{
		ProviderID: m.resolvedSessionModel.ProviderID,
		ModelID:    m.resolvedSessionModel.ModelID,
	}
	if sel.ProviderID == "" && sel.ModelID == "" {
		// No server-resolved model yet — fall back to the pending next selection so a
		// pre-connect effort change still preserves whatever model is queued.
		sel.ProviderID = m.createModelSelection.ProviderID
		sel.ModelID = m.createModelSelection.ModelID
	}
	sel.ReasoningEffort = effort
	return sel
}

type effortControl struct {
	rect  cellRect
	close bool
}

func (s *effortState) footerControls(width int) ([]string, []effortControl) {
	var lines []string
	var controls []effortControl
	for _, action := range []struct {
		hint, name     string
		enabled, close bool
	}{
		{s.deps.marks.choose, "Apply", s.deps.keys.Choose.Enabled(), false},
		{s.deps.marks.closeOnly, "Close", s.deps.keys.Close.Enabled(), true},
	} {
		if !action.enabled {
			continue
		}
		label := "[" + terminaltext.SanitizeSingleLine(action.hint) + " " + action.name + "]"
		w := ansi.StringWidth(label)
		if w > width {
			return nil, nil
		}
		x := 0
		if len(lines) > 0 {
			x = ansi.StringWidth(lines[len(lines)-1]) + 1
		}
		if len(lines) == 0 || x+w > width {
			lines = append(lines, "")
			x = 0
		}
		y := len(lines) - 1
		if x > 0 {
			lines[y] += " "
		}
		lines[y] += s.deps.theme.Style("viewport").Bold(true).Render(label)
		controls = append(controls, effortControl{rect: cellRect{x0: x, x1: x + w, y0: y, y1: y + 1}, close: action.close})
	}
	return lines, controls
}

func (s *effortState) footerLines(width int) []string {
	th, hk := s.deps.theme, s.deps.marks
	var navigation []string
	for i, pair := range []struct {
		up, down      key.Binding
		marks, action string
	}{
		{s.deps.keys.Up, s.deps.keys.Down, hk.navUp + "/" + hk.navDown, "move"},
		{s.deps.keys.ScrollU, s.deps.keys.ScrollD, hk.scroll, "page"},
		{s.deps.keys.ScrollTop, s.deps.keys.ScrollBottom, hk.jump, "first/last"},
	} {
		marks := pair.marks
		if !pair.up.Enabled() && !pair.down.Enabled() {
			continue
		}
		if !pair.up.Enabled() {
			marks = firstKey(pair.down, "")
			if i == 2 {
				pair.action = "last"
			}
		}
		if !pair.down.Enabled() {
			marks = firstKey(pair.up, "")
			if i == 2 {
				pair.action = "first"
			}
		}
		navigation = append(navigation, marks+" "+pair.action)
	}
	footer, _ := s.footerControls(width)
	if len(navigation) > 0 {
		footer = append(footer, skillsTextLines(th.Style("muted"), strings.Join(navigation, " · "), width)...)
	}
	return append(footer, skillsTextLines(th.Style("muted"), "● current  ·  auto = provider default", width)...)
}

func (s *effortState) Render(width, height int) (string, []ClickableRegion) {
	offer := [2]int{width, height}
	reveal := offer != s.offer || s.list.RevealPending()
	s.offer = offer
	s.compact = true
	s.hitTargets = nil
	if width <= 0 || height <= 0 {
		s.list.SetGeometry(0, 0, 2, bounded.Clip)
		return "", nil
	}
	th, hk := s.deps.theme, s.deps.marks
	title := skillsTextLines(th.Style("askTitle"), "Reasoning effort", width)
	footer := s.footerLines(width)
	controlsText, controls := s.footerControls(width)
	var warning []string
	if s.choices.note != "" {
		style := th.Style("muted")
		if s.choices.warning {
			style = th.Style("warning")
		}
		warning = skillsTextLines(style, s.choices.note, width)
	}
	rows := height - len(title) - len(warning) - len(footer)
	if rows < 1 {
		footer = controlsText
		rows = height - len(title) - len(warning) - len(footer)
	}
	s.list.SetGeometry(width, rows, 2, bounded.Clip)
	if !s.list.Valid() || ((s.deps.keys.Choose.Enabled() || s.deps.keys.Close.Enabled()) && len(controls) == 0) {
		return renderSkillsCompact(th, hk, width), nil
	}
	s.compact = false
	s.hitTargets = make(map[HitID]effortHit)
	var regions []ClickableRegion
	addHit := func(rect cellRect, target effortHit) {
		if s.deps.hits != nil {
			id := s.deps.hits.allocate()
			s.hitTargets[id] = target
			regions = append(regions, ClickableRegion{rect: rect, hit: id})
		}
	}
	view := s.list.ViewWithIndicators(rows, reveal)
	result := title
	if rows == 1 && (view.Above > 0 || view.Below > 0) {
		// Capacity-one overflow has no reserved indicator rows. Reuse the header.
		result[len(result)-1] = ansi.Cut(th.Style("askTitle").Render(fmt.Sprintf("↑ %d ↓ %d more · Reasoning effort", view.Above, view.Below)), 0, width)
	} else if view.Above > 0 {
		result = append(result, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↑ %d more", view.Above)), 0, width))
	}
	for _, row := range view.Rows {
		p := presentListRow(row, th.Style("spinner"), th.Style("muted"))
		text := ansi.Cut(p.Style.Render(p.Text), 0, width)
		addHit(cellRect{x0: 0, x1: ansi.StringWidth(text), y0: len(result), y1: len(result) + 1}, effortHit{tier: row.ID})
		result = append(result, text)
	}
	if rows > 1 && view.Below > 0 {
		result = append(result, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↓ %d more", view.Below)), 0, width))
	}
	result = append(result, warning...)
	for _, control := range controls {
		rect := control.rect
		rect.y0 += len(result)
		rect.y1 += len(result)
		addHit(rect, effortHit{tier: s.list.CursorID(), apply: !control.close, close: control.close})
	}
	result = append(result, footer...)
	return strings.Join(result, "\n"), regions
}

// effortRowText is the per-tier row label: the tier name plus a short hint for the
// sentinel so "auto" is self-explanatory in the list. ASCII-safe, layout-stable.
func effortRowText(tier string) string {
	if tier == effortAuto {
		return effortAuto + "    (provider default)"
	}
	return tier
}
