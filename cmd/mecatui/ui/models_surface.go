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
	"github.com/stacklok/mecatl/cmd/mecatui/internal/renderfmt"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// modelsView is the active /models overlay.
type modelsView int

const (
	modelsNone modelsView = iota
	modelsPanel

	// modelsNormalChromeWidth distinguishes wrapped help in narrow offers from
	// ordinary card rendering, which keeps each help line together.
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
	rowBudget    int              // view cache, refreshed from Render geometry
	hitItems     map[HitID]string // view cache: collision-safe model ID, replaced by every Render frame
	hitControls  map[HitID]modelsControlHit
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

type modelsControlHit struct {
	selectModel      bool
	selected, filter string
}

type modelsControl struct {
	rect        cellRect
	selectModel bool
}

func (s *modelsState) canSelect() bool {
	_, ok := s.chosen()
	return ok && s.deps.keys.Choose.Enabled()
}

func (s *modelsState) selectModel() bool {
	if s.rowBudget == 0 || !s.canSelect() || s.intent != nil {
		return false
	}
	chosen, _ := s.chosen()
	s.intent = modelsSelectIntent{client.ModelSelection{ProviderID: chosen.ProviderID, ModelID: chosen.ID}, modelLabel(chosen)}
	return true
}

func (s *modelsState) closeOrClear() bool {
	if !s.deps.keys.Close.Enabled() {
		return false
	}
	if s.rowBudget > 0 && s.filter.Value() != "" {
		s.filter.SetValue("")
		s.syncFilter()
		return false
	}
	return true
}

func (s *modelsState) footerControls(width int) ([]string, []modelsControl) {
	back := footerCloseLabel
	if s.filter.Value() != "" {
		back = "Clear filter"
	}
	lines, rendered := renderFooterControls(s.deps.theme, width, []footerControlSpec{
		{hint: s.deps.marks.choose, label: "Use model", enabled: s.canSelect()},
		{hint: s.deps.marks.closeOnly, label: back, enabled: s.deps.keys.Close.Enabled()},
	})
	controls := make([]modelsControl, len(rendered))
	for i, control := range rendered {
		controls[i] = modelsControl{rect: control.rect, selectModel: control.index == 0}
	}
	return lines, controls
}

func (s *modelsState) modalMaxOuterWidth() int {
	frame := s.deps.theme.Style("askCard").GetHorizontalFrameSize()
	prefix, suffix := modelsFixedLines(*s)
	width := 0
	for _, line := range append(prefix, suffix...) {
		width = max(width, ansi.StringWidth(terminaltext.ExpandTabs(line, terminaltext.DefaultTabWidth)))
	}
	for _, model := range s.catalog.models {
		text := modelRowText(s.catalog.active, s.catalog.globalDefault, s.catalog.configProvenanceProviderIDs, model)
		for _, line := range strings.Split(text, "\n") {
			width = max(width, 4+ansi.StringWidth(line))
		}
	}
	return min(128, width+frame)
}

func (s *modelsState) Render(width, height int) (string, []ClickableRegion) {
	s.hitControls = nil
	list := s.listControl()
	if width <= 0 || height <= 0 {
		s.rowBudget = 0
		s.hitItems = nil
		return "", nil
	}
	prefix, suffix, controls := modelsFrameLines(*s, s.provenance, width, height)
	s.rowBudget = max(0, height-len(prefix)-len(suffix))
	if s.rowBudget == 0 || ((s.canSelect() || s.deps.keys.Close.Enabled()) && len(controls) == 0) {
		s.rowBudget = 0
		s.hitItems = nil
		if !s.deps.keys.Close.Enabled() {
			return boundedDisplayLine(s.deps.theme.Style("muted").Render("terminal too small"), width), nil
		}
		return renderSkillsCompact(s.deps.theme, s.deps.marks, width), nil
	}
	s.syncList(width, s.rowBudget)
	// Measure without moving the live browsing anchor; short lists stay content-sized.
	measurement := *list
	bodyRows := len(measurement.View().Rows)
	view := list.ViewWithIndicators(bodyRows, list.RevealPending())
	s.hitItems = make(map[HitID]string)

	return s.renderRows(width, len(prefix)+bodyRows+len(suffix), prefix, suffix, controls, view)
}

func (s *modelsState) renderRows(width, height int, prefix, suffix []string, controls []modelsControl, view bounded.ListView) (string, []ClickableRegion) {
	lines := make([]string, 0, height)
	appendChrome := func(line string) {
		if len(lines) < max(0, height) {
			lines = append(lines, boundedDisplayLine(line, width))
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
		appendChrome(s.deps.theme.Style("muted").Render(fmt.Sprintf("↑ %d items", view.Above)))
	}
	for _, row := range view.Rows {
		presentation := presentListRow(row, s.deps.theme.Style("spinner"), s.deps.theme.Style("muted"))
		y := len(lines)
		appendRow(presentation.Style.Render(presentation.Text))
		if y < len(lines) && s.deps.hits != nil {
			id := s.deps.hits.allocate()
			x1 := min(max(0, width), lipgloss.Width(lines[y]))
			if x1 > 0 {
				regions = append(regions, ClickableRegion{rect: cellRect{x0: 0, x1: x1, y0: y, y1: y + 1}, hit: id})
				s.hitItems[id] = row.ID
			}
		}
	}
	if view.Below > 0 {
		appendChrome(s.deps.theme.Style("muted").Render(fmt.Sprintf("↓ %d items", view.Below)))
	}
	for len(lines) < height-len(suffix) {
		lines = append(lines, "")
	}
	s.hitControls = make(map[HitID]modelsControlHit)
	for _, control := range controls {
		if s.deps.hits == nil {
			break
		}
		rect := control.rect
		rect.y0 += len(lines)
		rect.y1 += len(lines)
		id := s.deps.hits.allocate()
		chosen, _ := s.chosen()
		s.hitControls[id] = modelsControlHit{selectModel: control.selectModel, selected: modelIdentity(chosen), filter: s.filter.Value()}
		regions = append(regions, ClickableRegion{rect: rect, hit: id})
	}
	for _, line := range suffix {
		appendChrome(line)
	}
	return strings.Join(lines, "\n"), regions
}

func (s *modelsState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	s.hitItems, s.hitControls = nil, nil
	move, navigation := boundedFilterNavigationMove(msg, s.deps.keys)
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		return nil, true, s.closeOrClear()
	case s.rowBudget == 0:
		return nil, true, false
	case navigation:
		s.moveCursor(move)
	case key.Matches(msg, s.deps.keys.SetGlobalDefault):
		if chosen, ok := s.chosen(); ok {
			s.intent = modelsGlobalDefaultIntent{client.ModelSelection{ProviderID: chosen.ProviderID, ModelID: chosen.ID}, modelLabel(chosen)}
		}
	case key.Matches(msg, s.deps.keys.Choose):
		return nil, true, s.selectModel()
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
		if target, current := s.hitControls[hit.ID]; current {
			s.hitItems, s.hitControls = nil, nil
			chosen, _ := s.chosen()
			if s.rowBudget == 0 || target.selected != modelIdentity(chosen) || target.filter != s.filter.Value() {
				return nil, true, false
			}
			if target.selectModel {
				return nil, true, s.selectModel()
			}
			return nil, true, s.closeOrClear()
		}
		identity, current := s.hitItems[hit.ID]
		s.hitItems, s.hitControls = nil, nil
		if !current || s.rowBudget == 0 || s.loading || s.err != nil {
			return nil, true, false
		}
		list := s.listControl()
		list.SetItems(modelsBoundedItems(s.catalog, s.filtered))
		list.SelectID(identity)
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
	s.hitItems, s.hitControls = nil, nil
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
	s.hitItems, s.hitControls = nil, nil
	if s.rowBudget == 0 {
		return nil, true
	}
	if move, ok := boundedWheelMove(msg); ok {
		s.listControl().Scroll(move)
	}
	return nil, true
}
func (s *modelsState) Close() {
	s.view = modelsNone
	s.hitItems, s.hitControls = nil, nil
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
	s.hitItems, s.hitControls = nil, nil
	s.filtered = filterModels(s.catalog.models, s.filter.Value())
	list := s.listControl()
	list.SetItems(modelsBoundedItems(s.catalog, s.filtered))
	if len(s.filtered) > 0 && list.CursorID() == "" {
		list.SetCursor(0)
	}
}

func (s *modelsState) syncList(width, height int) {
	list := s.listControl()
	hadCursor := list.CursorID() != ""
	list.SetGeometry(width, height, 3, bounded.Wrap)
	list.SetItems(modelsBoundedItems(s.catalog, s.filtered))
	if !hadCursor {
		list.SetCursor(0)
	}
}

func (s *modelsState) moveCursor(move bounded.Move) {
	list := s.listControl()
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

func modelIdentity(model client.ModelInfo) string {
	return aggregateScopedID(model.ProviderID, model.ID)
}

func modelsBoundedItems(catalog modelCatalog, models []client.ModelInfo) []bounded.ListItem {
	items := make([]bounded.ListItem, 0, len(models))
	for _, model := range models {
		items = append(items, bounded.ListItem{
			ID:          modelIdentity(model),
			Text:        modelRowText(catalog.active, catalog.globalDefault, catalog.configProvenanceProviderIDs, model),
			StatusCells: modelStatusCells(catalog.active, catalog.globalDefault, model),
		})
	}
	return items
}

func modelsFixedLines(picker modelsState) (prefix, suffix []string) {
	prefix, suffix, _ = modelsFrameLines(picker, picker.provenance, modelsNormalChromeWidth, -1)
	return prefix, suffix
}

func modelsFrameLines(picker modelsState, prov string, width, height int) (prefix, suffix []string, controls []modelsControl) {
	prefix = modelsPrefix(picker, prov)
	if picker.err == nil {
		statuses := renderProviderStatusLines(picker.catalog.statuses, len(picker.catalog.models) == 0)
		if !picker.loading && len(picker.catalog.models) > 0 && len(picker.filtered) > 0 && len(statuses) > 0 {
			// Keep provider remediation visually separate from selectable model rows.
			suffix = append(suffix, "")
		}
		for _, status := range statuses {
			suffix = append(suffix, picker.deps.theme.Style("errorText").Render(terminaltext.SanitizeSingleLine(status)))
		}
	}
	suffix = append(suffix, "")
	buttons, controls := picker.footerControls(width)
	for i := range controls {
		controls[i].rect.y0 += len(suffix)
		controls[i].rect.y1 += len(suffix)
	}
	suffix = append(suffix, buttons...)
	navigation := "type to filter · ↑/↓/" + picker.deps.marks.scrollUp + " move"
	if !picker.loading && picker.err == nil && len(picker.filtered) > 0 && picker.deps.keys.SetGlobalDefault.Enabled() {
		navigation += " · " + picker.deps.marks.setGlobalDefault + " set global default"
	}
	help := []string{
		navigation,
		"● current  ★ global default",
		"reason = emits reasoning · set its effort tier with /effort",
	}
	if anyUncachedModel(picker.catalog.models) {
		help = append(help, "no-cache = no prompt-cache breakpoint sent; costly for Claude models")
	}
	for _, line := range help {
		lines := []string{picker.deps.theme.Style("muted").Render(line)}
		if width < modelsNormalChromeWidth {
			lines = skillsTextLines(picker.deps.theme.Style("muted"), line, width)
		}
		if height < 0 || height-len(prefix)-len(suffix)-len(lines) >= 3 {
			suffix = append(suffix, lines...)
		}
	}
	return prefix, suffix, controls
}

func modelsPrefix(picker modelsState, prov string) (prefix []string) {
	title := "Models"
	if len(picker.filtered) > 0 {
		title += "  (" + strconv.Itoa(len(picker.filtered)) + ")"
	}
	prefix = append(prefix, picker.deps.theme.Style("askTitle").Render(title))
	if prov != "" {
		prefix = append(prefix, picker.deps.theme.Style("muted").Render(terminaltext.SanitizeSingleLine(prov)))
	}
	prefix = append(prefix, picker.filter.View(), "", picker.deps.theme.Style("warning").Render(modelSwitchDisclosure), "")
	if picker.loading {
		return append(prefix, picker.deps.theme.Style("muted").Render("loading…"))
	}
	if picker.err != nil {
		return append(prefix,
			picker.deps.theme.Style("errorText").Render("✗ list models: "+terminaltext.SanitizeSingleLine(picker.err.Error())),
			picker.deps.theme.Style("muted").Render(modelsErrorHint),
		)
	}
	if len(picker.catalog.models) == 0 {
		for _, line := range strings.Split(terminaltext.Sanitize(modelsEmptyCopy(picker.deps.caps, picker.catalog.statuses)), "\n") {
			prefix = append(prefix, picker.deps.theme.Style("muted").Render(line))
		}
		return prefix
	}
	if len(picker.filtered) == 0 {
		prefix = append(prefix, picker.deps.theme.Style("muted").Render("no models match "+strconv.Quote(picker.filter.Value())))
	}
	return prefix
}

func boundedDisplayLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line, _, _ = strings.Cut(line, "\n")
	return ansi.Truncate(line, width, "")
}
func (s *modelsState) chosen() (client.ModelInfo, bool) {
	cursor := s.listControl().Cursor()
	if s.loading || s.err != nil || cursor < 0 || cursor >= len(s.filtered) {
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
	return terminaltext.ExpandTabs(line, terminaltext.DefaultTabWidth)
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
		segs = append(segs, renderfmt.HumanizeTokens(mi.ContextLimit))
	}
	// Mark a row mecatl sends no cache breakpoint for. The row stays
	// SELECTABLE; marking rather than hiding is deliberate.
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
// no-cache marker, gating its legend line. It must apply the SAME
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
