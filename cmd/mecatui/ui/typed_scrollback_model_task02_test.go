package ui

import (
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestMecatuiTypedScrollbackModel_Scenario2_OrdinaryProjectionAndLateResolution(t *testing.T) {
	var c conversation
	c.addUser("ship it")
	c.startAssistant()
	c.appendReasoning("check the contract")
	c.appendAssistant("done")
	c.addTool("first", "Read", `{"path":"first.go"}`)
	first := c.scrollback.SnapshotAt(c.scrollback.Len() - 1)
	c.addNotice("interleaved notice")
	c.addTool("second", "Grep", `{"pattern":"TODO"}`)
	c.resolveTool("first", "contents", false)

	resolved := c.scrollback.SnapshotAt(2)
	if resolved.ID != first.ID || resolved.Revision != first.Revision+1 {
		t.Fatalf("late result changed identity/revision = %#v, want ID %d revision %d", resolved, first.ID, first.Revision+1)
	}
	tool, ok := resolved.Payload.(scrollback.ToolCardSnapshot)
	if !ok || !tool.Resolved || tool.Result.Body != "contents" {
		t.Fatalf("late result snapshot = %#v, want resolved ordinary tool", resolved.Payload)
	}
	if got := c.scrollback.SnapshotAt(3).Payload.Kind(); got != scrollback.KindNotice {
		t.Fatalf("interleaved card kind = %v, want notice", got)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario3_RenderCacheUsesTypedRevision(t *testing.T) {
	var c conversation
	c.addTool("call", "Read", `{"path":"go.mod"}`)
	r := newCacheRenderer()
	r.renderConversation(&c, false)
	before := r.blockRenders
	c.resolveTool("call", "module", false)
	r.renderConversation(&c, false)
	if got := r.blockRenders - before; got != 1 {
		t.Fatalf("late typed result rendered %d blocks, want 1", got)
	}
}
