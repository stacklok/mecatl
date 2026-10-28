package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

type reflectionsView uint8

const (
	reflectionsList reflectionsView = iota
	reflectionsDetail
	reflectionsConfirm
)

const (
	reflectionActionApprove = "approve"
	reflectionActionReject  = "reject"
	reflectionActionUndo    = "undo"
	reflectionActionOpen    = "open"
	reflectionActionRetry   = "retry"
	reflectionActionCancel  = "cancel"
)

type reflectionsState struct {
	deps                            surfaceDeps
	client                          client.ReflectionClient
	workspace                       string
	view                            reflectionsView
	closed, loading, pending, stale bool
	err                             error
	page                            client.LearningProposalPage
	cursors, destination            client.ReflectionCursors
	previous                        []client.ReflectionCursors
	pageDirection                   int
	list                            bounded.List
	detail                          *client.LearningProposal
	target                          client.LearningProposal
	details, confirmation           bounded.Viewport
	confirm                         *reflectionConfirmation
	read, mutation                  uint64
	readKind                        string
	compact                         bool                    // view cache: refreshed on every offer without discarding bounded offsets
	detailRows, confirmationRows    int                     // view cache: physical laid-out content counts
	hitTargets                      map[HitID]reflectionHit // view cache: same-pass, frame-scoped controls
}

type reflectionConfirmation struct {
	proposal client.LearningProposal
	action   string
}

type reflectionHit struct {
	action, identity, version string
	view                      reflectionsView
}

// Browser commands retain their public client contracts; only this open instance
// may consume their results. Reads and submitted mutations have separate lifetimes.
type reflectionsResultMsg struct {
	owner          *reflectionsState
	request        uint64
	kind, identity string
	result         tea.Msg
}

type reflectionExecution struct{ sessionID string }
type reflectionExecutionMsg struct {
	request *reflectionExecution
	result  tea.Msg
}

func (m Model) openReflections() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.modal != nil || m.deps.Reflections == nil || !m.caps.LearningProposals {
		return m, nil
	}
	s := &reflectionsState{deps: (&m).surfaceDeps(), client: m.deps.Reflections, workspace: m.deps.Workspace, compact: true}
	m.modal = s
	m.prompt.Blur()
	return m, s.listCmd(client.ReflectionCursors{}, 0)
}

func (m Model) runReflect() (tea.Model, tea.Cmd) {
	if !m.caps.Reflection || m.phase != phaseIdle || m.deps.Reflections == nil || m.sessionID == "" || m.conv.isEmpty() {
		m.statusMsg = m.deps.Theme.Style("warning").Render("/reflect needs the current completed session; finish a turn or select an available completed session")
		return m, nil
	}
	r := &reflectionExecution{sessionID: m.sessionID}
	m.reflectRequest = r
	m.statusMsg = m.deps.Theme.Style("muted").Render("reflecting session…")
	cmd := client.ReflectSessionCmd(m.deps.Ctx, m.deps.Reflections, m.sessionID, 0)
	return m, func() tea.Msg { return reflectionExecutionMsg{request: r, result: cmd()} }
}

func (m Model) updateReflectMsg(msg tea.Msg) (tea.Model, bool) {
	r, ok := msg.(reflectionExecutionMsg)
	if !ok {
		return m, false
	}
	if r.request == nil || r.request != m.reflectRequest || r.request.sessionID != m.sessionID {
		return m, true
	}
	x, ok := r.result.(client.ReflectionMsg)
	if !ok || x.RequestID != r.request.sessionID || x.Proposal != nil {
		return m, true
	}
	m.reflectRequest = nil
	if x.Err != nil {
		m.statusMsg = m.deps.Theme.Style("errorText").Render(client.ReflectionErrorText(x.Err))
	} else if x.Receipt != nil {
		if x.Receipt.Abstained {
			message := x.Receipt.Message
			if message == "" {
				message = "reflection abstained"
			}
			m.statusMsg = m.deps.Theme.Style("muted").Render(reflectionDisplayText(message, 160))
		} else {
			count := x.Receipt.Staged + x.Receipt.Promoted + x.Receipt.Conflicted
			label := "proposals"
			if count == 1 {
				label = "proposal"
			}
			m.statusMsg = m.deps.Theme.Style("success").Render(fmt.Sprintf("reflection completed: %d %s", count, label))
		}
	}
	return m, true
}

