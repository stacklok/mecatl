package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestMecatuiTypedScrollbackModel_Scenario1_RebuildResetsDocumentIdentity(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.conv.addNotice("old document marker")
	m.conv.recordFileChange("old.go")
	oldBlock := m.conv.scrollback.SnapshotAt(0)
	oldAppendix, ok := m.conv.scrollback.AppendixSnapshot()
	if !ok {
		t.Fatal("old document appendix missing")
	}
	m.refreshView()
	if len(m.rend.blocks.rendered) == 0 {
		t.Fatal("old document did not populate renderer cache")
	}
	m.sel.active = true
	m.selBase = "old selection"
	m.conversationView = conversationView{mode: anchored, anchor: readingAnchor{blockID: uint64(oldBlock.ID)}}

	m = m.resetSession()
	if m.conv.scrollback.Len() != 0 {
		t.Fatal("rebuild retained scrollback cards")
	}
	if _, ok := m.conv.scrollback.AppendixSnapshot(); ok {
		t.Fatal("rebuild retained appendix identity or membership")
	}
	if len(m.rend.blocks.rendered) != 0 || m.sel.active || m.selBase != "" || m.conversationView.mode != followTail {
		t.Fatalf("rebuild retained document projection: cache=%d selection=%+v base=%q view=%+v", len(m.rend.blocks.rendered), m.sel, m.selBase, m.conversationView)
	}

	m.conv.addNotice("new document marker")
	m.conv.recordFileChange("new.go")
	newBlock := m.conv.scrollback.SnapshotAt(0)
	newAppendix, ok := m.conv.scrollback.AppendixSnapshot()
	if !ok || newBlock.ID != 1 || newAppendix.ID != 2 {
		t.Fatalf("new document identities did not restart document-locally: block=%d appendix=%+v", newBlock.ID, newAppendix)
	}
	if oldBlock.ID != newBlock.ID || oldAppendix.ID != newAppendix.ID {
		t.Fatal("precondition: rebuilt document did not reuse document-local numeric slots")
	}
	m.refreshView()
	if got := m.vp.GetContent(); !strings.Contains(got, "new document marker") || strings.Contains(got, "old document marker") {
		t.Fatalf("rebuilt document reused stale rendered state: %q", got)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario2_LiveAndReplayProjectionParity(t *testing.T) {
	live, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	live.phase = phaseRunning
	live.conv.addUser("inspect") // live prompts are projected at submission time.

	events := []tea.Msg{
		client.TurnStartMsg{Turn: 1},
		client.ReasoningDeltaMsg{Text: "reason"},
		client.AssistantDeltaMsg{Text: "answer"},
		client.ToolCallMsg{ID: "read", Name: "Read", Args: `{"path":"a.go"}`},
		client.HookMsg{Text: "checked", Phase: "PreToolUse", Tool: "Read"},
		client.ToolResultMsg{CallID: "read", Content: "contents"},
		client.ToolCallMsg{ID: "sub", Name: "Subagent", Args: `{}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "inspect"},
		client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "sub", ChildID: "child", Stop: "end_turn"},
		client.ToolResultMsg{CallID: "sub", Content: "done"},
		client.ToolCallMsg{ID: "team", Name: "Team", Args: `{}`},
		client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team", TeamID: "team-1", Roster: []client.TeamMemberSpec{{Name: "reviewer"}}},
		client.TeamMsg{Kind: client.TeamTasks, ParentCallID: "team", TeamID: "team-1", Tasks: []client.TeamTask{{ID: "task", Description: "review"}}},
		client.ToolResultMsg{CallID: "team", Content: "joined"},
		client.TurnEndMsg{Usage: client.Usage{InputTokens: 10, OutputTokens: 2}},
		client.CompactionMsg{},
		client.DeliveryNoteMsg{ScheduleName: "nightly", FireID: "fire-1", Text: "delivered"},
		client.ResultMsg{Stop: stopError, Error: "provider failed", Permanent: true},
	}
	for _, event := range events {
		live = applyAll(live, event)
	}

	replay := sessionsState{}
	replay.applyReplayEvent(client.UserPromptMsg{Text: "inspect"})
	for _, event := range events {
		replay.applyReplayEvent(event)
	}

	liveSnapshots := conversationSnapshots(&live.conv.scrollback)
	replaySnapshots := conversationSnapshots(&replay.transcript.scrollback)
	if !reflect.DeepEqual(liveSnapshots, replaySnapshots) {
		t.Fatalf("live/replay typed projection diverged:\nlive:   %#v\nreplay: %#v", liveSnapshots, replaySnapshots)
	}
}

func conversationSnapshots(c *scrollback.Conversation) []scrollback.BlockSnapshot {
	out := make([]scrollback.BlockSnapshot, c.Len())
	for i := range out {
		out[i] = c.SnapshotAt(i)
	}
	return out
}
