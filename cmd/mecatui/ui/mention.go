package ui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// maxMentionRows caps the menu body at eight physical rows, including up to two
// overflow indicators.
const maxMentionRows = 8

// maxMentionCandidates bounds the sorted matching paths retained by a scan.
const maxMentionCandidates = 64

// maxMentionWalk bounds how many directory entries syncMention's bounded walk
// visits before it stops descending, so opening the menu in a huge workspace tree
// never blocks the update goroutine. The walk is breadth-ish (it short-circuits
// once it has visited this many entries); the cap is generous enough to surface a
// normal project's files but firm enough to stay snappy.
const maxMentionWalk = 4000

// mentionState holds the @-mention file-completion menu's state on the Model. It
// mirrors paletteState (value-embedded, an inline dropdown over the input, NOT a
// phase/overlay): normal typing flows to the textarea and the menu merely reacts
// to the @-token the input now contains. It is mutually exclusive with the slash
// palette — a line is EITHER a "/command" (palette) or has an "@token" word
// (mention), never both drive completion at once (commandPrefix is single-token
// "/…"; an @token requires a non-"/" line).
type mentionState struct {
	// open reports whether the menu is showing. True only while the input has an
	// @-token at the trailing word, the user has not dismissed it with esc, and at
	// least one workspace file matches.
	open bool
	// dismissed latches an esc dismissal so the menu stays closed until the @-token
	// changes/leaves (mirrors paletteState.dismissed).
	dismissed bool

	// matches is the filtered list of workspace-relative file paths matching the
	// current @-token (recomputed on each input change, capped).
	matches []string
	// list is pointer-owned so Bubble Tea Model copies retain one stable selection
	// and viewport anchor. Raw displayed paths are IDs and completion values.
	list *bounded.List
}

func (st *mentionState) syncList() {
	if st.list == nil {
		st.list = new(bounded.List)
	}
	items := make([]bounded.ListItem, 0, len(st.matches))
	for _, path := range st.matches {
		items = append(items, bounded.ListItem{ID: path, Text: "@" + terminaltext.SanitizeSingleLine(path)})
	}
	st.list.SetItems(items)
}

func (st mentionState) selected() string {
	if st.list == nil {
		return ""
	}
	index := st.list.Cursor()
	if index < 0 || index >= len(st.matches) {
		return ""
	}
	return st.matches[index]
}

// mentionToken reports whether s (the single-line input) has an @-mention token
// being typed at the trailing word and, if so, returns the token text after the
// "@" (which may be empty for a bare "@"). The token is the last whitespace-
// delimited word, and it qualifies only when it STARTS with "@" — so "describe
// @img" yields "img", a bare "@" yields "", and "hello" or "a@b" (the "@" is not
// at a word start) yields ok=false. A multi-line input disqualifies it (like the
// palette, completion drives the first line only), as does a leading "/" line
// (that is the slash palette's domain — the two are mutually exclusive).
func mentionToken(s string) (token string, ok bool) {
	if strings.ContainsRune(s, '\n') {
		return "", false
	}
	if strings.HasPrefix(strings.TrimLeft(s, " \t"), "/") {
		return "", false // a command line belongs to the slash palette
	}
	// The trailing word is everything after the last run of whitespace.
	last := s
	if i := strings.LastIndexAny(s, " \t"); i >= 0 {
		last = s[i+1:]
	}
	if !strings.HasPrefix(last, "@") {
		return "", false
	}
	return last[1:], true
}

// syncMention recomputes the menu's open/matches state from the current textarea
// content. It is called from afterInputEdit after every idle/running keystroke
// that may have changed the input, alongside syncPalette (the two are mutually
// exclusive — at most one opens). Unlike the palette there is no async fetch: the
// workspace file list is gathered synchronously by a bounded directory walk, which
// is cheap for a normal tree and capped (maxMentionWalk) for a huge one.
func (m Model) syncMention() Model {
	token, ok := mentionToken(m.prompt.Value())
	if !ok {
		// Left mention mode: reset (including the esc-dismiss latch) so a later "@"
		// opens it afresh.
		m.mention.open = false
		m.mention.dismissed = false
		m.mention.matches = nil
		m.mention.syncList()
		return m
	}
	if m.mention.dismissed {
		m.mention.open = false
		return m
	}
	m.mention.matches = matchMentionFilesWithHome(m.deps.Workspace, token, mentionHomeDir(m.deps))
	m.mention.syncList()
	m.mention.open = len(m.mention.matches) > 0
	return m
}

