package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// workspaceState owns a single frozen observation, not a subscription to the run.
type workspaceState struct {
	deps      surfaceDeps
	sourceID  string
	identity  []workspaceField
	observed  []string
	openedAt  time.Time
	cancel    context.CancelFunc
	loading   bool
	closed    bool
	available bool
	snapshot  WorkspaceSnapshot
	err       error
	filter    textinput.Model
	viewport  bounded.Viewport
	compact   bool // view cache: recomputed before all input
	totalRows int  // view cache: laid-out inspector rows
}

type workspaceResultMsg struct {
	owner    *workspaceState
	sourceID string
	snapshot WorkspaceSnapshot
	err      error
}

type workspaceRowKind uint8

const (
	workspaceBlank workspaceRowKind = iota
	workspaceHeading
	workspaceFieldRow
	workspacePathRow
	workspaceNote
	workspaceError
	workspaceWarning
)

type workspaceField struct{ label, value string }

type workspaceRow struct {
	kind                workspaceRowKind
	text, label, suffix string
}

// openWorkspace is shared by client-only entry points, including during a run.
func (m Model) openWorkspace() (tea.Model, tea.Cmd) {
	if (m.phase != phaseIdle && m.phase != phaseRunning) || m.modal != nil ||
		m.team.view != teamNone || m.agentsInv.view != agentsInvNone ||
		m.sessionDetailsOpen || m.executionPicker.open {
		return m, nil
	}
	ti := textinput.New()
	ti.Placeholder = "filter paths…"
	ti.Focus()
	s := &workspaceState{
		deps: (&m).surfaceDeps(), sourceID: m.sessionID, openedAt: time.Now(),
		filter: ti, compact: true, available: m.deps.WorkspaceSnapshot != nil,
	}
	if m.sessionID != "" {
		s.identity = append(s.identity, workspaceField{"Session", m.sessionID})
	}
	// Connection identity is descriptive, never a URL or a proxy for locality.
	switch m.deps.ConnectionMode {
	case "embedded":
		s.identity = append(s.identity, workspaceField{"Connection", m.deps.ConnectionMode})
	case "connect":
		s.identity = append(s.identity, workspaceField{"Connection", "remote"})
	}
	p := m.activePlacement
	for _, field := range []workspaceField{
		{"Placement", p.Kind}, {"Label", p.Label}, {"Branch", p.Branch}, {"Revision", p.Revision},
	} {
		if field.value != "" {
			s.identity = append(s.identity, field)
		}
	}
	if appendix, ok := m.conv.scrollback.AppendixSnapshot(); ok {
		s.observed = appendix.Files
	}
	m.modal = s
	m.prompt.Blur()
	var load tea.Cmd
	if s.available {
		ctx, cancel := context.WithCancel(s.deps.ctx)
		s.cancel, s.loading = cancel, true
		inspect, sourceID := m.deps.WorkspaceSnapshot, s.sourceID
		load = func() tea.Msg {
			snapshot, err := inspect(ctx, sourceID)
			return workspaceResultMsg{owner: s, sourceID: sourceID, snapshot: snapshot, err: err}
		}
	}
	return m, tea.Batch(load, textinput.Blink)
}

func (*workspaceState) survivesRunCompletion() bool { return true }
func (*workspaceState) modalMaxOuterWidth() int     { return 128 }
func (s *workspaceState) modalFrame() bool          { return !s.compact }

func (s *workspaceState) Close() {
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.filter.Blur()
}

func (s *workspaceState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	if result, ok := msg.(workspaceResultMsg); ok {
		if s.closed || result.owner != s || result.sourceID != s.sourceID || !s.loading {
			return nil, true, false
		}
		s.loading, s.snapshot, s.err = false, result.snapshot, result.err
		if s.cancel != nil {
			s.cancel()
		}
		return nil, true, false
	}
	if paste, ok := msg.(tea.PasteMsg); ok {
		if !s.compact {
			s.filter.SetValue(s.filter.Value() + terminaltext.SanitizeSingleLine(paste.Content))
			s.viewport.Reset()
		}
		return nil, true, false
	}
	// Stream events and other root-owned messages must continue to reduce.
	return nil, false, false
}

