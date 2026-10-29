package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// ConnectTarget is public saved-target metadata. It deliberately has no token,
// credential reference, CA contents, or other secret-shaped fields.
type ConnectTarget struct {
	Target   string
	Issuer   string
	ClientID string
	Audience string
}

// ConnectController is the narrow composition-owned saved-target listing seam.
// The UI can only display its public metadata and emit an intent; it cannot
// authenticate or launch a browser.
type ConnectController interface {
	ListConnectTargets(context.Context) ([]ConnectTarget, error)
}

// ConnectAction is the closed set of operations main may perform after the TUI
// exits. The zero value is invalid so an incomplete intent fails loudly.
type ConnectAction uint8

// ConnectAction values are the closed set of operations main may perform after
// the TUI exits.
const (
	connectCommand = "connect"

	ConnectSaved ConnectAction = iota + 1
	Reauthenticate
	RetryAfterCleanup
	AddTarget
)

// ConnectRestartIntent is consumed by main after Bubble Tea exits. Target is
// public canonical registry metadata only.
type ConnectRestartIntent struct {
	Target string
	Action ConnectAction
	// ResumeSessionID is retained only for same-target recovery actions. Main
	// re-checks ownership through GetSession before adopting it.
	ResumeSessionID string
}

type connectState struct {
	deps                          surfaceDeps
	lister                        ConnectController
	cancel                        context.CancelFunc
	request                       uint64
	loading                       bool
	listFailed                    bool
	err                           string
	reason                        client.AuthReason
	failedTarget, resumeSessionID string
	targets                       []ConnectTarget
	unavailable                   []string
	items                         []bounded.ListItem
	list                          bounded.List
	confirm                       *ConnectRestartIntent
	intent                        surfaceIntent
	compact                       bool   // view cache: refreshed from every Render offer
	offer                         [2]int // view cache: last usable list geometry, for reveal on resize
}

type connectTargetsMsg struct {
	owner   *connectState
	request uint64
	targets []ConnectTarget
	err     error
}

type connectSelectIntent struct{ restart ConnectRestartIntent }

func (connectSelectIntent) isSurfaceIntent() {}
func (s *connectState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func (m Model) connectStartupCmd() tea.Cmd {
	return m.modal.(*connectState).listCmd()
}

func (m Model) openConnect() (tea.Model, tea.Cmd) {
	return m.openConnectRecovery("", "", "", "")
}

func (m *Model) installConnect(err string, reason client.AuthReason, target, resume string) *connectState {
	m.closeModal()
	s := &connectState{deps: m.surfaceDeps(), lister: m.deps.Connect, err: err, reason: reason, failedTarget: target, resumeSessionID: resume, loading: true, compact: true}
	m.modal = s
	m.prompt.Blur()
	return s
}

func (m Model) openConnectRecovery(err string, reason client.AuthReason, target, resume string) (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Connect == nil {
		return m, nil
	}
	s := m.installConnect(err, reason, target, resume)
	return m, s.listCmd()
}

func (s *connectState) listCmd() tea.Cmd {
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(s.deps.ctx)
	s.cancel = cancel
	s.request++
	s.loading = true
	request := s.request
	return func() tea.Msg {
		targets, err := s.lister.ListConnectTargets(ctx)
		return connectTargetsMsg{owner: s, request: request, targets: targets, err: err}
	}
}

func (s *connectState) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.request++
	s.intent = nil
}

func (s *connectState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	result, ok := msg.(connectTargetsMsg)
	if !ok {
		return nil, false, false
	}
	if result.owner != s || result.request != s.request || !s.loading {
		return nil, true, false
	}
	s.loading = false
	s.listFailed = result.err != nil
	if result.err != nil {
		s.unavailable = nil
		s.err = "saved targets are unavailable"
		s.targets = nil
		s.items = nil
		s.list.SetItems(nil)
	} else {
		s.targets = append([]ConnectTarget(nil), result.targets...)
		s.syncItems()
		for i, item := range s.items {
			if item.ID == "target:"+s.failedTarget {
				s.list.SetCursor(i)
				break
			}
		}
	}
	return nil, true, false
}

func connectDisplay(text string) string {
	return terminaltext.ExpandTabs(terminaltext.Sanitize(text), terminaltext.DefaultTabWidth)
}

