// Package ui models_surface.go owns the dynamic picker-local /models surface;
// durable catalog reconciliation and selection effects remain root-owned by Model.
package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// modelsView is the active /models overlay.
type modelsView int

const (
	modelsNone modelsView = iota
	modelsPanel

	// modelsNormalChromeWidth distinguishes the compact fallback, where all
	// chrome must fit, from ordinary card rendering, which measures natural text.
	modelsNormalChromeWidth = 80
)

// modelsState owns the dynamic /models picker. Durable catalog state remains on Model.
// While open, requestToken belongs to this surface: it accepts only its own list
// response so results from earlier picker instances cannot reach the root reducer.
type modelsState struct {
	view         modelsView
	requestToken uint64
	catalog      modelCatalog
	provenance   string
	loading      bool
	err          error
	filtered     []client.ModelInfo
	filter       textinput.Model
	list         *bounded.List
	rowBudget    int           // view cache, refreshed from Render geometry
	hitItems     map[HitID]int // view cache, replaced by every Render frame
	revealCursor bool
	deps         surfaceDeps
	intent       surfaceIntent
}

type modelsCatalogIntent struct{ msg client.ModelsMsg }

func (modelsCatalogIntent) isSurfaceIntent() {}

type modelsSelectIntent struct {
	selection client.ModelSelection
	label     string
}

func (modelsSelectIntent) isSurfaceIntent() {}

type modelsGlobalDefaultIntent struct {
	selection client.ModelSelection
	label     string
}

func (modelsGlobalDefaultIntent) isSurfaceIntent() {}

func (s *modelsState) Render(width, height int) (string, []ClickableRegion) {
	prefix, suffix := modelsFixedLines(*s, s.provenance)
	s.rowBudget = max(0, height-len(prefix)-len(suffix))
	list := s.listControl()
	reveal := s.syncList(width, s.rowBudget) || s.revealCursor
	view := list.ViewWithIndicators(s.rowBudget, reveal)
	s.revealCursor = false
	s.hitItems = make(map[HitID]int)

	lines := make([]string, 0, height)
	appendChrome := func(line string) {
		if len(lines) < max(0, height) {
			// Keep normal Models chrome intact so the surrounding card can retain its
			// historical natural width. Compact geometry still needs a hard bound.
			if width < modelsNormalChromeWidth {
				line = boundedDisplayLine(line, width)
			}
			lines = append(lines, line)
		}
	}
	appendRow := func(line string) {
		if len(lines) < max(0, height) {
			lines = append(lines, boundedDisplayLine(line, width))
		}
	}
	for _, line := range prefix {
		appendChrome(line)
	}
	regions := make([]ClickableRegion, 0, len(view.Rows))
	if view.Above > 0 {
		appendChrome(s.deps.theme.Style("muted").Render(fmt.Sprintf("↑ %d lines", view.Above)))
	}
	if len(view.Rows) > 0 {
		for _, row := range view.Rows {
			presentation := presentListRow(row, s.deps.theme.Style("spinner"), s.deps.theme.Style("muted"))
			y := len(lines)
			appendRow(presentation.Style.Render(presentation.Text))
			if y < len(lines) && s.deps.hits != nil {
				id := s.deps.hits.allocate()
				x1 := min(max(0, width), lipgloss.Width(lines[y]))
				if x1 > 0 {
					regions = append(regions, ClickableRegion{rect: cellRect{x0: 0, x1: x1, y0: y, y1: y + 1}, hit: id})
					s.hitItems[id] = row.ItemIndex
				}
			}
		}
	}
	if view.Below > 0 {
		appendChrome(s.deps.theme.Style("muted").Render(fmt.Sprintf("↓ %d lines", view.Below)))
	}
	for _, line := range suffix {
		appendChrome(line)
	}
	return strings.Join(lines, "\n"), regions
}

