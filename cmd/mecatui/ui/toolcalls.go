package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// runToolcalls opens the current session's local tool-call projection. It has no
// server dependency, so it remains available while a run streams.
func (m Model) runToolcalls() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle && m.phase != phaseRunning {
		return m, nil
	}
	m.prompt.Blur()
	s := &toolcallsState{open: true, deps: (&m).surfaceDeps()}
	s.setEntries(m.toolcallEntries(), true)
	m.modal = s
	return m, nil
}

type toolcallEntry struct {
	blockID  scrollback.BlockID
	revision uint64
	index    int
	name     string
	intent   string
	resolved bool
	failed   bool
}

type toolcallDetail struct {
	callID, name, intent string
	result               scrollback.ToolResult
	resolved, failed     bool
	resultReceived       bool
}

type toolcallsDetailIntent struct {
	blockID scrollback.BlockID
}

func (toolcallsDetailIntent) isSurfaceIntent() {}

type toolcallsState struct {
	open        bool
	deps        surfaceDeps
	entries     []toolcallEntry
	selected    int
	detail      bool
	detailEntry *toolcallDetail
	intent      surfaceIntent
	window      *bounded.Viewport
	width       int
	anchor      int
	lines       int
	follow      bool
	list        *bounded.List
	listFollow  bool
	compact     bool
	hitItems    map[HitID]scrollback.BlockID // view cache: visible rows from the current Render frame
}

func (*toolcallsState) modalPlacement() modalPlacement { return modalPlacementFill }
func (*toolcallsState) Close()                         {}

func (s *toolcallsState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func (s *toolcallsState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	hit, ok := msg.(surfaceHitMsg)
	if !ok || !s.open || s.compact || s.detail {
		return nil, false, false
	}
	blockID, current := s.hitItems[hit.ID]
	if !current {
		return nil, true, false
	}
	for i, entry := range s.entries {
		if entry.blockID == blockID {
			s.selected = i
			if s.list != nil {
				s.list.SetCursor(i)
				s.listFollow = i == len(s.entries)-1
			}
			s.detail = true
			s.intent = toolcallsDetailIntent{blockID: s.entries[s.selected].blockID}
			s.window = new(bounded.Viewport)
			s.width = 0
			s.anchor = 0
			s.follow = false
			return nil, true, false
		}
	}
	return nil, true, false
}

func toolcallsTooSmallHint(width int, dismiss string) string {
	if width <= 0 {
		return ""
	}
	message := "too small · " + dismiss
	if ansi.StringWidth(message) <= width {
		return message
	}
	message = "small · " + dismiss
	if ansi.StringWidth(message) <= width {
		return message
	}
	if ansi.StringWidth(dismiss) <= width {
		return dismiss
	}
	return ansi.Truncate(dismiss, width, "")
}

type toolcallRowKind uint8

const (
	toolcallBody toolcallRowKind = iota
	toolcallIdentity
	toolcallHeading
	toolcallError
	toolcallArgument
	toolcallField
)

type toolcallDetailRow struct {
	text, label             string
	kind                    toolcallRowKind
	identityName            string
	statusGlyph, statusText string
	statusStyle             string
}

func toolcallStatus(resolved, failed bool) (glyph, text, style string) {
	switch {
	case failed:
		return "✗", statusFailed, "toolErr"
	case resolved:
		return "✓", statusDone, "toolOk"
	default:
		return "…", "running", "toolName"
	}
}

type toolcallPresentation struct {
	intentAction string
	intentKeys   []string
	argumentKeys []string
}

const toolSourceArg = "source"

var toolcallPresentations = map[string]toolcallPresentation{
	"Read":             {intentAction: "Read", intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg, "offset", "limit"}},
	"ListDir":          {intentAction: "List", intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg, "depth"}},
	"Glob":             {intentAction: "Find", intentKeys: []string{"pattern"}, argumentKeys: []string{"pattern", toolPathArg}},
	"Grep":             {intentAction: "Search", intentKeys: []string{"pattern"}, argumentKeys: []string{"pattern", toolPathArg}},
	toolEditName:       {intentAction: "Edit", intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg, "old_string", "new_string"}},
	toolWriteName:      {intentAction: "Write", intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg, "content"}},
	"Copy":             {intentAction: "Copy", intentKeys: []string{toolSourceArg, "destination"}, argumentKeys: []string{toolSourceArg, "destination"}},
	"Move":             {intentAction: "Move", intentKeys: []string{toolSourceArg, "destination"}, argumentKeys: []string{toolSourceArg, "destination"}},
	"Remove":           {intentAction: "Remove", intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg}},
	"Shell":            {intentAction: "Run", intentKeys: []string{"command"}, argumentKeys: []string{"command"}},
	"WebFetch":         {intentAction: "Fetch", intentKeys: []string{toolURLArg}, argumentKeys: []string{toolURLArg}},
	"FetchMcpResource": {intentAction: "Fetch", intentKeys: []string{"uri"}, argumentKeys: []string{"uri"}},
}