func (s *connectState) syncItems() {
	s.items, s.unavailable = nil, nil
	for _, target := range s.targets {
		text := connectDisplay(target.Target + "\n" + target.Issuer + " · " + target.ClientID + " · " + target.Audience)
		if s.reason == client.AuthRejected && target.Target == s.failedTarget {
			s.unavailable = append(s.unavailable, connectDisplay(target.Target)+" — unavailable: credential rejected")
			continue
		}
		s.items = append(s.items, bounded.ListItem{ID: "target:" + target.Target, Text: text})
	}
	if s.reason != client.AuthRejected {
		s.items = append(s.items, bounded.ListItem{ID: "add", Text: "OIDC sign-in for a new target…"})
	}
	s.list.SetItems(s.items)
}

func (s *connectState) selectedIntent() (ConnectRestartIntent, bool) {
	if s.loading || len(s.items) == 0 {
		return ConnectRestartIntent{}, false
	}
	id := s.list.CursorID()
	if id == "add" {
		return ConnectRestartIntent{Action: AddTarget}, true
	}
	for _, target := range s.targets {
		if id != "target:"+target.Target {
			continue
		}
		if s.reason == client.AuthRejected && target.Target == s.failedTarget {
			return ConnectRestartIntent{}, false
		}
		intent := ConnectRestartIntent{Target: target.Target, Action: ConnectSaved}
		if target.Target == s.failedTarget {
			switch s.reason {
			case client.AuthCredentialCleanup, client.AuthStorageUnavailable:
				intent.Action = RetryAfterCleanup
			case client.AuthNotEnrolled, client.AuthSessionExpired, client.AuthCredentialUnusable, client.AuthTargetChanged:
				intent.Action = Reauthenticate
			}
		}
		if intent.Action == Reauthenticate || intent.Action == RetryAfterCleanup {
			intent.ResumeSessionID = s.resumeSessionID
		}
		return intent, true
	}
	return ConnectRestartIntent{}, false
}

func (s *connectState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if key.Matches(msg, s.deps.keys.Close) {
		if s.confirm != nil {
			s.confirm = nil
			return nil, true, false
		}
		return nil, true, true
	}
	if s.compact {
		return nil, true, false
	}
	if s.confirm != nil {
		if key.Matches(msg, s.deps.keys.Choose) {
			s.intent = connectSelectIntent{restart: *s.confirm}
		}
		return nil, true, false
	}
	if s.loading {
		return nil, true, false
	}
	if move, ok := boundedNavigationMove(msg, s.deps.keys); ok {
		s.list.Move(move)
	} else if key.Matches(msg, s.deps.keys.Choose) {
		if intent, ok := s.selectedIntent(); ok {
			s.confirm = &intent
		}
	}
	return nil, true, false
}

func (s *connectState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if !s.compact && s.confirm == nil && !s.loading {
		if move, ok := boundedWheelMove(msg); ok {
			s.list.Scroll(move)
		}
	}
	return nil, true
}

func connectActionLabel(action ConnectAction) string {
	switch action {
	case ConnectSaved:
		return "connect"
	case Reauthenticate:
		return "sign in again"
	case RetryAfterCleanup:
		return "retry connection"
	case AddTarget:
		return "sign in"
	default:
		return ""
	}
}

func connectConfirmationLabel(action ConnectAction) string {
	switch action {
	case ConnectSaved:
		return "Connect to this saved target?"
	case Reauthenticate:
		return "Sign in again and connect?"
	case RetryAfterCleanup:
		return "Retry connection without opening a browser?"
	case AddTarget:
		return "Sign in to a new target?"
	default:
		return ""
	}
}

