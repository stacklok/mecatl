package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type dreamView int

const (
	dreamClosed dreamView = iota
	dreamTargets
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
	scroll         int
	decision       string
	regenerateFrom dreamView
	requestID      uint64
}

func (m Model) openDream() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Dream == nil || m.caps.ManualDream == nil {
		return m, nil
	}
	m.prompt.Blur()
	selected := 0
	if !m.caps.ManualDream.ProjectMemory.Generate && m.caps.ManualDream.UserModel.Generate {
		selected = 1
	}
	m.dream = dreamState{view: dreamTargets, target: selected}
	return m, nil
}

func (m Model) closeDream() (tea.Model, tea.Cmd) {
	m.dreamGen++
	m.dream = dreamState{}
	return m, m.prompt.Focus()
}

func (m Model) dreamTarget() (string, client.DreamTargetCapability) {
	if m.dream.target == 1 {
		return client.DreamTargetUserModel, m.caps.ManualDream.UserModel
	}
	return client.DreamTargetProjectMemory, m.caps.ManualDream.ProjectMemory
}

func (m Model) generateDream() (tea.Model, tea.Cmd) {
	target, capability := m.dreamTarget()
	if !capability.Generate {
		return m, nil
	}
	m.dreamGen++
	m.dreamRequest++
	m.dream.view = dreamGenerating
	m.dream.err = nil
	m.dream.requestID = m.dreamRequest
	return m, client.GenerateDreamPlanCmd(m.deps.Ctx, m.deps.Dream, target, m.dreamGen, m.dreamRequest)
}

//nolint:gocyclo // the explicit review/confirmation state machine is intentionally visible
func (m Model) onDreamKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.dream.view == dreamClosed {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		switch m.dream.view {
		case dreamConfirmApply, dreamConfirmDismiss:
			m.dream.view = dreamReview
			return m, nil, true
		case dreamConfirmRegenerate:
			m.dream.view = m.dream.regenerateFrom
			if m.dream.view != dreamReview && m.dream.view != dreamReceipt {
				m.dream.view = dreamReceipt
			}
			return m, nil, true
		case dreamReview, dreamReceipt:
			m.dream.view = dreamTargets
			m.dream.plan, m.dream.receipt, m.dream.err = nil, nil, nil
			return m, nil, true
		default:
			x, cmd := m.closeDream()
			return x, cmd, true
		}
	}
	switch m.dream.view {
	case dreamTargets:
		if key.Matches(msg, m.keys.Up) || key.Matches(msg, m.keys.Down) {
			m.dream.target = 1 - m.dream.target
			return m, nil, true
		}
		if key.Matches(msg, m.keys.Choose) {
			x, cmd := m.generateDream()
			return x, cmd, true
		}
	case dreamReview:
		switch {
		case key.Matches(msg, m.keys.Up):
			m.dream.scroll = max(0, m.dream.scroll-1)
		case key.Matches(msg, m.keys.Down):
			m.dream.scroll++
		case key.Matches(msg, m.keys.ScrollU):
			m.dream.scroll = max(0, m.dream.scroll-10)
		case key.Matches(msg, m.keys.ScrollD):
			m.dream.scroll += 10
		case msg.String() == "a":
			_, capability := m.dreamTarget()
			if capability.Decide {
				m.dream.view = dreamConfirmApply
			}
		case msg.String() == "x":
			_, capability := m.dreamTarget()
			if capability.Decide {
				m.dream.view = dreamConfirmDismiss
			}
		case msg.String() == "r":
			m.dream.regenerateFrom = dreamReview
			m.dream.view = dreamConfirmRegenerate
		}
	case dreamReceipt:
		failed := m.dream.receipt != nil && (m.dream.receipt.Conflicted > 0 || m.dream.receipt.Failed > 0)
		errorKind := client.ClassifyDreamDecisionError(m.dream.err)
		retryable := errorKind == client.DreamDecisionInProgress || errorKind == client.DreamDecisionUnknown
		regenerable := errorKind == client.DreamDecisionPlanGone || errorKind == client.DreamDecisionTerminalConflict
		switch {
		case msg.String() == "t" && m.dream.err != nil && retryable && m.dream.plan != nil && m.dream.decision != "":
			m.dreamGen++
			m.dreamRequest++
			m.dream.view = dreamDeciding
			m.dream.requestID = m.dreamRequest
			return m, client.DecideDreamPlanCmd(m.deps.Ctx, m.deps.Dream, m.dream.plan.ID, m.dream.decision, m.dreamGen, m.dreamRequest), true
		case msg.String() == "r" && ((m.dream.err == nil && failed) || regenerable):
			m.dream.regenerateFrom = dreamReceipt
			m.dream.view = dreamConfirmRegenerate
		}
	case dreamConfirmRegenerate:
		if key.Matches(msg, m.keys.Choose) {
			x, cmd := m.generateDream()
			return x, cmd, true
		}
	case dreamConfirmApply, dreamConfirmDismiss:
		if key.Matches(msg, m.keys.Choose) && m.dream.plan != nil {
			decision := client.DreamDecisionApply
			if m.dream.view == dreamConfirmDismiss {
				decision = client.DreamDecisionDismiss
			}
			m.dreamGen++
			m.dreamRequest++
			m.dream.view = dreamDeciding
			m.dream.decision = decision
			m.dream.requestID = m.dreamRequest
			return m, client.DecideDreamPlanCmd(m.deps.Ctx, m.deps.Dream, m.dream.plan.ID, decision, m.dreamGen, m.dreamRequest), true
		}
	}
	return m, nil, true
}