func (s *modelsState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		if s.filter.Value() != "" {
			s.filter.SetValue("")
			s.syncFilter()
			return nil, true, false
		}
		return nil, true, true
	case msg.String() == keyMenuUp:
		s.moveCursor(bounded.LineUp)
	case msg.String() == keyMenuDown:
		s.moveCursor(bounded.LineDown)
	case key.Matches(msg, s.deps.keys.ScrollU):
		s.moveCursor(bounded.PageUp)
	case key.Matches(msg, s.deps.keys.ScrollD):
		s.moveCursor(bounded.PageDown)
	case key.Matches(msg, s.deps.keys.ScrollTop):
		s.moveCursor(bounded.Top)
	case key.Matches(msg, s.deps.keys.ScrollBottom):
		s.moveCursor(bounded.End)
	case key.Matches(msg, s.deps.keys.SetGlobalDefault):
		if chosen, ok := s.chosen(); ok {
			s.intent = modelsGlobalDefaultIntent{client.ModelSelection{ProviderID: chosen.ProviderID, ModelID: chosen.ID}, modelLabel(chosen)}
		}
	case key.Matches(msg, s.deps.keys.Choose):
		if chosen, ok := s.chosen(); ok {
			s.intent = modelsSelectIntent{client.ModelSelection{ProviderID: chosen.ProviderID, ModelID: chosen.ID}, modelLabel(chosen)}
			return nil, true, true
		}
		return nil, true, false
	default:
		var cmd tea.Cmd
		s.filter, cmd = s.filter.Update(msg)
		s.syncFilter()
		return cmd, true, false
	}
	return nil, true, false
}

func (s *modelsState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	if hit, ok := msg.(surfaceHitMsg); ok {
		if s.view == modelsNone {
			return nil, false, false
		}
		index, current := s.hitItems[hit.ID]
		if !current {
			return nil, true, false
		}
		s.listControl().SetCursor(index)
		s.revealCursor = true
		return nil, true, false
	}
	result, ok := msg.(client.ModelsMsg)
	if !ok {
		return nil, false, false
	}
	// The open picker owns every catalog response. Its current request alone may
	// update the picker or hand a durable catalog intent back to Model; a result
	// from another picker instance must not fall through to root handling.
	if result.RequestToken != s.requestToken {
		return nil, true, false
	}
	s.loading, s.err = false, result.Err
	if result.Err != nil {
		s.catalog.statuses, s.catalog.configProvenanceProviderIDs = nil, nil
	} else {
		s.catalog.models, s.catalog.statuses = result.Models, result.Statuses
		s.catalog.configProvenanceProviderIDs = configProvenanceProviderSet(result.Statuses)
		s.syncFilter()
	}
	s.intent = modelsCatalogIntent{result}
	return nil, true, false
}

func (s *modelsState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	list := s.listControl()
	if msg.Mouse().Button == tea.MouseWheelUp {
		list.Scroll(bounded.LineUp)
	} else {
		list.Scroll(bounded.LineDown)
	}
	return nil, true
}
func (s *modelsState) Close() {
	s.view = modelsNone
	s.hitItems = nil
}
func (s *modelsState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}
func (s *modelsState) listControl() *bounded.List {
	if s.list == nil {
		s.list = new(bounded.List)
	}
	return s.list
}

func (s *modelsState) syncFilter() {
	s.filtered = filterModels(s.catalog.models, s.filter.Value())
	list := s.listControl()
	list.SetItems(modelsBoundedItems(s.catalog, s.filtered))
	if len(s.filtered) > 0 && list.CursorID() == "" {
		list.SetCursor(0)
	}
}

func (s *modelsState) syncList(width, height int) bool {
	list := s.listControl()
	hadCursor := list.CursorID() != ""
	list.SetGeometry(width, height, 3, bounded.Wrap)
	list.SetItems(modelsBoundedItems(s.catalog, s.filtered))
	reveal := len(s.filtered) > 0 && !hadCursor
	if !hadCursor {
		list.SetCursor(0)
	}
	return reveal
}

func (s *modelsState) moveCursor(move bounded.Move) {
	list := s.listControl()
	s.revealCursor = true
	if list.Valid() {
		list.Move(move)
		return
	}
	cursor, delta := list.Cursor(), max(1, list.Height())
	switch move {
	case bounded.LineUp:
		cursor = clampBounded(cursor-1, len(s.filtered))
	case bounded.LineDown:
		cursor = clampBounded(cursor+1, len(s.filtered))
	case bounded.PageUp:
		cursor = clampBounded(cursor-delta, len(s.filtered))
	case bounded.PageDown:
		cursor = clampBounded(cursor+delta, len(s.filtered))
	case bounded.Top:
		cursor = 0
	case bounded.End:
		cursor = clampBounded(len(s.filtered)-1, len(s.filtered))
	}
	list.SetCursor(cursor)
}

func modelsBoundedItems(catalog modelCatalog, models []client.ModelInfo) []bounded.ListItem {
	items := make([]bounded.ListItem, 0, len(models))
	for _, model := range models {
		items = append(items, bounded.ListItem{
			ID:          model.ProviderID + "\x00" + model.ID,
			Text:        modelRowText(catalog.active, catalog.globalDefault, catalog.configProvenanceProviderIDs, model),
			StatusCells: modelStatusCells(catalog.active, catalog.globalDefault, model),
		})
	}
	return items
}

