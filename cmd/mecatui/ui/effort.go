package ui

import (
	"fmt"
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
// # Vocab + the auto sentinel
//
// The neutral picker enum (in order) is auto, low, medium, high, xhigh, max. "auto"
// means UNSET — operator/provider default — and is sent to the server as the EMPTY
// string (client.ModelSelection.ReasoningEffort == ""). The picker presents it as
// "auto" for readability but maps it to "" when building the selection, so the
// persisted state stays clean (an unset effort is absent from models.yaml, not the
// literal "auto"). effortValue does that mapping; effortLabel does the inverse for
// display. The server normalises/clamps anyway (e.g. openai "max" echoes "high"),
// and the LIVE display reads m.resolvedSessionModel.ReasoningEffort (the resolved value),
// so the picker only ever needs to send the operator's REQUEST.

// effortAuto is the picker label for the UNSET tier; it maps to the empty string on
// the wire and in persisted state (see the package header). Named once so the label
// list, effortValue, and effortLabel share one spelling.
const effortAuto = "auto"

const (
	effortMaxOuterWidth  = 128
	effortRowGutterWidth = 3
	effortWarning        = "this model has no reasoning support — a tier will be ignored"
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

// effortState owns the picker for one open lifetime; session effects stay on Model.
type effortState struct {
	deps        surfaceDeps
	list        bounded.List
	noReasoning bool
	intent      surfaceIntent
	compact     bool   // view cache: refreshed from every Render offer
	offer       [2]int // view cache: last Render geometry, for selection reveal on resize
	hitTargets  map[HitID]effortHit
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
	for _, tier := range effortTiers {
		width = max(width, effortRowGutterWidth+ansi.StringWidth(effortRowText(tier)))
	}
	for _, line := range s.footerLines(effortMaxOuterWidth - frame) {
		width = max(width, ansi.StringWidth(line))
	}
	if s.noReasoning {
		width = max(width, ansi.StringWidth(effortWarning))
	}
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
		for i, tier := range effortTiers {
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
		s.intent = effortSelectIntent{effort: effortValue(s.list.CursorID())}
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

// effortValue maps a picker label to the wire/state value: the auto sentinel becomes
// "" (unset), every other tier is itself. This is the SINGLE place the "auto"→""
// convention is applied when building a selection.
func effortValue(label string) string {
	if label == effortAuto {
		return ""
	}
	return label
}

// effortLabel is the inverse: the empty (unset) value displays as the auto sentinel,
// every other value is itself. Used to mark the current row and to find the cursor's
// initial position.
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
	s := &effortState{deps: (&m).surfaceDeps(), noReasoning: m.currentModelNoReasoning(), compact: true}
	items := make([]bounded.ListItem, len(effortTiers))
	for i, tier := range effortTiers {
		items[i] = bounded.ListItem{ID: tier, Text: effortRowText(tier)}
		if tier == effortLabel(m.resolvedSessionModel.ReasoningEffort) {
			items[i].StatusCells[0] = "●"
		}
	}
	s.list.SetItems(items)
	s.list.SetCursor(effortCursorFor(m.resolvedSessionModel.ReasoningEffort))
	m.modal = s
	m.prompt.Blur()
	return m, nil
}

// effortCursorFor returns the effortTiers index whose label matches the current
// (effective) effort value, or 0 (auto) when none matches (an unknown/unset effort
// lands on auto, the safe default).
func effortCursorFor(current string) int {
	want := effortLabel(current)
	for i, tier := range effortTiers {
		if tier == want {
			return i
		}
	}
	return 0
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
	if s.noReasoning {
		warning = skillsTextLines(th.Style("warning"), effortWarning, width)
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
		return effortAuto + "    (operator / provider default)"
	}
	return tier
}
