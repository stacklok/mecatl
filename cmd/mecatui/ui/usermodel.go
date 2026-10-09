package ui

// usermodel.go owns the open-only, read-only saved-memory inspector. The Model
// retains only the request token across surface lifetimes.
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

type userModelView uint8

const (
	userModelPanel userModelView = iota
	userModelDetail
)

type userModelState struct {
	deps        surfaceDeps
	detailer    client.UserModelDetailer
	generation  uint64
	view        userModelView
	loading     bool
	err         error
	model       client.UserModel
	detail      *client.UserModelDetail
	requestKey  string
	list        *bounded.List
	viewport    *bounded.Viewport
	compact     bool     // view cache: refreshed from every Render offer
	listWidth   int      // view cache: preserves list anchors across model refreshes
	detailLines []string // view cache: wrapped again on every Render
}

func (m Model) openUserModel() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.UserModel == nil || !m.caps.UserModel || m.modal != nil {
		return m, nil
	}
	m.prompt.Blur()
	m.userModelRequestToken++
	s := &userModelState{deps: (&m).surfaceDeps(), generation: m.userModelRequestToken, view: userModelPanel, loading: true, list: &bounded.List{}, viewport: &bounded.Viewport{}}
	s.detailer, _ = m.deps.UserModel.(client.UserModelDetailer)
	m.modal = s
	return m, client.GetUserModelCmdTagged(m.deps.Ctx, m.deps.UserModel, s.generation)
}

func (*userModelState) modalMaxOuterWidth() int { return 128 }
func (s *userModelState) modalFrame() bool      { return !s.compact }
func (*userModelState) Close()                  {}

func (s *userModelState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if key.Matches(msg, s.deps.keys.Close) {
		if s.compact {
			return nil, true, true
		}
		if s.view == userModelDetail {
			s.generation++
			s.view = userModelPanel
			s.detail = nil
			s.requestKey = ""
			s.loading = false
			s.err = nil
			return nil, true, false
		}
		return nil, true, true
	}
	if s.compact {
		return nil, true, false
	}
	if s.view == userModelDetail {
		move, ok := userModelMove(msg, s.deps.keys)
		if ok && s.viewport != nil {
			s.viewport.Move(move, len(s.detailLines))
		}
		return nil, true, false
	}
	if move, ok := userModelMove(msg, s.deps.keys); ok && s.list != nil {
		if s.requestKey != "" {
			s.generation++
			s.requestKey = ""
			s.loading = false
		}
		s.list.Move(move)
		return nil, true, false
	}
	if key.Matches(msg, s.deps.keys.Choose) && !s.loading && s.list != nil && len(s.model.Entries) > 0 && s.detailer != nil {
		selected := s.list.CursorID()
		if selected == "" {
			return nil, true, false
		}
		s.generation++
		s.requestKey = selected
		s.loading = true
		s.err = nil
		return client.GetUserModelEntryCmdTagged(s.deps.ctx, s.detailer, selected, s.generation), true, false
	}
	return nil, true, false
}

func userModelMove(msg tea.KeyPressMsg, keys keyMap) (bounded.Move, bool) {
	switch {
	case key.Matches(msg, keys.Up):
		return bounded.LineUp, true
	case key.Matches(msg, keys.Down):
		return bounded.LineDown, true
	case key.Matches(msg, keys.ScrollU):
		return bounded.PageUp, true
	case key.Matches(msg, keys.ScrollD):
		return bounded.PageDown, true
	case key.Matches(msg, keys.ScrollTop):
		return bounded.Top, true
	case key.Matches(msg, keys.ScrollBottom):
		return bounded.End, true
	default:
		return 0, false
	}
}

func (s *userModelState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if s.compact {
		return nil, true
	}
	move := bounded.LineDown
	if msg.Button == tea.MouseWheelUp {
		move = bounded.LineUp
	} else if msg.Button != tea.MouseWheelDown {
		return nil, true
	}
	if s.view == userModelPanel && s.list != nil {
		s.list.Scroll(move)
	}
	if s.view == userModelDetail && s.viewport != nil {
		s.viewport.Move(move, len(s.detailLines))
	}
	return nil, true
}