func (m Model) updateDreamMsg(msg tea.Msg) (tea.Model, bool) {
	x, ok := msg.(client.DreamMsg)
	if !ok {
		return m, false
	}
	if m.dream.view == dreamClosed || x.Generation != m.dreamGen || x.RequestID != m.dream.requestID {
		return m, true
	}
	m.dream.err = x.Err
	if m.dream.view == dreamGenerating {
		if x.Err == nil && x.Plan != nil {
			m.dream.plan = x.Plan
			m.dream.scroll = 0
			m.dream.view = dreamReview
		} else {
			m.dream.view = dreamTargets
		}
		return m, true
	}
	if m.dream.view == dreamDeciding {
		m.dream.receipt = x.Receipt
		m.dream.view = dreamReceipt
	}
	return m, true
}

func renderDreamOverlay(th theme.Theme, st dreamState, caps client.Capabilities, hk helpKeys, width, height int) string {
	lines := []string{th.Style("overlayTitle").Render("Dream — manual memory maintenance")}
	switch st.view {
	case dreamTargets:
		lines = append(lines,
			th.Style("warning").Render("Generating sends the selected memory's full values and descriptions to the configured model and spends tokens."),
			"This does not enable or change scheduled consolidation.", "", "Select a target, then press Enter to confirm the spend:")
		if caps.ManualDream != nil {
			lines = append(lines, dreamTargetLine(st.target == 0, "project memory", caps.ManualDream.ProjectMemory), dreamTargetLine(st.target == 1, "user model", caps.ManualDream.UserModel))
			if !caps.ManualDream.ProjectMemory.Generate && !caps.ManualDream.UserModel.Generate {
				lines = append(lines, "", th.Style("warning").Render("No target is currently available; generation is disabled."))
			}
		}
		if st.err != nil {
			lines = append(lines, "", th.Style("errorText").Render(sanitizeTerminal(st.err.Error())))
		}
	case dreamGenerating:
		lines = append(lines, th.Style("muted").Render("generating plan…"))
	case dreamReview:
		capability := client.DreamTargetCapability{}
		if caps.ManualDream != nil {
			capability = caps.ManualDream.ProjectMemory
			if st.plan != nil && st.plan.Target == client.DreamTargetUserModel {
				capability = caps.ManualDream.UserModel
			}
		}
		lines = append(lines, renderDreamPlan(st.plan, width, capability.Decide, capability.UnavailableReason)...)
	case dreamConfirmApply:
		lines = append(lines, fmt.Sprintf("Apply this whole plan? %d sources will be processed.", st.plan.SourceCount), "Synthesized survivor revisions shown in the plan will be written; displayed sources retire atomically per operation.", "", "Enter apply   Esc back")
	case dreamConfirmDismiss:
		lines = append(lines, "Dismiss this whole plan? This performs zero memory mutation.", "", "Enter dismiss   Esc back")
	case dreamConfirmRegenerate:
		lines = append(lines, "Generate a fresh plan? This sends the selected full memory to the model again and spends more tokens.", "", "Enter regenerate   Esc back")
	case dreamDeciding:
		lines = append(lines, th.Style("muted").Render(st.decision+"ing plan…"))
	case dreamReceipt:
		lines = append(lines, renderDreamReceipt(st)...)
	}
	if (st.view == dreamReview || st.view == dreamReceipt) && len(lines) > max(4, height-6) {
		window := max(4, height-6)
		start := min(max(0, st.scroll), max(0, len(lines)-window))
		end := min(len(lines), start+window)
		lines = append(append([]string(nil), lines[start:end]...), fmt.Sprintf("lines %d–%d of %d", start+1, end, len(lines)))
	}
	lines = append(lines, "", th.Style("muted").Render(hk.closeOnly+" back/close  ↑/↓ scroll"))
	return centerCard(th, strings.Join(lines, "\n"), width, height)
}