func toolcallArgumentLines(name, arguments string) []string {
	rows := toolcallArgumentRows(name, arguments)
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = row.text
	}
	return lines
}

func toolcallArgumentRows(name, arguments string) []toolcallDetailRow {
	var fields map[string]any
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	if decoder.Decode(&fields) != nil || fields == nil {
		return []toolcallDetailRow{{text: "Original arguments: " + terminaltext.Sanitize(arguments), label: "Original arguments:", kind: toolcallArgument}}
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return []toolcallDetailRow{{text: "Original arguments: " + terminaltext.Sanitize(arguments), label: "Original arguments:", kind: toolcallArgument}}
	}

	presentation := toolcallPresentations[name]
	ordered := append([]string(nil), presentation.argumentKeys...)
	seen := make(map[string]bool, len(ordered))
	for _, key := range ordered {
		seen[key] = true
	}
	var extra []string
	for key := range fields {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	ordered = append(ordered, extra...)

	lines := make([]toolcallDetailRow, 0, len(ordered))
	for _, key := range ordered {
		if value, ok := fields[key]; ok {
			lines = appendArgumentTree(lines, argumentLabel(key), value)
		}
	}
	return lines
}

// Decode once, then walk the decoded tree instead of reparsing each subtree.
// Cap indentation so deeply nested valid input cannot force quadratic output.
func appendArgumentTree(lines []toolcallDetailRow, label string, value any) []toolcallDetailRow {
	type item struct {
		label string
		value any
		depth int
	}
	stack := []item{{label: label, value: value}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		prefix := strings.Repeat("  ", min(current.depth, 16)) + terminaltext.SanitizeSingleLine(current.label) + ":"
		add := func(value string) {
			lines = append(lines, toolcallDetailRow{text: prefix + value, label: prefix, kind: toolcallArgument})
		}
		switch v := current.value.(type) {
		case map[string]any:
			if len(v) == 0 {
				add(" (empty object)")
				continue
			}
			add("")
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for i := len(keys) - 1; i >= 0; i-- {
				stack = append(stack, item{argumentLabel(keys[i]), v[keys[i]], current.depth + 1})
			}
		case []any:
			if len(v) == 0 {
				add(" (empty array)")
				continue
			}
			add("")
			for i := len(v) - 1; i >= 0; i-- {
				stack = append(stack, item{fmt.Sprintf("[%d]", i), v[i], current.depth + 1})
			}
		case string:
			add(" " + terminaltext.Sanitize(v))
		case json.Number:
			add(" " + terminaltext.Sanitize(string(v)))
		case bool:
			add(" " + fmt.Sprint(v))
		default:
			add(" null")
		}
	}
	return lines
}

func argumentSummary(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if strings.HasPrefix(value, "{") {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			return fmt.Sprintf("%d fields", len(fields))
		}
	}
	if strings.HasPrefix(value, "[") {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			return fmt.Sprintf("%d items", len(items))
		}
	}
	return argumentValue(raw)
}

func toolcallIntent(name string, fields map[string]json.RawMessage) string {
	if presentation, ok := toolcallPresentations[name]; ok {
		values := make([]string, len(presentation.intentKeys))
		for i, key := range presentation.intentKeys {
			values[i] = argumentSummary(fields[key])
		}
		return presentation.intentAction + " " + strings.Join(values, " → ")
	}
	for _, key := range []string{"target", toolPathArg, "uri", toolURLArg, "command", "query", "prompt", "task"} {
		if raw, ok := fields[key]; ok {
			return terminaltext.SanitizeSingleLine(name) + " " + argumentSummary(raw)
		}
	}
	return terminaltext.SanitizeSingleLine(name)
}

func argumentValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return "null"
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return terminaltext.Sanitize(value)
	}
	return terminaltext.Sanitize(string(raw))
}

func argumentLabel(key string) string {
	if key == "" {
		return "(empty key)"
	}
	labels := map[string]string{
		"uri": "URI", toolURLArg: "URL", "old_string": "Old string", "new_string": "New string",
	}
	if label, ok := labels[key]; ok {
		return label
	}
	_, size := utf8.DecodeRuneInString(key)
	return strings.ToUpper(key[:size]) + strings.ReplaceAll(key[size:], "_", " ")
}

func toolcallIntentFor(name, arguments string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &fields) != nil || fields == nil {
		return terminaltext.SanitizeSingleLine(name)
	}
	return toolcallIntent(name, fields)
}

func (m Model) toolcallEntries() []toolcallEntry { return m.toolcallEntriesSince(nil) }

func (m Model) toolcallEntriesSince(previous []toolcallEntry) []toolcallEntry {
	cached := make(map[scrollback.BlockID]toolcallEntry, len(previous))
	for _, entry := range previous {
		cached[entry.blockID] = entry
	}
	entries := make([]toolcallEntry, 0)
	for i := 0; i < m.conv.scrollback.Len(); i++ {
		metadata, ok := m.conv.scrollback.ToolCallMetadataAt(i)
		if !ok {
			continue
		}
		intent := cached[metadata.ID].intent
		if old, ok := cached[metadata.ID]; !ok || old.revision != metadata.Revision {
			intent = ansi.Truncate(terminaltext.SanitizeSingleLine(toolcallIntentFor(metadata.Name, metadata.Arguments)), 120, "…")
		}
		entries = append(entries, toolcallEntry{
			blockID: metadata.ID, revision: metadata.Revision, index: i,
			name:     ansi.Truncate(terminaltext.SanitizeSingleLine(metadata.Name), 120, "…"),
			intent:   intent,
			resolved: metadata.Resolved,
			failed:   metadata.Failed || subagentStopErrored(metadata.Stop),
		})
	}
	return entries
}

// setEntries preserves an earlier reader's block identity while new calls arrive.
// A reader already following the newest row advances to the new newest row.
func (s *toolcallsState) setEntries(entries []toolcallEntry, opening bool) {
	if opening {
		s.listFollow = true
	}
	var selected scrollback.BlockID
	following := !s.detail && (opening || (s.listFollow && len(s.entries) > 0 && s.selected == len(s.entries)-1))
	if !following && s.selected >= 0 && s.selected < len(s.entries) {
		selected = s.entries[s.selected].blockID
	}
	s.entries = entries
	if len(entries) == 0 {
		s.selected = 0
		return
	}
	if following {
		s.selected = len(entries) - 1
		return
	}
	for i := range entries {
		if entries[i].blockID == selected {
			s.selected = i
			return
		}
	}
	s.selected = min(s.selected, len(entries)-1)
}

func (m *Model) syncToolcalls() {
	if s, ok := m.modal.(*toolcallsState); ok {
		s.setEntries(m.toolcallEntriesSince(s.entries), false)
		if s.detail {
			s.refreshDetail(&m.conv.scrollback)
		}
	}
}

func (s *toolcallsState) refreshDetail(c *scrollback.Conversation) {
	entry := s.entries[s.selected]
	snapshot := c.SnapshotAt(entry.index)
	if snapshot.ID != entry.blockID {
		s.detailEntry = nil
		return
	}
	var call scrollback.ToolCall
	var result scrollback.ToolResult
	var received bool
	switch card := snapshot.Payload.(type) {
	case scrollback.ToolCardSnapshot:
		call, result, received = card.Call, card.Result, card.Resolved
	case scrollback.SubagentCardSnapshot:
		call, result, received = card.Call, card.Result, card.Resolved
	case scrollback.TeamCardSnapshot:
		call, result, received = card.Call, card.Result, card.Resolved
	}
	s.detailEntry = &toolcallDetail{callID: call.ID, name: call.Name, intent: call.Arguments, result: result,
		resolved: entry.resolved, failed: entry.failed, resultReceived: received}
}