func (s *workspaceState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if key.Matches(msg, s.deps.keys.Close, s.deps.keys.Workspace) || msg.Code == tea.KeyEscape {
		return nil, true, true
	}
	if s.compact {
		return nil, true, false
	}
	if move, ok := boundedFilterNavigationMove(msg, s.deps.keys); ok {
		s.viewport.Move(move, s.totalRows)
		return nil, true, false
	}
	before := s.filter.Value()
	var cmd tea.Cmd
	s.filter, cmd = s.filter.Update(msg)
	if s.filter.Value() != before {
		s.viewport.Reset()
	}
	return cmd, true, false
}

func (s *workspaceState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if !s.compact {
		if move, ok := boundedWheelMove(msg); ok {
			s.viewport.Move(move, s.totalRows)
		}
	}
	return nil, true
}

func (s *workspaceState) Render(width, height int) (string, []ClickableRegion) {
	s.compact = true
	if width <= 0 || height <= 0 {
		return "", nil
	}
	th := s.deps.theme
	prefix := skillsTextLines(th.Style("askTitle"), "Workspace snapshot", width)
	prefix = append(prefix, s.styledRows([]workspaceRow{{kind: workspaceHeading, text: "Session context"}}, width)...)
	for _, field := range s.identity {
		prefix = append(prefix, s.styledRows([]workspaceRow{{kind: workspaceFieldRow, label: field.label, text: field.value}}, width)...)
	}
	prefix = append(prefix, "")
	s.filter.SetWidth(max(1, width-2))
	prefix = append(prefix, ansi.Cut(s.filter.View(), 0, width))
	prefixRows := len(prefix)
	footer := skillsTextLines(th.Style("muted"), s.deps.marks.workspace+" / "+s.deps.marks.closeOnly+": close · reopen to resample", width)
	// Two indicator rows keep the viewport offer stable while scrolling.
	rows := height - len(prefix) - len(footer) - 2
	if width < 12 || rows < 1 {
		return renderSkillsCompact(th, s.deps.marks, width), nil
	}
	s.compact = false
	lines := s.styledRows(s.content(), width)
	s.totalRows = len(lines)
	s.viewport.SetGeometry(width, rows, 0, bounded.Clip)
	view := s.viewport.View(lines)
	above, below := "", ""
	if view.Above > 0 {
		above = fmt.Sprintf("↑ %d more", view.Above)
	}
	if view.Below > 0 {
		below = fmt.Sprintf("↓ %d more", view.Below)
	}
	prefix = append(prefix, ansi.Cut(th.Style("muted").Render(above), 0, width))
	prefix = append(prefix, view.Rows...)
	for len(prefix) < prefixRows+1+rows {
		prefix = append(prefix, "")
	}
	prefix = append(prefix, ansi.Cut(th.Style("muted").Render(below), 0, width))
	prefix = append(prefix, footer...)
	return strings.Join(prefix, "\n"), nil
}

func (s *workspaceState) content() []workspaceRow {
	lines := s.workspaceMetadataContent()
	matched := make([]bool, len(s.observed))
	query := strings.ToLower(s.filter.Value())
	if s.available && !s.loading && s.err == nil && s.snapshot.Git != nil {
		lines = append(lines, s.workspaceGitFilesContent(matched, query)...)
	}
	return append(lines, s.workspaceObservedPathsContent(matched, query)...)
}

