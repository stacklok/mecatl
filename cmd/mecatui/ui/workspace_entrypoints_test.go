package ui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestWorkspaceEntryPoints(t *testing.T) {
	for _, running := range []bool{false, true} {
		for _, steering := range []bool{false, true} {
			for _, route := range []string{"typed", "palette", "key", "remapped ctrl+f12", "remapped ctrl+f13"} {
				for _, serverCommands := range []bool{false, true} {
					t.Run(fmt.Sprintf("running=%t/steer=%t/%s/server=%t", running, steering, route, serverCommands), func(t *testing.T) {
						m, send := builtinDispatchModel(t, client.Capabilities{Steer: steering, SlashCommands: serverCommands}, false)
						if running {
							m.phase = phaseRunning
						}
						ph := m.phase
						ctx, cancel := context.WithCancel(t.Context())
						defer cancel()
						m.cancelRun = cancel
						m.queued = []string{"queued follow-up"}
						m.queuePaused = "paused"
						steer := &steerState{Phase: steerSent, Text: "existing steer"}
						m.steer = steer
						calls := 0
						m.deps.WorkspaceSnapshot = func(context.Context, string) (WorkspaceSnapshot, error) { calls++; return WorkspaceSnapshot{}, nil }
						// Even a server command with this exact name cannot replace the builtin.
						m.palette.commands = []client.Command{{Name: "workspace", Description: "server collision"}}
						m.palette.fetched = true
						var cmd tea.Cmd
						closeKey := tea.KeyPressMsg{Code: tea.KeyEscape}
						switch route {
						case "typed":
							m.prompt.Rewrite(" /WORKSPACE ")
							m, cmd = pressEnter(t, m)
						case "palette":
							m = typeText(t, m, "/worksp")
							if !m.palette.open || len(m.palette.filtered) != 1 || !m.palette.filtered[0].Builtin || m.palette.filtered[0].Name != "workspace" || !strings.Contains(m.palette.filtered[0].Description, "snapshot") {
								t.Fatalf("missing unconditional builtin palette row: %+v", m.palette)
							}
							m, cmd = pressEnter(t, m)
						case "key", "remapped ctrl+f12", "remapped ctrl+f13":
							m.prompt.Rewrite("unsent draft")
							key := tea.KeyPressMsg{Code: tea.KeyF10}
							if strings.HasPrefix(route, "remapped") {
								m.keys = applyKeyOverrides(m.keys, map[string][]string{"Workspace": {"ctrl+f12", "ctrl+f13"}})
								m, _ = pressKey(m, key)
								if m.modal != nil {
									t.Fatal("default key remained active after rebind")
								}
								key = tea.KeyPressMsg{Code: tea.KeyF12, Mod: tea.ModCtrl}
								if route == "remapped ctrl+f13" {
									key.Code = tea.KeyF13
								}
							}
							closeKey = key
							m, cmd = pressKey(m, key)
						}
						s, ok := m.modal.(*workspaceState)
						if !ok {
							t.Fatalf("entry point did not open workspace: %T", m.modal)
						}
						if m.phase != ph || m.prompt.Focused() || ctx.Err() != nil || !reflect.DeepEqual(m.queued, []string{"queued follow-up"}) || m.steer != steer || m.queuePaused != "paused" {
							t.Fatal("opening changed run, queue, steer, or prompt ownership")
						}
						wantDraft := ""
						if route == "key" || strings.HasPrefix(route, "remapped") {
							wantDraft = "unsent draft"
						}
						if m.prompt.Value() != wantDraft {
							t.Fatalf("draft = %q, want %q", m.prompt.Value(), wantDraft)
						}
						if calls != 0 {
							t.Fatal("snapshot ran synchronously")
						}
						m = applyAll(m, firstBatchLeaf(t, cmd))
						if calls != 1 {
							t.Fatalf("snapshot calls = %d", calls)
						}
						body, _ := s.Render(120, 60)
						closeHint := "f10 / esc: close"
						if strings.HasPrefix(route, "remapped") {
							closeHint = "ctrl+f12 / esc: close"
						}
						if !strings.Contains(stripANSIstr(body), closeHint) {
							t.Fatalf("workspace footer lacks live toggle/close hint: %q", stripANSIstr(body))
						}
						m = applyAll(m, tea.PasteMsg{Content: "nonempty filter"})
						if s.filter.Value() != "nonempty filter" {
							t.Fatalf("workspace filter = %q, want nonempty filter", s.filter.Value())
						}
						if strings.HasPrefix(route, "remapped") {
							m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF10})
							if m.modal != s {
								t.Fatal("default key closed workspace after rebind")
							}
						}
						m, _ = pressKey(m, closeKey)
						if m.modal != nil || !s.closed || m.phase != ph || ctx.Err() != nil || m.prompt.Value() != wantDraft || !m.prompt.Focused() || !reflect.DeepEqual(m.queued, []string{"queued follow-up"}) || m.steer != steer || m.queuePaused != "paused" || len(send.frames()) != 0 {
							t.Fatal("opening/closing submitted, cancelled, queued, steered, or changed the draft")
						}
					})
				}
			}
		}
	}
}

func TestWorkspaceArgumentsAndModalPriority(t *testing.T) {
	for _, running := range []bool{false, true} {
		m, send := builtinDispatchModel(t, client.Capabilities{Steer: true}, false)
		if running {
			m.phase = phaseRunning
		}
		m = typeText(t, m, "/workspace unexpected")
		m, _ = pressEnter(t, m)
		if m.modal != nil || m.prompt.Value() != "/workspace unexpected" || !strings.Contains(stripANSIstr(m.statusMsg), "does not take arguments") || len(send.frames()) != 0 || len(m.queued) != 0 || m.steer != nil {
			t.Fatal("bad arguments were not rejected locally with input preserved")
		}
		m.prompt.Reset()
		mm, _ := m.runHelp()
		m = mm.(Model)
		owner := m.modal
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF10})
		if m.modal != owner {
			t.Fatal("F10 replaced another modal")
		}
	}
}

func TestWorkspaceRemappedHelp(t *testing.T) {
	keys := applyKeyOverrides(defaultKeys(), map[string][]string{"Workspace": {"ctrl+f12", "ctrl+f13"}})
	help := stripANSIstr(helpBody(aztec(), client.Capabilities{}, keyMarkings(keys)))
	for _, line := range strings.Split(help, "\n") {
		if strings.Contains(line, "inspect a workspace snapshot") {
			if !strings.Contains(line, "ctrl+f12/ctrl+f13") || !strings.Contains(line, "/workspace") || strings.Contains(line, "f10") {
				t.Fatalf("wrong workspace help: %s", line)
			}
			return
		}
	}
	t.Fatal("missing unconditional workspace help")
}