func (*reflectionsState) modalPlacement() modalPlacement { return modalPlacementFill }
func (s *reflectionsState) Close()                       { s.closed = true; s.read++; s.hitTargets = nil }

func reflectionIdentity(p client.LearningProposal) string {
	return fmt.Sprintf("%t:%q:%q", p.ProjectScoped, p.Project, p.ID)
}

func (s *reflectionsState) wrap(cmd tea.Cmd, kind, identity string, request uint64) tea.Cmd {
	return func() tea.Msg {
		return reflectionsResultMsg{owner: s, request: request, kind: kind, identity: identity, result: cmd()}
	}
}
func (s *reflectionsState) listCmd(destination client.ReflectionCursors, direction int) tea.Cmd {
	s.read++
	s.loading, s.err, s.readKind = true, nil, "list"
	s.destination, s.pageDirection = destination, direction
	return s.wrap(client.ListReflectionsCmd(s.deps.ctx, s.client, "", destination, s.workspace, s.read), "list", "", s.read)
}
func (s *reflectionsState) detailCmd(p client.LearningProposal) tea.Cmd {
	s.read++
	s.loading, s.err, s.readKind = true, nil, "detail"
	s.target, s.view, s.confirm = p, reflectionsDetail, nil
	return s.wrap(client.GetReflectionCmd(s.deps.ctx, s.client, p.ID, p.Project, s.read), "detail", reflectionIdentity(p), s.read)
}
func (s *reflectionsState) syncList() {
	items := make([]bounded.ListItem, 0, len(s.page.Proposals))
	for _, p := range s.page.Proposals {
		summary := p.Key
		if summary == "" {
			summary = p.Title
		}
		text := p.Status + "  " + summary
		items = append(items, bounded.ListItem{ID: reflectionIdentity(p), Text: terminaltext.ExpandTabs(terminaltext.Sanitize(text), terminaltext.DefaultTabWidth)})
	}
	s.list.SetItems(items)
}
func (s *reflectionsState) selected() (client.LearningProposal, bool) {
	for _, p := range s.page.Proposals {
		if reflectionIdentity(p) == s.list.CursorID() {
			return p, true
		}
	}
	return client.LearningProposal{}, false
}

func (s *reflectionsState) handleHit(hit surfaceHitMsg) (tea.Cmd, bool, bool) {
	target, exists := s.hitTargets[hit.ID]
	s.hitTargets = nil
	if !exists || s.closed || s.compact || target.view != s.view {
		return nil, true, false
	}
	if target.action == "select" {
		if !s.loading && !s.pending {
			for i, p := range s.page.Proposals {
				if reflectionIdentity(p) == target.identity {
					s.list.SetCursor(i)
					break
				}
			}
		}
		return nil, true, false
	}
	if target.identity != "" && (s.detail == nil || reflectionIdentity(*s.detail) != target.identity || s.detail.Version != target.version) {
		return nil, true, false
	}
	cmd, closed := s.action(target.action)
	return cmd, true, closed
}

func (s *reflectionsState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	if hit, ok := msg.(surfaceHitMsg); ok {
		return s.handleHit(hit)
	}
	r, ok := msg.(reflectionsResultMsg)
	if !ok {
		return nil, false, false
	}
	if s.closed || r.owner != s {
		return nil, true, false
	}
	s.hitTargets = nil
	if r.kind == "list" {
		return s.handleListResult(r)
	}
	return s.handleProposalResult(r)
}