func (s *toolcallsState) Render(width, height int) (string, []ClickableRegion) {
	s.compact = false
	s.hitItems = nil
	if !s.open || width <= 0 || height <= 0 {
		s.list = nil
		return "", nil
	}
	th := s.deps.theme
	line := func(style lipgloss.Style, text string) string {
		return ansi.Cut(style.Render(terminaltext.Sanitize(text)), 0, width) + "\x1b[0m"
	}
	if height < 5 || width < 12 {
		s.compact, s.list = true, nil
		return line(th.Style("muted"), toolcallsTooSmallHint(width, s.deps.marks.closeOnly)), nil
	}
	title := line(th.Style("askTitle"), "Tool calls")
	if s.detail {
		hint := s.deps.marks.navUp + "/" + s.deps.marks.navDown + " · " + s.deps.marks.scroll + " · " + s.deps.marks.jumpTopFull + "/" + s.deps.marks.jumpEndFull + " · " + s.deps.marks.closeOnly + " back"
		if ansi.StringWidth(hint) > width {
			s.compact, s.list = true, nil
			return line(th.Style("muted"), toolcallsTooSmallHint(width, s.deps.marks.closeOnly)), nil
		}
		return s.renderDetail(width, height, title, line), nil
	}
	footerText := s.deps.marks.navUp + "/" + s.deps.marks.navDown + " · " + s.deps.marks.scroll + " · " + s.deps.marks.choose + " detail · " + s.deps.marks.closeOnly + " close"
	if ansi.StringWidth(footerText) > width {
		s.compact, s.list = true, nil
		return line(th.Style("muted"), toolcallsTooSmallHint(width, s.deps.marks.closeOnly)), nil
	}
	footer := line(th.Style("muted"), footerText)
	bodyHeight := height - 4
	if bodyHeight < 1 {
		s.compact, s.list = true, nil
		return line(th.Style("muted"), toolcallsTooSmallHint(width, s.deps.marks.closeOnly)), nil
	}
	body := []string{title, ""}
	if len(s.entries) == 0 {
		return toolcallsPanel(append(body, line(th.Style("muted"), "no tool calls in this session.")), height, footer), nil
	}
	if s.list == nil {
		s.list = new(bounded.List)
	}
	items := make([]bounded.ListItem, len(s.entries))
	for i, entry := range s.entries {
		glyph, status, _ := toolcallStatus(entry.resolved, entry.failed)
		items[i] = bounded.ListItem{ID: fmt.Sprintf("%d", entry.blockID), Text: status + " · " + entry.name + " · " + entry.intent, StatusCells: [2]string{glyph}}
	}
	s.list.SetGeometry(width, bodyHeight, 2, bounded.Clip)
	s.list.SetItems(items)
	if s.list.CursorID() != items[s.selected].ID {
		s.list.SetCursor(s.selected)
	}
	view := s.list.ViewWithIndicators(bodyHeight, s.list.RevealPending())
	body, regions := s.renderListRows(body, view, width, line)
	return toolcallsPanel(body, height, footer), regions
}

func toolcallsPanel(body []string, height int, footer string) string {
	for len(body) < height-2 {
		body = append(body, "")
	}
	return strings.Join(append(body, "", footer), "\n")
}

func (s *toolcallsState) renderListRows(body []string, view bounded.ListView, width int, line func(lipgloss.Style, string) string) ([]string, []ClickableRegion) {
	regions := make([]ClickableRegion, 0, len(view.Rows))
	s.hitItems = make(map[HitID]scrollback.BlockID, len(view.Rows))
	if view.Above > 0 {
		body = append(body, line(s.deps.theme.Style("muted"), fmt.Sprintf("↑ %d items", view.Above)))
	}
	for _, row := range view.Rows {
		presentation := presentListRow(row, s.deps.theme.Style("toolName"), s.deps.theme.Style("toolArgs"))
		statusStyle := s.deps.theme.Style("toolName")
		if row.ItemIndex >= 0 && row.ItemIndex < len(s.entries) {
			_, _, slot := toolcallStatus(s.entries[row.ItemIndex].resolved, s.entries[row.ItemIndex].failed)
			statusStyle = s.deps.theme.Style(slot)
		}
		y := len(body)
		body = append(body, ansi.Cut(renderToolcallListRow(presentation, statusStyle), 0, width)+"\x1b[0m")
		if s.deps.hits == nil || row.ItemIndex < 0 || row.ItemIndex >= len(s.entries) {
			continue
		}
		id := s.deps.hits.allocate()
		x1 := min(max(0, width), lipgloss.Width(body[y]))
		if x1 > 0 {
			regions = append(regions, ClickableRegion{rect: cellRect{x0: 0, x1: x1, y0: y, y1: y + 1}, hit: id})
			s.hitItems[id] = s.entries[row.ItemIndex].blockID
		}
	}
	if view.Below > 0 {
		body = append(body, line(s.deps.theme.Style("muted"), fmt.Sprintf("↓ %d items", view.Below)))
	}
	return body, regions
}

