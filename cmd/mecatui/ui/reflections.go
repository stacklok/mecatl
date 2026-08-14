package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type reflectionsView int

const (
	reflectionsNone reflectionsView = iota
	reflectionsList
	reflectionsDetail
)

type reflectionsState struct {
	view         reflectionsView
	loading      bool
	err          error
	page         client.LearningProposalPage
	cursors      client.ReflectionCursors
	previous     []client.ReflectionCursors
	cursor       int
	detail       *client.LearningProposal
	detailScroll int
	stale        bool
	requestID    string
}

func (m Model) openReflections() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Reflections == nil {
		return m, nil
	}
	m.ta.Blur()
	m.reflectionsGen++
	m.reflections = reflectionsState{view: reflectionsList, loading: true}
	return m, client.ListReflectionsCmd(m.deps.Ctx, m.deps.Reflections, "", client.ReflectionCursors{}, m.activeWorkspace, m.reflectionsGen)
}
func (m Model) runReflect() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Reflections == nil || m.sessionID == "" || m.conv.isEmpty() {
		m.statusMsg = m.deps.Theme.Style("warning").Render("/reflect needs the current completed session; finish a turn or select an available completed session")
		return m, nil
	}
	m.reflectionsGen++
	m.statusMsg = m.deps.Theme.Style("muted").Render("reflecting session…")
	return m, client.ReflectSessionCmd(m.deps.Ctx, m.deps.Reflections, m.sessionID, m.reflectionsGen)
}
func (m Model) closeReflections() (tea.Model, tea.Cmd) {
	m.reflectionsGen++
	m.reflections = reflectionsState{}
	return m, m.ta.Focus()
}

