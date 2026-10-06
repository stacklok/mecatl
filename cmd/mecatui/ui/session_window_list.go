package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/renderfmt"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// windowFilters is the list's filter tab order: every row, then each status.
var windowFilters = []string{"", windowStatusNeedsApproval, windowStatusRunning, windowStatusIdle, windowStatusFailed}

const (
	// windowListDetailsMinWidth is the narrowest window that shows the session
	// details beside the table; narrower windows stack them below it.
	windowListDetailsMinWidth = 90
	windowListStatusWidth     = len(windowStatusNeedsApproval) + 2
	windowListUpdatedWidth    = 9
	windowListDetailLines     = 6
)

// windowActivity fingerprints what the Updated column tracks: the row status
// and the newest transcript card. Spinner ticks and redraws leave it unchanged.
type windowActivity struct {
	status   string
	cards    int
	lastID   scrollback.BlockID
	revision uint64
}

func windowActivityOf(m Model) windowActivity {
	a := windowActivity{status: m.windowRowStatus(), cards: m.conv.scrollback.Len()}
	if a.cards > 0 {
		meta := m.conv.scrollback.MetadataAt(a.cards - 1)
		a.lastID, a.revision = meta.ID, meta.Revision
	}
	return a
}

func (w window) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// visibleRows is listRows narrowed to the open list's filter.
func (w window) visibleRows() []windowRow {
	rows := w.listRows()
	if w.list == nil || w.list.filter == "" {
		return rows
	}
	out := rows[:0:0]
	for _, row := range rows {
		if row.status == w.list.filter {
			out = append(out, row)
		}
	}
	return out
}

