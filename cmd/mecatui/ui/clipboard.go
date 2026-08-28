package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// mediaExts is the cheap allowlist tryPasteMediaPath uses to fast-reject a pasted
// token before touching the filesystem: only a path whose extension looks like an
// image/audio file is a candidate for media staging. The authoritative decision is
// still the content sniff in client.StagePathMedia — this is just a quick filter so
// ordinary prose pastes never stat/read a file.
var mediaExts = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {},
	".wav": {}, ".mp3": {}, ".m4a": {}, ".ogg": {}, ".flac": {},
}

// isMediaExt reports whether path has a known image/audio extension (case-folded).
func isMediaExt(path string) bool {
	_, ok := mediaExts[strings.ToLower(filepath.Ext(path))]
	return ok
}

// stagedAttachment is a clipboard image the user has pasted (ctrl+v) but not yet
// sent. It is proto-free — just the sniffed mime and the raw bytes — so the ui
// never names a proto type; client.StageClipboardImage rebuilds it into a Content
// part at submit time. It is keyed in Model.stagedMedia by its literal "[Image
// #N]" marker, which also appears in the textarea text.
type stagedAttachment struct {
	mime string
	data []byte
}

// clipboardResultMsg carries a successful clipboard read back to the reducer: a
// mime (image/* → stage a part; text → insert into the textarea) and its bytes.
type clipboardResultMsg struct {
	mime string
	data []byte
}

// clipboardErrMsg carries a clipboard read failure (no backend, empty, oversize,
// backend error) back to the reducer for an appropriate status / transcript line.
type clipboardErrMsg struct{ err error }

// onClipboardPaste handles ctrl+v. A nil Clipboard collaborator (the inject-time
// disable, mirroring nil MCP/Cmds) yields a benign "unavailable" status. Otherwise
// it returns a command that reads the OS clipboard off the update goroutine,
// surfacing a clipboardResultMsg or clipboardErrMsg. The caps.Image gate is decided
// at RESULT time (onClipboardResult), NOT here: ctrl+v must still paste TEXT on an
// image-incapable model (the spec's image-first, text-fallback contract), so a read
// is always attempted; only an IMAGE result is refused when caps.Image is false.
func (m Model) onClipboardPaste() (tea.Model, tea.Cmd) {
	if m.deps.Clipboard == nil {
		m.statusMsg = m.deps.Theme.Style("muted").Render("clipboard paste unavailable")
		return m, nil
	}
	cb := m.deps.Clipboard
	ctx := m.deps.Ctx
	return m, func() tea.Msg {
		mime, data, err := cb.Read(ctx)
		if err != nil {
			return clipboardErrMsg{err: err}
		}
		return clipboardResultMsg{mime: mime, data: data}
	}
}

// onClipboardResult reduces a successful clipboard read. An image is cap-gated
// (refused with a clear message when the server's model takes no images) and,
// when accepted, staged under a fresh monotonic "[Image #N]" marker that is also
// inserted into the textarea; the part is rebuilt and attached at submit time. A
// text result is inserted into the textarea verbatim regardless of caps (the
// text-fallback), through the same afterInputEdit funnel typed input uses so the
// /@ palettes resync.
func (m Model) onClipboardResult(msg clipboardResultMsg) (tea.Model, tea.Cmd) {
	if strings.HasPrefix(msg.mime, "image/") {
		if !m.caps.Image {
			m.statusMsg = m.deps.Theme.Style("muted").Render("this model takes no images — clipboard image not attached")
			return m, nil
		}
		m.nextMediaN++
		marker := fmt.Sprintf("[Image #%d]", m.nextMediaN)
		if m.stagedMedia == nil {
			m.stagedMedia = make(map[string]stagedAttachment)
		}
		m.stagedMedia[marker] = stagedAttachment(msg)
		m.insertText(marker)
		return m.afterInputEdit(nil)
	}
	// Text fallback: insert the clipboard text into the prompt.
	m.insertText(string(msg.data))
	return m.afterInputEdit(nil)
}

// primaryReadMsg carries the shell primary-selection read (the middle-click
// paste, issue #43) back to the reducer: the selection text, or the error that
// decides between the OSC52 fallback (ErrNoClipboardTool) and a silent no-op.
type primaryReadMsg struct {
	text string
	err  error
}