func (s *userModelState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	switch um := msg.(type) {
	case client.UserModelMsg:
		if um.Generation != s.generation || s.requestKey != "" || s.view != userModelPanel {
			return nil, true, false
		}
		s.loading = false
		s.err = um.Err
		if um.Err == nil {
			s.setModel(um.UserModel)
		}
		return nil, true, false
	case client.UserModelDetailMsg:
		if um.Generation != s.generation || s.view != userModelPanel || um.RequestKey == "" || um.RequestKey != s.requestKey || s.list == nil || s.list.CursorID() != s.requestKey {
			return nil, true, false
		}
		s.loading = false
		s.err = um.Err
		if um.Err != nil {
			s.requestKey = ""
			return nil, true, false
		}
		s.setModel(um.UserModel)
		s.detail = um.UserModel.Detail
		if s.detail == nil || s.detail.Current.Key != um.RequestKey {
			s.detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: um.RequestKey}}
		}
		s.viewport.Reset()
		s.view = userModelDetail
		return nil, true, false
	default:
		return nil, false, false
	}
}

func (s *userModelState) setModel(model client.UserModel) {
	s.model = model
	s.setListItems(s.listWidth)
}

const (
	userModelDescriptionMarker = "\x1b[8m"
	userModelDescriptionRail   = "│ "
)

func (s *userModelState) setListItems(width int) bool {
	items := make([]bounded.ListItem, 0, len(s.model.Entries))
	contentWidth := width - 2 // one selection cell and its trailing padding cell
	railsFit := true
	for _, e := range s.model.Entries {
		text := terminaltext.Sanitize(e.Key)
		if e.Description != "" {
			description := terminaltext.Sanitize(e.Description)
			if railsFit {
				if lines, ok := userModelDescriptionLines(description, contentWidth); ok {
					text += "\n" + strings.Join(lines, "\n")
				} else {
					railsFit = false
				}
			}
			if !railsFit {
				text += "\n" + description
			}
		}
		items = append(items, bounded.ListItem{ID: e.Key, Text: text})
	}
	s.list.SetItems(items)
	return railsFit
}

func userModelDescriptionLines(description string, contentWidth int) ([]string, bool) {
	descriptionWidth := contentWidth - ansi.StringWidth(userModelDescriptionRail)
	if descriptionWidth < 1 {
		return nil, false
	}
	var lines []string
	for _, source := range strings.Split(description, "\n") {
		for _, line := range strings.Split(ansi.Hardwrap(source, descriptionWidth, true), "\n") {
			// Hardwrap leaves a grapheme that cannot fit on its own line intact.
			// Do not let bounded.List wrap the marked rail line in that case.
			if ansi.StringWidth(line) > descriptionWidth {
				return nil, false
			}
			lines = append(lines, userModelDescriptionMarker+userModelDescriptionRail+line)
		}
	}
	return lines, true
}

const userModelDisabledNote = "Saved memory is not enabled on this server.\nStart or connect to a server with memory enabled."

func userModelEmptyCopy(caps client.Capabilities) string {
	if !caps.UserModel {
		return userModelDisabledNote
	}
	return "No saved memory yet."
}

func (s *userModelState) contentRowsFit(width, height, titleLines, metaLines, footerLines int) (int, bool) {
	rows := height - titleLines - metaLines - footerLines
	if width < 5 || rows < 1 {
		return 0, false
	}
	if s.view == userModelPanel && !s.loading && s.err == nil && len(s.model.Entries) > 0 {
		if !s.setListItems(width) {
			return 0, false
		}
		s.listWidth = width
	}
	return rows, true
}