func dreamTargetLine(selected bool, label string, capability client.DreamTargetCapability) string {
	mark := "  "
	if selected {
		mark = "> "
	}
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
	return mark + label + " — " + state
}

func renderDreamPlan(plan *client.DreamPlan, width int, canDecide bool, unavailableReason string) []string {
	if plan == nil {
		return []string{"No plan returned."}
	}
	lines := []string{"target: " + dreamTargetLabel(plan.Target), "expires: " + plan.ExpiresAt.Format("2006-01-02 15:04:05 MST"), fmt.Sprintf("planned operations: %d  planned sources: %d", plan.PlannedOperationCount, plan.SourceCount), "Exact duplicates keep the survivor unchanged.", "Synthesized replacements write the displayed replacement and retire displayed sources atomically per operation."}
	for i, op := range plan.Operations {
		lines = append(lines, "", fmt.Sprintf("operation %d — kind: %s", i+1, reflectionDisplayText(op.Kind, 48)), fmt.Sprintf("exact-duplicate eligible: %t", op.ExactDuplicateEligible))
		lines = append(lines, renderDreamParticipant("survivor", op.Survivor, width)...)
		for j, source := range op.Sources {
			lines = append(lines, renderDreamParticipant(fmt.Sprintf("source %d", j+1), source, width)...)
		}
		lines = append(lines, framedDreamField("replacement value", op.Replacement.Value, width-12)...)
		lines = append(lines, framedDreamField("replacement description", op.Replacement.Description, width-12)...)
		lines = append(lines, framedDreamField("reason", op.Reason, width-12)...)
	}
	if !canDecide {
		reason := reflectionDisplayText(unavailableReason, 120)
		if reason == "" {
			reason = "decision service unavailable"
		}
		return append(lines, "", "apply/dismiss unavailable: "+reason, "r regenerate (spends tokens)")
	}
	return append(lines, "", "a apply whole plan   x dismiss with zero mutation   r regenerate (spends tokens)")
}

func renderDreamParticipant(label string, p client.DreamParticipant, width int) []string {
	lines := framedDreamField(label+" key", p.Key, width-12)
	lines = append(lines, framedDreamField(label+" value", p.Value, width-12)...)
	return append(lines, framedDreamField(label+" description", p.Description, width-12)...)
}

func framedDreamField(label, value string, width int) []string {
	width = max(12, width)
	var out []string
	for _, physical := range strings.Split(value, "\n") {
		quoted := strconv.QuoteToGraphic(physical)
		wrapped := wrapReflectionField("", quoted, width)
		if len(wrapped) == 0 {
			wrapped = []string{"\"\""}
		}
		for i, line := range wrapped {
			prefix := "│   "
			if i == 0 {
				prefix = "│ " + label + ": "
			}
			out = append(out, prefix+line)
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
			return []string{
				"The same " + decision + " decision is still in progress.",
				"t retry the SAME decision with plan " + planID + " to retrieve its idempotent authoritative receipt",
				"No opposite decision or fresh-plan generation is available while it is in progress.",
			}
		case client.DreamDecisionConflict:
			return []string{
				"A conflicting decision is in progress; this old plan is no longer actionable.",
				"No retry, opposite decision, or fresh-plan generation is available until it reaches a known terminal state.",
			}
		case client.DreamDecisionTerminalConflict:
			return []string{
				"This plan already reached a different terminal decision and is no longer actionable.",
				"r generate a fresh plan with another provider call (spends more tokens)",
			}
		case client.DreamDecisionPlanGone:
			return []string{
				"This plan is unavailable after expiry, restart, or routing to another server and is no longer actionable.",
				"The old decision cannot be retried.",
				"r generate a fresh plan with another provider call (spends more tokens)",
			}
		default:
			return []string{
				"Decision result unknown: the first request may already have applied.",
				"t retry the SAME " + decision + " decision with plan " + planID + " to retrieve its idempotent authoritative receipt",
				"No opposite decision or automatic regeneration is available.",
			}
		}
	}
	if st.receipt == nil {
		return []string{"No receipt returned."}
	}
	r := st.receipt
	lines := []string{"disposition: " + reflectionDisplayText(r.Disposition, 64), fmt.Sprintf("planned: %d  applied: %d  conflicted: %d  skipped: %d  failed: %d", r.Planned, r.Applied, r.Conflicted, r.Skipped, r.Failed)}
	if r.Conflicted > 0 {
		lines = append(lines, "Memory changed after generation; nothing is auto-refreshed.")
	}
	if r.Failed > 0 {
		lines = append(lines, "Partial result: some sources failed; counts above are the authoritative outcome.")
	}
	if r.Conflicted > 0 || r.Failed > 0 {
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