// primaryPasteCmd returns the async primary-selection read a middle-click
// triggers: the SHELL backend when a Clipboard collaborator is wired (read off
// the update goroutine, exactly like the ctrl+v read), falling back to the OSC52
// primary read (tea.ReadPrimaryClipboard) when there is none. The OSC52 path is
// STATELESS: nothing is parked or timed — the insert happens only if the terminal
// ever answers with a ClipboardMsg{Selection: 'p'} (see onClipboardMsg). That
// matters because OSC52 READ is blocked in some terminals (Ptyxis/VTE), where the
// query simply never gets a response.
func (m Model) primaryPasteCmd() tea.Cmd {
	cb := m.deps.Clipboard
	if cb == nil {
		return tea.ReadPrimaryClipboard
	}
	ctx := m.deps.Ctx
	return func() tea.Msg {
		text, err := cb.ReadPrimary(ctx)
		return primaryReadMsg{text: text, err: err}
	}
}

// onPrimaryRead reduces the shell primary-selection read. No backend (or a
// platform without a primary selection) falls back to the OSC52 primary read;
// any other failure — including an empty selection — is a SILENT no-op
// (statusline noise for a paste miss is worse than nothing; deliberately quieter
// than the ctrl+v error surface, where the user explicitly asked for a paste of
// something they believe exists). A successful read inserts via the bracketed-
// paste pipeline (insertPrimaryPaste).
func (m Model) onPrimaryRead(msg primaryReadMsg) (tea.Model, tea.Cmd) {
	if errors.Is(msg.err, client.ErrNoClipboardTool) {
		return m, tea.ReadPrimaryClipboard
	}
	if msg.err != nil {
		return m, nil
	}
	return m.insertPrimaryPaste(msg.text)
}

// onClipboardMsg reduces an OSC52 clipboard-read response from the terminal. Only
// a PRIMARY-selection response ('p' — the middle-click paste's OSC52 fallback) is
// consumed; a system-clipboard response ('c') is ignored (the app never issues
// tea.ReadClipboard, and ctrl+v owns the system-clipboard paste via the shell
// backend). Stateless by design: there is no pending-request flag — a 'p' response
// only ever arrives because we queried, and a late one inserting into an
// input-accepting phase is exactly what the user asked for.
func (m Model) onClipboardMsg(msg tea.ClipboardMsg) (tea.Model, tea.Cmd) {
	if msg.Selection != 'p' {
		return m, nil
	}
	return m.insertPrimaryPaste(msg.Content)
}

// insertPrimaryPaste routes retrieved primary-selection text through the SAME
// pipeline as a bracketed paste (onPaste): the overlay/phase gates re-apply at
// delivery time (a read that lands after a modal opened is dropped, not leaked
// behind it), a huge selection stages behind a [Pasted text #N] placeholder
// (issue #45 parity) instead of flooding the buffer, and the slash/mention
// palettes resync. Empty or whitespace-only selections are a silent no-op.
func (m Model) insertPrimaryPaste(text string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	return m.onPaste(tea.PasteMsg{Content: text})
}

// onClipboardErr reduces a clipboard read failure. A missing backend gets an
// actionable install hint; an empty clipboard is a benign status (no transcript
// noise); any other error (oversize image, backend failure) is a loud transcript
// error.
func (m Model) onClipboardErr(msg clipboardErrMsg) tea.Model {
	switch {
	case errors.Is(msg.err, client.ErrNoClipboardTool):
		m.statusMsg = m.deps.Theme.Style("muted").Render("image paste needs wl-clipboard (Wayland) / xclip (X11) installed")
	case errors.Is(msg.err, client.ErrEmptyClipboard):
		m.statusMsg = m.deps.Theme.Style("muted").Render("clipboard is empty")
	default:
		m.conv.addError("clipboard: " + msg.err.Error())
		m.refreshView()
	}
	return m
}