func (s *reflectionsState) handleListResult(r reflectionsResultMsg) (tea.Cmd, bool, bool) {
	if !s.loading || r.request != s.read || s.readKind != "list" || s.view != reflectionsList || r.identity != "" {
		return nil, true, false
	}
	x, valid := r.result.(client.ReflectionsMsg)
	if !valid || x.Generation != r.request {
		return nil, true, false
	}
	s.loading, s.err = false, x.Err
	if x.Err == nil {
		changed := s.destination != s.cursors
		switch s.pageDirection {
		case 1:
			s.previous = append(s.previous, s.cursors)
		case -1:
			s.previous = s.previous[:len(s.previous)-1]
		}
		s.cursors, s.page = s.destination, x.Page
		s.pageDirection = 0
		if changed {
			s.list = bounded.List{}
		}
		s.syncList()
	}
	return nil, true, false
}

func (s *reflectionsState) acceptsProposalResult(r reflectionsResultMsg, mutation bool) bool {
	if mutation {
		return s.pending && r.request == s.mutation && s.confirm != nil && r.kind == s.confirm.action && r.identity == reflectionIdentity(s.confirm.proposal)
	}
	return r.kind == "detail" && s.loading && r.request == s.read && s.readKind == "detail" && s.view == reflectionsDetail && r.identity == reflectionIdentity(s.target)
}

func (s *reflectionsState) handleProposalResult(r reflectionsResultMsg) (tea.Cmd, bool, bool) {
	mutation := r.kind == reflectionActionApprove || r.kind == reflectionActionReject || r.kind == reflectionActionUndo
	if !s.acceptsProposalResult(r, mutation) {
		return nil, true, false
	}
	x, valid := r.result.(client.ReflectionMsg)
	if !valid || x.Generation != r.request || x.RequestID != s.target.ID || x.Receipt != nil {
		return nil, true, false
	}
	if x.Err == nil && (x.Proposal == nil || reflectionIdentity(*x.Proposal) != r.identity || x.Proposal.Kind != s.target.Kind) {
		return nil, true, false
	}
	if mutation {
		s.pending = false
		s.read++
		s.confirm = nil
		s.view = reflectionsDetail
	} else {
		s.loading = false
	}
	s.err = x.Err
	if x.Err != nil {
		if mutation {
			s.stale = true
		}
		return nil, true, false
	}
	p := *x.Proposal
	s.detail, s.target, s.stale = &p, p, false
	s.details.Reset()
	for i, row := range s.page.Proposals {
		if reflectionIdentity(row) == r.identity {
			s.page.Proposals[i] = p
		}
	}
	s.syncList()
	return nil, true, false
}

func reflectionApprovable(p client.LearningProposal) bool {
	if (p.Status != client.ProposalStatusStaged && p.Status != client.ProposalStatusDeferred) || len(p.Evidence) == 0 || !p.PromotionAvailable {
		return false
	}
	for _, evidence := range p.Evidence {
		if !evidence.Available {
			return false
		}
	}
	return true
}
func reflectionEligible(p client.LearningProposal, action string) bool {
	switch action {
	case reflectionActionApprove:
		return reflectionApprovable(p)
	case reflectionActionReject:
		return p.Status == client.ProposalStatusStaged
	case reflectionActionUndo:
		return p.Status == client.ProposalStatusPromoted && p.PromotionAvailable
	}
	return false
}
func (s *reflectionsState) back() bool {
	if s.compact || s.pending {
		return true
	}
	if s.view == reflectionsConfirm {
		s.confirm = nil
		s.view = reflectionsDetail
		return false
	}
	if s.view == reflectionsDetail {
		s.read++
		s.loading = false
		s.err = nil
		s.detail = nil
		s.confirm = nil
		s.view = reflectionsList
		return false
	}
	return true
}

