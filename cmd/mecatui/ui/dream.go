package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

type dreamView int

const (
	dreamTargets dreamView = iota
	dreamGenerating
	dreamReview
	dreamConfirmApply
	dreamConfirmDismiss
	dreamConfirmRegenerate
	dreamDeciding
	dreamReceipt
)

type dreamState struct {
	view           dreamView
	target         int
	plan           *client.DreamPlan
	receipt        *client.DreamReceipt
	err            error
	viewport       *bounded.Viewport
	list           *bounded.List
	hitTargets     map[HitID]int // view cache: enabled target rows in the current Render frame
	decision       string
	regenerateFrom dreamView
	requestID      uint64
	deps           surfaceDeps
	client         client.DreamClient
	closed         bool
	compact        bool
	// view cache: last parent's card-content offer and reader row count, refreshed by Render.
	width, height, readerRows int
}

type dreamResultMsg struct {
	owner     *dreamState
	phase     dreamView
	requestID uint64
	result    client.DreamMsg
}

func (s *dreamState) setSurfacePresentation(p surfacePresentation) { s.deps.refreshPresentation(p) }

func (*dreamState) modalMaxOuterWidth() int { return 128 }
func (s *dreamState) modalFrame() bool      { return !s.compact }

func (m Model) openDream() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.modal != nil || m.deps.Dream == nil || m.caps.ManualDream == nil {
		return m, nil
	}
	m.prompt.Blur()
	selected := -1
	if m.caps.ManualDream.ProjectMemory.Generate {
		selected = 0
	} else if m.caps.ManualDream.UserModel.Generate {
		selected = 1
	}
	m.modal = &dreamState{view: dreamTargets, target: selected, deps: (&m).surfaceDeps(), client: m.deps.Dream}
	return m, nil
}

func (s *dreamState) dreamTarget() (string, client.DreamTargetCapability) {
	if s.target == 1 {
		return client.DreamTargetUserModel, s.deps.caps.ManualDream.UserModel
	}
	return client.DreamTargetProjectMemory, s.deps.caps.ManualDream.ProjectMemory
}

func (s *dreamState) dreamCmd(phase dreamView, decision string) tea.Cmd {
	s.requestID++
	request := s.requestID
	var cmd tea.Cmd
	if phase == dreamGenerating {
		target, _ := s.dreamTarget()
		cmd = client.GenerateDreamPlanCmd(s.deps.ctx, s.client, target, request, request)
	} else {
		cmd = client.DecideDreamPlanCmd(s.deps.ctx, s.client, s.plan.ID, decision, request, request)
	}
	return func() tea.Msg {
		return dreamResultMsg{owner: s, phase: phase, requestID: request, result: cmd().(client.DreamMsg)}
	}
}

func (s *dreamState) canGenerate() bool {
	if s.target < 0 || s.deps.caps.ManualDream == nil {
		return false
	}
	_, capability := s.dreamTarget()
	return capability.Generate
}

func (s *dreamState) generateDream() tea.Cmd {
	if !s.canGenerate() {
		return nil
	}
	s.view = dreamGenerating
	s.err = nil
	return s.dreamCmd(dreamGenerating, "")
}