func modelsFixedLines(picker modelsState, prov string) (prefix, suffix []string) {
	title := "Models"
	if len(picker.filtered) > 0 {
		title += "  (" + strconv.Itoa(len(picker.filtered)) + ")"
	}
	prefix = append(prefix, picker.deps.theme.Style("askTitle").Render(title))
	if prov != "" {
		prefix = append(prefix, picker.deps.theme.Style("muted").Render(prov))
	}
	prefix = append(prefix, picker.filter.View(), "", picker.deps.theme.Style("warning").Render(modelSwitchDisclosure), "")
	if picker.loading {
		prefix = append(prefix, picker.deps.theme.Style("muted").Render("loading…"))
	} else if picker.err != nil {
		prefix = append(prefix,
			picker.deps.theme.Style("errorText").Render("✗ list models: "+terminaltext.Sanitize(picker.err.Error())),
			picker.deps.theme.Style("muted").Render(modelsErrorHint),
		)
	} else if len(picker.catalog.models) == 0 {
		for _, line := range strings.Split(modelsEmptyCopy(picker.deps.caps, picker.catalog.statuses), "\n") {
			prefix = append(prefix, picker.deps.theme.Style("muted").Render(line))
		}
	} else if len(picker.filtered) == 0 {
		prefix = append(prefix, picker.deps.theme.Style("muted").Render("no models match "+strconv.Quote(picker.filter.Value())+" — "+picker.deps.marks.closeOnly+" to clear"))
	}
	for _, status := range renderProviderStatusLines(picker.catalog.statuses, len(picker.catalog.models) == 0) {
		suffix = append(suffix, picker.deps.theme.Style("errorText").Render(terminaltext.Sanitize(status)))
	}
	suffix = append(suffix, "",
		picker.deps.theme.Style("muted").Render("type to filter · ↑/↓/"+picker.deps.marks.scrollUp+" move · "+picker.deps.marks.choose+" use · "+picker.deps.marks.setGlobalDefault+" set global default · "+picker.deps.marks.closeOnly+" clear filter / close"),
		picker.deps.theme.Style("muted").Render("● current  ★ global default"),
		picker.deps.theme.Style("muted").Render("reason = emits reasoning · set its effort tier with /effort"))
	return prefix, suffix
}

func boundedDisplayLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(strings.Split(line, "\n")[0], width, "")
}
func (s *modelsState) chosen() (client.ModelInfo, bool) {
	cursor := s.listControl().Cursor()
	if cursor < 0 || cursor >= len(s.filtered) {
		return client.ModelInfo{}, false
	}
	return s.filtered[cursor], true
}

func filterModels(models []client.ModelInfo, q string) []client.ModelInfo {
	if q == "" {
		return models
	}
	lq := strings.ToLower(q)
	out := make([]client.ModelInfo, 0, len(models))
	for _, mi := range models {
		if strings.Contains(strings.ToLower(mi.ProviderID), lq) || strings.Contains(strings.ToLower(mi.ID), lq) || strings.Contains(strings.ToLower(mi.DisplayName), lq) {
			out = append(out, mi)
		}
	}
	return out
}

const modelSwitchDisclosure = "Switching models is expensive as it clears caches."

const modelsDisabledNote = "Model selection is not available on this server.\nConfigure a provider on the server, then reconnect."
const modelsErrorHint = "the model service may be unavailable — check mecated is running (log: $XDG_STATE_HOME/mecatl/mecatui.log)"
const modelsGatewayEmptyNote = "your gateway credential lists no models — ask your platform admin or re-run `thv llm setup`"

func modelsEmptyCopy(caps client.Capabilities, statuses []client.ProviderStatus) string {
	if s, ok := promotedStatus(statuses); ok {
		if s.State == "empty" {
			return modelsGatewayEmptyNote
		}
		return providerStatusLine(s)
	}
	if !caps.ModelSelection {
		return modelsDisabledNote
	}
	return "No selectable models advertised."
}
func promotedStatus(statuses []client.ProviderStatus) (client.ProviderStatus, bool) {
	for _, s := range statuses {
		if s.State != "" && s.State != "ok" {
			return s, true
		}
	}
	return client.ProviderStatus{}, false
}

var customProviderStatusCopy = map[string]string{
	"unreachable":  "model service not reachable — check provider configuration",
	"unauthorized": "model service rejected access — check provider access configuration",
	"empty":        "no selectable models",
}
var toolhiveStatusCopy = map[string]string{"unreachable": "gateway not reachable", "unauthorized": "gateway rejected the credential", "empty": "credential lists no models"}
var openAICodexStatusCopy = map[string]string{"unreachable": "ChatGPT Codex service not reachable", "unauthorized": "manual token rejected", "empty": "account lists no selectable models"}

