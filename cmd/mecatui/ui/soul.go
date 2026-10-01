package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// runSoul is the Open transition for the soul (persona) surface. It validates
// (idle + fetcher wired), blurs the textarea, constructs the state dynamically
// (never a pre-declared Model field), installs it on m.modal, and fires the
// GetSoul RPC; the result lands on soulState.HandleMsg. Only registered when
// caps.Soul && the soul collaborator is wired.
func (m Model) runSoul() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Soul == nil {
		return m, nil
	}
	m.prompt.Blur() // modal owns the keyboard while open
	s := &soulState{view: soulPanel, loading: true, deps: (&m).surfaceDeps()}
	m.modal = s
	return m, func() tea.Msg {
		return soulResultMsg{owner: s, result: client.GetSoulCmd(m.deps.Ctx, m.deps.Soul)().(client.SoulMsg)}
	}
}

// soulView discriminates the read-only persona inspector.
type soulView int

const (
	soulNone soulView = iota
	soulPanel
)

type soulResultMsg struct {
	owner  *soulState
	result client.SoulMsg
}

type soulState struct {
	view      soulView
	loading   bool
	err       error
	soul      client.Soul
	deps      surfaceDeps
	viewport  *bounded.Viewport
	bodyWidth int
	total     int
	compact   bool
}

func (*soulState) modalMaxOuterWidth() int { return 128 }
func (s *soulState) modalFrame() bool      { return !s.compact }

// Render receives the parent's measured askCard content offer.
func (s *soulState) Render(width, height int) (string, []ClickableRegion) {
	s.compact = false
	s.bodyWidth, s.total = 0, 0
	if s.view != soulPanel || width <= 0 || height <= 0 {
		s.viewport = nil
		return "", nil
	}
	th := s.deps.theme
	text := func(style lipgloss.Style, value string) []string {
		var rows []string
		for _, line := range strings.Split(value, "\n") {
			for _, part := range strings.Split(ansi.Wrap(line, width, ""), "\n") {
				rows = append(rows, style.Render(part))
			}
		}
		return rows
	}
	title := text(th.Style("askTitle"), "Soul (persona)")
	footer := text(th.Style("muted"), soulPanelFooter(s.soul, s.deps.marks))
	var meta []string
	var rows []string
	switch {
	case s.loading:
		rows = text(th.Style("muted"), "loading…")
	case s.err != nil:
		rows = text(th.Style("errorText"), "get soul: "+terminaltext.Sanitize(s.err.Error()))
	case !s.soul.Present && s.soul.Provenance == client.SoulProvenanceNone:
		emptyMessage := "No user or project soul is available."
		if !s.deps.caps.Soul {
			emptyMessage = soulDisabledNote
		}
		rows = text(th.Style("muted"), emptyMessage)
	default:
		meta = text(th.Style("muted"), renderSoulMeta(s.soul))
		if s.soul.Content == "" {
			rows = text(th.Style("muted"), "(soul not loaded)")
		} else {
			rows = text(th.Style("toolArgs"), terminaltext.Sanitize(s.soul.Content))
		}
	}
	fixed := len(title) + 2 + len(footer) // title/body and body/footer separators
	if len(meta) > 0 {
		fixed += len(meta) + 1
	}
	capacity := height - fixed
	bodyHeight := capacity
	if len(rows) > capacity {
		bodyHeight-- // reserve an indicator, even when offset is at an endpoint
	}
	if bodyHeight < 1 {
		s.compact = true
		s.viewport = nil
		return ansi.Cut(th.Style("muted").Render(s.deps.marks.closeOnly+" close"), 0, width) + "\x1b[0m", nil
	}
	if s.viewport == nil {
		s.viewport = &bounded.Viewport{}
	}
	s.bodyWidth, s.total = width, len(rows)
	s.viewport.SetGeometry(width, bodyHeight, 0, bounded.Clip)
	projection := s.viewport.View(rows)
	body := append(append([]string{}, title...), "")
	if len(meta) > 0 {
		body = append(body, meta...)
		body = append(body, "")
	}
	body = append(body, projection.Rows...)
	if len(rows) > bodyHeight {
		indicator := fmt.Sprintf("lines %d–%d of %d", projection.Above+1, len(rows)-projection.Below, len(rows))
		body = append(body, ansi.Cut(th.Style("muted").Render(indicator), 0, width)+"\x1b[0m")
	}
	body = append(body, "")
	body = append(body, footer...)
	return strings.Join(body, "\n"), nil
}