// handled consumes input; closed requests generic teardown, not an internal back step.
//
//nolint:gocyclo // the explicit review/confirmation state machine is intentionally visible
func (s *dreamState) HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	if s.compact {
		return nil, true, key.Matches(msg, s.deps.keys.Close)
	}
	if key.Matches(msg, s.deps.keys.Close) {
		switch s.view {
		case dreamConfirmApply, dreamConfirmDismiss:
			s.view = dreamReview
		case dreamConfirmRegenerate:
			s.view = s.regenerateFrom
			if s.view != dreamReview && s.view != dreamReceipt {
				s.view = dreamReceipt
			}
		case dreamReview, dreamReceipt:
			s.view = dreamTargets
			s.plan, s.receipt, s.err = nil, nil, nil
		default:
			return nil, true, true
		}
		return nil, true, false
	}
	switch s.view {
	case dreamTargets:
		if key.Matches(msg, s.deps.keys.Up) || key.Matches(msg, s.deps.keys.Down) {
			caps := s.deps.caps.ManualDream
			if caps != nil && caps.ProjectMemory.Generate && caps.UserModel.Generate {
				s.target = 1 - s.target
				if s.list != nil {
					s.list.SetCursor(s.target)
				}
			}
		} else if key.Matches(msg, s.deps.keys.Choose) {
			return s.generateDream(), true, false
		}
	case dreamReview:
		switch {
		case s.moveKey(msg):
		case msg.String() == "a":
			_, capability := s.dreamTarget()
			if capability.Decide {
				s.view = dreamConfirmApply
			}
		case msg.String() == "x":
			_, capability := s.dreamTarget()
			if capability.Decide {
				s.view = dreamConfirmDismiss
			}
		case msg.String() == "r" && s.canGenerate():
			s.regenerateFrom, s.view = dreamReview, dreamConfirmRegenerate
		}
	case dreamReceipt:
		failed := s.receipt != nil && (s.receipt.Conflicted > 0 || s.receipt.Failed > 0)
		errorKind := client.ClassifyDreamDecisionError(s.err)
		retryable := errorKind == client.DreamDecisionInProgress || errorKind == client.DreamDecisionUnknown
		regenerable := errorKind == client.DreamDecisionPlanGone || errorKind == client.DreamDecisionTerminalConflict
		switch {
		case s.moveKey(msg):
		case msg.String() == "t" && s.err != nil && retryable && s.plan != nil && s.decision != "":
			s.view = dreamDeciding
			return s.dreamCmd(dreamDeciding, s.decision), true, false
		case msg.String() == "r" && s.canGenerate() && ((s.err == nil && failed) || regenerable):
			s.regenerateFrom, s.view = dreamReceipt, dreamConfirmRegenerate
		}
	case dreamConfirmRegenerate:
		if key.Matches(msg, s.deps.keys.Choose) {
			return s.generateDream(), true, false
		}
	case dreamConfirmApply, dreamConfirmDismiss:
		if key.Matches(msg, s.deps.keys.Choose) && s.plan != nil {
			decision := client.DreamDecisionApply
			if s.view == dreamConfirmDismiss {
				decision = client.DreamDecisionDismiss
			}
			s.view, s.decision = dreamDeciding, decision
			return s.dreamCmd(dreamDeciding, decision), true, false
		}
	}
	return nil, true, false
}

func (s *dreamState) moveKey(msg tea.KeyPressMsg) bool {
	move := bounded.LineDown
	switch {
	case key.Matches(msg, s.deps.keys.Down):
	case key.Matches(msg, s.deps.keys.Up):
		move = bounded.LineUp
	case key.Matches(msg, s.deps.keys.ScrollD):
		move = bounded.PageDown
	case key.Matches(msg, s.deps.keys.ScrollU):
		move = bounded.PageUp
	default:
		return false
	}
	s.moveViewport(move)
	return true
}

func (s *dreamState) moveViewport(move bounded.Move) {
	if s.view != dreamReview && s.view != dreamReceipt || s.compact {
		return
	}
	if s.viewport != nil {
		s.viewport.Move(move, s.readerRows)
	}
}

func (s *dreamState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	move := bounded.LineDown
	if msg.Mouse().Button == tea.MouseWheelUp {
		move = bounded.LineUp
	} else if msg.Mouse().Button != tea.MouseWheelDown {
		return nil, true
	}
	if s.view == dreamTargets && !s.compact && s.list != nil {
		s.list.Scroll(move)
	} else {
		s.moveViewport(move)
	}
	return nil, true
}

