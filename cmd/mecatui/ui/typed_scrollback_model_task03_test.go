package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestMecatuiTypedScrollbackModel_Scenario2_ToolDelegationLifecycleInterleavings(t *testing.T) {
	var c conversation
	c.addTool("sub", "Subagent", `{}`)
	c.addNotice("between calls")
	c.addTool("team", "Team", `{}`)
	subID := c.scrollback.SnapshotAt(0).ID
	teamID := c.scrollback.SnapshotAt(2).ID
	applySubagentTo(&c, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "inspect"})
	applySubagentTo(&c, client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "sub", ChildID: "child", InnerKind: "tool.call", ToolName: "Read", ToolCount: 1})
	applyTeamTo(&c, client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team", TeamID: "team", Roster: []client.TeamMemberSpec{{Name: "reviewer"}}})
	if !c.resolveTool("sub", "started", false) || !c.resolveTool("team", "joined", false) {
		t.Fatal("typed results did not resolve")
	}
	// A background completion after its parent result remains on the original card.
	applySubagentTo(&c, client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "sub", ChildID: "child", ToolCount: 1, Stop: "end_turn"})
	sub := c.scrollback.SnapshotAt(0)
	team := c.scrollback.SnapshotAt(2)
	if sub.ID != subID || team.ID != teamID {
		t.Fatalf("specialization replaced card identity: sub=%d/%d team=%d/%d", sub.ID, subID, team.ID, teamID)
	}
	if got := sub.Payload.(scrollback.SubagentCardSnapshot); !got.Resolved || !got.Update.Done || got.Call.Arguments != `{}` {
		t.Fatalf("subagent lifecycle lost call/result/end state: %#v", got)
	}
	if got := team.Payload.(scrollback.TeamCardSnapshot); !got.Resolved || got.Result.Body != "joined" || len(got.Update.Lanes) != 1 {
		t.Fatalf("team lifecycle lost specialized state: %#v", got)
	}
	if c.scrollback.SnapshotAt(1).Payload.Kind() != scrollback.KindNotice {
		t.Fatal("interleaved notice moved")
	}
}

func TestMecatuiTypedScrollbackModel_Scenario2_DelegationPreviewIsolation(t *testing.T) {
	var c conversation
	const canary = "CHILD_PRIVATE_CANARY"
	c.addTool("sub", "Subagent", `{}`)
	applySubagentTo(&c, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "inspect"})
	applySubagentTo(&c, client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "sub", ChildID: "child", InnerKind: "message.delta", Text: canary + "\x1b[31m", ToolCount: 1})
	c.resolveTool("sub", "started", false)
	p := c.scrollback.SnapshotAt(0).Payload.(scrollback.SubagentCardSnapshot)
	if len(p.Update.Trace) != 1 || strings.Contains(p.Update.Trace[0].Text, "\x1b") {
		t.Fatalf("preview was not retained as scrubbed bounded trace: %#v", p.Update.Trace)
	}
	if strings.Contains(p.Result.Body, canary) {
		t.Fatal("raw child content reached parent tool result")
	}
	if c.subagentFleet[0].trace[0].text != p.Update.Trace[0].Text {
		t.Fatal("fleet projection diverged from typed scrubbed preview")
	}
}

func TestMecatuiTypedScrollbackModel_Scenario3_FrameAnchorAndSelectionContinuity(t *testing.T) {
	var c conversation
	c.addTool("sub", "Subagent", `{}`)
	applySubagentTo(&c, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "inspect"})
	c.addNotice("tail")
	r := newCacheRenderer()
	before := r.renderConversationFrame(&c, false)
	anchor, ok := before.observedAnchorForRow(0, towardStart)
	if !ok {
		t.Fatal("missing specialized-card anchor")
	}
	c.resolveTool("sub", "started", false) // non-tail typed update
	r.setWidth(48)
	after := r.renderConversationFrame(&c, true)
	if len(after.lines) != len(after.provenance) {
		t.Fatalf("frame/provenance drift: %d lines, %d rows", len(after.lines), len(after.provenance))
	}
	if _, ok := after.rowForAnchor(anchor); !ok {
		t.Fatalf("specialized-card anchor %+#v was not restored after reflow/result", anchor)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario3_ScrollbackRetentionAndDepthContract(t *testing.T) {
	var c conversation
	for i := 0; i < 64; i++ {
		c.addNotice("settled")
	}
	c.addTool("sub", "Subagent", `{}`)
	applySubagentTo(&c, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "inspect"})
	r := newCacheRenderer()
	r.renderConversationFrame(&c, false)
	if got, want := c.scrollback.Len(), 65; got != want {
		t.Fatalf("logical document retained %d cards, want %d", got, want)
	}
	if got, want := len(r.blocks.rendered), c.scrollback.Len(); got != want {
		t.Fatalf("cache has %d entries for %d cards; migration retained a second transcript", got, want)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario2_DelegationCardsRemainSpecialized(t *testing.T) {
	var c conversation
	c.addTool("sub", "Subagent", `{}`)
	c.addTool("team", "Team", `{}`)
	applySubagentTo(&c, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "inspect"})
	applyTeamTo(&c, client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team", TeamID: "team", Roster: []client.TeamMemberSpec{{Name: "member"}}})
	if _, ok := c.scrollback.SnapshotAt(0).Payload.(scrollback.SubagentCardSnapshot); !ok {
		t.Fatal("Subagent card was not specialized")
	}
	if _, ok := c.scrollback.SnapshotAt(1).Payload.(scrollback.TeamCardSnapshot); !ok {
		t.Fatal("Team card was not specialized")
	}
	if len(c.parallelGroups) != 0 {
		t.Fatal("Parallel projection became scrollback state")
	}
	if len(c.subagentFleet) != 1 || c.subagentFleet[0].childID != "child" {
		t.Fatalf("fleet side projection was not retained: %#v", c.subagentFleet)
	}
}