func (s *userModelState) Render(width, height int) (string, []ClickableRegion) {
	s.compact = true
	if width <= 0 || height <= 0 {
		return "", nil
	}
	th := s.deps.theme
	title := "Saved memory"
	footer := "↑/↓ select · enter inspect · the agent saves and updates these facts · " + s.deps.marks.closeOnly + " close"
	if s.view == userModelDetail {
		title = "User model detail"
		footer = s.deps.marks.scroll + " scroll · read-only · ask the agent to remove or restore this saved fact · " + s.deps.marks.closeOnly + " back"
	}
	titleLines := skillsTextLines(th.Style("askTitle"), title, width)
	footerLines := skillsTextLines(th.Style("muted"), footer, width)
	meta := []string(nil)
	if s.view == userModelPanel && !s.loading && s.err == nil && len(s.model.Entries) > 0 {
		meta = skillsTextLines(th.Style("muted"), renderUserModelMeta(s.model), width)
	}
	rows, fits := s.contentRowsFit(width, height, len(titleLines), len(meta), len(footerLines))
	if !fits {
		return renderSkillsCompact(th, s.deps.marks, width), nil
	}
	s.compact = false
	var body []string
	if s.view == userModelPanel {
		if s.loading || s.err != nil || len(s.model.Entries) == 0 {
			text := userModelEmptyCopy(s.deps.caps)
			style := th.Style("muted")
			if s.loading {
				text = "loading…"
			}
			if s.err != nil {
				text = "get user model: " + terminaltext.Sanitize(s.err.Error())
				style = th.Style("errorText")
			}
			v := s.viewport
			v.SetGeometry(width, rows, 0, bounded.Wrap)
			body = v.View([]string{style.Render(text)}).Rows
		} else {
			s.list.SetGeometry(width, rows, 1, bounded.Wrap)
			view := s.list.ViewWithIndicators(rows, s.list.RevealPending())
			if view.Above > 0 {
				body = append(body, th.Style("muted").Render(fmt.Sprintf("↑ %d more", view.Above)))
			}
			for _, row := range view.Rows {
				body = append(body, s.renderListRow(row))
			}
			if view.Below > 0 {
				body = append(body, th.Style("muted").Render(fmt.Sprintf("↓ %d more", view.Below)))
			}
		}
	} else {
		lines := s.detailContent(width)
		s.viewport.SetGeometry(width, rows, 0, bounded.Wrap)
		// Viewport wraps the caller's lines. Keep the physical line count in sync
		// with navigation even when a terminal resize reflows the content.
		s.detailLines = lines
		v := s.viewport.View(lines)
		body = v.Rows
	}
	result := append(titleLines, meta...)
	result = append(result, body...)
	result = append(result, footerLines...)
	return strings.Join(result, "\n"), nil
}

func (s *userModelState) renderListRow(row bounded.ListRow) string {
	p := presentListRow(row, s.deps.theme.Style("toolName"), s.deps.theme.Style("toolArgs"))
	if !strings.HasPrefix(row.Text, userModelDescriptionMarker) {
		return p.Style.Render(p.Text)
	}
	const gutterWidth = 2 // one selection cell and its trailing padding cell
	description := strings.TrimPrefix(row.Text, userModelDescriptionMarker+userModelDescriptionRail)
	return p.Style.Render(p.Text[:gutterWidth]) + s.deps.theme.Style("muted").Render(userModelDescriptionRail) + p.Style.Render(description)
}

func (s *userModelState) detailContent(width int) []string {
	th := s.deps.theme
	if s.loading {
		return []string{th.Style("muted").Render("loading…")}
	}
	if s.err != nil {
		return []string{th.Style("errorText").Render(terminaltext.Sanitize(s.err.Error()))}
	}
	if s.detail == nil {
		return nil
	}
	rev := s.detail.Current
	lines := []string{th.Style("toolName").Render(terminaltext.Sanitize(rev.Key))}
	for _, row := range []struct{ label, value string }{{"status", rev.Status}, {"version", rev.Version}, {"saved by", rev.Writer}, {"source", rev.Origin}, {"session", rev.SourceSessionID}, {"proposal", rev.SourceProposalID}, {"description", rev.Description}} {
		if row.value != "" {
			lines = append(lines, th.Style("muted").Render(row.label+": "+terminaltext.Sanitize(row.value)))
		}
	}
	if !rev.UpdatedAt.IsZero() {
		lines = append(lines, th.Style("muted").Render("updated: "+rev.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z")))
	}
	lines = append(lines, "", th.Style("toolArgs").Render(terminaltext.Sanitize(rev.Value)), "")
	if !s.detail.HistoryAvailable {
		lines = append(lines, th.Style("muted").Render("Earlier versions are unavailable on this server."))
	} else {
		lines = append(lines, th.Style("muted").Render(fmt.Sprintf("Earlier versions: %d", len(s.detail.History))))
		for _, prior := range s.detail.History {
			lines = append(lines, th.Style("toolArgs").Render(terminaltext.Sanitize(fmt.Sprintf("%s · %s · %s", prior.Version, prior.Status, prior.UpdatedAt.UTC().Format("2006-01-02")))))
		}
	}
	// Normalize to physical lines for navigation; the viewport still owns the
	// final fit and clamp when the card is resized.
	var physical []string
	for _, line := range lines {
		physical = append(physical, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	return physical
}

func renderUserModelMeta(um client.UserModel) string {
	segs := []string{plural(len(um.Entries), "fact"), fmt.Sprintf("%d bytes", um.SizeBytes)}
	if um.SHA256 != "" {
		short := um.SHA256
		if len(short) > 12 {
			short = short[:12]
		}
		segs = append(segs, "sha:"+terminaltext.Sanitize(short))
	}
	return strings.Join(segs, " · ")
}
