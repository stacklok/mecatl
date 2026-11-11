package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

const (
	mcpPickerFirstLast    = "first/last"
	mcpPickerActionNext   = "next"
	mcpPickerActionCancel = "cancel"
)

// Length-prefix the raw server so delimiters and display sanitization cannot alias identities.
func mcpItemID(server, name string) string { return fmt.Sprintf("%d:%s%s", len(server), server, name) }

func resourceLabel(r client.MCPResource) string {
	if r.Name != "" {
		return r.Name
	}
	return r.URI
}

func (s *mcpState) resourceItems() []bounded.ListItem {
	items := make([]bounded.ListItem, 0, len(s.resources))
	for _, r := range s.resources {
		items = append(items, bounded.ListItem{ID: mcpItemID(r.Server, r.URI), Text: connectDisplay(resourceLabel(r) + "  " + r.Server)})
	}
	return items
}

func (s *mcpState) promptItems() []bounded.ListItem {
	items := make([]bounded.ListItem, 0, len(s.prompts))
	for _, p := range s.prompts {
		marker := ""
		if hasRequiredArgs(p) {
			marker = "  (args)"
		}
		items = append(items, bounded.ListItem{ID: mcpItemID(p.Server, p.Name), Text: connectDisplay(p.Name + "  " + p.Server + marker)})
	}
	return items
}

func (s *mcpState) setResources(resources []client.MCPResource) {
	s.resources = nil
	seen := make(map[string]bool, len(resources))
	for _, r := range resources {
		id := mcpItemID(r.Server, r.URI)
		if !seen[id] {
			s.resources = append(s.resources, r)
			seen[id] = true
		}
	}
	s.resList.SetItems(s.resourceItems())
}

func (s *mcpState) setPrompts(prompts []client.MCPPrompt) {
	s.prompts = nil
	seen := make(map[string]bool, len(prompts))
	for _, p := range prompts {
		id := mcpItemID(p.Server, p.Name)
		if !seen[id] {
			s.prompts = append(s.prompts, p)
			seen[id] = true
		}
	}
	s.prList.SetItems(s.promptItems())
}

func (s *mcpState) pickerFooter(width int, available bool) []string {
	th, hk := s.deps.theme, s.deps.marks
	action, closeLabel := "select", "close"
	switch s.view {
	case mcpResources:
		action = "read"
	case mcpResourcePrev:
		action, closeLabel = "insert into prompt", "back"
	}
	actions := hk.closeOnly + " " + closeLabel
	if available && s.deps.keys.Choose.Enabled() {
		actions = hk.choose + " " + action + " · " + actions
	}
	lines := skillsTextLines(th.Style("muted"), connectDisplay(actions), width)
	var nav []string
	for _, pair := range []struct {
		up, down     key.Binding
		marks, label string
	}{
		{s.deps.keys.Up, s.deps.keys.Down, hk.navUp + "/" + hk.navDown, "move"},
		{s.deps.keys.ScrollU, s.deps.keys.ScrollD, hk.scroll, "page"},
		{s.deps.keys.ScrollTop, s.deps.keys.ScrollBottom, hk.jump, mcpPickerFirstLast},
	} {
		if !pair.up.Enabled() && !pair.down.Enabled() {
			continue
		}
		marks := pair.marks
		if s.view == mcpResourcePrev {
			switch pair.label {
			case "move":
				pair.label = "scroll"
			case mcpPickerFirstLast:
				pair.label = "top/end"
			}
		}
		if !pair.up.Enabled() {
			marks = firstKey(pair.down, "")
			switch pair.label {
			case mcpPickerFirstLast:
				pair.label = "last"
			case "top/end":
				pair.label = "end"
			}
		}
		if !pair.down.Enabled() {
			marks = firstKey(pair.up, "")
			switch pair.label {
			case mcpPickerFirstLast:
				pair.label = "first"
			case "top/end":
				pair.label = "top"
			}
		}
		nav = append(nav, marks+" "+pair.label)
	}
	if available {
		lines = append(lines, skillsTextLines(th.Style("muted"), connectDisplay(strings.Join(nav, " · ")), width)...)
	}
	return lines
}