func (s *reflectionsState) action(action string) (tea.Cmd, bool) {
	switch action {
	case "close", "back", reflectionActionCancel:
		return nil, s.back()
	}
	if s.compact || s.closed || s.pending || s.loading || !s.deps.caps.LearningProposals {
		return nil, false
	}
	if s.view == reflectionsList {
		return s.listAction(action), false
	}
	switch action {
	case reflectionActionRetry:
		if s.view == reflectionsDetail && (s.err != nil || s.stale) {
			return s.detailCmd(s.target), false
		}
	case reflectionActionApprove, reflectionActionReject, reflectionActionUndo:
		if s.view == reflectionsDetail && s.detail != nil && !s.stale && reflectionEligible(*s.detail, action) {
			s.confirm = &reflectionConfirmation{proposal: *s.detail, action: action}
			s.view = reflectionsConfirm
			s.confirmation.Reset()
		}
	case "confirm":
		return s.submitConfirmation(), false
	}
	return nil, false
}

func (s *reflectionsState) submitConfirmation() tea.Cmd {
	if s.view != reflectionsConfirm || s.confirm == nil || s.detail == nil || s.stale {
		return nil
	}
	c, p := s.confirm, *s.detail
	if reflectionIdentity(p) != reflectionIdentity(c.proposal) || p.Version != c.proposal.Version || !reflectionEligible(p, c.action) {
		s.confirm = nil
		s.view = reflectionsDetail
		return nil
	}
	s.read++
	s.loading = false
	s.pending = true
	s.mutation++
	s.err = nil
	var cmd tea.Cmd
	if c.action == reflectionActionUndo {
		cmd = client.UndoReflectionCmd(s.deps.ctx, s.client, p, p.Project, s.mutation)
	} else {
		cmd = client.DecideReflectionCmd(s.deps.ctx, s.client, p, c.action, p.Project, s.mutation)
	}
	return s.wrap(cmd, c.action, reflectionIdentity(p), s.mutation)
}

func (s *reflectionsState) listAction(action string) tea.Cmd {
	switch action {
	case reflectionActionOpen:
		if p, ok := s.selected(); ok {
			s.detail = nil
			s.details.Reset()
			return s.detailCmd(p)
		}
	case "next":
		if (!s.page.OperatorDone || !s.page.ProjectDone) && s.err == nil {
			return s.listCmd(client.ReflectionCursors{Operator: s.page.OperatorNextCursor, Project: s.page.ProjectNextCursor, OperatorDone: s.page.OperatorDone, ProjectDone: s.page.ProjectDone}, 1)
		}
	case "previous":
		if len(s.previous) > 0 && s.err == nil {
			return s.listCmd(s.previous[len(s.previous)-1], -1)
		}
	case reflectionActionRetry:
		if s.err != nil {
			return s.listCmd(s.destination, s.pageDirection)
		}
	}
	return nil
}

func (s *reflectionsState) move(move bounded.Move, wheel bool) {
	switch s.view {
	case reflectionsList:
		if wheel {
			s.list.Scroll(move)
		} else {
			s.list.Move(move)
		}
	case reflectionsDetail:
		s.details.Move(move, s.detailRows)
	case reflectionsConfirm:
		s.confirmation.Move(move, s.confirmationRows)
	}
}
func (s *reflectionsState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	s.hitTargets = nil
	if key.Matches(msg, s.deps.keys.Close) {
		return nil, true, s.back()
	}
	if s.compact || s.closed {
		return nil, true, false
	}
	if move, ok := boundedNavigationMove(msg, s.deps.keys); ok {
		s.move(move, false)
		return nil, true, false
	}
	if key.Matches(msg, s.deps.keys.Choose) {
		action := reflectionActionOpen
		if s.view == reflectionsConfirm {
			action = "confirm"
		}
		cmd, closed := s.action(action)
		return cmd, true, closed
	}
	actions := map[string]string{"a": reflectionActionApprove, "x": reflectionActionReject, "u": reflectionActionUndo, "r": reflectionActionRetry}
	if s.view == reflectionsList {
		actions = map[string]string{"n": "next", "p": "previous", "r": reflectionActionRetry}
	}
	cmd, closed := s.action(actions[msg.String()])
	return cmd, true, closed
}
func (s *reflectionsState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	s.hitTargets = nil
	if move, ok := boundedWheelMove(msg); ok && !s.compact {
		s.move(move, true)
	}
	return nil, true
}

