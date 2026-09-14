// Package ui models_surface.go owns the dynamic picker-local /models surface;
// durable catalog reconciliation and selection effects remain root-owned by Model.
package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// modelsView is the active /models overlay.
type modelsView int

const (
	modelsNone modelsView = iota
	modelsPanel
	modelsMinRows = 3
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
	rowBudget    int // view cache, refreshed from Render geometry
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
	s.rowBudget = modelsRowBudgetFor(height, modelsPanelFixedRows(*s, s.provenance, s.deps.marks))
	return renderModelsPanel(s.deps.theme, s.catalog, *s, s.deps.caps, s.provenance, s.deps.marks, s.rowBudget, width), nil
}

func (s *modelsState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	budget := s.rowBudget
	if budget == 0 {
		budget = modelsMinRows
	}
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		if s.filter.Value() != "" {
			s.filter.SetValue("")
			s.syncFilter()
			return nil, true, false
		}
		return nil, true, true
	case msg.String() == keyMenuUp:
		s.cursor = clampModelsCursor(s.cursor-1, len(s.filtered))
	case msg.String() == keyMenuDown:
		s.cursor = clampModelsCursor(s.cursor+1, len(s.filtered))
	case key.Matches(msg, s.deps.keys.ScrollU):
		s.cursor = clampModelsCursor(s.cursor-budget, len(s.filtered))
	case key.Matches(msg, s.deps.keys.ScrollD):
		s.cursor = clampModelsCursor(s.cursor+budget, len(s.filtered))
	case key.Matches(msg, s.deps.keys.ScrollTop):
		s.cursor = 0
	case key.Matches(msg, s.deps.keys.ScrollBottom):
		s.cursor = clampModelsCursor(len(s.filtered)-1, len(s.filtered))
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

func (*modelsState) HandleWheel(tea.MouseWheelMsg) (tea.Cmd, bool) { return nil, true }
func (*modelsState) Close()                                        {}
func (s *modelsState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}
func (s *modelsState) syncFilter() {
	s.filtered = filterModels(s.catalog.models, s.filter.Value())
	s.cursor = clampModelsCursor(s.cursor, len(s.filtered))
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
	if b := height - fixedRows; b >= modelsMinRows {
		return b
	}
	return modelsMinRows
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
var toolhiveStatusCopy = map[string]string{"unreachable": "proxy not reachable", "unauthorized": "gateway rejected the credential", "empty": "credential lists no models"}
var openAICodexStatusCopy = map[string]string{"unreachable": "ChatGPT Codex service not reachable", "unauthorized": "manual token rejected", "empty": "account lists no selectable models"}

func providerStatusLine(s client.ProviderStatus) string {
	copyByState := customProviderStatusCopy
	switch s.ProviderID {
	case "toolhive":
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

func renderModelsPanel(th theme.Theme, catalog modelCatalog, picker modelsState, caps client.Capabilities, prov string, hk helpKeys, rowBudget int, widths ...int) string {
	width := 0
	if len(widths) > 0 {
		width = widths[0]
	}
	var b strings.Builder
	title := "Models"
	if !picker.loading && picker.err == nil && len(picker.filtered) > 0 {
		start, end := scrollWindow(picker.cursor, len(picker.filtered), rowBudget)
		title += "  " + modelsPositionLabel(start, end, len(picker.filtered))
	}
	b.WriteString(th.Style("askTitle").Render(title) + "\n")
	if prov != "" {
		b.WriteString(renderToolCardText(th.Style("muted"), prov, width) + "\n")
	}
	b.WriteString(picker.filter.View() + "\n\n")
	b.WriteString(th.Style("warning").Render(modelSwitchDisclosure) + "\n\n")
	switch {
	case picker.loading:
		b.WriteString(th.Style("muted").Render("loading…") + "\n")
	case picker.err != nil:
		b.WriteString(th.Style("errorText").Render("✗ list models: "+sanitizeTerminal(picker.err.Error())) + "\n")
		b.WriteString(th.Style("muted").Render(modelsErrorHint) + "\n")
	case len(catalog.models) == 0:
		b.WriteString(th.Style("muted").Render(modelsEmptyCopy(caps, catalog.statuses)) + "\n")
	case len(picker.filtered) == 0:
		b.WriteString(th.Style("muted").Render("no models match "+strconv.Quote(picker.filter.Value())+" — "+hk.closeOnly+" to clear") + "\n")
	default:
		start, end := scrollWindow(picker.cursor, len(picker.filtered), rowBudget)
		for i := start; i < end; i++ {
			mi := picker.filtered[i]
			b.WriteString(renderRow(th, modelRowText(catalog.active, catalog.globalDefault, catalog.configProvenanceProviderIDs, mi), i == picker.cursor, width) + "\n")
		}
	}
	if picker.err == nil {
		statuses := renderProviderStatusLines(catalog.statuses, len(catalog.models) == 0)
		if modelsRowsRendered(picker) && len(statuses) > 0 {
			b.WriteString("\n")
		}
		for _, line := range statuses {
			b.WriteString(renderToolCardText(th.Style("errorText"), sanitizeTerminal(line), width) + "\n")
		}
	}
	b.WriteString("\n" + th.Style("muted").Render("type to filter · ↑/↓/"+hk.scrollUp+" move · "+hk.choose+" use · "+hk.setGlobalDefault+" set global default · "+hk.closeOnly+" clear filter / close"))
	b.WriteString("\n" + th.Style("muted").Render("● current  ★ global default"))
	b.WriteString("\n" + th.Style("muted").Render("reason = emits reasoning · set its effort tier with /effort"))
	return b.String()
}

func modelsPositionLabel(start, end, total int) string {
	if end-start >= total {
		return "(" + strconv.Itoa(total) + ")"
	}
	return "(" + strconv.Itoa(start+1) + "–" + strconv.Itoa(end) + " of " + strconv.Itoa(total) + ")"
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