func (s *soulState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if key.Matches(msg, s.deps.keys.Close) {
		return nil, true, true
	}
	if s.viewport != nil {
		move := bounded.LineDown
		switch {
		case key.Matches(msg, s.deps.keys.Down):
		case key.Matches(msg, s.deps.keys.Up):
			move = bounded.LineUp
		case key.Matches(msg, s.deps.keys.ScrollD):
			move = bounded.PageDown
		case key.Matches(msg, s.deps.keys.ScrollU):
			move = bounded.PageUp
		case key.Matches(msg, s.deps.keys.ScrollBottom):
			move = bounded.End
		case key.Matches(msg, s.deps.keys.ScrollTop):
			move = bounded.Top
		default:
			return nil, true, false
		}
		s.viewport.Move(move, s.total)
	}
	return nil, true, false
}

func (s *soulState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if s.viewport != nil {
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			s.viewport.Move(bounded.LineUp, s.total)
		case tea.MouseWheelDown:
			s.viewport.Move(bounded.LineDown, s.total)
		}
	}
	return nil, true
}

func (s *soulState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	sm, ok := msg.(soulResultMsg)
	if !ok || sm.owner != s {
		return nil, false, false
	}
	s.loading = false
	if sm.result.Err != nil {
		s.err = sm.result.Err
		return nil, true, false
	}
	s.err = nil
	s.soul = sm.result.Soul
	if s.viewport != nil {
		s.viewport.Reset()
	}
	return nil, true, false
}

func (*soulState) Close() {}

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
			label += " · changed since Mecatl last recorded it"
		}
		return label
	case client.SoulProvenanceProject:
		if !s.Present {
			if !s.Trusted {
				return "project · not loaded because this workspace is not trusted"
			}
			return "project · not loaded"
		}
		label := "project · trusted"
		if s.Drifted {
			label += " · changed since Mecatl last recorded it"
		}
		return label
	default:
		return "none"
	}
}

// soulPanelFooter keeps ownership guidance aligned with server provenance.
// The soul is agent-read-only; user and project sources have different remedies.
func soulPanelFooter(s client.Soul, hk helpKeys) string {
	var ownership string
	switch s.Provenance {
	case client.SoulProvenanceUser:
		ownership = "you control this soul · edit its source file to change it; the agent can suggest wording"
	case client.SoulProvenanceProject:
		ownership = "controlled by this project · change its soul file or ask a maintainer"
	default:
		ownership = "read-only persona"
	}
	return ownership + " · " + hk.scroll + " scroll · " + hk.closeOnly + " close"
}

// renderSoulMeta renders the dim metadata line: trust label · N bytes · sha (short).
// soulTrustLabel emits only hardcoded literals today, but the line is wrapped in
// terminaltext.Sanitize defensively so the function's "EVERY server-derived string is
// sanitized" contract still holds if someone later interpolates a server string into
// the label.
func renderSoulMeta(s client.Soul) string {
	segs := []string{terminaltext.Sanitize(soulTrustLabel(s)), fmt.Sprintf("%d bytes", s.SizeBytes)}
	if s.SHA256 != "" {
		short := s.SHA256
		if len(short) > 12 {
			short = short[:12]
		}
		segs = append(segs, "sha:"+terminaltext.Sanitize(short))
	}
	return strings.Join(segs, " · ")
}