func (s *workspaceState) workspaceMetadataContent() []workspaceRow {
	lines := []workspaceRow{{kind: workspaceHeading, text: "Git state"}}
	if s.snapshot.Directory != "" {
		lines = append(lines, workspaceRow{kind: workspaceFieldRow, label: "Directory", text: strconv.Quote(s.snapshot.Directory)})
	}
	git := s.snapshot.Git
	switch {
	case !s.available:
		lines = append(lines, workspaceRow{kind: workspaceNote, text: "Local inspection unavailable"})
	case s.loading:
		lines = append(lines, workspaceRow{kind: workspaceNote, text: "Inspecting local workspace…"})
	case s.err != nil:
		lines = append(lines, workspaceRow{kind: workspaceError, text: "Local inspection unavailable: " + s.err.Error()})
	case git == nil:
		lines = append(lines, workspaceRow{kind: workspaceNote, text: "Confirmed non-repository (no Git snapshot)"})
	default:
		if git.Root != s.snapshot.Directory {
			lines = append(lines, workspaceRow{kind: workspaceFieldRow, label: "Repository root", text: strconv.Quote(git.Root)})
		}
		branch := git.Branch
		if git.Detached {
			branch = "detached HEAD " + git.Head[:min(12, len(git.Head))]
		} else if git.Unborn {
			branch += " (unborn)"
		}
		lines = append(lines, workspaceRow{kind: workspaceFieldRow, label: "Git", text: branch})
		if git.Upstream != "" {
			lines = append(lines, workspaceRow{kind: workspaceFieldRow, label: "Upstream", text: git.Upstream})
		}
		if git.Divergence != nil {
			lines = append(lines, workspaceRow{kind: workspaceFieldRow, label: "Local refs", text: fmt.Sprintf("ahead %d · behind %d", git.Divergence.Ahead, git.Divergence.Behind)})
		}
		c := git.Counts
		lines = append(lines,
			workspaceRow{kind: workspaceFieldRow, label: "Changes", text: fmt.Sprintf("staged %d · unstaged %d · untracked %d · conflicted %d (counts overlap)", c.Staged, c.Unstaged, c.Untracked, c.Conflicted)},
			workspaceRow{kind: workspaceNote, text: "Submodule working trees not inspected"},
		)
	}
	if !s.snapshot.CapturedAt.IsZero() {
		lines = append(lines, workspaceRow{kind: workspaceFieldRow, label: "Inspection captured", text: s.snapshot.CapturedAt.Format(time.RFC3339)})
	}
	if s.available && !s.loading && s.err == nil && git != nil {
		lines = append(lines, workspaceRow{kind: workspaceWarning, text: "Non-atomic snapshot; may already be stale."})
	}
	return lines
}

func (s *workspaceState) workspaceGitFilesContent(matched []bool, query string) []workspaceRow {
	git := s.snapshot.Git
	lines := []workspaceRow{{kind: workspaceBlank}, {kind: workspaceHeading, text: "Git files"}, {kind: workspaceNote, text: "XY = index/worktree · [observed] = observed Edit/Write path"}}
	visible := 0
	for _, file := range git.Files {
		observed := false
		for i, path := range s.observed {
			if workspacePathMatches(s.snapshot.Directory, git.Root, path, file.Path) ||
				(file.OriginalPath != "" && workspacePathMatches(s.snapshot.Directory, git.Root, path, file.OriginalPath)) {
				matched[i], observed = true, true
			}
		}
		row := workspaceRow{kind: workspacePathRow, label: string([]byte{file.Index, file.Worktree}) + " ", text: strconv.Quote(file.Path)}
		if file.Untracked {
			row.label = "?? "
		}
		if file.OriginalPath != "" {
			row.text += " ← " + strconv.Quote(file.OriginalPath)
		}
		if file.Conflicted {
			row.suffix += " [conflicted]"
		}
		if observed {
			row.suffix += " [observed]"
		}
		if workspaceMatchesFilter(row.label+row.text+row.suffix, query) {
			lines = append(lines, row)
			visible++
		}
	}
	if len(git.Files) == 0 {
		lines = append(lines, workspaceRow{kind: workspaceNote, text: "No Git changes in inspected scope"})
	} else if visible == 0 {
		lines = append(lines, workspaceRow{kind: workspaceNote, text: "No Git files match filter"})
	}
	return lines
}