func (s *dreamState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	if hit, ok := msg.(surfaceHitMsg); ok {
		if s.view == dreamTargets && !s.compact {
			if target, found := s.hitTargets[hit.ID]; found {
				s.target = target
				s.list.SetCursor(target)
			}
		}
		return nil, true, false
	}
	x, ok := msg.(dreamResultMsg)
	if !ok {
		return nil, false, false
	}
	if s.closed || x.owner != s || x.requestID != s.requestID || x.phase != s.view || x.result.RequestID != x.requestID || x.result.Generation != x.requestID {
		return nil, true, false
	}
	s.err = x.result.Err
	switch s.view {
	case dreamGenerating:
		if x.result.Err == nil && x.result.Plan != nil {
			s.plan = x.result.Plan
			if s.viewport == nil {
				s.viewport = new(bounded.Viewport)
			}
			s.viewport.Reset()
			s.view = dreamReview
		} else {
			s.view = dreamTargets
		}
	case dreamDeciding:
		s.receipt = x.result.Receipt
		if s.viewport == nil {
			s.viewport = new(bounded.Viewport)
		}
		s.viewport.Reset()
		s.view = dreamReceipt
	}
	return nil, true, false
}

func (s *dreamState) Close() { s.closed = true }

func (s *dreamState) Render(width, height int) (string, []ClickableRegion) {
	s.width, s.height = width, height
	s.hitTargets = nil
	s.readerRows = 0
	s.compact = !s.fits(width, height)
	if s.compact {
		if s.viewport != nil {
			s.viewport.SetGeometry(0, 0, 0, bounded.Clip)
		}
		if width <= 0 || height <= 0 {
			return "", nil
		}
		return dreamCompact(s.deps.theme, s.deps.marks, width), nil
	}
	if s.view == dreamReview || s.view == dreamReceipt {
		return s.renderReader(width, height), nil
	}
	if s.view == dreamTargets {
		return s.renderTargets(width, height)
	}
	return strings.Join(s.nonReaderRows(width), "\n"), nil
}

func (s *dreamState) fits(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if s.view == dreamReview || s.view == dreamReceipt {
		_, _, _, _, rows := dreamReaderLayout(s.deps.theme, *s, s.deps.caps, s.deps.marks, width, height)
		return rows > 0
	}
	if s.view == dreamTargets {
		before, after := s.targetChrome(width)
		return width >= 3 && height >= len(before)+len(after)+1
	}
	return len(s.nonReaderRows(width)) <= height
}

func dreamGenerateMessage(err error) string {
	switch client.ClassifyDreamGenerateError(err) {
	case client.DreamGenerateFailed:
		return "Plan generation failed. Retry; if it persists, check server diagnostics."
	case client.DreamGenerateDeadline:
		return "Plan generation timed out. Retry later or check server availability."
	case client.DreamGenerateCapacity:
		return "Too many pending Dream plans. Dismiss an existing plan or retry later."
	case client.DreamGenerateUnavailable:
		return "Dream generation is unavailable on this server. Check its configuration."
	default:
		return "Could not generate a plan. Check the connection and retry."
	}
}

func (s *dreamState) targetChrome(width int) (before, after []string) {
	th, hk := s.deps.theme, s.deps.marks
	before = dreamPhysicalRows([]string{
		th.Style("overlayTitle").Render("Dream — manual memory maintenance"),
		th.Style("warning").Render("Generating sends the selected memory's full values and descriptions to the configured model and spends tokens."),
		"This does not enable or change scheduled consolidation.", "",
	}, width)
	if s.target >= 0 {
		instruction := "Select a target, then " + hk.choose + " to confirm the spend:"
		if caps := s.deps.caps.ManualDream; caps.ProjectMemory.Generate && caps.UserModel.Generate {
			instruction = hk.navUp + "/" + hk.navDown + " move · " + instruction
		}
		before = append(before, dreamPhysicalRows([]string{instruction}, width)...)
	} else {
		before = append(before, dreamPhysicalRows([]string{th.Style("warning").Render("No target is currently available; generation is disabled.")}, width)...)
	}
	if s.err != nil {
		after = append(after, dreamPhysicalRows([]string{"", th.Style("errorText").Render(dreamGenerateMessage(s.err))}, width)...)
	}
	after = append(after, dreamPhysicalRows([]string{"", th.Style("muted").Render(hk.closeOnly + " back/close")}, width)...)
	return before, after
}