func (s *reflectionsState) reserved(chord string, choose bool) bool {
	bindings := []key.Binding{s.deps.keys.Close, s.deps.keys.Up, s.deps.keys.Down, s.deps.keys.ScrollU, s.deps.keys.ScrollD, s.deps.keys.ScrollTop, s.deps.keys.ScrollBottom}
	if choose {
		bindings = append(bindings, s.deps.keys.Choose)
	}
	for _, binding := range bindings {
		if binding.Enabled() {
			for _, k := range binding.Keys() {
				if k == chord {
					return true
				}
			}
		}
	}
	return false
}
func (s *reflectionsState) chooseHint() string {
	if s.deps.keys.Choose.Enabled() {
		for _, chord := range s.deps.keys.Choose.Keys() {
			if !s.reserved(chord, false) {
				return chord
			}
		}
	}
	return ""
}

type reflectionControl struct{ label, action, chord string }

func (s *reflectionsState) controls() []reflectionControl {
	closeControl := reflectionControl{"Close", "close", s.deps.marks.closeOnly}
	if s.pending || !s.deps.caps.LearningProposals {
		return []reflectionControl{closeControl}
	}
	switch s.view {
	case reflectionsList:
		return append(s.listControls(), closeControl)
	case reflectionsDetail:
		return append(s.detailControls(), reflectionControl{"Back", "back", s.deps.marks.closeOnly})
	case reflectionsConfirm:
		return []reflectionControl{{"Confirm", "confirm", s.chooseHint()}, {"Cancel", reflectionActionCancel, s.deps.marks.closeOnly}}
	}
	return []reflectionControl{closeControl}
}

func (s *reflectionsState) listControls() []reflectionControl {
	var out []reflectionControl
	if s.loading {
		return out
	}
	if _, ok := s.selected(); ok {
		out = append(out, reflectionControl{"Open", reflectionActionOpen, s.chooseHint()})
	}
	if s.err != nil {
		return append(out, reflectionControl{"Retry", reflectionActionRetry, "r"})
	}
	if !s.page.OperatorDone || !s.page.ProjectDone {
		out = append(out, reflectionControl{"Next", "next", "n"})
	}
	if len(s.previous) > 0 {
		out = append(out, reflectionControl{"Previous", "previous", "p"})
	}
	return out
}

func (s *reflectionsState) detailControls() []reflectionControl {
	var out []reflectionControl
	if s.loading {
		return out
	}
	if s.err != nil || s.stale {
		out = append(out, reflectionControl{"Refresh", reflectionActionRetry, "r"})
	}
	if s.detail != nil && !s.stale {
		for _, c := range []reflectionControl{{"Approve", reflectionActionApprove, "a"}, {"Reject", reflectionActionReject, "x"}, {"Undo", reflectionActionUndo, "u"}} {
			if reflectionEligible(*s.detail, c.action) {
				out = append(out, c)
			}
		}
	}
	return out
}

func (s *reflectionsState) controlLabel(c reflectionControl) string {
	chord := c.chord
	switch c.action {
	case reflectionActionApprove, reflectionActionReject, reflectionActionUndo, "next", "previous", reflectionActionRetry:
		if s.reserved(chord, true) {
			chord = ""
		}
	}
	if chord != "" {
		return "[" + c.label + " " + chord + "]"
	}
	return "[" + c.label + "]"
}

