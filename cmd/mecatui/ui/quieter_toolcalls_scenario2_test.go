package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/keymap"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

var (
	ctrlT  = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	ctrlF9 = tea.KeyPressMsg{Code: tea.KeyF9, Mod: tea.ModCtrl}
	f9     = tea.KeyPressMsg{Code: tea.KeyF9}
)

// scenario2Model builds a session-ready model at the given geometry. A running
// model carries a real client.Stream over transport fakes.
func scenario2Model(t *testing.T, width, height int, noMouse bool, ph phase, overrides map[string][]string) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background(), NoMouse: noMouse, KeyOverrides: overrides})
	m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: height})
	m.sessionID = "sess-test-0001"
	if ph == phaseRunning {
		m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	}
	m.phase = ph
	return m
}

func renderInspector(t *testing.T, m Model, width, height int) string {
	t.Helper()
	s := toolcallsForTest(t, m)
	if s == nil {
		t.Fatal("inspector not open")
	}
	body, _ := s.Render(width, height)
	return stripANSIstr(body)
}

func TestMecatuiQuieterToolCalls_Scenario2_InspectorShortcutAndFocus(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
		noMouse       bool
	}{{"narrow-nomouse", 40, 12, true}, {"normal-mouse", 100, 30, false}} {
		for _, ph := range []struct {
			name  string
			phase phase
		}{{"idle", phaseIdle}, {"running", phaseRunning}} {
			t.Run(size.name+"/"+ph.name, func(t *testing.T) {
				// Empty session: the shortcut shows the same empty state as /toolcalls.
				empty := scenario2Model(t, size.width, size.height, size.noMouse, ph.phase, nil)
				want := renderInspector(t, openToolcallsForTest(t, empty), size.width, size.height)
				opened, _ := pressKey(empty, ctrlT)
				if got := renderInspector(t, opened, size.width, size.height); got != want {
					t.Fatalf("empty shortcut inspector differs from /toolcalls:\n got %q\nwant %q", got, want)
				}
				opened, _ = pressKey(opened, tea.KeyPressMsg{Code: tea.KeyEsc})
				if opened.modal != nil {
					t.Fatalf("esc did not close empty inspector: %T", opened.modal)
				}

				// Populated, scrolled-up reader with a draft.
				m := scenario2Model(t, size.width, size.height, size.noMouse, ph.phase, nil)
				m.conv.addUser("show me a long answer")
				m.conv.appendAssistant(strings.Repeat("line of streamed output\n", 120))
				for _, id := range []string{"one", "two", "three"} {
					m = applyAll(m,
						client.ToolCallMsg{ID: id, Name: "Read", Args: `{"path":"` + id + `.go"}`},
						client.ToolResultMsg{CallID: id, Content: "contents of " + id},
					)
				}
				m.conv.appendAssistant(strings.Repeat("trailing output\n", 60))
				m.conversationView.mode = followTail
				m.refreshView()
				m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
				if m.conversationView.mode != anchored || m.vp.YOffset() == 0 || m.vp.AtBottom() {
					t.Fatalf("precondition: want anchored mid-conversation reader, mode=%v offset=%d atBottom=%v", m.conversationView.mode, m.vp.YOffset(), m.vp.AtBottom())
				}
				m.prompt.Rewrite("draft survives")
				beforeOffset, beforeAnchor := m.vp.YOffset(), m.conversationView.anchor

				// Parity with /toolcalls: entries, selection, and rendered list.
				ref := toolcallsForTest(t, openToolcallsForTest(t, m))
				refBody := renderInspector(t, openToolcallsForTest(t, m), size.width, size.height)
				m, _ = pressKey(m, ctrlT)
				got := toolcallsForTest(t, m)
				if got == nil || len(got.entries) != 3 || len(got.entries) != len(ref.entries) || got.selected != ref.selected {
					t.Fatalf("shortcut inspector entries/selection differ from /toolcalls: got %#v want %#v", got, ref)
				}
				for i := range ref.entries {
					if got.entries[i].blockID != ref.entries[i].blockID {
						t.Fatalf("entry %d block = %d, want %d", i, got.entries[i].blockID, ref.entries[i].blockID)
					}
				}
				if body := renderInspector(t, m, size.width, size.height); body != refBody {
					t.Fatalf("shortcut inspector render differs from /toolcalls:\n got %q\nwant %q", body, refBody)
				}

				// List and detail own their ordinary key input. No inspector key may
				// submit the hidden draft or mutate transcript/tool state, and handled
				// keys emit no command that could carry approval/control traffic.
				beforeBlocks, beforeEntries := m.conv.scrollback.Len(), len(toolcallsForTest(t, m).entries)
				beforeCalls := make([]scrollback.BlockSnapshot, beforeBlocks)
				for i := range beforeCalls {
					beforeCalls[i] = m.conv.scrollback.SnapshotAt(i)
				}
				beforePhase, beforeDraft := m.phase, m.prompt.Value()
				compactBefore := toolcallsForTest(t, m).compact
				for _, key := range []tea.KeyPressMsg{{Code: 'x'}, {Code: tea.KeyEnter}} {
					var cmd tea.Cmd
					m, cmd = pressKey(m, key)
					if cmd != nil {
						t.Fatalf("inspector list key %q emitted command output", key)
					}
					if m.conv.scrollback.Len() != beforeBlocks || len(toolcallsForTest(t, m).entries) != beforeEntries || m.phase != beforePhase || m.prompt.Value() != beforeDraft {
						t.Fatalf("inspector list key %q changed hidden state: blocks=%d entries=%d phase=%v draft=%q", key, m.conv.scrollback.Len(), len(toolcallsForTest(t, m).entries), m.phase, m.prompt.Value())
					}
				}
				if !compactBefore {
					if !toolcallsForTest(t, m).detail {
						t.Fatal("list Enter did not reach inspector detail")
					}
					for _, key := range []tea.KeyPressMsg{{Code: 'y'}, {Code: tea.KeyEnter}} {
						var cmd tea.Cmd
						m, cmd = pressKey(m, key)
						if cmd != nil || m.conv.scrollback.Len() != beforeBlocks || len(toolcallsForTest(t, m).entries) != beforeEntries || m.phase != beforePhase || m.prompt.Value() != beforeDraft {
							t.Fatalf("inspector detail key %q leaked into transcript, control output, or prompt", key)
						}
					}
					m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
				}
				for i, before := range beforeCalls {
					if after := m.conv.scrollback.SnapshotAt(i); !reflect.DeepEqual(after, before) {
						t.Fatalf("inspector keys mutated existing conversation block %d", i)
					}
				}

				// A call and its result arrive through Update while the inspector is open.
				m = applyAll(m,
					client.ToolCallMsg{ID: "late", Name: "Read", Args: `{"path":"late.go"}`},
					client.ToolResultMsg{CallID: "late", Content: "arrived while inspecting"},
				)
				got = toolcallsForTest(t, m)
				if len(got.entries) != 4 || got.entries[3].state != toolcallDone {
					t.Fatalf("streamed call/result did not reach the open inspector: %#v", got.entries)
				}
				_ = toolBlockID(t, m.conv.scrollback, "late") // landed in scrollback

				// Selection/detail behave as /toolcalls: enter opens detail and esc backs
				// out. A frame too small for the list footer uses the shared compact state,
				// where enter is consumed without opening detail.
				_ = renderInspector(t, m, size.width, size.height)
				compact := toolcallsForTest(t, m).compact
				refState := toolcallsForTest(t, openToolcallsForTest(t, m))
				refState.Render(size.width, size.height)
				if compact != refState.compact {
					t.Fatalf("compact = %v, /toolcalls compact = %v", compact, refState.compact)
				}
				m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				if got := toolcallsForTest(t, m); got == nil || got.detail == compact {
					t.Fatalf("enter detail=%v in compact=%v inspector", got != nil && got.detail, compact)
				}
				if !compact {
					m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
					if s := toolcallsForTest(t, m); s == nil || s.detail {
						t.Fatal("esc from detail must return to the inspector list")
					}
				}
				if m.prompt.Value() != "draft survives" {
					t.Fatalf("inspector keys reached the prompt: %q", m.prompt.Value())
				}
				m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
				if m.modal != nil {
					t.Fatalf("esc did not close the inspector: %T", m.modal)
				}
				if m.prompt.Value() != "draft survives" {
					t.Fatalf("draft = %q after close", m.prompt.Value())
				}
				if m.conversationView.mode != anchored || m.vp.YOffset() != beforeOffset || m.conversationView.anchor != beforeAnchor {
					t.Fatalf("reader moved: mode=%v offset=%d anchor=%+v, want anchored offset=%d anchor=%+v",
						m.conversationView.mode, m.vp.YOffset(), m.conversationView.anchor, beforeOffset, beforeAnchor)
				}
				if m.phase != ph.phase {
					t.Fatalf("phase = %v, want %v", m.phase, ph.phase)
				}
			})
		}
	}
}