// insertText inserts a marker or clipboard payload at the current cursor. Markers
// keep their separating space, while an active textarea selection is replaced.
func (m *Model) insertText(s string) {
	if m.ta.HasSelection() {
		m.promptInsert(s + " ")
		return
	}
	val := m.ta.Value()
	if val != "" && !strings.HasSuffix(val, " ") && !strings.HasSuffix(val, "\n") {
		val += " "
	}
	m.promptRewrite(val + s + " ")
}

// tryPasteMediaPath handles a bracketed paste whose payload is a single FILE PATH
// to a media file (the common "drag an image onto the terminal" flow, which many
// terminals deliver as a pasted path): it stages the file as a clipboard-style
// attachment under a fresh "[Image #N]" marker and reports ok=true. It is
// conservative so an ordinary text paste is never mis-handled — it requires a
// single whitespace-free token with a media extension, that resolves to an
// EXISTING REGULAR FILE, and that client.StagePathMedia accepts (sniffs as a
// cap-allowed, in-size image/audio). On ANY miss it returns ok=false so onPaste
// falls through to the literal-text insert (iteration-1 behaviour) — a pasted path
// that isn't a stageable media file stays literal prose, never a loud error.
func (m Model) tryPasteMediaPath(content string) (tea.Model, tea.Cmd, bool) {
	tok := strings.TrimSpace(content)
	if tok == "" || strings.ContainsAny(tok, " \t\n") {
		return m, nil, false // multi-token / multi-line paste is prose, not a path
	}
	if !isMediaExt(tok) {
		return m, nil, false
	}
	abs := resolveMention(m.deps.Workspace, tok)
	if fi, err := os.Stat(abs); err != nil || !fi.Mode().IsRegular() {
		return m, nil, false
	}
	mime, data, _, err := client.StagePathMedia(abs, m.caps)
	if err != nil {
		return m, nil, false // unreadable / non-media / cap-gated / oversize → literal
	}
	m.nextMediaN++
	marker := fmt.Sprintf("[Image #%d]", m.nextMediaN)
	if m.stagedMedia == nil {
		m.stagedMedia = make(map[string]stagedAttachment)
	}
	m.stagedMedia[marker] = stagedAttachment{mime: mime, data: data}
	m.insertText(marker)
	mm, cmd := m.afterInputEdit(nil)
	return mm, cmd, true
}

// survivingMarkers returns the staged-media markers (sorted ascending by their N,
// so attachments ride in display order) whose literal "[Image #N]" string still
// appears in text — i.e. the user did not delete them while editing. Deleted
// markers are dropped from the send, which is how an over-eager paste is undone.
func survivingMarkers(text string, staged map[string]stagedAttachment) []string {
	var out []string
	for marker := range staged {
		if strings.Contains(text, marker) {
			out = append(out, marker)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return markerN(out[i]) < markerN(out[j])
	})
	return out
}

// markerN extracts the integer N from an "[Image #N]" marker for ordering; a
// malformed marker sorts first (N=0), which is harmless (markers are well-formed
// by construction).
func markerN(marker string) int {
	var n int
	_, _ = fmt.Sscanf(marker, "[Image #%d]", &n)
	return n
}

// stripMarker removes a "[Image #N]" marker from text WITHOUT collapsing the
// prompt's newlines or other structure: it drops the marker together with exactly
// one adjacent space (the separator insertText added), trying "marker " then
// " marker" then a bare marker, so no double space is left where the marker sat.
// Newlines, blank lines, and code-block indentation are untouched — only the stray
// space around the stripped token is removed. The caller does a final TrimSpace.
func stripMarker(text, marker string) string {
	switch {
	case strings.Contains(text, marker+" "):
		return strings.ReplaceAll(text, marker+" ", "")
	case strings.Contains(text, " "+marker):
		return strings.ReplaceAll(text, " "+marker, "")
	default:
		return strings.ReplaceAll(text, marker, "")
	}
}

// pasteCharThreshold / pasteLineThreshold are the staging cutoffs for a bracketed
// text paste (issue #45): a payload of >= pasteCharThreshold runes OR >=
// pasteLineThreshold lines is staged behind a "[Pasted text #N]" placeholder
// instead of entering the textarea. The textarea re-wraps (and SHA-256-keys) every
// logical line per rendered frame, so a huge paste in the buffer makes EVERY
// subsequent keypress O(paste) — staging keeps the buffer (and thus the per-frame
// input render) small. Below both thresholds the paste is byte-identical to the
// pre-staging behaviour.
const (
	pasteCharThreshold = 2000
	pasteLineThreshold = 30
)

