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

func TestAgentCommandParsing(t *testing.T) {
	for _, tc := range []struct {
		in       string
		name     string
		bare, ok bool
	}{
		{"/agent sfs", "sfs", false, true},
		{"  /agent   sfs  ", "sfs", false, true},
		{"/AGENT sfs", "sfs", false, true},
		{"/agent", "", true, true},
		{"/agent   ", "", true, true},
		{"/agents", "", false, false},
		{"/agentx foo", "", false, false},
		{"hello /agent sfs", "", false, false},
	} {
		name, bare, ok := agentCommand(tc.in)
		if name != tc.name || bare != tc.bare || ok != tc.ok {
			t.Errorf("agentCommand(%q) = (%q, %v, %v), want (%q, %v, %v)", tc.in, name, bare, ok, tc.name, tc.bare, tc.ok)
		}
	}
}

// The agent-bound create must carry the operator's active model selection like
// every other create path: a def without its own model inherits it, whereas a
// zero selection falls to the server default (which may be a model the account
// cannot afford — the observed 402 on max_tokens).
func TestCreateSessionWithAgentCmdCarriesActiveSelection(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
	})
	m.createModelSelection = client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}

	msg := m.createSessionWithAgentCmd("old-session", "devils-advocate")()
	if _, ok := msg.(client.SessionReadyMsg); !ok {
		t.Fatalf("msg = %T, want SessionReadyMsg", msg)
	}
	conv.mu.Lock()
	defer conv.mu.Unlock()
	if conv.agentDefRequested != "devils-advocate" {
		t.Fatalf("agentDefRequested = %q, want devils-advocate", conv.agentDefRequested)
	}
	if conv.createdSel != (client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}) {
		t.Fatalf("createdSel = %+v, want the active selection", conv.createdSel)
	}
	if len(conv.closedIDs) != 1 || conv.closedIDs[0] != "old-session" {
		t.Fatalf("closedIDs = %v, want the old session closed", conv.closedIDs)
	}
}

func TestCreateSessionWithAgentCmdUnknownDefinitionIsRecoverable(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, agentDefErr: errors.New("boom")}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
	})
	msg := m.createSessionWithAgentCmd("", "nope")()
	failed, ok := msg.(agentSessionFailedMsg)
	if !ok {
		t.Fatalf("msg = %T, want agentSessionFailedMsg (NOT the terminal ConnectErrMsg)", msg)
	}
	if failed.name != "nope" || failed.unknown {
		t.Fatalf("failed = %+v, want name=nope and unknown=false for a non-InvalidArgument error", failed)
	}
}

func newAgentPickModel(t *testing.T, ag client.AgentLister) (Model, *fakeConv) {
	t.Helper()
	conv := &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: client.Capabilities{Agents: true}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Agents: ag, Theme: theme.New("aztec", theme.AztecPalette()),
		Workspace: "/workspace", Mode: "default", Ctx: context.Background(), NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: conv.caps},
	)
	return m, conv
}

// Bare /agent opens a filterable picker over the server's definitions; Enter on
// the highlighted row starts a session bound to it.
func TestAgentPickerFilterAndSelectStartsBoundSession(t *testing.T) {
	m, conv := newAgentPickModel(t, sampleAgents())
	mm, cmd := m.openAgentPicker()
	m = feedCmd(t, mm.(Model), cmd)

	picker, ok := m.modal.(*agentPickState)
	if !ok {
		t.Fatalf("modal = %T, want *agentPickState", m.modal)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "scout") || !strings.Contains(body, "writer") {
		t.Fatalf("picker should list both definitions, got:\n%s", body)
	}
	if len(picker.filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(picker.filtered))
	}

	m = typeText(t, m, "wri")
	if got := m.modal.(*agentPickState).filtered; len(got) != 1 || got[0].Name != "writer" {
		t.Fatalf("filter %q left %+v, want only writer", "wri", got)
	}

	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = feedCmd(t, mm.(Model), cmd)
	conv.mu.Lock()
	got := conv.agentDefRequested
	conv.mu.Unlock()
	if got != "writer" {
		t.Fatalf("agentDefRequested = %q, want writer", got)
	}
	if m.modal != nil {
		t.Fatalf("picker should close after a pick, modal = %T", m.modal)
	}
}

func TestAgentPickerIgnoresResultFromAnotherInstance(t *testing.T) {
	st := &agentPickState{loading: true}
	other := &agentPickState{}
	_, handled, _ := st.HandleMsg(agentPickResultMsg{owner: other, result: client.AgentsMsg{Agents: []client.Agent{{Name: "x"}}}})
	if !handled || !st.loading || len(st.agents) != 0 {
		t.Fatalf("a stale picker's result must be swallowed without updating this picker: handled=%v loading=%v agents=%v", handled, st.loading, st.agents)
	}
}

func TestAgentPickerShowsListError(t *testing.T) {
	m, _ := newAgentPickModel(t, &fakeAgents{err: errors.New("boom")})
	mm, cmd := m.openAgentPicker()
	m = feedCmd(t, mm.(Model), cmd)
	if body := stripANSIstr(m.View().Content); !strings.Contains(body, "list agents: boom") {
		t.Fatalf("picker should surface the list error, got:\n%s", body)
	}
}

// Submitting bare /agent consumes the command line, so the slash-command palette
// it opened must close too (an argument already closes the palette on typing).
func TestBareAgentCommandClosesPalette(t *testing.T) {
	m, _ := newAgentPickModel(t, sampleAgents())
	m = typeText(t, m, "/agent")
	if !m.palette.open {
		t.Fatal("palette should be open after typing /agent")
	}
	mm, _, handled := m.dispatchBareBuiltin("/agent")
	if !handled {
		t.Fatal("dispatchBareBuiltin(/agent) not handled")
	}
	if mm.(Model).palette.open {
		t.Fatal("palette still open after submitting /agent")
	}
}
