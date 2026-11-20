package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// lockedDiag records diagnostics from any goroutine.
type lockedDiag struct {
	mu      sync.Mutex
	entries []diagEntry
}

func (d *lockedDiag) Log(_ context.Context, level port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = append(d.entries, diagEntry{level: level, msg: msg, args: args})
}

func (d *lockedDiag) With(...any) port.Diagnostics { return d }

// find returns the args of the entries logged with msg.
func (d *lockedDiag) find(msg string) [][]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out [][]any
	for _, e := range d.entries {
		if e.msg == msg {
			out = append(out, e.args)
		}
	}
	return out
}

// diagArg returns the value logged under key in args.
func diagArg(args []any, key string) string {
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == key {
			return fmt.Sprint(args[i+1])
		}
	}
	return ""
}

// TestIdentityBrokerRegistersHelperAgents is #2555's slice test through the
// real wiring: with the real broker and no shell, a subagent the main agent
// creates is registered before it runs, with its own identity, the root's
// main agent as its parent, and the definition it runs.
func TestIdentityBrokerRegistersHelperAgents(t *testing.T) {
	cases := []struct {
		name, args, definition string
	}{
		{name: "a default subagent", args: `{"prompt":"look"}`, definition: "system/explorer"},
		{name: "a subagent naming an operator definition", args: `{"prompt":"review","agent":"code-reviewer"}`, definition: "global/code-reviewer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentsDir := t.TempDir()
			def := "---\nname: code-reviewer\ndescription: Reviews code.\nmodel: inherit\ntools: [Read, Grep]\n---\nReview the change.\n"
			if err := os.WriteFile(filepath.Join(agentsDir, "code-reviewer.md"), []byte(def), 0o600); err != nil {
				t.Fatal(err)
			}
			llm := mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(tc.args))),
				mockllm.TextTurn("child done"),
				mockllm.TextTurn("parent done"),
			)
			diag := &lockedDiag{}
			built, err := buildIsolated(t, t.Context(), Config{
				Workspace: t.TempDir(), MockProvider: llm, NoSoul: true, NoShell: true, IdentityBroker: true,
				AgentsDirs: []string{agentsDir}, AllowAllTools: true, GuardrailsDisabled: true, Diagnostics: diag,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(built.Close)
			alice := &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}
			ctx := session.WithPrincipal(t.Context(), alice)
			parent, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(ctx, parent.ID, "delegate")
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			if got := drainRun(run); got != "parent done" {
				t.Fatalf("terminal text = %q, want parent done", got)
			}
			built.Service.FinishRun(parent.ID, run)

			childID := session.SessionID("subagent-" + string(parent.ID) + "-delegate")
			registered := diag.find("registered helper agent")
			if len(registered) != 1 {
				t.Fatalf("helper registrations logged = %v, want one", registered)
			}
			args := registered[0]
			if diagArg(args, "session") != string(childID) || diagArg(args, "parent_session") != string(parent.ID) || diagArg(args, "definition") != tc.definition {
				t.Fatalf("helper registration logged %v, want %s under %s running %s", args, childID, parent.ID, tc.definition)
			}

			// The broker holds the registration: the child's incarnation is
			// taken, under the main agent, with the root's owner.
			child, err := built.Service.GetSession(ctx, childID)
			if err != nil {
				t.Fatalf("GetSession(%s): %v", childID, err)
			}
			open, found, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(parent.ID)})
			if err != nil || !found {
				t.Fatalf("FindSession = %v, %v", found, err)
			}
			main, err := built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
				SessionID: open.ID, HostSessionID: string(parent.ID), Owner: open.Owner, Incarnation: string(parent.Incarnation()),
				Definition: builtinMainRef(),
			})
			if err != nil {
				t.Fatalf("registering the main agent again: %v", err)
			}
			// Another definition for the child's incarnation is a takeover.
			_, err = built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
				SessionID: open.ID, HostSessionID: string(childID), Owner: open.Owner, Incarnation: string(child.Incarnation()),
				Definition: identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinBranch, Revision: builtinMainRef().Revision},
				Parent:     main.Agent, ParentHostSessionID: string(parent.ID),
			})
			if !errors.Is(err, identitybroker.NewError(identitybroker.CodeAlreadyExists, "", "")) {
				t.Fatalf("re-registering the helper with another definition = %v, want ALREADY_EXISTS", err)
			}
			if strings.HasSuffix(main.Agent.Subject, string(childID)) {
				t.Fatalf("the main agent's ID %q names the child", main.Agent.Subject)
			}
		})
	}
}