func (s *mcpState) modalMaxOuterWidth() int {
	frame := s.deps.theme.Style("askCard").GetHorizontalFrameSize()
	// These legacy views are outside the picker migration. Preserve their natural card width.
	if s.view == mcpPanel {
		return lipgloss.Width(renderMCPPanel(s.deps.theme, *s, s.deps.caps, s.deps.marks)) + frame
	}
	if s.view == mcpPromptArgs || s.view == mcpResourcePrev {
		return 128
	}
	title, items := "MCP resources", s.resourceItems()
	if s.view == mcpPrompts {
		title, items = "MCP prompts", s.promptItems()
	}
	width := ansi.StringWidth(title)
	for _, line := range s.pickerFooter(128, true) {
		width = max(width, ansi.StringWidth(line))
	}
	for _, item := range items {
		for _, line := range strings.Split(item.Text, "\n") {
			width = max(width, 3+ansi.StringWidth(line))
		}
	}
	for _, line := range strings.Split(connectDisplay(s.errMsg), "\n") {
		width = max(width, ansi.StringWidth(line))
	}
	if len(items) == 0 {
		width = max(width, ansi.StringWidth(s.pickerEmptyNote()))
	}
	return min(128, width+frame)
}

func (s *mcpState) modalFrame() bool { return !s.compact }

func (s *mcpState) pickerEmptyNote() string {
	kind := "resources"
	if s.view == mcpPrompts {
		kind = "prompts"
	}
	return mcpEmptyCopy(s.deps.caps, "No "+kind+" advertised by the connected MCP servers.")
}

func (s *mcpState) pickerCompact(width int) string {
	s.compact = true
	label := "close"
	if s.view == mcpResourcePrev || s.view == mcpPromptArgs {
		label = "back"
	}
	return ansi.Cut(s.deps.theme.Style("muted").Render(connectDisplay(s.deps.marks.closeOnly+" "+label)), 0, width)
}

func (s *mcpState) pickerHit(rect cellRect, control int) {
	if s.deps.hits == nil || rect.x1 <= rect.x0 {
		return
	}
	id := s.deps.hits.allocate()
	s.hitControls[id] = control
	s.regions = append(s.regions, ClickableRegion{rect: rect, hit: id})
}