// Controls and their hit boxes are laid out together; the root publishes only the
// regions returned by View, never a geometry-only input preparation pass.
func (s *reflectionsState) controlRows(width int) ([]string, []ClickableRegion) {
	var rows []string
	var regions []ClickableRegion
	x, y := 0, 0
	for _, c := range s.controls() {
		parts := reflectionPhysicalRows([]string{s.controlLabel(c)}, width)
		if x > 0 && (len(parts) > 1 || x+ansi.StringWidth(parts[0]) > width) {
			x = 0
			y++
		}
		for i, part := range parts {
			if i > 0 {
				x = 0
				y++
			}
			for len(rows) <= y {
				rows = append(rows, "")
			}
			w := ansi.StringWidth(part)
			rows[y] += part
			if s.deps.hits != nil && w > 0 {
				id := s.deps.hits.allocate()
				target := reflectionHit{action: c.action, view: s.view}
				if s.detail != nil && s.view != reflectionsList {
					target.identity, target.version = reflectionIdentity(*s.detail), s.detail.Version
				}
				s.hitTargets[id] = target
				regions = append(regions, ClickableRegion{rect: cellRect{x0: x, x1: x + w, y0: y, y1: y + 1}, hit: id})
			}
			x += w
		}
		if x < width {
			rows[y] += " "
			x++
		}
	}
	return rows, regions
}

func (s *reflectionsState) navigationHint() string {
	used := map[string]bool{}
	if s.deps.keys.Close.Enabled() {
		for _, chord := range s.deps.keys.Close.Keys() {
			used[chord] = true
		}
	}
	var hints []string
	for _, nav := range []struct {
		binding key.Binding
		label   string
	}{
		{s.deps.keys.Up, "up"}, {s.deps.keys.Down, "down"},
		{s.deps.keys.ScrollU, "page up"}, {s.deps.keys.ScrollD, "page down"},
		{s.deps.keys.ScrollTop, "top"}, {s.deps.keys.ScrollBottom, "end"},
	} {
		if !nav.binding.Enabled() {
			continue
		}
		for _, chord := range nav.binding.Keys() {
			if !used[chord] {
				hints = append(hints, chord+": "+nav.label)
				break
			}
		}
		for _, chord := range nav.binding.Keys() {
			used[chord] = true
		}
	}
	return strings.Join(hints, "  ")
}

