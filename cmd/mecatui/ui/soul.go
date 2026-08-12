package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// soulView is the active soul (persona) inspection overlay (none = closed). Like
// the /skills and /agents panels it is a read-only inventory layered over the
// conversation: idle-only, esc to dismiss. UNLIKE those (short lists) the soul is a
// multi-KB BODY of persona text, so this panel is SCROLLABLE: a line-window over
// the content with pgup/pgdown (and up/down, home/end) moving the viewport. The
// soul is agent-read-only; the panel only displays it, never edits it.
type soulView int

const (
	soulNone  soulView = iota // overlay closed
	soulPanel                 // read-only, scrollable persona inspector
)

// soulBodyLines is the fixed number of soul-content lines the panel shows at once
// (the scroll window). A fixed budget keeps the panel — and its goldens —
// deterministic regardless of terminal height, and is plenty for a persona at a
// glance while keeping the card from swallowing the screen. Content longer than
// this scrolls; shorter content shows in full with no scroll indicator.
const soulBodyLines = 12

// soulState holds the soul overlay state on the Model. It is value-embedded so the
// Model stays a plain struct that Update copies. The Soul value is replaced
// wholesale on each RPC result (never mutated in place) so the value-copy
// semantics hold. scroll is the 0-based index of the first visible content line.
type soulState struct {
	view    soulView
	loading bool // the GetSoul RPC is in flight
	err     error
	soul    client.Soul
	scroll  int // first visible content line (clamped in the key handlers)
}

// openSoul opens the inspection panel and fires the GetSoul RPC. Only callable
// while idle and when a soul fetcher is wired; returns the model unchanged
// otherwise. The result arrives as a client.SoulMsg handled in updateSoulMsg.
func (m Model) openSoul() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Soul == nil {
		return m, nil
	}
	m.ta.Blur() // overlay owns the keyboard while open
	m.soul = soulState{view: soulPanel, loading: true}
	return m, client.GetSoulCmd(m.deps.Ctx, m.deps.Soul)
}

// closeSoul dismisses the overlay and returns focus to the prompt input.
func (m Model) closeSoul() (tea.Model, tea.Cmd) {
	m.soul = soulState{}
	cmd := m.ta.Focus()
	return m, cmd
}

// onSoulKey routes key presses while the soul overlay is open. esc closes it; the
// scroll keys (pgup/pgdown, up/down, home/end) move the content window. Every
// other key is swallowed (handled=true) so it never leaks into idle input. Returns
// handled=false only when the overlay is closed.
func (m Model) onSoulKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.soul.view == soulNone {
		return m, nil, false
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		mm, cmd := m.closeSoul()
		return mm, cmd, true
	case key.Matches(msg, m.keys.ScrollD), key.Matches(msg, m.keys.Down):
		m.soul.scroll = clampSoulScroll(m.soul.scroll+1, m.soul.soul.Content)
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollU), key.Matches(msg, m.keys.Up):
		m.soul.scroll = clampSoulScroll(m.soul.scroll-1, m.soul.soul.Content)
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollBottom):
		m.soul.scroll = clampSoulScroll(soulMaxScroll(m.soul.soul.Content), m.soul.soul.Content)
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollTop):
		m.soul.scroll = 0
		return m, nil, true
	}
	return m, nil, true
}

// updateSoulMsg reduces a client.SoulMsg into the overlay state. It fires no
// follow-up command (single-shot read), returning only the model + handled flag;
// handled=false for any other message so Update can fall through.
func (m Model) updateSoulMsg(msg tea.Msg) (tea.Model, bool) {
	sm, ok := msg.(client.SoulMsg)
	if !ok {
		return m, false
	}
	m.soul.loading = false
	if sm.Err != nil {
		m.soul.err = sm.Err
		return m, true
	}
	m.soul.err = nil
	m.soul.soul = sm.Soul
	m.soul.scroll = 0
	return m, true
}

// soulMaxScroll is the largest valid scroll offset for content: total lines minus
// the visible window, never negative.
func soulMaxScroll(content string) int {
	return maxScrollOffset(len(soulContentLines(content)), soulBodyLines)
}

// clampSoulScroll bounds want into [0, soulMaxScroll].
func clampSoulScroll(want int, content string) int {
	return clampScroll(want, len(soulContentLines(content)), soulBodyLines)
}

// soulContentLines splits the (already-sanitized at render time) content into
// lines. An empty body yields no lines.
func soulContentLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

// renderSoulOverlay draws the soul panel centred over the conversation region via
// centerCard. All server-derived strings are terminal-sanitized.
func renderSoulOverlay(th theme.Theme, st soulState, caps client.Capabilities, hk helpKeys, width, height int) string {
	if st.view != soulPanel {
		return ""
	}
	return centerCard(th, renderSoulPanel(th, st, caps, hk, width), width, height)
}