// assertApprovalPending proves no verdict was sent or resolved and the prompt is
// unchanged.
func assertApprovalPending(t *testing.T, m Model, askID, prompt string) *approvalSurface {
	t.Helper()
	s := approvalSurfaceOf(t, m)
	if m.pendingApproval != nil || m.phase != phaseAwaitingApproval || s.ask.AskID != askID || m.stream.ApprovalResolved(askID) {
		t.Fatalf("verdict resolved: pending=%v phase=%v ask=%q streamResolved=%v", m.pendingApproval, m.phase, s.ask.AskID, m.stream.ApprovalResolved(askID))
	}
	if m.prompt.Value() != prompt {
		t.Fatalf("prompt = %q, want %q", m.prompt.Value(), prompt)
	}
	return s
}

func TestMecatuiQuieterToolCalls_Scenario2_ContextualShortcutAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name   string
		build  func(*testing.T) Model
		detail bool
		title  string
	}{
		{"shell", func(t *testing.T) Model { return shellAskModel(t, longShellArgs) }, true, "Ask args: Shell"},
		{"edit", func(t *testing.T) Model {
			return approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:edit-1", Tool: "Edit", Args: `{"path":"main.go","old_string":"old line","new_string":"new line"}`})
		}, true, "Approval details: Edit"},
		{"write", func(t *testing.T) Model {
			return approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:write-1", Tool: "Write", Args: `{"path":"new.go","content":"package main"}`})
		}, true, "Approval details: Write"},
		{"plan", func(t *testing.T) Model { return planAskModel(t, true) }, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			askID := approvalSurfaceOf(t, m).ask.AskID
			m.prompt.Rewrite("draft")
			beforeExpand := m.expandConversation

			// ExpandConversation is inert while approval owns the keyboard.
			m, _ = pressKey(m, f9)
			if s := assertApprovalPending(t, m, askID, "draft"); s.argsViewOpen || m.expandConversation != beforeExpand {
				t.Fatal("ExpandConversation changed approval state")
			}

			m, _ = pressKey(m, ctrlT)
			s := assertApprovalPending(t, m, askID, "draft")
			if s.argsViewOpen != tc.detail || m.expandConversation != beforeExpand {
				t.Fatalf("Toolcalls detail open=%v expand=%v, want open=%v expand=%v", s.argsViewOpen, m.expandConversation, tc.detail, beforeExpand)
			}
			if tc.detail {
				if view := stripANSIstr(m.View().Content); !strings.Contains(view, tc.title) {
					t.Fatalf("detail view missing %q:\n%s", tc.title, view)
				}
				m, _ = pressKey(m, f9)
				if s := assertApprovalPending(t, m, askID, "draft"); !s.argsViewOpen {
					t.Fatal("ExpandConversation changed the open detail view")
				}
				m, _ = pressKey(m, ctrlT)
				if s := assertApprovalPending(t, m, askID, "draft"); s.argsViewOpen {
					t.Fatal("second Toolcalls press must close approval details")
				}
			}
		})
	}

	t.Run("help overlay swallows Toolcalls", func(t *testing.T) {
		m := scenario2Model(t, 100, 30, false, phaseIdle, nil)
		m, _ = pressKey(m, qmark())
		if !m.showHelp {
			t.Fatal("precondition: help did not open")
		}
		m, _ = pressKey(m, ctrlT)
		if !m.showHelp || m.modal != nil || m.prompt.Value() != "" {
			t.Fatalf("help must own Toolcalls: showHelp=%v modal=%T prompt=%q", m.showHelp, m.modal, m.prompt.Value())
		}
	})

	t.Run("rebind follows effective chord", func(t *testing.T) {
		overrides := map[string][]string{"Toolcalls": {"ctrl+f9"}}
		res, err := keymap.Parse(overrides)
		if err != nil {
			t.Fatal(err)
		}
		if err := keymap.Validate(res); err != nil {
			t.Fatalf("ctrl+f9 Toolcalls rejected: %v", err)
		}

		m := scenario2Model(t, 100, 30, false, phaseRunning, overrides)
		const askID = "sess-test-0001:1:bash-1"
		m = applyAll(m, client.PermissionAskMsg{AskID: askID, Tool: "Shell", Args: `{"command":"echo hi"}`, Reason: "Shell requires approval"})
		view := stripANSIstr(m.View().Content)
		if !strings.Contains(view, "ctrl+f9 full args") || strings.Contains(view, "ctrl+t full args") {
			t.Fatalf("approval hint does not follow rebound Toolcalls:\n%s", view)
		}
		m, _ = pressKey(m, ctrlT)
		if s := assertApprovalPending(t, m, askID, ""); s.argsViewOpen {
			t.Fatal("old ctrl+t still toggles approval details after rebind")
		}
		m, _ = pressKey(m, ctrlF9)
		if s := assertApprovalPending(t, m, askID, ""); !s.argsViewOpen {
			t.Fatal("rebound Toolcalls chord did not open approval details")
		}

		conv := scenario2Model(t, 100, 30, false, phaseIdle, overrides)
		conv, _ = pressKey(conv, ctrlT)
		if conv.modal != nil || conv.prompt.Value() != "" {
			t.Fatalf("old ctrl+t acted after rebind: modal=%T prompt=%q", conv.modal, conv.prompt.Value())
		}
		conv, _ = pressKey(conv, ctrlF9)
		if toolcallsForTest(t, conv) == nil {
			t.Fatal("rebound Toolcalls chord did not open the inspector")
		}
	})

	t.Run("approval detail and focus keys remain functional", func(t *testing.T) {
		m := shellAskModel(t, longShellArgs)
		askID := approvalSurfaceOf(t, m).ask.AskID
		m, _ = pressKey(m, ctrlT)
		s := assertApprovalPending(t, m, askID, "")
		if !s.argsViewOpen {
			t.Fatal("precondition: Toolcalls did not open approval details")
		}
		m, _ = pressKey(m, tea.KeyPressMsg{Code: 'r'})
		s = assertApprovalPending(t, m, askID, "")
		if !s.argsViewRaw {
			t.Fatal("RawArgs did not toggle in approval details")
		}
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
		s = assertApprovalPending(t, m, askID, "")
		if s.ask.focusedVerdict != client.VerdictAllowAlways {
			t.Fatalf("tab focused %v, want allow always", s.ask.focusedVerdict)
		}
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
		s = assertApprovalPending(t, m, askID, "")
		if s.ask.focusedVerdict != client.VerdictDeny {
			t.Fatalf("right focused %v, want deny", s.ask.focusedVerdict)
		}
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
		s = assertApprovalPending(t, m, askID, "")
		if s.ask.focusedVerdict != client.VerdictAllowAlways {
			t.Fatalf("left focused %v, want allow always", s.ask.focusedVerdict)
		}
	})

	t.Run("Validate rejects verdict overlap", func(t *testing.T) {
		for _, tc := range []struct {
			verdict   string
			overrides map[string][]string
		}{
			{"Allow", map[string][]string{"Toolcalls": {"enter"}}},
			{"Allow", map[string][]string{"Toolcalls": {"ctrl+a"}, "Allow": {"ctrl+a"}}},
			{"AllowAlways", map[string][]string{"Toolcalls": {"ctrl+w"}, "AllowAlways": {"ctrl+w"}}},
			{"Deny", map[string][]string{"Toolcalls": {"esc"}}},
			{"Deny", map[string][]string{"Toolcalls": {"ctrl+e"}, "Deny": {"ctrl+e"}}},
		} {
			res, err := keymap.Parse(tc.overrides)
			if err != nil {
				t.Fatal(err)
			}
			err = keymap.Validate(res)
			if err == nil || !strings.Contains(err.Error(), `"Toolcalls" and "`+tc.verdict+`"`) {
				t.Errorf("Validate(%v) = %v, want Toolcalls/%s collision", tc.overrides, err, tc.verdict)
			}
		}
	})
}
