package acp

import (
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestHistoryEventsOrder asserts the PURE synthesizer walks the conversation in
// order and emits message/tool-call/tool-result events such that a tool_call
// always precedes its matching tool_call_update (open-before-update).
func TestHistoryEventsOrder(t *testing.T) {
	c := &session.Conversation{Messages: []session.Message{
		session.NewUserMessage("please read"),
		session.NewAssistantMessage("looking", []session.ToolCall{
			session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"/x"}`)),
		}),
		session.NewToolMessage(session.NewToolResult("c1", "ok")),
		session.NewAssistantMessage("all done", nil),
	}}

	evs := historyEvents(c)
	wantTypes := []session.EventType{
		session.EvMessageDelta,
		session.EvToolCall,
		session.EvToolResult,
		session.EvMessageDelta,
	}
	if len(evs) != len(wantTypes) {
		t.Fatalf("got %d events, want %d: %+v", len(evs), len(wantTypes), evs)
	}
	for i, want := range wantTypes {
		if evs[i].Type != want {
			t.Errorf("event %d type = %q, want %q", i, evs[i].Type, want)
		}
	}

	// The first message delta carries the assistant text.
	if evs[0].Text != "looking" {
		t.Errorf("first message delta text = %q, want %q", evs[0].Text, "looking")
	}
	// The tool call carries id/name.
	if evs[1].ToolCall == nil || evs[1].ToolCall.ID != "c1" || evs[1].ToolCall.Name != "Read" {
		t.Errorf("tool call = %+v", evs[1].ToolCall)
	}
	// The tool result is paired by call id.
	if evs[2].ToolResult == nil || evs[2].ToolResult.CallID != "c1" {
		t.Errorf("tool result = %+v", evs[2].ToolResult)
	}

	callIdx, resultIdx := indexOfToolCall(evs, "c1"), indexOfToolResult(evs, "c1")
	if callIdx < 0 || resultIdx < 0 {
		t.Fatalf("missing tool call (%d) or result (%d) for c1", callIdx, resultIdx)
	}
	if callIdx >= resultIdx {
		t.Errorf("tool_call index %d must precede tool_call_update index %d (open-before-update)", callIdx, resultIdx)
	}
}

// TestHistoryEventsSystemAndUserDropped asserts system and user messages produce
// no events (no system surface; the editor renders user turns locally).
func TestHistoryEventsSystemAndUserDropped(t *testing.T) {
	c := &session.Conversation{Messages: []session.Message{
		session.NewSystemMessage("you are a helpful agent"),
		session.NewUserMessage("hello"),
	}}
	if evs := historyEvents(c); len(evs) != 0 {
		t.Fatalf("got %d events, want 0 (system+user dropped): %+v", len(evs), evs)
	}
}

// TestHistoryEventsReasoningNotReplayed asserts an assistant message that carries
// only opaque replay (no text, no tool calls) yields no events
// — the encrypted blob must never be surfaced as a thought chunk.
func TestHistoryEventsReasoningNotReplayed(t *testing.T) {
	c := &session.Conversation{Messages: []session.Message{
		{Role: session.RoleAssistant, Replay: &session.MessageReplay{Provider: &session.ProviderReplay{Format: "private", Version: 1, Data: []byte("OPAQUE-ENCRYPTED-REASONING-BLOB")}}},
	}}
	if evs := historyEvents(c); len(evs) != 0 {
		t.Fatalf("got %d events, want 0 (reasoning blob not replayed): %+v", len(evs), evs)
	}
}

// TestHistoryEventsErrorResultPreserved asserts a failed tool result keeps its
// IsError flag on the synthesized EvToolResult, so projectUpdate yields a failed
// tool_call_update.
func TestHistoryEventsErrorResultPreserved(t *testing.T) {
	c := &session.Conversation{Messages: []session.Message{
		session.NewAssistantMessage("", []session.ToolCall{
			session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"/x"}`)),
		}),
		session.NewToolMessage(session.NewToolError("c1", "boom")),
	}}
	evs := historyEvents(c)
	resultIdx := indexOfToolResult(evs, "c1")
	if resultIdx < 0 {
		t.Fatalf("missing tool result for c1 in %+v", evs)
	}
	if !evs[resultIdx].ToolResult.IsError {
		t.Errorf("tool result IsError = false, want true (error preserved)")
	}
}

// TestHistoryEventsMultiCallOrdering asserts the stronger guarantee for a single
// assistant message issuing several tool calls: every tool_call (all opened from
// the one assistant message) precedes every tool_call_update (from the subsequent
// tool-role messages), so no card is ever updated before it is opened.
func TestHistoryEventsMultiCallOrdering(t *testing.T) {
	c := &session.Conversation{Messages: []session.Message{
		session.NewAssistantMessage("reading two files", []session.ToolCall{
			session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"/a"}`)),
			session.NewToolCall("c2", "Read", json.RawMessage(`{"path":"/b"}`)),
		}),
		session.NewToolMessage(session.NewToolResult("c1", "ok a")),
		session.NewToolMessage(session.NewToolResult("c2", "ok b")),
	}}

	evs := historyEvents(c)
	for _, id := range []session.ToolCallID{"c1", "c2"} {
		callIdx, resultIdx := indexOfToolCall(evs, id), indexOfToolResult(evs, id)
		if callIdx < 0 || resultIdx < 0 {
			t.Fatalf("missing tool call (%d) or result (%d) for %s", callIdx, resultIdx, id)
		}
		if callIdx >= resultIdx {
			t.Errorf("%s: tool_call index %d must precede tool_call_update index %d", id, callIdx, resultIdx)
		}
	}
	// Both opens must precede both updates (the strictly stronger cross-call guarantee).
	lastCall := max(indexOfToolCall(evs, "c1"), indexOfToolCall(evs, "c2"))
	firstResult := min(indexOfToolResult(evs, "c1"), indexOfToolResult(evs, "c2"))
	if lastCall >= firstResult {
		t.Errorf("last tool_call index %d must precede first tool_call_update index %d", lastCall, firstResult)
	}
}

func indexOfToolCall(evs []session.Event, id session.ToolCallID) int {
	for i, ev := range evs {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.ID == id {
			return i
		}
	}
	return -1
}

func indexOfToolResult(evs []session.Event, id session.ToolCallID) int {
	for i, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == id {
			return i
		}
	}
	return -1
}