func (s *dreamState) targetLayout(width, height int) (before, after []string, capacity int) {
	before, after = s.targetChrome(width)
	capacity = height - len(before) - len(after)
	if s.list == nil {
		s.list = new(bounded.List)
	}
	caps := s.deps.caps.ManualDream
	items := []bounded.ListItem{
		{ID: client.DreamTargetProjectMemory, Text: dreamTargetLine("project memory", caps.ProjectMemory)},
		{ID: client.DreamTargetUserModel, Text: dreamTargetLine("user model", caps.UserModel)},
	}
	s.list.SetGeometry(width, capacity, 0, bounded.Wrap)
	s.list.SetItems(items)
	if s.target >= 0 && s.list.Cursor() != s.target {
		s.list.SetCursor(s.target)
	}
	return before, after, capacity
}

func (s *dreamState) renderTargets(width, height int) (string, []ClickableRegion) {
	before, after, capacity := s.targetLayout(width, height)
	view := s.list.ViewWithIndicators(capacity, s.target >= 0 && s.list.RevealPending())
	body := append([]string{}, before...)
	var regions []ClickableRegion
	s.hitTargets = make(map[HitID]int)
	if view.Above > 0 {
		body = append(body, ansi.Cut(s.deps.theme.Style("muted").Render(fmt.Sprintf("↑ %d items", view.Above)), 0, width)+"\x1b[0m")
	}
	for _, row := range view.Rows {
		caps := s.deps.caps.ManualDream
		enabled := row.ItemIndex == 0 && caps.ProjectMemory.Generate || row.ItemIndex == 1 && caps.UserModel.Generate
		if !enabled || s.target < 0 || row.ItemIndex != s.target {
			row.Selected, row.CursorMarker = false, false
		}
		y := len(body)
		presentation := presentListRow(row, s.deps.theme.Style("spinner"), s.deps.theme.Style("muted"))
		body = append(body, ansi.Cut(presentation.Style.Render(presentation.Text), 0, width)+"\x1b[0m")
		if enabled && s.deps.hits != nil {
			id := s.deps.hits.allocate()
			x1 := min(width, ansi.StringWidth(body[y]))
			if x1 > 0 {
				regions = append(regions, ClickableRegion{rect: cellRect{x0: 0, x1: x1, y0: y, y1: y + 1}, hit: id})
				s.hitTargets[id] = row.ItemIndex
			}
		}
	}
	if view.Below > 0 {
		body = append(body, ansi.Cut(s.deps.theme.Style("muted").Render(fmt.Sprintf("↓ %d items", view.Below)), 0, width)+"\x1b[0m")
	}
	return strings.Join(append(body, after...), "\n"), regions
}
func (s *dreamState) nonReaderRows(width int) []string {
	th, hk := s.deps.theme, s.deps.marks
	lines := []string{th.Style("overlayTitle").Render("Dream — manual memory maintenance")}
	switch s.view {
	case dreamGenerating:
		lines = append(lines, th.Style("muted").Render("generating plan…"))
	case dreamConfirmApply:
		if s.plan != nil {
			lines = append(lines, fmt.Sprintf("Apply this whole plan? %d sources will be processed.", s.plan.SourceCount))
		}
		lines = append(lines, "Synthesized survivor revisions shown in the plan will be written; displayed sources retire atomically per operation.", "", hk.choose+" apply   "+hk.closeOnly+" back")
	case dreamConfirmDismiss:
		lines = append(lines, "Dismiss this whole plan? This performs zero memory mutation.", "", hk.choose+" dismiss   "+hk.closeOnly+" back")
	case dreamConfirmRegenerate:
		lines = append(lines, "Generate a fresh plan? This sends the selected full memory to the model again and spends more tokens.", "", hk.choose+" regenerate   "+hk.closeOnly+" back")
	case dreamDeciding:
		lines = append(lines, th.Style("muted").Render(s.decision+"ing plan…"))
	}
	lines = append(lines, "", th.Style("muted").Render(hk.closeOnly+" back/close"))
	return dreamPhysicalRows(lines, width)
}

