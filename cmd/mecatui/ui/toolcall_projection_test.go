package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestToolcallProjectionStates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*scrollback.Conversation)
		state toolcallProjectionState
	}{
		{"pending", func(c *scrollback.Conversation) { c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Read"}) }, toolcallPending},
		{"finished success without result", func(c *scrollback.Conversation) {
			c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Read"})
			c.Tools().Finish("call", false)
		}, toolcallAwaitingResult},
		{"finished failure without result", func(c *scrollback.Conversation) {
			c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Read"})
			c.Tools().Finish("call", true)
		}, toolcallFailed},
		{"provisional", func(c *scrollback.Conversation) {
			c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Read"})
			c.Tools().ResolveAvailable("call", scrollback.ToolResult{})
		}, toolcallProvisional},
		{"canonical ok", func(c *scrollback.Conversation) {
			c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Read"})
			c.Tools().Resolve("call", scrollback.ToolResult{})
		}, toolcallDone},
		{"canonical error", func(c *scrollback.Conversation) {
			c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Read"})
			c.Tools().Resolve("call", scrollback.ToolResult{IsError: true})
		}, toolcallFailed},
		{"subagent terminal success without result", func(c *scrollback.Conversation) {
			c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Subagent"})
			c.Subagents().Start("call", scrollback.SubagentStart{})
			c.Subagents().Update("call", scrollback.SubagentUpdate{Done: true})
		}, toolcallAwaitingResult},
		{"team terminal failure without result", func(c *scrollback.Conversation) {
			c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Team"})
			c.Teams().Start("call", scrollback.TeamStart{})
			c.Teams().Update("call", scrollback.TeamUpdate{Done: true, Stop: "error"})
		}, toolcallFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var c scrollback.Conversation
			test.setup(&c)
			metadata, ok := c.ToolCallMetadataAt(0)
			if !ok {
				t.Fatal("metadata missing")
			}
			got := projectToolCall(metadata)
			if got.state != test.state {
				t.Fatalf("state = %v, want %v", got.state, test.state)
			}
			if got.settled() != (test.state == toolcallDone || test.state == toolcallFailed) {
				t.Fatalf("settled = %v for state %v", got.settled(), test.state)
			}
		})
	}
}

func TestToolcallProjectionProvisionalConfirmationRetainsBlock(t *testing.T) {
	var c scrollback.Conversation
	c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Read"})
	if !c.Tools().ResolveAvailable("call", scrollback.ToolResult{}) {
		t.Fatal("provisional result rejected")
	}
	before, _ := c.ToolCallMetadataAt(0)
	if !c.Tools().Resolve("call", scrollback.ToolResult{}) {
		t.Fatal("canonical result rejected")
	}
	after, _ := c.ToolCallMetadataAt(0)
	if got := projectToolCall(before); got.state != toolcallProvisional {
		t.Fatalf("provisional state = %v", got.state)
	}
	if got := projectToolCall(after); got.state != toolcallDone || got.blockID != before.ID {
		t.Fatalf("confirmed projection = %#v", got)
	}
	if c.Len() != 1 {
		t.Fatalf("cards = %d, want 1", c.Len())
	}
}

func TestToolcallProjectionIdentityAndIntentAreSanitized(t *testing.T) {
	var c scrollback.Conversation
	c.Tools().Add(scrollback.ToolCall{ID: "call", Name: "mcp__github__issue_write", Arguments: `{"path":"\u001b[2Jtarget"}`})
	metadata, _ := c.ToolCallMetadataAt(0)
	got := projectToolCall(metadata)
	if got.displayName != "GitHub · Issue write" || got.fullName != "mcp__github__issue_write" {
		t.Fatalf("identity = %#v", got)
	}
	if strings.ContainsAny(got.displayName+got.fullName+got.intent, "\x1b\n\r") {
		t.Fatalf("projection was not single-line sanitized: %#v", got)
	}

	c.Tools().Add(scrollback.ToolCall{ID: "unknown", Name: "unknown\x1btool", Arguments: `not json`})
	metadata, _ = c.ToolCallMetadataAt(1)
	got = projectToolCall(metadata)
	if got.displayName != "unknowntool" || got.intent != "unknowntool" {
		t.Fatalf("unknown projection = %#v", got)
	}
}

func TestToolcallProjectionInspectorListRow(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("mcp", "mcp__github__issue_write", `{"path":"issue-42"}`)
	model, _ := m.runToolcalls()
	m = model.(Model)
	s := toolcallsForTest(t, m)
	body, _ := s.Render(240, 12)
	entry := s.entries[0].toolcallProjection
	_, status, _ := entry.state.status()
	want := status + " · " + entry.displayName + " · " + entry.intent
	if !strings.Contains(stripANSIstr(body), want) {
		t.Fatalf("list row does not use projection:\nwant %q\ngot %q", want, stripANSIstr(body))
	}
}