func mentionHomeDir(deps Deps) func() (string, error) {
	if deps.homeDir != nil {
		return deps.homeDir
	}
	return os.UserHomeDir
}

// matchMentionFilesWithHome walks the workspace root, or the local client-process
// home for a literal ~/ token. Leading ./ and ../ components select the matching
// local root while retaining their original spelling in completion results.
func matchMentionFilesWithHome(workspace, token string, homeDir func() (string, error)) []string {
	root, matchToken, prefix := workspace, token, ""
	if rest, ok := strings.CutPrefix(token, "~/"); ok {
		home, err := homeDir()
		if err != nil || home == "" {
			return nil
		}
		root, matchToken, prefix = home, rest, "~/"
	} else if root == "" {
		return nil
	}
	for {
		if rest, ok := strings.CutPrefix(matchToken, "../"); ok {
			root = filepath.Join(root, "..")
			matchToken = rest
			prefix += "../"
			continue
		}
		if rest, ok := strings.CutPrefix(matchToken, "./"); ok {
			matchToken = rest
			prefix += "./"
			continue
		}
		return matchFiles(root, matchToken, prefix, maxMentionCandidates)
	}
}

// matchFiles walks root (bounded) and returns up to limit paths whose path matches
// token. Results retain prefix, which is ~/ for local-home completion.
func matchFiles(root, token, prefix string, limit int) []string {
	if root == "" {
		return nil
	}
	lower := strings.ToLower(token)
	var out []string
	visited := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; never abort the whole walk
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir // prune dot-dirs (.git, .scratch, …) entirely
			}
			return nil
		}
		visited++
		if visited > maxMentionWalk {
			return fs.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if lower == "" ||
			strings.Contains(strings.ToLower(rel), lower) ||
			strings.Contains(strings.ToLower(name), lower) {
			out = append(out, prefix+rel)
		}
		return nil
	})
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// parseMentionPaths extracts the path of every @-mention in text: a whitespace-
// delimited token that STARTS with "@" (at a word boundary — "a@b" is an email-ish
// literal, not a mention) yields the text after the "@". It scans the WHOLE prompt
// (every word, across lines), unlike mentionToken which inspects only the trailing
// word for the live menu. A trailing "@" with no path is skipped (empty token).
// The returned paths are in appearance order, as typed (resolved by the caller).
func parseMentionPaths(text string) []string {
	var out []string
	for _, word := range strings.Fields(text) {
		if !strings.HasPrefix(word, "@") {
			continue
		}
		p := word[1:]
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// resolveMentionWithHome turns a user-typed @-mention path into an absolute local
// path. A literal ~/ prefix resolves against the mecatui process's home directory; a
// failed home lookup leaves it unresolved so it remains ordinary prompt prose.
func resolveMentionWithHome(workspace, p string, homeDir func() (string, error)) (string, bool) {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, err := homeDir()
		if err != nil || home == "" {
			return "", false
		}
		return filepath.Join(home, rest), true
	}
	if filepath.IsAbs(p) {
		return p, true
	}
	if workspace == "" {
		return "", false
	}
	return filepath.Join(workspace, p), true
}

// resolveMention retains the clipboard path-paste behavior: absolute paths are
// used as typed and relative paths resolve against the workspace (or process cwd
// when no workspace is configured).
func resolveMention(workspace, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workspace, p)
}

// attachableMentions resolves the prompt's @-mention tokens against the workspace
// and STAT-FILTERS them to existing REGULAR files — the gate that decides
// attachment-vs-prose. A token whose final component does not lstat to a regular
// non-symlink file (nonexistent, a directory, a symlink, a device, …) is NOT an
// attachment: it stays literal prose in the prompt, so "ping me @oncall" or
// "@somedir" sends as text with no error. Only the surviving path plus original
// mention label are handed to client.ExpandMentions, which revalidates the opened
// file identity before reading it.
func attachableMentions(workspace, text string, homeDir func() (string, error)) []client.MentionAttachment {
	var out []client.MentionAttachment
	for _, tok := range parseMentionPaths(text) {
		path, ok := resolveMentionWithHome(workspace, tok, homeDir)
		if !ok {
			continue
		}
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
			out = append(out, client.MentionAttachment{Path: path, Label: tok})
		}
	}
	return out
}