func (s *connectState) footerLines(width int, action ConnectAction) []string {
	th, hk := s.deps.theme, s.deps.marks
	actions := []string{hk.closeOnly + ": close"}
	if action != 0 && s.deps.keys.Choose.Enabled() {
		actions = append([]string{hk.choose + ": " + connectActionLabel(action)}, actions...)
	}
	var navigation []string
	for i, pair := range []struct {
		up, down     key.Binding
		marks, label string
	}{
		{s.deps.keys.Up, s.deps.keys.Down, hk.navUp + "/" + hk.navDown, "select"},
		{s.deps.keys.ScrollU, s.deps.keys.ScrollD, hk.scroll, "page"},
		{s.deps.keys.ScrollTop, s.deps.keys.ScrollBottom, hk.jump, "first/last"},
	} {
		if s.loading || len(s.items) == 0 {
			break
		}
		if !pair.up.Enabled() && !pair.down.Enabled() {
			continue
		}
		marks := pair.marks
		if !pair.up.Enabled() {
			marks = firstKey(pair.down, "")
			if i == 2 {
				pair.label = "last"
			}
		}
		if !pair.down.Enabled() {
			marks = firstKey(pair.up, "")
			if i == 2 {
				pair.label = "first"
			}
		}
		navigation = append(navigation, marks+" "+pair.label)
	}
	lines := skillsTextLines(th.Style("muted"), strings.Join(actions, " · "), width)
	if len(navigation) > 0 {
		lines = append(lines, skillsTextLines(th.Style("muted"), strings.Join(navigation, " · "), width)...)
	}
	return lines
}

func (s *connectState) modalMaxOuterWidth() int {
	frame := s.deps.theme.Style("askCard").GetHorizontalFrameSize()
	width := ansi.StringWidth("Connect to remote")
	texts := []string{s.err, connectAuthHint(s.reason, s.failedTarget)}
	for _, line := range s.footerLines(128, RetryAfterCleanup) {
		width = max(width, ansi.StringWidth(line))
	}
	for _, item := range s.items {
		for _, line := range strings.Split(item.Text, "\n") {
			width = max(width, 2+ansi.StringWidth(line))
		}
	}
	texts = append(texts, s.unavailable...)
	for _, text := range texts {
		for _, line := range strings.Split(connectDisplay(text), "\n") {
			width = max(width, ansi.StringWidth(line))
		}
	}
	return min(128, width+frame)
}
func (s *connectState) modalFrame() bool { return !s.compact }

func (s *connectState) Render(width, height int) (string, []ClickableRegion) {
	offer := [2]int{width, height}
	reveal := offer != s.offer || s.list.RevealPending()
	s.compact = true
	if width <= 0 || height <= 0 {
		return "", nil
	}
	th, hk := s.deps.theme, s.deps.marks
	if width <= 2 {
		return renderSkillsCompact(th, hk, width), nil
	}
	title := skillsTextLines(th.Style("askTitle"), "Connect to remote", width)
	if s.confirm != nil {
		return s.renderConfirmation(title, width, height), nil
	}
	prefix := title
	if s.err != "" {
		prefix = append(prefix, skillsTextLines(th.Style("warning"), connectDisplay(s.err), width)...)
	}
	if hint := connectAuthHint(s.reason, s.failedTarget); hint != "" && !s.listFailed {
		prefix = append(prefix, skillsTextLines(th.Style("muted"), connectDisplay(hint), width)...)
	}
	for _, text := range s.unavailable {
		prefix = append(prefix, skillsTextLines(th.Style("warning"), text, width)...)
	}
	action := ConnectAction(0)
	if intent, ok := s.selectedIntent(); ok {
		action = intent.Action
	}
	// Reserve the longest action hint so selection cannot resize the card.
	footer := s.footerLines(width, action)
	footerRows := max(len(footer), len(s.footerLines(width, RetryAfterCleanup)))
	footer = append(footer, make([]string, footerRows-len(footer))...)
	rows := height - len(prefix) - len(footer)
	spacing := 0
	if rows >= 5 {
		spacing = 2
		rows -= spacing
	}
	list := s.list
	list.SetGeometry(width, rows, 1, bounded.Wrap)
	if !list.Valid() {
		return renderSkillsCompact(th, hk, width), nil
	}
	var state []string
	if s.loading {
		state = skillsTextLines(th.Style("muted"), "Loading saved targets…", width)
	} else if s.listFailed {
		state = []string{}
	} else if len(s.items) == 0 {
		state = skillsTextLines(th.Style("muted"), "No available saved targets", width)
	}
	if len(state) > rows {
		return renderSkillsCompact(th, hk, width), nil
	}
	s.compact = false
	s.list, s.offer = list, offer
	lines := prefix
	if spacing > 0 {
		lines = append(lines, "")
	}
	lines = s.appendInventoryRows(lines, state, width, rows, reveal)
	if spacing > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	for i := range lines {
		lines[i] = ansi.Cut(lines[i], 0, width)
	}
	return strings.Join(lines, "\n"), nil
}

