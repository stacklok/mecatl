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
	hitTargets                    map[HitID]connectHit
}

type connectMouseAction uint8

const (
	connectRow connectMouseAction = iota
	connectPrimary
	connectBack
)

type connectHit struct {
	action       connectMouseAction
	id, selected string
	operation    ConnectRestartIntent
	confirmation ConnectRestartIntent
	confirming   bool
}

type connectControl struct {
	rect   cellRect
	action connectMouseAction
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
	s.hitTargets = nil
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
	s.intent, s.hitTargets = nil, nil
}

func (s *connectState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	if hit, ok := msg.(surfaceHitMsg); ok {
		return nil, true, s.handleHit(hit)
	}
	result, ok := msg.(connectTargetsMsg)
	if !ok {
		return nil, false, false
	}
	if result.owner != s || result.request != s.request || !s.loading {
		return nil, true, false
	}
	s.hitTargets = nil
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

func (s *connectState) handleHit(hit surfaceHitMsg) bool {
	target, current := s.hitTargets[hit.ID]
	s.hitTargets = nil
	operation, _ := s.selectedIntent()
	var confirmation ConnectRestartIntent
	if s.confirm != nil {
		confirmation = *s.confirm
	}
	if !current || s.compact || target.selected != s.list.CursorID() || target.operation != operation || target.confirming != (s.confirm != nil) || target.confirmation != confirmation {
		return false
	}
	if target.action != connectRow {
		return s.act(target.action)
	}
	if s.loading || s.listFailed || s.confirm != nil {
		return false
	}
	for i, item := range s.items {
		if item.ID == target.id {
			s.list.SetCursor(i)
			break
		}
	}
	return false
}

func connectDisplay(text string) string {
	return terminaltext.ExpandTabs(terminaltext.Sanitize(text), terminaltext.DefaultTabWidth)
}

func (s *connectState) syncItems() {
	s.hitTargets = nil
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

func (s *connectState) canChoose() bool {
	_, available := s.selectedIntent()
	return s.deps.keys.Choose.Enabled() && !s.loading && !s.listFailed && (s.confirm != nil || available)
}

func (s *connectState) act(action connectMouseAction) bool {
	if action == connectBack && s.deps.keys.Close.Enabled() {
		if s.confirm != nil {
			s.confirm = nil
		} else {
			return true
		}
	} else if action == connectPrimary && !s.compact && s.canChoose() && s.intent == nil {
		if s.confirm != nil {
			s.intent = connectSelectIntent{restart: *s.confirm}
		} else if intent, ok := s.selectedIntent(); ok {
			s.confirm = &intent
		}
	}
	return false
}

func (s *connectState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	s.hitTargets = nil
	if key.Matches(msg, s.deps.keys.Close) {
		return nil, true, s.act(connectBack)
	}
	if s.compact {
		return nil, true, false
	}
	if s.confirm != nil {
		if key.Matches(msg, s.deps.keys.Choose) {
			s.act(connectPrimary)
		}
		return nil, true, false
	}
	if s.loading {
		return nil, true, false
	}
	if move, ok := boundedNavigationMove(msg, s.deps.keys); ok {
		s.list.Move(move)
	} else if key.Matches(msg, s.deps.keys.Choose) {
		s.act(connectPrimary)
	}
	return nil, true, false
}

func (s *connectState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	s.hitTargets = nil
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

func (s *connectState) footerControls(width int, action ConnectAction) ([]string, []connectControl) {
	back := footerCloseLabel
	if s.confirm != nil {
		back = "Back"
	}
	lines, rendered := renderFooterControls(s.deps.theme, width, []footerControlSpec{
		{hint: s.deps.marks.choose, label: connectActionLabel(action), enabled: action != 0 && s.deps.keys.Choose.Enabled()},
		{hint: s.deps.marks.closeOnly, label: back, enabled: s.deps.keys.Close.Enabled()},
	})
	controls := make([]connectControl, len(rendered))
	for i, control := range rendered {
		controls[i] = connectControl{rect: control.rect, action: [...]connectMouseAction{connectPrimary, connectBack}[control.index]}
	}
	return lines, controls
}

func (s *connectState) addHit(regions *[]ClickableRegion, rect cellRect, action connectMouseAction, id string) {
	if s.deps.hits == nil {
		return
	}
	operation, _ := s.selectedIntent()
	target := connectHit{action: action, id: id, selected: s.list.CursorID(), operation: operation, confirming: s.confirm != nil}
	if s.confirm != nil {
		target.confirmation = *s.confirm
	}
	hit := s.deps.hits.allocate()
	s.hitTargets[hit] = target
	*regions = append(*regions, ClickableRegion{rect: rect, hit: hit})
}

func (s *connectState) addControls(regions *[]ClickableRegion, controls []connectControl, y int) {
	for _, control := range controls {
		rect := control.rect
		rect.y0 += y
		rect.y1 += y
		s.addHit(regions, rect, control.action, "")
	}
}

func (s *connectState) renderCompact(width int) string {
	if !s.deps.keys.Close.Enabled() {
		return ansi.Cut(s.deps.theme.Style("muted").Render("terminal too small"), 0, width)
	}
	if s.confirm != nil {
		return ansi.Cut(s.deps.theme.Style("muted").Render(s.deps.marks.closeOnly+" back"), 0, width)
	}
	return renderSkillsCompact(s.deps.theme, s.deps.marks, width)
}

func (s *connectState) navigationLines(width int) []string {
	th, hk := s.deps.theme, s.deps.marks
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
	if len(navigation) == 0 {
		return nil
	}
	return skillsTextLines(th.Style("muted"), strings.Join(navigation, " · "), width)
}

func (s *connectState) modalMaxOuterWidth() int {
	frame := s.deps.theme.Style("askCard").GetHorizontalFrameSize()
	width := ansi.StringWidth("Connect to remote")
	texts := []string{s.err, connectAuthHint(s.reason, s.failedTarget)}
	controls, _ := s.footerControls(128, RetryAfterCleanup)
	for _, line := range append(controls, s.navigationLines(128)...) {
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
	s.hitTargets = make(map[HitID]connectHit)
	if width <= 0 || height <= 0 {
		return "", nil
	}
	if width <= 2 {
		return s.renderCompact(width), nil
	}
	title := skillsTextLines(s.deps.theme.Style("askTitle"), "Connect to remote", width)
	if s.confirm != nil {
		return s.renderConfirmation(title, width, height)
	}
	return s.renderPicker(title, width, height, offer, reveal)
}

func (s *connectState) renderPicker(title []string, width, height int, offer [2]int, reveal bool) (string, []ClickableRegion) {
	th := s.deps.theme
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
	footer, controls := s.footerControls(width, action)
	if (action != 0 && s.deps.keys.Choose.Enabled() || s.deps.keys.Close.Enabled()) && len(controls) == 0 {
		return s.renderCompact(width), nil
	}
	// Reserve wrapped controls before budgeting the list; selection must not resize it.
	reserved, _ := s.footerControls(width, RetryAfterCleanup)
	footerRows := max(len(footer), len(reserved))
	footer = append(footer, make([]string, footerRows-len(footer))...)
	help := s.navigationLines(width)
	if height-len(prefix)-len(footer)-len(help) >= 3 {
		footer = append(footer, help...)
	}
	rows := height - len(prefix) - len(footer)
	spacing := 0
	if rows >= 5 {
		spacing = 2
		rows -= spacing
	}
	list := s.list
	list.SetGeometry(width, rows, 1, bounded.Wrap)
	if !list.Valid() {
		return s.renderCompact(width), nil
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
		return s.renderCompact(width), nil
	}
	s.compact = false
	s.list, s.offer = list, offer
	lines := prefix
	if spacing > 0 {
		lines = append(lines, "")
	}
	var regions []ClickableRegion
	lines = s.appendInventoryRows(lines, state, width, rows, reveal, &regions)
	if spacing > 0 {
		lines = append(lines, "")
	}
	s.addControls(&regions, controls, len(lines))
	lines = append(lines, footer...)
	for i := range lines {
		lines[i] = ansi.Cut(lines[i], 0, width)
	}
	return strings.Join(lines, "\n"), regions
}

func (s *connectState) renderConfirmation(title []string, width, height int) (string, []ClickableRegion) {
	th := s.deps.theme
	lines := append(title, skillsTextLines(th.Style("warning"), connectConfirmationLabel(s.confirm.Action), width)...)
	target := s.confirm.Target
	if s.confirm.Action == AddTarget {
		target = "OIDC sign-in for a new target…"
	}
	lines = append(lines, skillsTextLines(th.Style("spinner"), connectDisplay(target), width)...)
	action := ConnectAction(0)
	if s.canChoose() {
		action = s.confirm.Action
	}
	footer, controls := s.footerControls(width, action)
	if len(lines)+len(footer) > height || ((s.canChoose() || s.deps.keys.Close.Enabled()) && len(controls) == 0) {
		return s.renderCompact(width), nil
	}
	var regions []ClickableRegion
	s.addControls(&regions, controls, len(lines))
	lines = append(lines, footer...)
	s.compact = false
	for i := range lines {
		lines[i] = ansi.Cut(lines[i], 0, width)
	}
	return strings.Join(lines, "\n"), regions
}

func (s *connectState) appendInventoryRows(lines, state []string, width, rows int, reveal bool, regions *[]ClickableRegion) []string {
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
			text := ansi.Cut(p.Style.Render(p.Text), 0, width)
			y := len(lines) + len(body)
			s.addHit(regions, cellRect{x0: 0, x1: ansi.StringWidth(text), y0: y, y1: y + 1}, connectRow, row.ID)
			body = append(body, text)
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