// renderToolcallListRow keeps the row's cursor and selection styling while using
// the canonical semantic slot for its status glyph.
func renderToolcallListRow(row listRowPresentation, status lipgloss.Style) string {
	runes := []rune(row.Text)
	if len(runes) < 2 {
		return row.Style.Render(row.Text)
	}
	glyphStyle := row.Style.Foreground(status.GetForeground()).Bold(status.GetBold())
	return row.Style.Render(string(runes[:1])) + glyphStyle.Render(string(runes[1:2])) + row.Style.Render(string(runes[2:]))
}

func (s *toolcallsState) renderDetail(width, height int, title string, line func(lipgloss.Style, string) string) string {
	if s.detailEntry == nil {
		return strings.Join([]string{title, "", line(s.deps.theme.Style("muted"), "call unavailable · "+s.deps.marks.closeOnly+" back")}, "\n")
	}
	entry := *s.detailEntry
	if s.window == nil {
		s.window = new(bounded.Viewport)
		s.follow = true
	}
	header := []string{title, ""}
	footer := line(s.deps.theme.Style("muted"), s.deps.marks.navUp+"/"+s.deps.marks.navDown+" · "+s.deps.marks.scroll+" · "+s.deps.marks.jumpTopFull+"/"+s.deps.marks.jumpEndFull+" · "+s.deps.marks.closeOnly+" back")
	content := s.styledToolcallDetailLines(entry)
	if !s.follow && s.width > 0 && s.width != width {
		oldRows := toolcallRowCounts(content, s.width)
		newRows := toolcallRowCounts(content, width)
		oldStart, newStart, totalNew := 0, 0, 0
		for _, count := range newRows {
			totalNew += count
		}
		for i, count := range oldRows {
			if s.window.Offset() < oldStart+count {
				s.window.SetGeometry(width, height-len(header)-1, 0, bounded.Wrap)
				within := s.anchor / width
				s.window.SetOffset(newStart+min(within, newRows[i]-1), totalNew)
				break
			}
			oldStart += count
			newStart += newRows[i]
		}
	}
	s.width = width
	s.window.SetGeometry(width, height-len(header)-1, 0, bounded.Wrap)
	view := s.window.View(content)
	s.lines = view.Above + len(view.Rows) + view.Below
	if s.lines <= s.window.Height() {
		s.follow = true
	}
	if s.follow {
		s.window.Move(bounded.End, s.lines)
		view = s.window.View(content)
	}
	header = append(header, view.Rows...)
	for len(header) < height-1 {
		header = append(header, "")
	}
	return strings.Join(append(header, footer), "\n")
}

func (s *toolcallsState) styledToolcallDetailLines(entry toolcallDetail) []string {
	rows := toolcallDetailRows(entry)
	content := make([]string, len(rows))
	for i, row := range rows {
		text := terminaltext.Sanitize(row.text)
		switch row.kind {
		case toolcallIdentity:
			content[i] = s.styledToolcallIdentity(row)
		case toolcallHeading:
			content[i] = s.deps.theme.Style("toolName").Render(text)
		case toolcallError:
			content[i] = s.deps.theme.Style("errorText").Render(text)
		case toolcallArgument:
			label := "  " + terminaltext.Sanitize(row.label)
			value := strings.TrimPrefix(text, terminaltext.Sanitize(row.label))
			content[i] = s.deps.theme.Style("toolName").Render(label) +
				s.deps.theme.Style("toolArgs").Render(strings.ReplaceAll(value, "\n", "\n  "))
		case toolcallField:
			label := terminaltext.Sanitize(row.label)
			content[i] = s.deps.theme.Style("toolName").Render(label) +
				s.deps.theme.Style("toolArgs").Render(strings.TrimPrefix(text, label))
		case toolcallBody:
			// Body and typed result content retain their original left edge.
			content[i] = s.deps.theme.Style("toolArgs").Render(text)
		}
	}
	return content
}