//nolint:gocyclo // list/detail paging, scrolling, refresh, and lifecycle actions stay explicit
func (m Model) onReflectionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.reflections.view == reflectionsNone {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		if m.reflections.view == reflectionsDetail {
			m.reflections.view = reflectionsList
			m.reflections.detail = nil
			return m, nil, true
		}
		x, c := m.closeReflections()
		return x, c, true
	}
	if m.reflections.loading {
		return m, nil, true
	}
	if m.reflections.view == reflectionsList {
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.reflections.cursor > 0 {
				m.reflections.cursor--
			}
		case key.Matches(msg, m.keys.Down):
			if m.reflections.cursor < len(m.reflections.page.Proposals)-1 {
				m.reflections.cursor++
			}
		case msg.String() == "n":
			next := client.ReflectionCursors{
				Operator: m.reflections.page.OperatorNextCursor, Project: m.reflections.page.ProjectNextCursor,
				OperatorDone: m.reflections.page.OperatorDone, ProjectDone: m.reflections.page.ProjectDone,
			}
			if !next.OperatorDone || !next.ProjectDone {
				m.reflections.previous = append(m.reflections.previous, m.reflections.cursors)
				m.reflections.cursors = next
				m.reflectionsGen++
				m.reflections.loading = true
				return m, client.ListReflectionsCmd(m.deps.Ctx, m.deps.Reflections, "", next, m.activeWorkspace, m.reflectionsGen), true
			}
		case msg.String() == "p":
			if n := len(m.reflections.previous); n > 0 {
				previous := m.reflections.previous[n-1]
				m.reflections.previous = m.reflections.previous[:n-1]
				m.reflections.cursors = previous
				m.reflectionsGen++
				m.reflections.loading = true
				return m, client.ListReflectionsCmd(m.deps.Ctx, m.deps.Reflections, "", previous, m.activeWorkspace, m.reflectionsGen), true
			}
		case key.Matches(msg, m.keys.Choose):
			if m.reflections.cursor < len(m.reflections.page.Proposals) {
				p := m.reflections.page.Proposals[m.reflections.cursor]
				m.reflectionsGen++
				m.reflections.loading = true
				m.reflections.requestID = p.ID
				return m, client.GetReflectionCmd(m.deps.Ctx, m.deps.Reflections, p.ID, p.Project, m.reflectionsGen), true
			}
		}
	}
	if m.reflections.view == reflectionsDetail && m.reflections.detail != nil {
		p := *m.reflections.detail
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.reflections.detailScroll > 0 {
				m.reflections.detailScroll--
			}
			return m, nil, true
		case key.Matches(msg, m.keys.Down):
			m.reflections.detailScroll++
			return m, nil, true
		case key.Matches(msg, m.keys.ScrollU):
			m.reflections.detailScroll = max(0, m.reflections.detailScroll-10)
			return m, nil, true
		case key.Matches(msg, m.keys.ScrollD):
			m.reflections.detailScroll += 10
			return m, nil, true
		}
		if msg.String() == "r" && m.reflections.stale {
			m.reflectionsGen++
			m.reflections.loading = true
			m.reflections.requestID = p.ID
			return m, client.GetReflectionCmd(m.deps.Ctx, m.deps.Reflections, p.ID, p.Project, m.reflectionsGen), true
		}
		switch msg.String() {
		case "a":
			if !m.reflections.stale && reflectionApprovable(p) {
				m.reflectionsGen++
				m.reflections.loading = true
				return m, client.DecideReflectionCmd(m.deps.Ctx, m.deps.Reflections, p, "approve", p.Project, m.reflectionsGen), true
			}
		case "x":
			if !m.reflections.stale && p.Status == client.ProposalStatusStaged {
				m.reflectionsGen++
				m.reflections.loading = true
				return m, client.DecideReflectionCmd(m.deps.Ctx, m.deps.Reflections, p, "reject", p.Project, m.reflectionsGen), true
			}
		case "u":
			if !m.reflections.stale && p.Status == client.ProposalStatusPromoted && p.PromotionAvailable {
				m.reflectionsGen++
				m.reflections.loading = true
				return m, client.UndoReflectionCmd(m.deps.Ctx, m.deps.Reflections, p, p.Project, m.reflectionsGen), true
			}
		}
	}
	return m, nil, true
}
func (m Model) updateReflectionsMsg(msg tea.Msg) (tea.Model, bool) {
	switch x := msg.(type) {
	case client.ReflectionsMsg:
		if x.Generation != m.reflectionsGen || m.reflections.view == reflectionsNone {
			return m, true
		}
		m.reflections.loading = false
		m.reflections.err = x.Err
		if x.Err == nil {
			m.reflections.page = x.Page
			m.reflections.cursor = 0
		}
		return m, true
	case client.ReflectionMsg:
		if x.Generation != m.reflectionsGen {
			return m, true
		}
		if m.reflections.view == reflectionsNone {
			if x.Receipt != nil && x.Err == nil {
				if x.Receipt.Abstained {
					m.statusMsg = m.deps.Theme.Style("muted").Render("reflection " + reflectionDisplayText(x.Receipt.Disposition, 48) + ": abstained")
				} else {
					m.statusMsg = m.deps.Theme.Style("success").Render(fmt.Sprintf("reflection %s: %d staged, %d promoted, %d conflicted", reflectionDisplayText(x.Receipt.Disposition, 48), x.Receipt.Staged, x.Receipt.Promoted, x.Receipt.Conflicted))
				}
			} else if x.Err != nil {
				m.statusMsg = m.deps.Theme.Style("errorText").Render(sanitizeTerminal(x.Err.Error()))
			}
			return m, true
		}
		if x.RequestID != m.reflections.requestID {
			return m, true
		}
		m.reflections.loading = false
		m.reflections.err = x.Err
		m.reflections.stale = client.IsProposalConflict(x.Err)
		if x.Proposal != nil && x.Err == nil {
			p := *x.Proposal
			m.reflections.detail = &p
			m.reflections.detailScroll = 0
			m.reflections.stale = false
			m.reflections.view = reflectionsDetail
		}
		return m, true
	}
	return m, false
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

//nolint:gocyclo // explicit status/detail rendering keeps every bounded state visible
func renderReflectionsOverlay(th theme.Theme, st reflectionsState, caps client.Capabilities, hk helpKeys, width, height int) string {
	lines := []string{th.Style("overlayTitle").Render("Reflections")}
	if !caps.LearningProposals {
		lines = append(lines, th.Style("warning").Render("proposal review is not supported by this server"))
	} else if st.loading {
		lines = append(lines, th.Style("muted").Render("loading…"))
	} else if st.err != nil {
		lines = append(lines, th.Style("errorText").Render(sanitizeTerminal(st.err.Error())))
		if st.stale {
			lines = append(lines, "proposal changed; press r to refresh its current version and status")
		}
	} else if st.view == reflectionsDetail && st.detail != nil {
		p := st.detail
		lines = append(lines, th.Style("toolName").Render(sanitizeTerminal(p.ID)), "status: "+sanitizeTerminal(p.Status), "version: "+sanitizeTerminal(p.Version), "kind: "+sanitizeTerminal(p.Kind))
		if p.ProjectScoped {
			lines = append(lines, "scope: trusted project")
		} else {
			lines = append(lines, "scope: operator")
		}
		summary := p.Key
		if summary == "" {
			summary = p.Title
		}
		if summary != "" {
			lines = append(lines, wrapReflectionField("key: ", summary, width-12)...)
		}
		if p.Value != "" {
			lines = append(lines, wrapReflectionField("value: ", p.Value, width-12)...)
		}
		if p.Description != "" {
			lines = append(lines, wrapReflectionField("description: ", p.Description, width-12)...)
		}
		if p.Body != "" {
			lines = append(lines, wrapReflectionField("body: ", p.Body, width-12)...)
		}
		if len(p.Triggers) > 0 {
			triggers := make([]string, len(p.Triggers))
			for i := range p.Triggers {
				triggers[i] = reflectionDisplayText(p.Triggers[i], 48)
			}
			lines = append(lines, "triggers: "+strings.Join(triggers, ", "))
		}
		lines = append(lines, fmt.Sprintf("evidence: %d  decisions: %d", len(p.Evidence), len(p.Decisions)))
		for _, evidence := range p.Evidence {
			state := "unavailable"
			if evidence.Available {
				state = "available"
			} else if evidence.Availability != "" {
				state += " (" + reflectionDisplayText(evidence.Availability, 64) + ")"
			}
			line := fmt.Sprintf("  %s:%d  %s  source=%s", reflectionDisplayText(evidence.Locator, 24), evidence.Ordinal, state, reflectionDisplayText(evidence.SessionID, 64))
			if evidence.EventSeq != 0 {
				line += fmt.Sprintf(" seq=%d", evidence.EventSeq)
			}
			if evidence.ToolCallID != "" {
				line += " call=" + reflectionDisplayText(evidence.ToolCallID, 64)
			}
			lines = append(lines, line, "    digest="+reflectionDisplayText(evidence.Digest, 72))
			if evidence.Preview != "" {
				lines = append(lines, wrapReflectionField("    preview: ", evidence.Preview, width-12)...)
			}
		}
		if len(p.Decisions) > 0 {
			decision := p.Decisions[len(p.Decisions)-1]
			line := "decision: " + reflectionDisplayText(decision.Kind, 32)
			if decision.Reason != "" {
				line += " — " + reflectionDisplayText(decision.Reason, 120)
			}
			lines = append(lines, line)
		}
		if p.Promotion != nil {
			lines = append(lines, "receipt: "+reflectionDisplayText(p.Promotion.MemoryKey, 96)+" @ "+reflectionDisplayText(p.Promotion.ResultVersion, 64))
		}
		if p.LearnedSkillID != "" {
			lines = append(lines, "learned skill: "+reflectionDisplayText(p.LearnedSkillID, 96)+" (open /skills to inspect versions)")
		}
		switch p.Status {
		case client.ProposalStatusStaged, client.ProposalStatusDeferred:
			if p.Kind == "procedure" {
				if reflectionApprovable(*p) {
					lines = append(lines, "a materialize and evaluate learned-skill draft   x reject")
				} else {
					lines = append(lines, "skill materialization disabled: evidence unavailable or lifecycle unavailable; x reject")
				}
			} else if !p.PromotionAvailable {
				reason := p.PromotionUnavailableReason
				if reason == "" {
					reason = "memory target unavailable"
				}
				lines = append(lines, "approval disabled: "+reflectionDisplayText(reason, 120)+"; x reject")
			} else if reflectionApprovable(*p) {
				lines = append(lines, "a approve exact key/value/description above   x reject")
			} else {
				lines = append(lines, "approval disabled: evidence unavailable or changed; x reject")
			}
		case client.ProposalStatusPromoted:
			if p.PromotionAvailable {
				lines = append(lines, "u undo promotion")
			} else {
				reason := p.PromotionUnavailableReason
				if reason == "" {
					reason = "memory target unavailable"
				}
				lines = append(lines, "undo disabled: "+reflectionDisplayText(reason, 120))
			}
		}
	} else if len(st.page.Proposals) == 0 {
		lines = append(lines, th.Style("muted").Render("No proposals."))
	} else {
		window := max(1, height-8)
		start := max(0, min(st.cursor-window/2, len(st.page.Proposals)-window))
		end := min(len(st.page.Proposals), start+window)
		for i := start; i < end; i++ {
			p := st.page.Proposals[i]
			mark := "  "
			if i == st.cursor {
				mark = "> "
			}
			summary := p.Key
			if summary == "" {
				summary = p.Title
			}
			lines = append(lines, mark+sanitizeTerminal(p.Status)+"  "+sanitizeTerminal(summary))
		}
		if start > 0 || end < len(st.page.Proposals) {
			lines = append(lines, fmt.Sprintf("rows %d–%d of %d", start+1, end, len(st.page.Proposals)))
		}
	}
	if st.view == reflectionsDetail && len(lines) > max(4, height-6) {
		window := max(4, height-6)
		maxScroll := max(0, len(lines)-window)
		start := min(max(0, st.detailScroll), maxScroll)
		end := min(len(lines), start+window)
		visible := append([]string(nil), lines[start:end]...)
		visible = append(visible, fmt.Sprintf("lines %d–%d of %d", start+1, end, len(lines)))
		lines = visible
	}
	lines = append(lines, "", th.Style("muted").Render(hk.closeOnly+" close  ↑/↓ navigate  n/p pages  pgup/pgdown detail  enter detail"))
	return centerCard(th, strings.Join(lines, "\n"), width, height)
}

func wrapReflectionField(label, value string, width int) []string {
	width = max(12, width)
	value = sanitizeTerminal(value)
	var out []string
	for lineIndex, line := range strings.Split(value, "\n") {
		prefix := ""
		if lineIndex == 0 {
			prefix = label
		}
		runes := []rune(line)
		if len(runes) == 0 {
			out = append(out, prefix)
			continue
		}
		for len(runes) > 0 {
			available := max(1, width-len([]rune(prefix)))
			take := min(len(runes), available)
			out = append(out, prefix+string(runes[:take]))
			runes = runes[take:]
			prefix = strings.Repeat(" ", len([]rune(label)))
		}
	}
	return out
}

func reflectionDisplayText(value string, limit int) string {
	return truncate(sanitizeTerminal(strings.Join(strings.Fields(value), " ")), limit)
}