//nolint:gocyclo // one measured layout pass owns list/detail geometry and frame-local hits
func (s *reflectionsState) Render(width, height int) (string, []ClickableRegion) {
	s.compact = true
	s.hitTargets = make(map[HitID]reflectionHit)
	if width <= 0 || height <= 0 {
		return "", nil
	}
	th := s.deps.theme
	title := "Reflections"
	if s.view == reflectionsConfirm {
		title += " — confirm " + s.confirm.action
	}
	prefix := reflectionStyledRows(th.Style("toolName"), []string{title}, width)
	var supporting []string
	if !s.deps.caps.LearningProposals {
		supporting = append(supporting, "proposal review is not supported by this server")
	}
	if s.loading {
		supporting = append(supporting, "loading…")
	}
	if s.pending {
		supporting = append(supporting, "operation pending (closing does not undo it)")
	}
	prefix = append(prefix, reflectionStyledRows(th.Style("muted"), supporting, width)...)
	if s.err != nil {
		prefix = append(prefix, reflectionStyledRows(th.Style("errorText"), []string{client.ReflectionErrorText(s.err)}, width)...)
	}
	if s.stale {
		prefix = append(prefix, reflectionStyledRows(th.Style("muted"), []string{"Refresh authoritative detail before another decision; the last action may not have completed."}, width)...)
	}
	footer, controls := s.controlRows(width)
	navigation := reflectionPhysicalRows([]string{s.navigationHint()}, width)
	footer = append(footer, navigation...)
	// Overflow counters are a single clipped physical row, not unmeasured chrome.
	indicator := []string{""}
	rows := height - len(prefix) - len(footer) - len(indicator)
	listGap := 0
	if s.view == reflectionsList && rows >= 5 {
		listGap = 1
		prefix = append(prefix, "")
		footer = append([]string{""}, footer...)
		rows -= 2
	}
	if width < 4 || rows < 1 {
		s.hitTargets = nil
		return renderSkillsCompact(s.deps.theme, s.deps.marks, width), nil
	}
	var content []string
	var regions []ClickableRegion
	above, below := 0, 0
	if !s.deps.caps.LearningProposals {
		content = nil
	} else if s.view == reflectionsList {
		s.syncList()
		s.list.SetGeometry(width, rows, 1, bounded.Wrap)
		view := s.list.View()
		above, below = view.Above, view.Below
		for _, row := range view.Rows {
			p := presentListRow(row, s.deps.theme.Style("spinner"), s.deps.theme.Style("muted"))
			y := len(prefix) + len(content)
			content = append(content, ansi.Cut(p.Style.Render(p.Text), 0, width))
			if s.deps.hits != nil && !s.loading && !s.pending {
				id := s.deps.hits.allocate()
				s.hitTargets[id] = reflectionHit{action: "select", identity: row.ID, view: s.view}
				regions = append(regions, ClickableRegion{rect: cellRect{x0: 0, x1: width, y0: y, y1: y + 1}, hit: id})
			}
		}
		if len(content) == 0 && !s.loading && s.err == nil {
			content = reflectionStyledRows(th.Style("muted"), []string{"No proposals."}, width)
			content = content[:min(rows, len(content))]
		}
	} else {
		var source []string
		viewport, count := &s.details, &s.detailRows
		if s.detail != nil {
			source = reflectionDetailLines(*s.detail)
		}
		if s.view == reflectionsConfirm {
			source = append(reflectionEffectLines(*s.confirm), source...)
			viewport, count = &s.confirmation, &s.confirmationRows
		}
		lines := reflectionPhysicalRows(source, width)
		viewport.SetGeometry(width, rows, 0, bounded.Clip)
		*count = len(lines)
		view := viewport.View(lines)
		content, above, below = view.Rows, view.Above, view.Below
		for i := range content {
			content[i] = th.Style("viewport").Render(content[i])
		}
	}
	// Counters are clipped to the already measured indicator budget, not allowed
	// to wrap an extra row when their number of digits grows.
	indicator[0] = th.Style("muted").Render(ansi.Cut(fmt.Sprintf("↑ %d · ↓ %d more", above, below), 0, width))
	for i := range footer {
		footer[i] = th.Style("muted").Render(footer[i])
	}
	for len(content) < rows {
		content = append(content, "")
	}
	footerY := len(prefix) + len(content) + len(indicator) + listGap
	for _, region := range controls {
		region.rect.y0 += footerY
		region.rect.y1 += footerY
		regions = append(regions, region)
	}
	s.compact = false
	body := append(prefix, content...)
	body = append(body, indicator...)
	body = append(body, footer...)
	return lipgloss.NewStyle().Height(height).Render(strings.Join(body, "\n")), regions
}

func reflectionStyledRows(style lipgloss.Style, lines []string, width int) []string {
	rows := reflectionPhysicalRows(lines, width)
	for i := range rows {
		rows[i] = style.Render(rows[i])
	}
	return rows
}

func reflectionPhysicalRows(lines []string, width int) []string {
	var rows []string
	for _, line := range lines {
		line = terminaltext.ExpandTabs(terminaltext.Sanitize(line), terminaltext.DefaultTabWidth)
		for _, row := range strings.Split(ansi.Hardwrap(line, width, true), "\n") {
			rows = append(rows, ansi.Cut(row, 0, width))
		}
	}
	return rows
}