func (s *toolcallsState) styledToolcallIdentity(row toolcallDetailRow) string {
	nameStyle := s.deps.theme.Style("toolName")
	statusStyle := s.deps.theme.Style(row.statusStyle)
	return nameStyle.Render("Identity · ") +
		statusStyle.Render(row.statusGlyph) +
		nameStyle.Render(" "+terminaltext.Sanitize(row.identityName)+" · ") +
		statusStyle.Render(row.statusText)
}

// toolcallRowCounts uses the same bounded wrapping policy as the detail window
// to remap its top logical line when a terminal resize reflows earlier text.
func toolcallRowCounts(lines []string, width int) []int {
	probe := new(bounded.Viewport)
	probe.SetGeometry(width, 1, 0, bounded.Wrap)
	var counts []int
	for _, line := range lines {
		for _, source := range strings.Split(line, "\n") {
			view := probe.View([]string{source})
			counts = append(counts, view.Above+len(view.Rows)+view.Below)
		}
	}
	return counts
}

func (s *toolcallsState) recordAnchor() {
	if s.width == 0 || s.detailEntry == nil {
		return
	}
	start := 0
	for _, count := range toolcallRowCounts(s.styledToolcallDetailLines(*s.detailEntry), s.width) {
		if s.window.Offset() < start+count {
			s.anchor = (s.window.Offset() - start) * s.width
			return
		}
		start += count
	}
}

func toolcallDetailLines(entry toolcallDetail) []string {
	rows := toolcallDetailRows(entry)
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = row.text
	}
	return lines
}

func toolcallDetailRows(entry toolcallDetail) []toolcallDetailRow {
	glyph, status, style := toolcallStatus(entry.resolved, entry.failed)

	lines := []toolcallDetailRow{
		{text: "Identity · " + glyph + " " + terminaltext.Sanitize(entry.name) + " · " + status, kind: toolcallIdentity, identityName: entry.name, statusGlyph: glyph, statusText: status, statusStyle: style},
		{text: "Call: " + terminaltext.Sanitize(entry.callID), label: "Call:", kind: toolcallField},
		{text: "Arguments:", kind: toolcallHeading},
	}
	lines = append(lines, toolcallArgumentRows(entry.name, entry.intent)...)
	if !entry.resultReceived {
		return append(lines, toolcallDetailRow{}, toolcallDetailRow{text: "Result: pending", kind: toolcallHeading})
	}

	result := entry.result
	lines = append(lines, toolcallDetailRow{})
	if entry.failed {
		lines = append(lines, toolcallDetailRow{text: "Error:", kind: toolcallError})
	} else {
		lines = append(lines, toolcallDetailRow{text: "Result:", kind: toolcallHeading})
	}
	if result.Body != "" {
		for _, text := range toolcallResultBodyLines(entry.name, result.Body) {
			for _, line := range strings.Split(text, "\n") {
				lines = append(lines, toolcallDetailRow{text: line})
			}
		}
	}

	structured := result.StructuredContent
	var resources []toolcallDetailRow
	for _, a := range result.Artifacts {
		switch client.ContentBlockKind(a.Kind) {
		case client.ContentBlockText:
			if a.Text != "" && a.Text != result.Body {
				lines = append(lines, toolcallDetailRow{text: "Text:", kind: toolcallHeading}, toolcallDetailRow{text: terminaltext.Sanitize(a.Text)})
			}
		case client.ContentBlockStructuredContent:
			// The typed block is canonical when a field mirror is also present.
			structured = a.Text
		case client.ContentBlockResourceLink:
			resources = append(resources, toolcallDetailRow{text: "Resource: " + terminaltext.Sanitize(a.Name) + " · " + terminaltext.Sanitize(a.URL), label: "Resource:", kind: toolcallField})
		case client.ContentBlockEmbeddedResource:
			if len(a.Data) > 0 {
				resources = append(resources, toolcallDetailRow{text: "Embedded resource (" + terminaltext.Sanitize(a.MIMEType) + ", binary content)"})
			} else {
				resources = append(resources, toolcallDetailRow{text: "Embedded resource: " + terminaltext.Sanitize(a.Text), label: "Embedded resource:", kind: toolcallField})
			}
		case client.ContentBlockImage, client.ContentBlockAudio:
			resources = append(resources, toolcallDetailRow{text: a.Kind + " (" + terminaltext.Sanitize(a.MIMEType) + ", media content)"})
		}
	}
	if structured != "" {
		lines = append(lines, toolcallDetailRow{text: "Structured content · Structured JSON:", kind: toolcallHeading}, toolcallDetailRow{text: terminaltext.Sanitize(structured)})
	}
	if len(resources) > 0 {
		lines = append(lines, toolcallDetailRow{}, toolcallDetailRow{text: "Resources", kind: toolcallHeading})
		lines = append(lines, resources...)
	}
	return lines
}

