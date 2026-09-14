package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// maxMentionRows caps how many file-completion rows the @-mention menu shows at
// once, mirroring maxPaletteRows so the menu never pushes the input off-screen.
const maxMentionRows = 8

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
	// cursor is the selected row within matches (clamped to its bounds).
	cursor int
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
		m.mention.cursor = 0
		return m
	}
	if m.mention.dismissed {
		m.mention.open = false
		return m
	}
	m.mention.matches = matchMentionFilesWithHome(m.deps.Workspace, token, mentionHomeDir(m.deps))
	m.mention.open = len(m.mention.matches) > 0
	if m.mention.cursor >= len(m.mention.matches) {
		m.mention.cursor = 0
	}
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
		return matchFiles(root, matchToken, prefix, maxMentionRows)
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

// mentionMoveUp moves the selection up one row (no wrap), clamped to the top.
func (m *Model) mentionMoveUp() {
	if m.mention.cursor > 0 {
		m.mention.cursor--
	}
}

// mentionMoveDown moves the selection down one row (no wrap), clamped to the last.
func (m *Model) mentionMoveDown() {
	if m.mention.cursor < len(m.mention.matches)-1 {
		m.mention.cursor++
	}
}

// mentionComplete replaces the trailing @-token span in the input with the
// selected file path and a trailing space (ready to keep typing or add another
// "@"), closes the menu, and leaves the cursor after the space. It is a no-op when
// the menu has no selectable rows. The replacement is span-precise: only the
// trailing "@token" word is rewritten, so any text before it ("describe ") is
// preserved.
func (m Model) mentionComplete() Model {
	if !m.mention.open || m.mention.cursor >= len(m.mention.matches) {
		return m
	}
	path := m.mention.matches[m.mention.cursor]
	val := m.prompt.Value()
	// Find the start of the trailing word (the "@…" run) and rewrite from there.
	start := 0
	if i := strings.LastIndexAny(val, " \t"); i >= 0 {
		start = i + 1
	}
	m.prompt.Rewrite(val[:start] + "@" + path + " ")
	m.mention.open = false
	m.mention.matches = nil
	m.mention.cursor = 0
	return m
}

// mentionDismiss latches an esc dismissal: the menu closes but the input is left
// untouched, and it stays closed until the @-token changes or leaves.
func (m Model) mentionDismiss() Model {
	m.mention.open = false
	m.mention.dismissed = true
	return m
}

// renderMention draws the file-completion dropdown as a bordered card, mirroring
// renderPalette: the selected row is highlighted, the list windows to
// maxMentionRows around the selection, and every workspace-derived path is
// terminal-sanitized. It returns "" when the menu is not open (no "no match" note
// — an @-token with no matching file simply shows nothing, since "@" is also a
// legitimate literal character in prose).
func renderMention(th theme.Theme, st mentionState, width int) string {
	if !st.open || len(st.matches) == 0 {
		return ""
	}
	start, end := scrollWindow(st.cursor, len(st.matches), maxMentionRows)

	var b strings.Builder
	b.WriteString(th.Style("muted").Render("files") + "\n")
	for i := start; i < end; i++ {
		row := sanitizeTerminal("@" + st.matches[i])
		if i == st.cursor {
			b.WriteString(th.Style("askButtonActive").Render("› "+row) + "\n")
		} else {
			b.WriteString(th.Style("toolArgs").Render("  "+row) + "\n")
		}
	}
	b.WriteString(th.Style("muted").Render("↑/↓ select · tab/enter complete · esc dismiss"))

	card := th.Style("askCard").Render(b.String())
	if width > 0 {
		return lipgloss.NewStyle().MaxWidth(width).Render(card)
	}
	return card
}