// soulDisabledNote is the empty-state copy when soul is NOT enabled on the
// connected server (caps.Soul == false), with the remedy.
const soulDisabledNote = "No soul (persona) is enabled on this server.\n" +
	"Create ~/.config/mecatl/soul.md (or pass --soul-file) and reconnect."

// soulTrustLabel renders the soul's provenance + trust + drift state as a single
// human label for the metadata line. It distinguishes a loaded user/project soul
// from a project soul that was DROPPED untrusted (present=false), so the operator
// learns --trust-project would honour it. The ui only DISPLAYS this — trust/drift
// are computed in composition and projected; the ui never decides them.
func soulTrustLabel(s client.Soul) string {
	switch s.Provenance {
	case client.SoulProvenanceUser:
		label := "user"
		if s.Drifted {
			label += " · DRIFTED"
		}
		return label
	case client.SoulProvenanceProject:
		if !s.Present {
			if !s.Trusted {
				return "project (UNTRUSTED → not loaded; pass --trust-project)"
			}
			return "project (not loaded)"
		}
		label := "project (trusted)"
		if s.Drifted {
			label += " · DRIFTED"
		}
		return label
	default:
		return "none"
	}
}

// renderSoulPanel renders the read-only, scrollable persona inspector: a title, a
// dim metadata line (provenance/trust · size · sha), then a scroll-windowed view of
// the content. EVERY server-derived string is terminal-sanitized.
func renderSoulPanel(th theme.Theme, st soulState, caps client.Capabilities, hk helpKeys, width int) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Soul (persona)") + "\n\n")

	budget := cardTextWidth(width)
	switch {
	case st.loading:
		b.WriteString(th.Style("muted").Render("loading…") + "\n")
	case st.err != nil:
		line := "get soul: " + sanitizeTerminal(st.err.Error())
		if budget > 0 {
			line = ansi.Wrap(line, budget, "")
		}
		b.WriteString(th.Style("errorText").Render(line) + "\n")
	case !st.soul.Present && st.soul.Provenance == client.SoulProvenanceNone:
		// No soul selected and nothing dropped: distinguish "not enabled" from
		// "enabled but none present".
		if !caps.Soul {
			b.WriteString(th.Style("muted").Render(soulDisabledNote) + "\n")
		} else {
			b.WriteString(th.Style("muted").Render("No soul is present (no ~/.config/mecatl/soul.md and no project soul).") + "\n")
		}
	default:
		b.WriteString(th.Style("muted").Render(renderSoulMeta(st.soul)) + "\n\n")
		b.WriteString(renderSoulBody(th, st, budget))
	}

	// The scroll pair (ScrollU/ScrollD) and the close chord (Close) read the LIVE
	// keyMap markings (issue #457); with defaults the hint is byte-identical to the
	// historical literal.
	b.WriteString("\n" + th.Style("muted").Render("read-only persona · "+hk.scroll+" scroll · "+hk.closeOnly+" close"))
	return b.String()
}

// renderSoulMeta renders the dim metadata line: trust label · N bytes · sha (short).
// soulTrustLabel emits only hardcoded literals today, but the line is wrapped in
// sanitizeTerminal defensively so the function's "EVERY server-derived string is
// sanitized" contract still holds if someone later interpolates a server string into
// the label.
func renderSoulMeta(s client.Soul) string {
	segs := []string{sanitizeTerminal(soulTrustLabel(s)), fmt.Sprintf("%d bytes", s.SizeBytes)}
	if s.SHA256 != "" {
		short := s.SHA256
		if len(short) > 12 {
			short = short[:12]
		}
		segs = append(segs, "sha:"+sanitizeTerminal(short))
	}
	return strings.Join(segs, " · ")
}

// renderSoulBody renders the scroll-windowed content. When the body is empty (a
// dropped untrusted project soul has metadata but no content) it shows a note.
// Otherwise it wraps each line to the card budget, then windows the WRAPPED lines
// to soulBodyLines starting at st.scroll, and appends a "lines X–Y of N" indicator
// when the content exceeds the window.
func renderSoulBody(th theme.Theme, st soulState, budget int) string {
	if st.soul.Content == "" {
		return th.Style("muted").Render("(content not loaded)") + "\n"
	}
	// Wrap each raw line to the budget so a long persona line cannot overflow the
	// card; the scroll window then operates on the wrapped lines for honest paging.
	raw := soulContentLines(sanitizeTerminal(st.soul.Content))
	var rendered []string
	for _, ln := range raw {
		if budget > 0 {
			ln = ansi.Wrap(ln, budget, "")
		}
		for _, w := range strings.Split(ln, "\n") {
			rendered = append(rendered, th.Style("toolArgs").Render(w))
		}
	}
	return windowRenderedLines(th, rendered, st.scroll, soulBodyLines)
}