// toolcallResultBodyLines formats only Read's adapter-minted numbered rows for
// the inspector. It never changes the canonical scrollback result.
func toolcallResultBodyLines(name, body string) []string {
	if name != "Read" {
		return []string{terminaltext.Sanitize(body)}
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = terminaltext.Sanitize(readResultGutter(line))
	}
	return lines
}

func readResultGutter(line string) string {
	tab := strings.IndexByte(line, '\t')
	if tab < 6 {
		return line
	}
	prefix := line[:tab]
	digits := strings.TrimLeft(prefix, " ")
	if digits == "" || digits[0] == '0' || (len(prefix) > 6 && len(prefix) != len(digits)) {
		return line
	}
	for i := range digits {
		if digits[i] < '0' || digits[i] > '9' {
			return line
		}
	}
	return prefix + "  " + line[tab+1:]
}

func (s *toolcallsState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if bubbleskey.Matches(msg, s.deps.keys.Close) {
		if s.detail {
			s.detail = false
			s.detailEntry = nil
			return nil, true, false
		}
		return nil, true, true
	}
	if s.compact || len(s.entries) == 0 {
		return nil, true, false
	}
	move := bounded.LineDown
	switch {
	case bubbleskey.Matches(msg, s.deps.keys.Down):
	case bubbleskey.Matches(msg, s.deps.keys.Up):
		move = bounded.LineUp
	case bubbleskey.Matches(msg, s.deps.keys.ScrollD):
		move = bounded.PageDown
	case bubbleskey.Matches(msg, s.deps.keys.ScrollU):
		move = bounded.PageUp
	case bubbleskey.Matches(msg, s.deps.keys.ScrollBottom):
		move = bounded.End
	case bubbleskey.Matches(msg, s.deps.keys.ScrollTop):
		move = bounded.Top
	case bubbleskey.Matches(msg, s.deps.keys.Choose):
		if !s.detail {
			s.detail = true
			s.intent = toolcallsDetailIntent{blockID: s.entries[s.selected].blockID}
			s.window = new(bounded.Viewport)
			s.width = 0
			s.anchor = 0
			s.follow = false
		}
		return nil, true, false
	default:
		return nil, true, false
	}
	if s.detail {
		if s.window != nil {
			s.window.Move(move, s.lines)
			s.follow = s.window.Offset() >= max(0, s.lines-s.window.Height())
			if !s.follow {
				s.recordAnchor()
			}
		}
		return nil, true, false
	}
	if s.list != nil {
		s.list.Move(move)
		s.selected = s.list.Cursor()
		s.listFollow = s.selected == len(s.entries)-1
	}
	return nil, true, false
}

func (s *toolcallsState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if s.detail && s.window != nil {
		move := bounded.LineDown
		if msg.Mouse().Button == tea.MouseWheelUp {
			move = bounded.LineUp
		}
		s.window.Move(move, s.lines)
		s.follow = s.window.Offset() >= max(0, s.lines-s.window.Height())
		if !s.follow {
			s.recordAnchor()
		}
		return nil, true
	}
	if s.list == nil {
		return nil, true
	}
	s.list.Scroll(map[bool]bounded.Move{true: bounded.LineUp, false: bounded.LineDown}[msg.Mouse().Button == tea.MouseWheelUp])
	s.listFollow = s.selected == len(s.entries)-1 && s.list.View().Below == 0
	return nil, true
}
