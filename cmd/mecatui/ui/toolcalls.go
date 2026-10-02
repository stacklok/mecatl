package ui

import (
	"encoding/json"
	"fmt"
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

const (
	toolEditName  = "Edit"
	toolWriteName = "Write"
	toolPathArg   = "path"
	toolURLArg    = "url"
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

type toolcallsState struct {
	open        bool
	deps        surfaceDeps
	entries     []toolcallEntry
	selected    int
	detail      bool
	detailEntry *toolcallDetail
	window      *bounded.Viewport
	width       int
	anchor      int
	lines       int
	follow      bool
	list        *bounded.List
	compact     bool
	hitItems    map[HitID]scrollback.BlockID // view cache: visible rows from the current Render frame
}

func (*toolcallsState) modalPlacement() modalPlacement { return modalPlacementFill }
func (*toolcallsState) Close()                         {}
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
			}
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

func toolcallArgumentLines(name, arguments string) []string {
	fields := make(map[string]json.RawMessage)
	if json.Unmarshal([]byte(arguments), &fields) != nil || fields == nil {
		return []string{"Original arguments: " + terminaltext.Sanitize(arguments)}
	}

	known := map[string][]string{
		"Read":             {toolPathArg, "offset", "limit"},
		"ListDir":          {toolPathArg, "depth"},
		"Glob":             {"pattern", toolPathArg},
		"Grep":             {"pattern", toolPathArg},
		toolEditName:       {toolPathArg, "old_string", "new_string"},
		toolWriteName:      {toolPathArg, "content"},
		"Copy":             {"source", "destination"},
		"Move":             {"source", "destination"},
		"Remove":           {toolPathArg},
		"Shell":            {"command"},
		"WebFetch":         {toolURLArg},
		"FetchMcpResource": {"uri"},
	}
	ordered := append([]string(nil), known[name]...)
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

	lines := make([]string, 0, len(ordered))
	for _, key := range ordered {
		if raw, ok := fields[key]; ok {
			lines = appendArgumentLines(lines, argumentLabel(key), raw, "")
		}
	}
	return lines
}

func appendArgumentLines(lines []string, label string, raw json.RawMessage, indent string) []string {
	value := strings.TrimSpace(string(raw))
	prefix := indent + terminaltext.SanitizeSingleLine(label) + ":"
	if strings.HasPrefix(value, "{") {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			if len(fields) == 0 {
				return append(lines, prefix+" (empty object)")
			}
			lines = append(lines, prefix)
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				lines = appendArgumentLines(lines, argumentLabel(key), fields[key], indent+"  ")
			}
			return lines
		}
	}
	if strings.HasPrefix(value, "[") {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			if len(items) == 0 {
				return append(lines, prefix+" (empty array)")
			}
			lines = append(lines, prefix)
			for i, item := range items {
				lines = appendArgumentLines(lines, fmt.Sprintf("[%d]", i), item, indent+"  ")
			}
			return lines
		}
	}
	return append(lines, prefix+" "+argumentValue(raw))
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
	value := func(key string) string { return argumentSummary(fields[key]) }
	switch name {
	case "Read":
		return "Read " + value(toolPathArg)
	case "ListDir":
		return "List " + value(toolPathArg)
	case "Glob":
		return "Find " + value("pattern")
	case "Grep":
		return "Search " + value("pattern")
	case toolEditName:
		return "Edit " + value(toolPathArg)
	case toolWriteName:
		return "Write " + value(toolPathArg)
	case "Copy", "Move":
		return name + " " + value("source") + " → " + value("destination")
	case "Remove":
		return "Remove " + value(toolPathArg)
	case "Shell":
		return "Run " + value("command")
	case "WebFetch":
		return "Fetch " + value(toolURLArg)
	case "FetchMcpResource":
		return "Fetch " + value("uri")
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

func (m Model) toolcallEntries() []toolcallEntry {
	entries := make([]toolcallEntry, 0)
	for i := 0; i < m.conv.scrollback.Len(); i++ {
		metadata, ok := m.conv.scrollback.ToolCallMetadataAt(i)
		if !ok {
			continue
		}
		entries = append(entries, toolcallEntry{
			blockID: metadata.ID, index: i,
			name:     ansi.Truncate(terminaltext.SanitizeSingleLine(metadata.Name), 120, "…"),
			intent:   ansi.Truncate(terminaltext.SanitizeSingleLine(toolcallIntentFor(metadata.Name, metadata.Arguments)), 120, "…"),
			resolved: metadata.Resolved,
			failed:   metadata.Failed || subagentStopErrored(metadata.Stop),
		})
	}
	return entries
}

// setEntries preserves an earlier reader's block identity while new calls arrive.
// A reader already following the newest row advances to the new newest row.
func (s *toolcallsState) setEntries(entries []toolcallEntry, opening bool) {
	var selected scrollback.BlockID
	following := !s.detail && (opening || (len(s.entries) > 0 && s.selected == len(s.entries)-1))
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
		s.setEntries(m.toolcallEntries(), false)
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
	if len(s.entries) == 0 {
		return strings.Join([]string{title, "", line(th.Style("muted"), "no tool calls in this session."), "", footer}, "\n"), nil
	}
	bodyHeight := height - 4
	if bodyHeight < 1 {
		s.compact, s.list = true, nil
		return line(th.Style("muted"), toolcallsTooSmallHint(width, s.deps.marks.closeOnly)), nil
	}
	if s.list == nil {
		s.list = new(bounded.List)
	}
	items := make([]bounded.ListItem, len(s.entries))
	for i, entry := range s.entries {
		marker, status := "…", "running"
		if entry.resolved {
			marker, status = "✓", statusDone
		}
		if entry.failed {
			marker, status = "✗", statusFailed
		}
		items[i] = bounded.ListItem{ID: fmt.Sprintf("%d", entry.blockID), Text: status + " · " + entry.name + " · " + entry.intent, StatusCells: [2]string{marker}}
	}
	s.list.SetGeometry(width, bodyHeight, 1, bounded.Clip)
	s.list.SetItems(items)
	s.list.SetCursor(s.selected)
	view := s.list.View()
	body := []string{title, ""}
	regions := make([]ClickableRegion, 0, len(view.Rows))
	s.hitItems = make(map[HitID]scrollback.BlockID, len(view.Rows))
	for _, row := range view.Rows {
		presentation := presentListRow(row, th.Style("accent"), th.Style("muted"))
		y := len(body)
		body = append(body, ansi.Cut(presentation.Style.Render(presentation.Text), 0, width)+"\x1b[0m")
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
	body = append(body, "", footer)
	return strings.Join(body, "\n"), regions
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
	content := toolcallDetailLines(entry)
	for i, row := range content {
		if strings.HasPrefix(row, "Identity · ") {
			style := s.deps.theme.Style("askTitle")
			if entry.failed {
				style = s.deps.theme.Style("errorText")
			}
			content[i] = style.Render(row)
		} else if row == "Error:" {
			content[i] = s.deps.theme.Style("errorText").Render(row)
		} else if row == "Arguments:" || row == "Result:" || row == "Result: pending" ||
			strings.HasPrefix(row, "Structured content · ") || row == "Resources" {
			content[i] = s.deps.theme.Style("accent").Render(row)
		}
	}
	return content
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
	for _, count := range toolcallRowCounts(toolcallDetailLines(*s.detailEntry), s.width) {
		if s.window.Offset() < start+count {
			s.anchor = (s.window.Offset() - start) * s.width
			return
		}
		start += count
	}
}

func toolcallDetailLines(entry toolcallDetail) []string {
	status := "running"
	if entry.resolved {
		status = statusDone
	}
	if entry.failed {
		status = statusFailed
	}

	lines := []string{"Identity · " + terminaltext.Sanitize(entry.name) + " · " + status, "Call: " + terminaltext.Sanitize(entry.callID), "Arguments:"}
	lines = append(lines, toolcallArgumentLines(entry.name, entry.intent)...)
	if !entry.resultReceived {
		return append(lines, "", "Result: pending")
	}

	result := entry.result
	if entry.failed {
		lines = append(lines, "", "Error:")
	} else {
		lines = append(lines, "", "Result:")
	}
	if result.Body != "" {
		lines = append(lines, toolcallResultBodyLines(entry.name, result.Body)...)
	}

	structured := result.StructuredContent
	var resources []string
	for _, a := range result.Artifacts {
		switch client.ContentBlockKind(a.Kind) {
		case client.ContentBlockText:
			if a.Text != "" && a.Text != result.Body {
				lines = append(lines, "Text:", terminaltext.Sanitize(a.Text))
			}
		case client.ContentBlockStructuredContent:
			// The typed block is canonical when a field mirror is also present.
			structured = a.Text
		case client.ContentBlockResourceLink:
			resources = append(resources, "Resource: "+terminaltext.Sanitize(a.Name)+" · "+terminaltext.Sanitize(a.URL))
		case client.ContentBlockEmbeddedResource:
			if len(a.Data) > 0 {
				resources = append(resources, "Embedded resource ("+terminaltext.Sanitize(a.MIMEType)+", binary content)")
			} else {
				resources = append(resources, "Embedded resource: "+terminaltext.Sanitize(a.Text))
			}
		case client.ContentBlockImage, client.ContentBlockAudio:
			resources = append(resources, a.Kind+" ("+terminaltext.Sanitize(a.MIMEType)+", media content)")
		}
	}
	if structured != "" {
		lines = append(lines, "Structured content · Structured JSON:", terminaltext.Sanitize(structured))
	}
	if len(resources) > 0 {
		lines = append(lines, "", "Resources")
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
	s.list.Move(map[bool]bounded.Move{true: bounded.LineUp, false: bounded.LineDown}[msg.Mouse().Button == tea.MouseWheelUp])
	s.selected = s.list.Cursor()
	return nil, true
}