func (s *dreamState) renderReader(width, height int) string {
	th, hk := s.deps.theme, s.deps.marks
	title, footer, rows, contentWidth, bodyHeight := dreamReaderLayout(th, *s, s.deps.caps, hk, width, height)
	if s.viewport == nil {
		s.viewport = new(bounded.Viewport)
	}
	s.viewport.SetGeometry(contentWidth, bodyHeight, 0, bounded.Clip)
	s.readerRows = len(rows)
	projection := s.viewport.View(rows)
	body := append([]string{}, title...)
	body = append(body, projection.Rows...)
	if len(rows) > bodyHeight {
		indicator := th.Style("muted").Render(fmt.Sprintf("lines %d–%d of %d", projection.Above+1, len(rows)-projection.Below, len(rows)))
		body = append(body, ansi.Cut(indicator, 0, contentWidth)+"\x1b[0m")
	}
	body = append(body, footer...)
	return strings.Join(body, "\n")
}

func dreamCanDecide(st dreamState, caps client.Capabilities) bool {
	if caps.ManualDream == nil {
		return false
	}
	capability := caps.ManualDream.ProjectMemory
	if st.plan != nil && st.plan.Target == client.DreamTargetUserModel {
		capability = caps.ManualDream.UserModel
	}
	return capability.Decide
}

func dreamReaderFooterText(st dreamState, caps client.Capabilities, hk helpKeys) string {
	footerText := hk.closeOnly + " back/close  " + hk.navUp + "/" + hk.navDown + " scroll  " + hk.scroll + " page"
	switch st.view {
	case dreamReview:
		if dreamCanDecide(st, caps) {
			footerText += "  a apply  x dismiss"
		}
		if st.canGenerate() {
			footerText += "  r regenerate"
		}
	case dreamReceipt:
		kind := client.ClassifyDreamDecisionError(st.err)
		if st.err != nil && st.plan != nil && st.decision != "" && (kind == client.DreamDecisionInProgress || kind == client.DreamDecisionUnknown) {
			footerText += "  t retry"
		}
		if st.canGenerate() && (st.err == nil && st.receipt != nil && (st.receipt.Failed > 0 || st.receipt.Conflicted > 0) || st.err != nil && (kind == client.DreamDecisionPlanGone || kind == client.DreamDecisionTerminalConflict)) {
			footerText += "  r regenerate"
		}
	}
	return footerText
}

func dreamReaderLayout(th theme.Theme, st dreamState, caps client.Capabilities, hk helpKeys, width, height int) (title, footer, rows []string, contentWidth, bodyHeight int) {
	if width <= 0 || height <= 0 {
		return nil, nil, nil, 0, 0
	}
	contentWidth = width
	title = dreamPhysicalRows([]string{th.Style("overlayTitle").Render("Dream — manual memory maintenance")}, width)
	footer = dreamPhysicalRows([]string{th.Style("muted").Render(dreamReaderFooterText(st, caps, hk))}, width)
	rows = dreamPhysicalRows(dreamReaderLines(st, caps, width), width)
	bodyHeight = height - len(title) - len(footer)
	if len(rows) > bodyHeight {
		bodyHeight-- // the overflow indicator has its own reserved row
	}
	if bodyHeight < 1 {
		return nil, nil, nil, 0, 0
	}
	return title, footer, rows, contentWidth, bodyHeight
}

func dreamCompact(th theme.Theme, hk helpKeys, width int) string {
	return ansi.Cut(th.Style("muted").Render(hk.closeOnly+" close"), 0, max(0, width)) + "\x1b[0m"
}

func dreamReaderLines(st dreamState, caps client.Capabilities, width int) []string {
	if st.view == dreamReceipt {
		return renderDreamReceipt(st)
	}
	capability := client.DreamTargetCapability{}
	if caps.ManualDream != nil {
		capability = caps.ManualDream.ProjectMemory
		if st.plan != nil && st.plan.Target == client.DreamTargetUserModel {
			capability = caps.ManualDream.UserModel
		}
	}
	return renderDreamPlan(st.deps.theme, st.plan, width, capability.Decide, st.canGenerate(), capability.UnavailableReason)
}

