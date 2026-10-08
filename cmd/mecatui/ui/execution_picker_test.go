package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type executionTestSession struct {
	*fakeConv
	seen []client.ExecutionChoice
	err  error
}

func (s *executionTestSession) CreateSessionWithExecution(_ context.Context, _ client.ModelSelection, _ string, choice client.ExecutionChoice) (string, client.Capabilities, client.ResolvedModel, error) {
	s.seen = append(s.seen, choice)
	if s.err != nil {
		return "", client.Capabilities{}, client.ResolvedModel{}, s.err
	}
	return "new", client.Capabilities{ExecutionFiles: true, BuiltInShell: true}, client.ResolvedModel{}, nil
}

type executionTestLister struct {
	inventory client.ExecutionTemplateInventory
	err       error
}

func (s executionTestLister) ListExecutionTemplates(context.Context) (client.ExecutionTemplateInventory, error) {
	return s.inventory, s.err
}

func TestExecutionPickerNoFallbackAndCapabilities(t *testing.T) {
	revision := "v1-" + strings.Repeat("a", 64)
	session := &executionTestSession{fakeConv: &fakeConv{}}
	m := newTestModelFromDeps(Deps{Session: session, ExecutionTemplates: executionTestLister{inventory: client.ExecutionTemplateInventory{Items: []client.ExecutionTemplate{{ID: "safe", Revision: revision, Name: "Safe"}}}}, Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette())})
	m.phase = phaseIdle
	m.sessionID = "old"
	m.caps.ExecutionTemplates = true
	mm, cmd := m.openExecution()
	m = mm.(Model)
	if !m.executionPicker.loading || cmd == nil {
		t.Fatal("catalog did not load")
	}
	mm, _, _ = m.updateExecutionMsg(cmd())
	m = mm.(Model)
	if len(m.executionPicker.items) != 1 {
		t.Fatal("catalog entry missing")
	}
	m.executionPicker.cursor = 2
	mm, cmd, _ = m.onExecutionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("no creation command")
	}
	session.err = errors.New("private: do not display")
	mm, _, _ = m.updateExecutionMsg(cmd())
	m = mm.(Model)
	if m.sessionID != "old" || session.seen[0].Revision != revision || strings.Contains(m.renderExecutionPicker(), "private:") {
		t.Fatal("selection fell back or exposed upstream error")
	}
	session.err = nil
	mm, cmd, _ = m.onExecutionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	mm, _, _ = m.updateExecutionMsg(cmd())
	m = mm.(Model)
	if m.sessionID != "new" || !m.caps.ExecutionFiles || !m.caps.BuiltInShell {
		t.Fatal("creation did not bind true session capabilities")
	}
	if detail := renderSessionDetails(m.deps.Theme, m.sessionDetails(), helpKeys{}, 80, 25); !strings.Contains(detail, "Execution files: true") || !strings.Contains(detail, "Built-in Shell: true") {
		t.Fatal("session details did not show bound execution capabilities")
	}
}

func TestExecutionPickerInventoryStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"empty", nil, "No eligible templates"},
		{"disabled", client.ErrExecutionTemplatesDisabled, "catalog disabled"},
		{"unavailable", errors.New("private provider address"), "catalog unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModelFromDeps(Deps{Session: &executionTestSession{fakeConv: &fakeConv{}}, ExecutionTemplates: executionTestLister{err: tc.err}, Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette())})
			m.phase = phaseIdle
			m.caps.ExecutionTemplates = true
			mm, cmd := m.openExecution()
			m = mm.(Model)
			mm, _, _ = m.updateExecutionMsg(cmd())
			m = mm.(Model)
			if !strings.Contains(m.executionPicker.status, tc.want) || strings.Contains(m.executionPicker.status, "private") || len(m.executionPicker.items) != 0 {
				t.Fatalf("unexpected catalog state: %q", m.executionPicker.status)
			}
		})
	}
}

func TestExecutionPickerDisabledAndEmpty(t *testing.T) {
	session := &executionTestSession{fakeConv: &fakeConv{}}
	m := newTestModelFromDeps(Deps{Session: session, Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette())})
	m.phase = phaseIdle
	mm, cmd := m.openExecution()
	m = mm.(Model)
	if cmd != nil || len(m.executionPicker.items) != 0 || !strings.Contains(m.renderExecutionPicker(), "disabled") {
		t.Fatal("disabled catalog did not keep default and none")
	}
	m.executionPicker.cursor = 1
	mm, cmd, _ = m.onExecutionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = mm
	if cmd == nil {
		t.Fatal("none creation unavailable")
	}
	_ = cmd()
	if len(session.seen) != 1 || !session.seen[0].None {
		t.Fatal("none not forwarded")
	}
}