// windowUpdated renders an Updated cell relative to now.
func windowUpdated(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// windowStatusStyle colours a status dot and its word.
func windowStatusStyle(m Model, status string) lipgloss.Style {
	th := m.deps.Theme
	switch status {
	case windowStatusNeedsApproval:
		return th.Style("warning")
	case windowStatusRunning:
		return th.Style("accent")
	case windowStatusFailed:
		return th.Style("errorText")
	}
	return th.Style("muted")
}

// renderList renders the full-screen session list: header, filter tabs, the
// session table beside the selected session's details, any notice or delete
// confirmation, and the key footer.
func (w window) renderList(m Model) string {
	th := m.deps.Theme
	width := m.widthOr()
	height := m.height
	if height <= 0 {
		height = 24
	}
	rows := w.listRows()
	visible := w.visibleRows()

	header := " " + th.Style("title").Render("sessions in this window")
	if project := w.projectLabel(); project != "" {
		header += "   " + th.Style("muted").Render("project: "+project)
	}
	rule := th.Style("muted").Render(strings.Repeat("─", width))
	top := []string{truncateDisplayWidth(header, width), w.renderFilterTabs(m, rows, width), rule}

	// Each bottom section (notice, delete confirmation, footer) follows a rule.
	bottom := []string{rule}
	if w.list.notice != "" {
		bottom = append(bottom, windowText(th.Style("warning"), " "+w.list.notice, width), rule)
	}
	if c := w.deleteConfirm; c != nil {
		bottom = append(bottom, w.renderDeleteConfirm(m, *c), rule)
	}
	bottom = append(bottom, w.renderListFooter(m, width))
	bottomText := strings.Join(bottom, "\n")

	bodyHeight := max(3, height-len(top)-lipgloss.Height(bottomText))
	body := w.renderListBody(m, visible, width, bodyHeight)
	return strings.Join(top, "\n") + "\n" + body + "\n" + bottomText
}

// projectLabel names the window's default checkout: the placement label of the
// first session that is not in a worktree.
func (w window) projectLabel() string {
	for _, s := range w.sessions {
		if p := s.model.activePlacement; p.Branch == "" && p.Label != "" {
			return terminaltext.SanitizeSingleLine(p.Label)
		}
	}
	return ""
}

func (w window) renderFilterTabs(m Model, rows []windowRow, width int) string {
	th := m.deps.Theme
	tabs := make([]string, 0, len(windowFilters))
	for _, filter := range windowFilters {
		count := 0
		for _, row := range rows {
			if filter == "" || row.status == filter {
				count++
			}
		}
		name := filter
		if name == "" {
			name = "All"
		}
		label := fmt.Sprintf("%s %d", name, count)
		if filter == w.list.filter {
			tabs = append(tabs, th.Style("accent").Bold(true).Underline(true).Render(label))
		} else {
			tabs = append(tabs, th.Style("muted").Render(label))
		}
	}
	line := " " + strings.Join(tabs, "   ")
	hint := th.Style("muted").Render(firstKey(w.keys.NextTab, "tab") + "/shift+tab filter")
	if gap := width - lipgloss.Width(line) - lipgloss.Width(hint) - 1; gap >= 3 {
		return line + strings.Repeat(" ", gap) + hint
	}
	return truncateDisplayWidth(line, width)
}

// renderListBody lays the table and the details out side by side, or stacked
// in a narrow window.
func (w window) renderListBody(m Model, visible []windowRow, width, height int) string {
	selected, ok := windowRow{}, false
	if w.list.cursor >= 0 && w.list.cursor < len(visible) {
		selected, ok = visible[w.list.cursor], true
	}
	if width >= windowListDetailsMinWidth {
		left := width * 60 / 100
		right := width - left - 3
		table := w.renderTable(m, visible, left, height)
		var details []string
		if ok {
			details = w.renderDetails(m, selected, right, height)
		}
		divider := m.deps.Theme.Style("muted").Render(" │ ")
		lines := make([]string, height)
		for i := range lines {
			cell := ""
			if i < len(table) {
				cell = table[i]
			}
			line := cell + strings.Repeat(" ", max(0, left-lipgloss.Width(cell))) + divider
			if i < len(details) {
				line += details[i]
			}
			lines[i] = line
		}
		return strings.Join(lines, "\n")
	}
	tableHeight := min(height, len(visible)+2)
	if !ok || height-tableHeight < 4 {
		tableHeight = height
	}
	lines := w.renderTable(m, visible, width, tableHeight)
	if ok && tableHeight < height {
		lines = append(lines, m.deps.Theme.Style("muted").Render(strings.Repeat("─", width)))
		for _, line := range w.renderDetails(m, selected, width-2, height-tableHeight-1) {
			lines = append(lines, " "+line)
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:height], "\n")
}

// renderTable renders the column header and the rows, scrolled to keep the
// selection visible.
func (w window) renderTable(m Model, visible []windowRow, width, height int) []string {
	th := m.deps.Theme
	muted := th.Style("muted")
	// marker (2) + dot and space (2) lead every row; the name column keeps a
	// one-cell gap before Status.
	nameWidth := max(8, width-4-windowListStatusWidth-windowListUpdatedWidth)
	pad := func(s string, n int) string {
		s = truncateDisplayWidth(s, n)
		return s + strings.Repeat(" ", max(0, n-lipgloss.Width(s)))
	}
	lines := []string{muted.Render("    " + pad("Sessions", nameWidth) + pad("Status", windowListStatusWidth) + "Updated")}
	if len(visible) == 0 {
		return append(lines, muted.Render("    no "+w.list.filter+" sessions"))
	}
	rowsShown := max(1, height-1)
	start := 0
	if w.list.cursor >= rowsShown {
		start = w.list.cursor - rowsShown + 1
	}
	now := w.clock()
	for i := start; i < len(visible) && i < start+rowsShown; i++ {
		row := visible[i]
		marker, nameStyle := "  ", th.Style("toolArgs")
		if i == w.list.cursor {
			marker, nameStyle = th.Style("accent").Render("› "), th.Style("accent").Bold(true)
		}
		updated := windowUpdated(w.sessions[w.index(row.key)].updatedAt, now)
		status := windowStatusStyle(m, row.status)
		lines = append(lines, marker+status.Render("●")+" "+
			nameStyle.Render(pad(windowRowName(row, row.key == w.activeKey, nameWidth-1), nameWidth))+
			status.Render(pad(row.status, windowListStatusWidth))+
			muted.Render(updated))
	}
	return lines
}

// windowRowName fits a row's title, worktree branch, and current marker into
// width, shortening the title first so the branch stays visible. When even a
// short title does not fit, the current marker goes before the branch does.
func windowRowName(row windowRow, current bool, width int) string {
	const minTitle = 6
	branch := ""
	if row.branch != "" {
		branch = " (" + row.branch + ")"
	}
	suffix := branch
	if current {
		suffix += " · current"
	}
	if lipgloss.Width(suffix) > width-minTitle {
		suffix = branch
	}
	room := width - lipgloss.Width(suffix)
	if room < minTitle {
		return truncateDisplayWidth(row.label+suffix, width)
	}
	return truncateDisplayWidth(row.label, room) + suffix
}

// renderDetails renders the selected session's details column.
func (w window) renderDetails(m Model, row windowRow, width, height int) []string {
	th := m.deps.Theme
	muted, text := th.Style("muted"), th.Style("toolArgs")
	s, _ := w.modelFor(row.key)
	var lines []string
	add := func(style lipgloss.Style, value string, limit int) {
		wrapped := strings.Split(wrapCardText(terminaltext.SanitizeSingleLine(value), width), "\n")
		if limit > 0 && len(wrapped) > limit {
			wrapped = append(wrapped[:limit-1], truncateDisplayWidth(wrapped[limit-1]+" …", width))
		}
		for _, line := range wrapped {
			lines = append(lines, style.Render(line))
		}
	}
	rule := muted.Render(strings.Repeat("─", width))
	lines = append(lines, muted.Render("Session details"))
	add(th.Style("askTitle"), row.label, 2)
	lines = append(lines, windowStatusStyle(m, row.status).Render("● "+row.status), rule)
	if last := s.windowLastText(scrollback.KindAssistant); last != "" {
		lines = append(lines, muted.Render("Last message"))
		add(text, last, windowListDetailLines)
		lines = append(lines, rule)
	}
	lines = append(lines, muted.Render("Project"))
	project := terminaltext.SanitizeSingleLine(s.activePlacement.Label)
	if row.branch != "" && row.branch != project {
		project = strings.TrimSpace(project + " · " + row.branch)
	}
	if project == "" {
		project = "—"
	}
	add(text, project, 1)
	if model := s.headerModelLabel(); model != "" {
		add(text, "Model: "+model, 1)
	}
	if s.usage.InputTokens > 0 || s.usage.OutputTokens > 0 {
		add(text, "Tokens: "+renderfmt.HumanizeTokens(s.usage.InputTokens)+" in · "+renderfmt.HumanizeTokens(s.usage.OutputTokens)+" out", 1)
	}
	if prompt := s.windowLastText(scrollback.KindUser); prompt != "" {
		lines = append(lines, rule, muted.Render("Prompt"))
		add(text, prompt, windowListDetailLines)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// windowLastText returns the newest user or assistant card text, flattened to
// one line for the details column.
func (m Model) windowLastText(kind scrollback.Kind) string {
	for i := m.conv.scrollback.Len() - 1; i >= 0; i-- {
		if m.conv.scrollback.MetadataAt(i).Kind != kind {
			continue
		}
		var text string
		switch p := m.conv.scrollback.SnapshotAt(i).Payload.(type) {
		case scrollback.UserCardSnapshot:
			text = p.Text
		case scrollback.AssistantCardSnapshot:
			text = p.Text
		}
		if text = strings.Join(strings.Fields(text), " "); text != "" {
			return text
		}
	}
	return ""
}

// renderListFooter lists the list's keys; the w action appears only when the
// server can create worktrees.
func (window) renderListFooter(m Model, width int) string {
	hk := m.helpKeyMarkings()
	parts := []string{"↑/↓ move", hk.choose + " switch", "n new session"}
	if m.caps.CreateWorktrees {
		parts = append(parts, "w new worktree session")
	}
	parts = append(parts, "d delete", "o saved sessions (/sessions)", hk.closeOnly+" close")
	muted := m.deps.Theme.Style("muted")
	rows := strings.Split(wrapCardText(strings.Join(parts, " · "), max(1, width-1)), "\n")
	for i, row := range rows {
		rows[i] = muted.Render(" " + row)
	}
	return strings.Join(rows, "\n")
}
