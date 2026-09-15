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

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// modelsView is the active /models overlay.
type modelsView int

const (
	modelsNone modelsView = iota
	modelsPanel
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
	cursor       int
	list         boundedList
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
	s.rowBudget = modelsRowBudgetFor(height, modelsPanelFixedRows(*s, s.provenance, s.deps.marks))
	s.rowBudget = min(s.rowBudget, max(0, height-len(prefix)-len(suffix)))
	reveal := s.syncList(width, s.rowBudget) || s.revealCursor
	view := boundedListViewWithIndicators(&s.list, s.rowBudget, reveal)
	s.revealCursor = false
	s.cursor = s.list.cursor
	s.hitItems = make(map[HitID]int)

	lines := make([]string, 0, height)
	appendLine := func(line string) {
		if len(lines) < max(0, height) {
			lines = append(lines, boundedDisplayLine(line, width))
		}
	}
	for _, line := range prefix {
		appendLine(line)
	}
	regions := make([]ClickableRegion, 0, len(view.rows))
	if view.above > 0 {
		appendLine(s.deps.theme.Style("muted").Render(fmt.Sprintf("↑ %d lines", view.above)))
	}
	if len(view.rows) > 0 {
		for _, row := range view.rows {
			marker := "  "
			if row.cursorMarker {
				marker = "▶ "
			}
			text := marker + row.text
			style := s.deps.theme.Style("muted")
			if row.selected {
				style = s.deps.theme.Style("spinner")
			}
			y := len(lines)
			appendLine(style.Render(text))
			if y < len(lines) && s.deps.hits != nil {
				id := s.deps.hits.allocate()
				x1 := min(max(0, width), lipgloss.Width(lines[y]))
				if x1 > 0 {
					regions = append(regions, ClickableRegion{rect: cellRect{x0: 0, x1: x1, y0: y, y1: y + 1}, hit: id})
					s.hitItems[id] = row.itemIndex
				}
			}
		}
	}
	if view.below > 0 {
		appendLine(s.deps.theme.Style("muted").Render(fmt.Sprintf("↓ %d lines", view.below)))
	}
	for _, line := range suffix {
		appendLine(line)
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
		s.moveCursor(boundedLineUp)
	case msg.String() == keyMenuDown:
		s.moveCursor(boundedLineDown)
	case key.Matches(msg, s.deps.keys.ScrollU):
		s.moveCursor(boundedPageUp)
	case key.Matches(msg, s.deps.keys.ScrollD):
		s.moveCursor(boundedPageDown)
	case key.Matches(msg, s.deps.keys.ScrollTop):
		s.moveCursor(boundedTop)
	case key.Matches(msg, s.deps.keys.ScrollBottom):
		s.moveCursor(boundedEnd)
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
		s.list.setCursor(index)
		s.cursor = s.list.cursor
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
	if msg.Mouse().Button == tea.MouseWheelUp {
		s.list.scroll(boundedLineUp)
	} else {
		s.list.scroll(boundedLineDown)
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
func (s *modelsState) syncFilter() {
	s.filtered = filterModels(s.catalog.models, s.filter.Value())
	s.cursor = clampModelsCursor(s.cursor, len(s.filtered))
	s.list.setItems(modelsBoundedItems(s.catalog, s.filtered))
	if len(s.filtered) > 0 && s.list.cursorID == "" {
		s.list.setCursor(s.cursor)
	}
	s.cursor = s.list.cursor
}

func (s *modelsState) syncList(width, height int) bool {
	hadCursor := s.list.cursorID != ""
	s.list.setGeometry(width, height, 2, boundedWrap)
	s.list.setItems(modelsBoundedItems(s.catalog, s.filtered))
	reveal := len(s.filtered) > 0 && (!hadCursor || s.list.cursor != s.cursor)
	if reveal {
		s.list.setCursor(s.cursor)
	}
	s.cursor = s.list.cursor
	return reveal
}

func (s *modelsState) moveCursor(move boundedMove) {
	s.revealCursor = true
	if s.list.viewport.valid() {
		s.list.move(move)
		s.cursor = s.list.cursor
		return
	}
	delta := 1
	if s.rowBudget > 0 {
		delta = s.rowBudget
	}
	switch move {
	case boundedLineUp:
		s.cursor = clampModelsCursor(s.cursor-1, len(s.filtered))
	case boundedLineDown:
		s.cursor = clampModelsCursor(s.cursor+1, len(s.filtered))
	case boundedPageUp:
		s.cursor = clampModelsCursor(s.cursor-delta, len(s.filtered))
	case boundedPageDown:
		s.cursor = clampModelsCursor(s.cursor+delta, len(s.filtered))
	case boundedTop:
		s.cursor = 0
	case boundedEnd:
		s.cursor = clampModelsCursor(len(s.filtered)-1, len(s.filtered))
	}
}

func modelsBoundedItems(catalog modelCatalog, models []client.ModelInfo) []boundedListItem {
	items := make([]boundedListItem, 0, len(models))
	for _, model := range models {
		items = append(items, boundedListItem{
			id:   model.ProviderID + "\x00" + model.ID,
			text: modelRowText(catalog.active, catalog.globalDefault, catalog.configProvenanceProviderIDs, model),
		})
	}
	return items
}

func boundedListViewWithIndicators(list *boundedList, capacity int, reveal bool) boundedListView {
	reserved := 0
	var view boundedListView
	for range 3 {
		list.viewport.height = max(0, capacity-reserved)
		if reveal {
			list.revealCursor(list.layout())
		}
		view = list.view()
		needed := 0
		if view.above > 0 {
			needed++
		}
		if view.below > 0 {
			needed++
		}
		if needed <= reserved {
			list.reveal = false
			return view
		}
		reserved = needed
	}
	list.reveal = false
	return view
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
			picker.deps.theme.Style("errorText").Render("✗ list models: "+sanitizeTerminal(picker.err.Error())),
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
		suffix = append(suffix, picker.deps.theme.Style("errorText").Render(sanitizeTerminal(status)))
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
	rows := boundedWidthLines(line, width, boundedClip)
	if len(rows) == 0 {
		return ""
	}
	return rows[0]
}
func (s *modelsState) chosen() (client.ModelInfo, bool) {
	if s.cursor < 0 || s.cursor >= len(s.filtered) {
		return client.ModelInfo{}, false
	}
	return s.filtered[s.cursor], true
}

func clampModelsCursor(c, n int) int {
	if n <= 0 || c < 0 {
		return 0
	}
	if c >= n {
		return n - 1
	}
	return c
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

func modelsRowBudgetFor(height, fixedRows int) int {
	return max(0, height-fixedRows)
}

// modelsPanelFixedRows derives the list's viewport budget from the same variable
// content rendered around it, including the switch disclosure and provider status.
func modelsPanelFixedRows(picker modelsState, prov string, hk helpKeys) int {
	var b strings.Builder
	b.WriteString("Models\n")
	if prov != "" {
		b.WriteString(prov + "\n")
	}
	b.WriteString(picker.filter.View() + "\n\n")
	b.WriteString(modelSwitchDisclosure + "\n\n")
	statuses := renderProviderStatusLines(picker.catalog.statuses, len(picker.catalog.models) == 0)
	if modelsRowsRendered(picker) && len(statuses) > 0 {
		b.WriteString("separator\n")
	}
	for range statuses {
		b.WriteString("status\n")
	}
	b.WriteString("row\n\n")
	b.WriteString("type to filter · ↑/↓/" + hk.scrollUp + " move · " + hk.choose + " use · " + hk.setGlobalDefault + " set global default · " + hk.closeOnly + " clear filter / close\n")
	b.WriteString("● current  ★ global default\n")
	b.WriteString("reason = emits reasoning · set its effort tier with /effort")
	return strings.Count(b.String(), "\n")
}

func modelsRowsRendered(picker modelsState) bool {
	return !picker.loading && picker.err == nil && len(picker.catalog.models) > 0 && len(picker.filtered) > 0
}

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

func modelRowText(active, globalDefault client.ModelSelection, configProvenanceProviderIDs map[string]bool, mi client.ModelInfo) string {
	activeMark := " "
	if active.Matches(mi) {
		activeMark = "●"
	}
	defMark := " "
	if !globalDefault.IsZero() && globalDefault.Matches(mi) {
		defMark = "★"
	}
	segs := modelCapSegments(mi)
	if configProvenanceProviderIDs != nil && configProvenanceProviderIDs[mi.ProviderID] {
		segs = append([]string{"org"}, segs...)
	}
	line := activeMark + defMark + " " + sanitizeTerminal(mi.ProviderID) + " · " + sanitizeTerminal(modelLabel(mi))
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
		segs = append(segs, "img")
	}
	if mi.Reasoning {
		segs = append(segs, "reason")
	}
	if mi.ContextLimit > 0 {
		segs = append(segs, humanizeTokens(mi.ContextLimit))
	}
	return segs
}