func (s *workspaceState) workspaceObservedPathsContent(matched []bool, query string) []workspaceRow {
	lines := []workspaceRow{
		{kind: workspaceBlank},
		{kind: workspaceHeading, text: "Observed Edit/Write intentions"},
		{kind: workspaceFieldRow, label: "Captured", text: s.openedAt.Format(time.RFC3339)},
		{kind: workspaceNote, text: "Announced intent, not success or ownership of a diff."},
		{kind: workspaceNote, text: "Retained by this client view; omits Shell, delegated changes and resumed history."},
	}
	compared := s.available && !s.loading && s.err == nil && s.snapshot.Git != nil
	if compared {
		lines = append(lines,
			workspaceRow{kind: workspaceHeading, text: "Unmatched observed paths"},
			workspaceRow{kind: workspaceNote, text: "May be clean, committed, removed, ignored or outside repository."},
		)
	} else {
		lines = append(lines, workspaceRow{kind: workspaceHeading, text: "Captured observed paths"})
	}
	visible := 0
	for i, path := range s.observed {
		if !matched[i] && workspaceMatchesFilter(path, query) {
			lines = append(lines, workspaceRow{kind: workspacePathRow, text: strconv.Quote(path)})
			visible++
		}
	}
	if visible == 0 {
		message := "No observed paths"
		if compared {
			message = "No unmatched observed paths"
		}
		lines = append(lines, workspaceRow{kind: workspaceNote, text: message})
	}
	return lines
}

func (s *workspaceState) styledRows(rows []workspaceRow, width int) []string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.kind == workspaceBlank {
			lines = append(lines, "")
			continue
		}
		text := terminaltext.ExpandTabs(terminaltext.Sanitize(row.text), terminaltext.DefaultTabWidth)
		var styled string
		switch row.kind {
		case workspaceHeading:
			styled = s.deps.theme.Style("toolName").Render(text)
		case workspaceFieldRow:
			label := terminaltext.ExpandTabs(terminaltext.Sanitize(row.label), terminaltext.DefaultTabWidth)
			styled = s.deps.theme.Style("toolName").Render(label+": ") + s.deps.theme.Style("viewport").Render(text)
		case workspacePathRow:
			label := terminaltext.ExpandTabs(terminaltext.Sanitize(row.label), terminaltext.DefaultTabWidth)
			suffix := terminaltext.ExpandTabs(terminaltext.Sanitize(row.suffix), terminaltext.DefaultTabWidth)
			styled = s.deps.theme.Style("toolArgs").Render(label) + s.deps.theme.Style("viewport").Bold(true).Render(text) + s.deps.theme.Style("toolArgs").Render(suffix)
		case workspaceNote:
			styled = s.deps.theme.Style("muted").Render(text)
		case workspaceError:
			styled = s.deps.theme.Style("errorText").Render(text)
		case workspaceWarning:
			styled = s.deps.theme.Style("warning").Render(text)
		}
		for _, line := range strings.Split(ansi.Hardwrap(styled, width, true), "\n") {
			lines = append(lines, line+"\x1b[0m")
		}
	}
	return lines
}

func workspaceMatchesFilter(text, query string) bool {
	return strings.Contains(strings.ToLower(terminaltext.Sanitize(text)), query)
}

// Only a resolved local directory and Git root permit lexical path comparison.
// Neither this join nor presentation opens files or resolves symlinks.
func workspacePathMatches(directory, root, observed, gitPath string) bool {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(root) {
		return false
	}
	if !filepath.IsAbs(observed) {
		observed = filepath.Join(directory, observed)
	}
	return filepath.Clean(observed) == filepath.Clean(filepath.Join(root, filepath.FromSlash(gitPath)))
}