func (s *mcpState) pickerListBody(candidate *bounded.List, bodyRows, width, bodyY int, title string, prefix []string) ([]string, []string) {
	th := s.deps.theme
	var body []string
	view := candidate.ViewWithIndicators(bodyRows, candidate.RevealPending())
	if bodyRows == 1 && (view.Above > 0 || view.Below > 0) {
		// Keep the item and classified feedback visible with only one body row.
		indicator := fmt.Sprintf("↑ %d ↓ %d more", view.Above, view.Below)
		if s.errMsg == "" {
			prefix[len(prefix)-1] = ansi.Cut(th.Style("muted").Render(indicator), 0, width)
		} else {
			prefix[0] = ansi.Cut(th.Style("askTitle").Render(indicator+" · "+title), 0, width)
		}
	} else if view.Above > 0 {
		body = append(body, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↑ %d more", view.Above)), 0, width))
	}
	for _, row := range view.Rows {
		row.StatusCells[0] = "│"
		if row.ItemLine == 0 {
			row.StatusCells[0] = "┬"
		}
		p := presentListRow(row, th.Style("spinner"), th.Style("muted"))
		y := bodyY + len(body)
		s.pickerHit(cellRect{x0: 0, x1: min(width, ansi.StringWidth(p.Text)), y0: y, y1: y + 1}, row.ItemIndex)
		body = append(body, p.Style.Render(p.Text))
	}
	if bodyRows > 1 && view.Below > 0 {
		body = append(body, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↓ %d more", view.Below)), 0, width))
	}
	return body, prefix
}

func (s *mcpState) renderPickerList(width, height int) string {
	s.compact = true
	th := s.deps.theme
	title, list, items := "MCP resources", &s.resList, s.resourceItems()
	if s.view == mcpPrompts {
		title, list, items = "MCP prompts", &s.prList, s.promptItems()
	}
	prefix := skillsTextLines(th.Style("askTitle"), title, width)
	status := skillsTextLines(th.Style("muted"), "", width)
	if s.loading {
		status = skillsTextLines(th.Style("muted"), "loading…", width)
	}
	if s.errMsg != "" {
		status = skillsTextLines(th.Style("errorText"), connectDisplay("✗ "+s.errCls.String()+": "+s.errMsg), width)
	}
	prefix = append(prefix, status...)
	available := !s.loading && len(items) > 0
	footer := s.pickerFooter(width, available)
	// Loading and empty states must not change the reserved action/navigation geometry.
	footerRows := max(len(footer), len(s.pickerFooter(width, true)))
	footer = append(footer, make([]string, footerRows-len(footer))...)
	rows := height - len(prefix) - len(footer)
	spacing := 0
	if rows >= 5 {
		spacing = 2
		rows -= spacing
	}
	candidate := *list
	candidate.SetGeometry(width, rows, 2, bounded.Wrap)
	if !candidate.Valid() {
		return s.pickerCompact(width)
	}
	var body []string
	if len(items) == 0 && !s.loading && s.errMsg == "" {
		body = skillsTextLines(th.Style("muted"), connectDisplay(s.pickerEmptyNote()), width)
	}
	if len(body) > rows {
		return s.pickerCompact(width)
	}
	natural := 0
	for _, item := range items {
		natural += len(skillsTextLines(th.Style("muted"), item.Text, max(1, width-3)))
	}
	bodyRows := min(rows, max(1, max(natural, len(body))))
	if available {
		listBody, updatedPrefix := s.pickerListBody(&candidate, bodyRows, width, len(prefix)+spacing/2, title, prefix)
		body = append(body, listBody...)
		prefix = updatedPrefix
	}
	for len(body) < bodyRows {
		body = append(body, "")
	}
	*list = candidate
	s.compact = false
	lines := prefix
	if spacing > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, body...)
	if spacing > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}

func (s *mcpState) renderResourcePreview(width, height int) string {
	s.compact = true
	th := s.deps.theme
	prefix := skillsTextLines(th.Style("askTitle"), "resource preview", width)
	if width < 9 {
		return s.pickerCompact(width)
	}
	for _, field := range []struct{ label, value string }{
		{"Name:   ", resourceLabel(s.previewResource)},
		{"Server: ", s.previewResource.Server},
		{"URI:    ", s.previewResource.URI},
	} {
		for i, line := range skillsTextLines(th.Style("text"), connectDisplay(field.value), width-8) {
			label := "        "
			if i == 0 {
				label = field.label
			}
			prefix = append(prefix, th.Style("text").Bold(true).Render(label)+line)
		}
	}
	// Reserve a status line for physical-line overflow so scrolling never resizes the card.
	statusRow := len(prefix)
	prefix = append(prefix, "", ansi.Cut(th.Style("muted").Render("── Content ──"), 0, width))
	footer := s.pickerFooter(width, s.previewLoaded && !s.loading)
	rows := height - len(prefix) - len(footer)
	spacing := 0
	if rows >= 5 {
		spacing = 2
		rows -= spacing
	}
	if width < 1 || rows < 1 {
		return s.pickerCompact(width)
	}
	if s.previewWidth != width {
		s.previewRows = strings.Split(ansi.Hardwrap(s.previewDisplay, width, true), "\n")
		for i := range s.previewRows {
			s.previewRows[i] = ansi.Cut(s.previewRows[i], 0, width)
		}
		s.previewWidth = width
	}
	rows = min(rows, max(1, len(s.previewRows)))
	s.previewViewport.SetGeometry(width, rows, 0, bounded.Clip)
	view := s.previewViewport.View(s.previewRows)
	prefix[statusRow] = ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↑ %d · ↓ %d lines", view.Above, view.Below)), 0, width)
	lines := prefix
	if spacing > 0 {
		lines = append(lines, "")
	}
	for _, row := range view.Rows {
		lines = append(lines, th.Style("text").Render(row))
	}
	if spacing > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	s.compact = false
	return strings.Join(lines, "\n")
}