func dreamPhysicalRows(lines []string, width int) []string {
	var rows []string
	for _, line := range lines {
		if strings.HasPrefix(line, "│ ") {
			// The reader may wrap a framed model value again. Keep its provenance
			// marker on every resulting physical row, not only the first.
			for _, part := range strings.Split(ansi.Wrap(strings.TrimPrefix(line, "│ "), max(1, width-2), ""), "\n") {
				rows = append(rows, ansi.Cut("│ "+part, 0, width)+"\x1b[0m")
			}
			continue
		}
		for _, part := range strings.Split(ansi.Wrap(line, width, ""), "\n") {
			rows = append(rows, ansi.Cut(part, 0, width)+"\x1b[0m")
		}
	}
	return rows
}

func dreamTargetLine(label string, capability client.DreamTargetCapability) string {
	state := "available"
	switch {
	case !capability.Generate:
		state = "generation unavailable"
		if capability.UnavailableReason != "" {
			state += ": " + reflectionDisplayText(capability.UnavailableReason, 120)
		}
	case !capability.Decide:
		state = "generation available; apply/dismiss unavailable"
		if capability.UnavailableReason != "" {
			state += ": " + reflectionDisplayText(capability.UnavailableReason, 120)
		}
	}
	return label + " — " + state
}

func renderDreamPlan(th theme.Theme, plan *client.DreamPlan, width int, canDecide, canGenerate bool, unavailableReason string) []string {
	if plan == nil {
		return []string{"No plan returned."}
	}
	budget := max(1, width)
	lines := []string{
		dreamInfoField(th, "target", dreamTargetLabel(plan.Target)),
		dreamInfoField(th, "expires", plan.ExpiresAt.Format("2006-01-02 15:04:05 MST")),
		dreamInfoField(th, "planned operations", strconv.Itoa(plan.PlannedOperationCount)) + "  " + dreamInfoField(th, "planned sources", strconv.Itoa(plan.SourceCount)),
		"", "Exact duplicates keep the survivor unchanged.", "Synthesized replacements write the displayed replacement and retire displayed sources atomically per operation.",
	}
	for i, op := range plan.Operations {
		lines = append(lines, "", th.Style("overlayTitle").Render(wrapCardText(fmt.Sprintf("operation %d — kind: %s", i+1, op.Kind), budget)))
		lines = append(lines, dreamInfoField(th, "exact-duplicate eligible", strconv.FormatBool(op.ExactDuplicateEligible)))
		lines = append(lines, renderDreamParticipant(th, "survivor", op.Survivor, width)...)
		for j, source := range op.Sources {
			lines = append(lines, "")
			lines = append(lines, renderDreamParticipant(th, fmt.Sprintf("source %d", j+1), source, width)...)
		}
		lines = append(lines, "")
		lines = append(lines, framedDreamField(th, "replacement value", op.Replacement.Value, width-12)...)
		lines = append(lines, framedDreamField(th, "replacement description", op.Replacement.Description, width-12)...)
		lines = append(lines, "")
		lines = append(lines, framedDreamField(th, "reason", op.Reason, width-12)...)
	}
	var actions []string
	if canDecide {
		actions = append(actions, "a apply whole plan   x dismiss with zero mutation")
	} else {
		reason := reflectionDisplayText(unavailableReason, 120)
		if reason == "" {
			reason = "decision service unavailable"
		}
		actions = append(actions, "apply/dismiss unavailable: "+reason)
	}
	if canGenerate {
		actions = append(actions, "r regenerate (spends tokens)")
	}
	return append(lines, append([]string{""}, actions...)...)
}

func dreamInfoField(th theme.Theme, label, value string) string {
	return th.Style("muted").Render(label+":") + th.Style("viewport").Render(" "+terminaltext.Sanitize(value))
}