func reflectionDetailLines(p client.LearningProposal) []string {
	lines := []string{"id: " + p.ID, "status: " + p.Status, "version: " + p.Version, "kind: " + p.Kind}
	if p.ProjectScoped {
		lines = append(lines, "scope: trusted project")
	} else {
		lines = append(lines, "scope: operator")
	}
	if p.Project != "" {
		lines = append(lines, "request project: "+p.Project)
	}
	for _, field := range [][2]string{{"key", p.Key}, {"title", p.Title}, {"value", p.Value}, {"description", p.Description}, {"body", p.Body}} {
		if field[1] != "" {
			lines = append(lines, field[0]+": "+field[1])
		}
	}
	if len(p.Triggers) > 0 {
		lines = append(lines, "triggers: "+strings.Join(p.Triggers, ", "))
	}
	lines = append(lines, fmt.Sprintf("evidence: %d  decisions: %d", len(p.Evidence), len(p.Decisions)))
	for _, e := range p.Evidence {
		lines = append(lines, reflectionEvidenceLines(e)...)
	}
	for _, d := range p.Decisions {
		lines = append(lines, "decision: "+d.Kind+" — "+d.Reason)
	}
	if p.Promotion != nil {
		lines = append(lines, "receipt: "+p.Promotion.MemoryKey+" @ "+p.Promotion.ResultVersion)
	}
	if p.LearnedSkillID != "" {
		lines = append(lines, "learned skill: "+p.LearnedSkillID+" (open /skills to inspect versions)")
	}
	if p.Status == client.ProposalStatusStaged || p.Status == client.ProposalStatusDeferred {
		if reflectionApprovable(p) {
			if p.Kind == "procedure" {
				lines = append(lines, "materialize and evaluate learned-skill draft")
			} else {
				lines = append(lines, "approve exact key/value/description above")
			}
		} else {
			reason := p.PromotionUnavailableReason
			if reason == "" {
				reason = "evidence unavailable or changed, or target unavailable"
			}
			lines = append(lines, "approval disabled: "+reason)
		}
	}
	if p.Status == client.ProposalStatusPromoted && !p.PromotionAvailable {
		reason := p.PromotionUnavailableReason
		if reason == "" {
			reason = "memory target unavailable"
		}
		lines = append(lines, "undo disabled: "+reason)
	}
	return lines
}
func reflectionEvidenceLines(e client.LearningEvidence) []string {
	state := "unavailable"
	if e.Available {
		state = "available"
	} else if e.Availability != "" {
		state += " (" + e.Availability + ")"
	}
	line := fmt.Sprintf("  %s:%d  %s  source=%s", e.Locator, e.Ordinal, state, e.SessionID)
	if e.EventSeq != 0 {
		line += fmt.Sprintf(" seq=%d", e.EventSeq)
	}
	if e.ToolCallID != "" {
		line += " call=" + e.ToolCallID
	}
	lines := []string{line, "    digest=" + e.Digest}
	if e.Preview != "" {
		lines = append(lines, "    preview: "+boundedReflectionPreview(e.Preview))
	}
	return lines
}

func reflectionEffectLines(c reflectionConfirmation) []string {
	p := c.proposal
	lines := []string{"Confirm " + c.action + " proposal " + p.ID + " at displayed version " + p.Version + "?"}
	switch c.action {
	case reflectionActionApprove:
		if p.Kind == "procedure" {
			lines = append(lines, "Materialize and evaluate a learned-skill draft from the procedure below.")
		} else {
			lines = append(lines, "Write the exact memory key, value, and description shown below.")
		}
	case reflectionActionReject:
		lines = append(lines, "Reject this staged proposal.")
	case reflectionActionUndo:
		lines = append(lines, "Request undo of the promotion; the server checks the current target revision.")
		if r := p.Promotion; r != nil {
			lines = append(lines, "promotion key: "+r.MemoryKey, "result version: "+r.ResultVersion)
			if r.PreviousExists {
				lines = append(lines, "restore previous revision: "+r.PreviousVersion)
			} else {
				lines = append(lines, "receipt records no previous value")
			}
		} else {
			lines = append(lines, "promotion restoration receipt unavailable")
		}
	}
	return append(lines, "Closing after submission does not cancel the request.", "")
}

func boundedReflectionPreview(value string) string {
	value = terminaltext.Sanitize(value)
	if len(value) <= 1024 {
		return value
	}
	value = value[:1024]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func reflectionDisplayText(value string, limit int) string {
	return truncate(terminaltext.Sanitize(strings.Join(strings.Fields(value), " ")), limit)
}
