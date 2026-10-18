package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalAssistantAcceptanceAtomic(t *testing.T) {
	call := NewToolCall("retained", "Read", []byte(`{"path":"old"}`))
	for name, message := range map[string]Message{
		"empty":     NewAssistantMessage("", []ToolCall{NewToolCall("", "Read", nil)}),
		"duplicate": NewAssistantMessage("", []ToolCall{NewToolCall("new", "Read", nil), NewToolCall("new", "Read", nil)}),
		"collision": NewAssistantMessage("", []ToolCall{call}),
		"role":      NewUserMessage("not assistant"),
		"result":    {Role: RoleAssistant, ToolResult: ptrCanonicalResult(NewToolResult("retained", "wrong role"))},
		"binding": {Role: RoleAssistant, ToolCalls: []ToolCall{NewToolCall("new", "Read", nil)}, Replay: &MessageReplay{
			Bindings: []ToolCallBinding{{CallID: "new", PairingID: "private-token"}, {CallID: "new", PairingID: "private-token"}},
		}},
		"pairing": {Role: RoleAssistant, ToolCalls: []ToolCall{NewToolCall("new", "Read", nil), NewToolCall("other", "Read", nil)}, Replay: &MessageReplay{
			Bindings: []ToolCallBinding{{CallID: "new", PairingID: "private-token"}, {CallID: "other", PairingID: "private-token"}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mustOK(t, s.BeginTurn())
			mustOK(t, s.RecordAssistant(NewAssistantMessage("prior", []ToolCall{call})))
			mustOK(t, s.RecordToolResults([]ToolResult{NewToolResult(call.ID, "done")}))
			history, counters := CloneMessages(s.Conversation.Messages), s.Counters
			err := s.RecordAssistant(message)
			if err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("unsafe or absent rejection: %v", err)
			}
			if !reflect.DeepEqual(history, s.Conversation.Messages) || counters != s.Counters {
				t.Fatal("rejected assistant changed history or counters")
			}
		})
	}
}

func ptrCanonicalResult(result ToolResult) *ToolResult { return &result }

func TestCanonicalResultBatchAtomic(t *testing.T) {
	for name, results := range map[string][]ToolResult{
		"missing ID": {NewToolResult("a", "valid prefix"), NewToolError("", "bad")},
		"orphan":     {NewToolResult("a", "valid prefix"), NewToolError("absent", "bad")},
		"duplicate":  {NewToolError("a", "valid prefix"), NewToolResult("a", "duplicate")},
		"repeated":   {NewToolError("a", "valid prefix"), NewToolResult("done", "repeated")},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mustOK(t, s.BeginTurn())
			mustOK(t, s.RecordAssistant(NewAssistantMessage("", []ToolCall{
				NewToolCall("done", "Read", nil), NewToolCall("a", "Read", nil), NewToolCall("b", "Read", nil),
			})))
			mustOK(t, s.RecordToolResults([]ToolResult{NewToolError("done", "prior failure")}))
			history, counters := CloneMessages(s.Conversation.Messages), s.Counters
			if err := s.RecordToolResults(results); err == nil {
				t.Fatal("invalid batch accepted")
			}
			if !reflect.DeepEqual(history, s.Conversation.Messages) || counters != s.Counters {
				t.Fatal("rejected result batch changed history or counters")
			}
			mustOK(t, s.RecordToolResults([]ToolResult{NewToolResult("a", "ok"), NewToolResult("b", "ok")}))
			mustOK(t, ValidateToolPairing(s.Conversation.Messages))
			if s.Counters.ToolCalls != 3 || s.Counters.ConsecutiveFailures != 0 {
				t.Fatalf("valid finals counted incorrectly: %+v", s.Counters)
			}
		})
	}
}

func TestCanonicalHistoryRejectsRepeatedOccurrences(t *testing.T) {
	call := func(id ToolCallID) Message {
		return NewAssistantMessage("", []ToolCall{NewToolCall(id, "Read", nil)})
	}
	result := func(id ToolCallID) Message { return NewToolMessage(NewToolResult(id, "ok")) }
	for name, history := range map[string][]Message{
		"duplicate open":    {NewAssistantMessage("", []ToolCall{NewToolCall("a", "Read", nil), NewToolCall("a", "Read", nil)}), result("a")},
		"completed reuse":   {call("a"), result("a"), call("a"), result("a")},
		"repeated final":    {call("a"), result("a"), result("a")},
		"empty call":        {call(""), result("")},
		"missing final":     {call("a")},
		"wrong call role":   {{Role: RoleUser, ToolCalls: []ToolCall{NewToolCall("a", "Read", nil)}}, result("a")},
		"wrong result role": {call("a"), {Role: RoleAssistant, ToolResult: ptrCanonicalResult(NewToolResult("a", "bad"))}},
		"unknown role":      {{Role: "unknown"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateToolPairing(history); err == nil {
				t.Fatal("invalid canonical history accepted")
			}
			s := newTestSession(Limits{})
			if err := s.SeedHistory(history); err == nil || s.Conversation.Len() != 0 {
				t.Fatal("invalid seed changed history")
			}
			mustOK(t, s.BeginTurn())
			mustOK(t, s.RecordAssistant(NewAssistantMessage("retained", nil)))
			before, counters := CloneMessages(s.Conversation.Messages), s.Counters
			if err := s.ReplaceHistory(history); err == nil || !reflect.DeepEqual(before, s.Conversation.Messages) || counters != s.Counters {
				t.Fatal("invalid replacement changed history or counters")
			}
		})
	}
}

func TestCanonicalPersistenceAllowsOutstandingCalls(t *testing.T) {
	s := newTestSession(Limits{})
	mustOK(t, s.BeginTurn())
	mustOK(t, s.RecordAssistant(NewAssistantMessage("", []ToolCall{NewToolCall("a", "Read", nil)})))
	mustOK(t, s.ValidateIdentityPersistence())
	if err := ValidateToolPairing(s.Conversation.Messages); err == nil {
		t.Fatal("closed history accepted outstanding call")
	}
	s.Conversation.Messages = append(s.Conversation.Messages, s.Conversation.Messages[0])
	if err := s.ValidateIdentityPersistence(); err == nil {
		t.Fatal("persisted repeated canonical occurrence accepted")
	}
}