func providerStatusLine(s client.ProviderStatus) string {
	copyByState := customProviderStatusCopy
	switch s.ProviderID {
	case "toolhive", "toolhive-anthropic":
		copyByState = toolhiveStatusCopy
	case "openai-codex":
		copyByState = openAICodexStatusCopy
	}
	clause := copyByState[s.State]
	if clause == "" {
		clause = s.State
	}
	line := s.ProviderID + ": " + clause
	if s.Hint != "" {
		line += " — " + s.Hint
	}
	return line
}
func renderProviderStatusLines(statuses []client.ProviderStatus, inventoryEmpty bool) []string {
	promoted, hasPromoted := promotedStatus(statuses)
	var lines []string
	for _, s := range statuses {
		if s.State == "" || s.State == "ok" || inventoryEmpty && hasPromoted && s == promoted {
			continue
		}
		lines = append(lines, providerStatusLine(s))
	}
	return lines
}

func modelStatusCells(active, globalDefault client.ModelSelection, mi client.ModelInfo) [2]string {
	cells := [2]string{}
	if active.Matches(mi) {
		cells[0] = "●"
	}
	if !globalDefault.IsZero() && globalDefault.Matches(mi) {
		cells[1] = "★"
	}
	return cells
}

func modelRowText(_ client.ModelSelection, _ client.ModelSelection, configProvenanceProviderIDs map[string]bool, mi client.ModelInfo) string {
	segs := modelCapSegments(mi)
	if configProvenanceProviderIDs != nil && configProvenanceProviderIDs[mi.ProviderID] {
		segs = append([]string{"org"}, segs...)
	}
	line := terminaltext.Sanitize(mi.ProviderID) + " · " + terminaltext.Sanitize(modelLabel(mi))
	if len(segs) > 0 {
		line += "  " + strings.Join(segs, " ")
	}
	return line
}
func modelLabel(mi client.ModelInfo) string {
	if mi.DisplayName != "" {
		return mi.DisplayName
	}
	return mi.ID
}
func modelCapSegments(mi client.ModelInfo) []string {
	var segs []string
	if mi.Image {
		segs = append(segs, "image input")
	}
	if mi.Reasoning {
		segs = append(segs, "reason")
	}
	if mi.ContextLimit > 0 {
		segs = append(segs, humanizeTokens(mi.ContextLimit))
	}
	// ADR 0346: mark a row mecatl sends no cache breakpoint for. The row stays
	// SELECTABLE, and marking rather than hiding was the explicit decision
	// recorded in the acceptance plan.
	//
	// Since decision 1 arms the breakpoint on every Responses endpoint, the only
	// way to see PromptCached=false is a server started with --no-prompt-cache.
	// That is worth surfacing HERE rather than leaving to the build-once posture
	// line: a connect-mode mecatui never passed that flag (it is embedded-server
	// only) and the posture line goes to the log file, not the screen, so this
	// marker is the sole in-screen signal that every Claude turn is re-paying
	// full input.
	//
	// Scoped to Anthropic-family ids because that is where PromptCached=false is
	// DECISIVE: Anthropic caches only on an explicit ask, so no breakpoint means
	// no cache. For every other vendor false merely means mecatl sends no hint,
	// and an implicit-caching upstream (OpenAI, Gemini, DeepSeek, Grok) may well
	// cache anyway, so marking those would be a false alarm.
	if !mi.PromptCached && modelLooksAnthropic(mi.ID) {
		segs = append(segs, "no-cache")
	}
	return segs
}

// anyUncachedModel reports whether any row in the catalog would carry the
// no-cache marker, gating its legend line (ADR 0346). It must apply the SAME
// predicate modelCapSegments does, or the legend and the markers disagree.
func anyUncachedModel(models []client.ModelInfo) bool {
	for _, mi := range models {
		if !mi.PromptCached && modelLooksAnthropic(mi.ID) {
			return true
		}
	}
	return false
}

// modelLooksAnthropic is a DISPLAY-ONLY test for an Anthropic-family model id,
// in either the namespaced ("anthropic/claude-...") or bare ("claude-...")
// form. It decides whether to render a warning glyph, never what the harness
// sends, so a renderer-local heuristic is the right altitude — capability and
// cache truth stay composition-computed and arrive on ModelInfo.
func modelLooksAnthropic(id string) bool {
	m := strings.ToLower(strings.TrimSpace(id))
	return strings.HasPrefix(m, "anthropic/") || strings.HasPrefix(m, "claude")
}
