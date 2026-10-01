package ui

import (
	"context"
	"errors"
	"testing"

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