func renderDreamParticipant(th theme.Theme, label string, p client.DreamParticipant, width int) []string {
	lines := framedDreamField(th, label+" key", p.Key, width-12)
	lines = append(lines, framedDreamField(th, label+" value", p.Value, width-12)...)
	return append(lines, framedDreamField(th, label+" description", p.Description, width-12)...)
}

func framedDreamField(th theme.Theme, label, value string, width int) []string {
	width = max(12, width)
	var out []string
	for _, physical := range strings.Split(value, "\n") {
		quoted := terminaltext.Sanitize(strconv.QuoteToGraphic(physical))
		wrapped := strings.Split(wrapCardText(quoted, width), "\n")
		if len(wrapped) == 0 {
			wrapped = []string{"\"\""}
		}
		for i, line := range wrapped {
			if i == 0 {
				// Keep the provenance marker literal and unstyled: dreamPhysicalRows
				// recognizes it before rewrapping model-derived values.
				out = append(out, "│ "+th.Style("muted").Render(label+":")+th.Style("viewport").Render(" "+line))
				continue
			}
			out = append(out, "│   "+th.Style("viewport").Render(line))
		}
	}
	return out
}

func renderDreamReceipt(st dreamState) []string {
	if st.err != nil {
		planID := "(unknown)"
		if st.plan != nil {
			planID = reflectionDisplayText(st.plan.ID, 64)
		}
		decision := reflectionDisplayText(st.decision, 32)
		switch client.ClassifyDreamDecisionError(st.err) {
		case client.DreamDecisionInProgress:
			lines := []string{"The same " + decision + " decision is still in progress."}
			if st.plan != nil && st.decision != "" {
				lines = append(lines, "t retry the SAME decision with plan "+planID+" to retrieve its idempotent authoritative receipt")
			}
			return append(lines, "No opposite decision or fresh-plan generation is available while it is in progress.")
		case client.DreamDecisionConflict:
			return []string{
				"A conflicting decision is in progress; this old plan is no longer actionable.",
				"No retry, opposite decision, or fresh-plan generation is available until it reaches a known terminal state.",
			}
		case client.DreamDecisionTerminalConflict, client.DreamDecisionPlanGone:
			var lines []string
			if client.ClassifyDreamDecisionError(st.err) == client.DreamDecisionTerminalConflict {
				lines = []string{"This plan already reached a different terminal decision and is no longer actionable."}
			} else {
				lines = []string{"This plan is unavailable after expiry, restart, or routing to another server and is no longer actionable.", "The old decision cannot be retried."}
			}
			if st.canGenerate() {
				lines = append(lines, "r generate a fresh plan with another provider call (spends more tokens)")
			}
			return lines
		default:
			lines := []string{"Decision result unknown: the first request may already have applied."}
			if st.plan != nil && st.decision != "" {
				lines = append(lines, "t retry the SAME "+decision+" decision with plan "+planID+" to retrieve its idempotent authoritative receipt")
			}
			return append(lines, "No opposite decision or automatic regeneration is available.")
		}
	}
	if st.receipt == nil {
		return []string{"No receipt returned."}
	}
	r := st.receipt
	lines := []string{"│ disposition: " + reflectionDisplayText(r.Disposition, 64), fmt.Sprintf("planned: %d  applied: %d  conflicted: %d  skipped: %d  failed: %d", r.Planned, r.Applied, r.Conflicted, r.Skipped, r.Failed)}
	if r.Conflicted > 0 {
		lines = append(lines, "Memory changed after generation; nothing is auto-refreshed.")
	}
	if r.Failed > 0 {
		lines = append(lines, "Partial result: some sources failed; counts above are the authoritative outcome.")
	}
	if st.canGenerate() && (r.Conflicted > 0 || r.Failed > 0) {
		lines = append(lines, "Press r to confirm a fresh provider call; regeneration spends more tokens.")
	}
	return lines
}

func dreamTargetLabel(target string) string {
	if target == client.DreamTargetUserModel {
		return "user model"
	}
	return "project memory"
}