// mentionMoveUp moves the selection up one logical path (no wrap).
func (m *Model) mentionMoveUp() {
	if m.mention.list != nil {
		m.mention.list.Move(bounded.LineUp)
	}
}

// mentionMoveDown moves the selection down one logical path (no wrap).
func (m *Model) mentionMoveDown() {
	if m.mention.list != nil {
		m.mention.list.Move(bounded.LineDown)
	}
}

func (m Model) mentionVisible() bool {
	return m.mention.open && m.mention.list != nil && m.mention.list.Valid()
}

// mentionComplete replaces only the trailing @-token with the raw selected path.
func (m Model) mentionComplete() Model {
	path := m.mention.selected()
	if !m.mention.open || path == "" {
		return m
	}
	val := m.prompt.Value()
	start := 0
	if i := strings.LastIndexAny(val, " \t"); i >= 0 {
		start = i + 1
	}
	m.prompt.Rewrite(val[:start] + "@" + path + " ")
	m.mention.open = false
	m.mention.matches = nil
	m.mention.syncList()
	return m
}

// mentionDismiss closes the menu until the input leaves mention mode.
func (m Model) mentionDismiss() Model {
	m.mention.open = false
	m.mention.dismissed = true
	return m
}

func renderMention(th theme.Theme, st mentionState, width int) string {
	return renderMentionSized(th, st, width, maxMentionRows)
}

// renderMentionSized renders a clipped physical-row-bounded mention card.
func renderMentionSized(th theme.Theme, st mentionState, width, bodyRows int) string {
	cardWidth := min(128, width)
	cardStyle := th.Style("askCard")
	contentWidth := cardWidth - cardStyle.GetHorizontalFrameSize()
	if st.list == nil {
		st.syncList()
	}
	if bodyRows <= 0 || contentWidth < 3 || !st.open || len(st.matches) == 0 {
		st.list.SetGeometry(contentWidth, 0, 1, bounded.Clip)
		return ""
	}
	st.list.SetGeometry(contentWidth, bodyRows, 1, bounded.Clip)
	st.syncList()
	if !st.list.Valid() {
		return ""
	}
	// Mention has no independent physical-scroll action: every supported navigation
	// changes selection, so every rendered frame must keep that selection visible.
	view := st.list.ViewWithIndicators(bodyRows, true)
	header := "files"
	if bodyRows == 1 {
		// One-row cards present the shared logical overflow metadata in their header
		// rather than displacing their only selectable row with chrome.
		var overflow []string
		if view.Above > 0 {
			overflow = append(overflow, fmt.Sprintf("↑%d above", view.Above))
		}
		if view.Below > 0 {
			overflow = append(overflow, fmt.Sprintf("↓%d below", view.Below))
		}
		if len(overflow) > 0 {
			header = strings.Join(overflow, "·")
		}
	}
	if len(view.Rows) == 0 {
		return ""
	}
	lines := []string{th.Style("muted").Render(ansi.Cut(header, 0, contentWidth))}
	if bodyRows > 1 && view.Above > 0 {
		lines = append(lines, th.Style("muted").Render(ansi.Cut(fmt.Sprintf("  ↑ +%d above", view.Above), 0, contentWidth)))
	}
	for _, row := range view.Rows {
		presentation := presentListRow(row, th.Style("spinner"), th.Style("toolArgs"))
		lines = append(lines, presentation.Style.Render(presentation.Text))
	}
	if bodyRows > 1 && view.Below > 0 {
		lines = append(lines, th.Style("muted").Render(ansi.Cut(fmt.Sprintf("  ↓ +%d below", view.Below), 0, contentWidth)))
	}
	lines = append(lines, th.Style("muted").Render(ansi.Cut("↑/↓ select · pgup/pgdn page · tab/enter complete · esc dismiss", 0, contentWidth)))
	return cardStyle.Width(cardWidth).Render(strings.Join(lines, "\n"))
}