// pasteNeedsStaging reports whether a bracketed-paste payload must stage behind
// a placeholder: the paste alone crosses the rune/line thresholds, OR the
// CUMULATIVE buffer (what is already in the textarea plus this paste) crosses
// the rune threshold. The cumulative arm closes the repeated-paste hole — ten
// 1900-rune pastes are each under the per-paste threshold but together rebuild
// the exact O(buffer) per-keystroke lag the staging exists to prevent. Only the
// INCOMING paste is ever staged; typed text (and earlier, legitimately literal
// pastes) is never converted out of the buffer.
func pasteNeedsStaging(buffer, content string) bool {
	pasteRunes := utf8.RuneCountInString(content)
	return pasteRunes >= pasteCharThreshold ||
		strings.Count(content, "\n")+1 >= pasteLineThreshold ||
		utf8.RuneCountInString(buffer)+pasteRunes >= pasteCharThreshold
}

// stageLargePaste stages a large text paste under a fresh monotonic
// "[Pasted text #N]" marker (inserted into the textarea in the payload's place),
// mirroring the stagedMedia design: no-live-renumber + expand-at-submit. The full
// text lives only in Model.stagedPastes until submitPrompt/enqueuePrompt expands
// the surviving markers in place; a marker the user deletes while editing drops
// its content silently at submit (the image-marker UX). N is its own counter
// (separate numbering from "[Image #N]") and is never reused.
func (m Model) stageLargePaste(content string) (tea.Model, tea.Cmd) {
	m.nextPasteN++
	marker := fmt.Sprintf("[Pasted text #%d]", m.nextPasteN)
	if m.stagedPastes == nil {
		m.stagedPastes = make(map[string]string)
	}
	m.stagedPastes[marker] = content
	m.insertText(marker)
	return m.afterInputEdit(nil)
}

// survivingPasteMarkers returns the staged-paste markers (sorted ascending by
// their N) whose literal "[Pasted text #N]" string still appears in text — the
// paste-placeholder twin of survivingMarkers. Deleted markers are dropped from
// the expansion, which is how an over-eager paste is undone. The trailing "]"
// keeps markers prefix-safe ("#1]" is never a substring of "#11]").
func survivingPasteMarkers(text string, staged map[string]string) []string {
	var out []string
	for marker := range staged {
		if strings.Contains(text, marker) {
			out = append(out, marker)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return pasteMarkerN(out[i]) < pasteMarkerN(out[j])
	})
	return out
}

// pasteMarkerN extracts the integer N from a "[Pasted text #N]" marker for
// ordering; a malformed marker sorts first (N=0), which is harmless (markers are
// well-formed by construction).
func pasteMarkerN(marker string) int {
	var n int
	_, _ = fmt.Sscanf(marker, "[Pasted text #%d]", &n)
	return n
}

// expandPastePlaceholders replaces each surviving "[Pasted text #N]" marker in
// text with its staged full payload in ONE left-to-right pass (strings.Replacer):
// replaced text is never rescanned, so a payload that itself CONTAINS another
// surviving marker's literal text — entirely plausible in a pasted code/log
// excerpt — survives verbatim instead of having its embedded token re-substituted
// by a later pass (the sequential-ReplaceAll corruption). The Replacer does
// replace ALL occurrences of each marker, so a manually-typed duplicate of a
// staged marker duplicates the payload — the same as a hand-duplicated
// "[Image #N]" marker, accepted. It only rewrites the local text — clearing the
// staged store (and dropping deleted markers' content) is the caller's job,
// AFTER its loud-reject early returns, so a failed submit keeps the store intact
// for the retry (the input still holds the markers).
func expandPastePlaceholders(text string, staged map[string]string) string {
	markers := survivingPasteMarkers(text, staged)
	if len(markers) == 0 {
		return text
	}
	pairs := make([]string, 0, 2*len(markers))
	for _, marker := range markers {
		pairs = append(pairs, marker, staged[marker])
	}
	return strings.NewReplacer(pairs...).Replace(text)
}
