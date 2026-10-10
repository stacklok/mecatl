package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

// ExecutionCreator is optional; a template selector never substitutes the default.
type ExecutionCreator interface {
	CreateSessionWithExecution(ctx context.Context, sel client.ModelSelection, mode string, choice client.ExecutionChoice) (string, client.Capabilities, client.ResolvedModel, error)
}

type executionPickerState struct {
	open, loading, creating bool
	items                   []client.ExecutionTemplate
	cursor                  int // 0 = default; 1 = none; 2+ = exact catalog entries
	token                   uint64
	status                  string
}

type executionInventoryMsg struct {
	token     uint64
	inventory client.ExecutionTemplateInventory
	err       error
}
type executionCreatedMsg struct {
	token uint64
	oldID string
	ready client.SessionReadyMsg
	err   error
}

func (m Model) openExecution() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Session == nil || m.deps.DebugTarget != "" {
		return m, nil
	}
	if _, ok := m.deps.Session.(ExecutionCreator); !ok {
		return m, nil
	}
	m.executionPicker.token++
	m.executionPicker.open = true
	m.executionPicker.cursor = 0
	m.executionPicker.items = nil
	m.executionPicker.creating = false
	m.executionPicker.loading = m.caps.ExecutionTemplates && m.deps.ExecutionTemplates != nil
	m.executionPicker.status = "Template catalog disabled; deployment default and none remain available."
	m.prompt.Blur()
	if !m.executionPicker.loading {
		return m, nil
	}
	m.executionPicker.status = "Loading eligible templates…"
	token, deps := m.executionPicker.token, m.deps
	return m, func() tea.Msg {
		inventory, err := deps.ExecutionTemplates.ListExecutionTemplates(deps.Ctx)
		return executionInventoryMsg{token: token, inventory: inventory, err: err}
	}
}

func (m Model) onExecutionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if !m.executionPicker.open {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		if m.executionPicker.creating {
			return m, nil, true
		}
		m.executionPicker.open = false
		return m, m.prompt.Focus(), true
	}
	if m.executionPicker.creating {
		return m, nil, true
	}
	switch msg.String() {
	case "up":
		if m.executionPicker.cursor > 0 {
			m.executionPicker.cursor--
		}
		return m, nil, true
	case "down":
		if m.executionPicker.cursor < len(m.executionPicker.items)+1 {
			m.executionPicker.cursor++
		}
		return m, nil, true
	}
	if !key.Matches(msg, m.keys.Choose) {
		return m, nil, true
	}
	choice := client.ExecutionChoice{}
	if m.executionPicker.cursor == 1 {
		choice.None = true
	}
	if m.executionPicker.cursor >= 2 {
		item := m.executionPicker.items[m.executionPicker.cursor-2]
		choice.TemplateID, choice.Revision = item.ID, item.Revision
	}
	m.executionPicker.creating = true
	m.executionPicker.status = "Creating session with the selected execution option…"
	token, oldID, deps := m.executionPicker.token, m.sessionID, m.deps
	sel, mode := m.createModelSelection, m.desiredMode()
	return m, func() tea.Msg {
		var id string
		var caps client.Capabilities
		var resolved client.ResolvedModel
		var err error
		if choice == (client.ExecutionChoice{}) {
			id, caps, resolved, err = deps.Session.CreateSession(deps.Ctx, sel, mode)
		} else {
			id, caps, resolved, err = deps.Session.(ExecutionCreator).CreateSessionWithExecution(deps.Ctx, sel, mode, choice)
		}
		return executionCreatedMsg{token: token, oldID: oldID, ready: client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved, Mode: mode}, err: err}
	}, true
}

func (m Model) updateExecutionMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch result := msg.(type) {
	case executionInventoryMsg:
		if !m.executionPicker.open || result.token != m.executionPicker.token {
			return m, nil, true
		}
		m.executionPicker.loading = false
		if result.err != nil {
			if errors.Is(result.err, client.ErrExecutionTemplatesDisabled) {
				m.executionPicker.status = "Template catalog disabled; default and none still work."
			} else {
				m.executionPicker.status = "Template catalog unavailable. Retry /execution later; default and none still work."
			}
			return m, nil, true
		}
		m.executionPicker.items = result.inventory.Items
		if len(result.inventory.Items) == 0 {
			m.executionPicker.status = "No eligible templates; default and none still work."
		} else {
			m.executionPicker.status = "Select an exact eligible revision. Catalog entries are labels, not infrastructure controls."
		}
		return m, nil, true
	case executionCreatedMsg:
		if !m.executionPicker.open || result.token != m.executionPicker.token || m.sessionID != result.oldID {
			return m, nil, true
		}
		m.executionPicker.creating = false
		if result.err != nil {
			m.executionPicker.status = "Could not create with the selected execution option. The revision may have changed or capacity may be full. Refresh /execution and retry; no fallback was created."
			return m, nil, true
		}
		m.executionPicker.open = false
		m = m.resetSession()
		mm, cmd, _ := m.applySessionReady(result.ready)
		m = mm.(Model)
		m.statusMsg = fmt.Sprintf("execution ready — files: %t, built-in Shell: %t", result.ready.Capabilities.ExecutionFiles, result.ready.Capabilities.BuiltInShell)
		m.refreshView()
		return m, tea.Batch(cmd, m.closeSessionCmd(result.oldID), m.prompt.Focus()), true
	}
	return m, nil, false
}

func (m Model) renderExecutionPicker() string {
	st := m.executionPicker
	var b strings.Builder
	b.WriteString(m.deps.Theme.Style("title").Render("New session · execution") + "\n")
	b.WriteString("The current session stays unchanged until the new session is created.\n\n")
	rows := []string{"Deployment default (files and Shell depend on deployment)", "None (no files or built-in Shell)"}
	for _, item := range st.items {
		label := item.Name
		if label == "" {
			label = item.ID
		}
		rows = append(rows, terminaltext.Sanitize(label)+" ("+terminaltext.Sanitize(item.ID)+" / "+terminaltext.Sanitize(item.Revision)+")"+fmt.Sprintf(" — declared files: %t, built-in Shell: %t", item.DeclaredExecutionFiles, item.DeclaredBuiltInShell))
	}
	for i, row := range rows {
		prefix := "  "
		if i == st.cursor {
			prefix = "▶ "
		}
		b.WriteString(prefix + row + "\n")
	}
	b.WriteString("\n" + st.status + "\n↑/↓: select · enter: create · esc: cancel")
	return b.String()
}