func (s *connectState) renderConfirmation(title []string, width, height int) string {
	th, hk := s.deps.theme, s.deps.marks
	lines := append(title, skillsTextLines(th.Style("warning"), connectConfirmationLabel(s.confirm.Action), width)...)
	target := s.confirm.Target
	if s.confirm.Action == AddTarget {
		target = "OIDC sign-in for a new target…"
	}
	lines = append(lines, skillsTextLines(th.Style("spinner"), connectDisplay(target), width)...)
	footer := hk.closeOnly + ": back"
	if s.deps.keys.Choose.Enabled() {
		footer = hk.choose + ": " + connectActionLabel(s.confirm.Action) + " · " + footer
	}
	lines = append(lines, skillsTextLines(th.Style("muted"), footer, width)...)
	if len(lines) > height {
		return ansi.Cut(th.Style("muted").Render(hk.closeOnly+" back"), 0, width)
	}
	s.compact = false
	for i := range lines {
		lines[i] = ansi.Cut(lines[i], 0, width)
	}
	return strings.Join(lines, "\n")
}

func (s *connectState) appendInventoryRows(lines, state []string, width, rows int, reveal bool) []string {
	th := s.deps.theme
	body := state
	if state == nil {
		view := s.list.ViewWithIndicators(rows, reveal)
		if rows == 1 && (view.Above > 0 || view.Below > 0) {
			lines[0] = ansi.Cut(th.Style("askTitle").Render(fmt.Sprintf("↑ %d ↓ %d more · Connect to remote", view.Above, view.Below)), 0, width)
		} else if view.Above > 0 {
			body = append(body, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↑ %d more", view.Above)), 0, width))
		}
		for _, row := range view.Rows {
			p := presentListRow(row, th.Style("spinner"), th.Style("muted"))
			body = append(body, ansi.Cut(p.Style.Render(p.Text), 0, width))
		}
		if rows > 1 && view.Below > 0 {
			body = append(body, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↓ %d more", view.Below)), 0, width))
		}
	}
	// The inventory, rather than the selected window, determines the card height.
	natural := 0
	for _, item := range s.items {
		natural += len(skillsTextLines(th.Style("muted"), item.Text, max(1, width-2)))
	}
	bodyRows := min(rows, max(len(state), natural))
	for len(body) < bodyRows {
		body = append(body, "")
	}
	return append(lines, body...)
}

func connectAuthHint(reason client.AuthReason, target string) string {
	if reason == client.AuthNotEnrolled && target != "" {
		return "The server requires caller authentication. Use --auth-token, or, if this server supports OIDC enrollment, run mecatui login " + terminaltext.Sanitize(target) + "."
	}
	if reason == client.AuthAnonymousRejected && target != "" {
		return "The server rejected --anonymous because it requires caller authentication. Retry without --anonymous to use saved OIDC, use --auth-token, or, if supported, run mecatui login " + terminaltext.Sanitize(target) + "."
	}
	switch reason {
	case client.AuthNotEnrolled:
		return "The server requires caller authentication. Use --auth-token or, if supported, enroll with OIDC."
	case client.AuthAnonymousRejected:
		return "The server rejected --anonymous because it requires caller authentication. Retry without --anonymous to use saved OIDC, use --auth-token, or, if supported, enroll with OIDC."
	case client.AuthSessionExpired:
		return "Your session expired. Sign in again to reconnect."
	case client.AuthCredentialUnusable:
		return "The saved credential is corrupt. Sign in again to replace it."
	case client.AuthTargetChanged:
		return "This target changed while signing in (a concurrent logout or newer login). Sign in again."
	case client.AuthStorageUnavailable:
		return "Local credential storage or the saved issuer/CA trust is unavailable. Retry the connection after restoring it -- signing in again will not fix this."
	case client.AuthCredentialCleanup:
		return "Stored credential cleanup is still in progress. Retry the connection before signing in again."
	case client.AuthRejected:
		return "The server rejected this credential. Re-login is disabled for this target; check issuer, audience, and CA settings."
	default:
		return ""
	}
}

// ConnectRestartIntent reports a deliberate internal restart request. It is
// intentionally a value with only public metadata.
func (m Model) ConnectRestartIntent() (ConnectRestartIntent, bool) {
	if m.connectIntent == nil {
		return ConnectRestartIntent{}, false
	}
	return *m.connectIntent, true
}
