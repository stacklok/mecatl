package session

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

func replayMessage() Message {
	return Message{
		Role: RoleAssistant, Text: "accepted",
		ToolCalls: []ToolCall{{ID: "engine-1", Name: "Read", Args: []byte(`{"x":1}`)}},
		Replay: &MessageReplay{
			Bindings: []ToolCallBinding{{CallID: "engine-1", PairingID: "provider-1", ItemID: "item-1"}},
			Provider: &ProviderReplay{Format: "future/items", Version: 99, Data: []byte{0xff, 0, ' ', '\n'}},
		},
	}
}

func TestLegacyMessageOmitsReplay(t *testing.T) {
	data, err := json.Marshal(NewAssistantMessage("legacy", nil))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["Replay"]; ok {
		t.Fatal("no-carrier message changed legacy JSON shape")
	}
}

func TestNewToolCallIDConcurrent(t *testing.T) {
	const workers, each = 32, 100
	ids := make(chan ToolCallID, workers*each)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range each {
				ids <- NewToolCallID()
			}
		})
	}
	wg.Wait()
	close(ids)
	seen := make(map[ToolCallID]bool)
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("empty or duplicate ID %q", id)
		}
		seen[id] = true
	}
}

func TestRecordAssistantReplayValidationAtomic(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*Message)
		valid bool
	}{
		{"valid", func(*Message) {}, true},
		{"bindings without private payload", func(m *Message) { m.Replay.Provider = nil }, true},
		{"absent legacy carrier", func(m *Message) { m.Replay = nil }, true},
		{"missing bindings", func(m *Message) { m.Replay.Bindings = nil }, false},
		{"empty bindings", func(m *Message) { m.Replay.Bindings = []ToolCallBinding{} }, false},
		{"empty call ID", func(m *Message) { m.ToolCalls[0].ID = ""; m.Replay.Bindings[0].CallID = "" }, false},
		{"empty pairing ID", func(m *Message) { m.Replay.Bindings[0].PairingID = "" }, false},
		{"foreign binding", func(m *Message) { m.Replay.Bindings[0].CallID = "foreign" }, false},
		{"duplicate binding", func(m *Message) { m.Replay.Bindings = append(m.Replay.Bindings, m.Replay.Bindings[0]) }, false},
		{"duplicate call", func(m *Message) { m.ToolCalls = append(m.ToolCalls, m.ToolCalls[0]) }, false},
		{"duplicate provider key", func(m *Message) {
			m.ToolCalls = append(m.ToolCalls, ToolCall{ID: "engine-2"})
			m.Replay.Bindings = append(m.Replay.Bindings, ToolCallBinding{CallID: "engine-2", PairingID: "provider-1"})
		}, false},
		{"wrong role", func(m *Message) { m.Role = RoleUser }, false},
		{"opaque replay without calls", func(m *Message) { m.ToolCalls = nil; m.Replay.Bindings = nil }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mustOK(t, s.BeginTurn())
			mustOK(t, s.RecordAssistant(NewAssistantMessage("prior", nil)))
			before := CloneMessages(s.Conversation.Messages)
			counters := s.Counters
			m := replayMessage()
			tt.edit(&m)
			err := s.RecordAssistant(m)
			if (err == nil) != tt.valid {
				t.Fatalf("RecordAssistant error = %v, valid = %v", err, tt.valid)
			}
			if !tt.valid && (!reflect.DeepEqual(before, s.Conversation.Messages) || counters != s.Counters || s.State != StateRunning) {
				t.Fatal("rejection mutated aggregate")
			}
		})
	}
}

func TestRecordAssistantReplayOwnsBytes(t *testing.T) {
	s := newTestSession(Limits{})
	mustOK(t, s.BeginTurn())
	m := replayMessage()
	want := CloneMessages([]Message{m})
	mustOK(t, s.RecordAssistant(m))
	m.ToolCalls[0].Args[0] = '!'
	m.Replay.Bindings[0].PairingID = "changed"
	m.Replay.Provider.Data[0] = 0
	m.Replay.Provider.Version = 1
	if !reflect.DeepEqual(s.Conversation.Messages, want) {
		t.Fatal("accepted message aliases caller")
	}
}

func TestCloneMessagesNestedOwnership(t *testing.T) {
	m := replayMessage()
	m.Parts = []Content{{Data: []byte{1}, Audience: []string{"user"}}}
	m.ToolResult = &ToolResult{CallID: "engine-1", Parts: []Content{{Data: []byte{2}, Audience: []string{"assistant"}}}}
	original := []Message{m}
	cloned := CloneMessages(original)
	cloned[0].ToolCalls[0].Args[0] = '!'
	cloned[0].Parts[0].Data[0] = 3
	cloned[0].Parts[0].Audience[0] = "changed"
	cloned[0].ToolResult.Content = "changed"
	cloned[0].ToolResult.Parts[0].Data[0] = 4
	cloned[0].ToolResult.Parts[0].Audience[0] = "changed"
	cloned[0].Replay.Bindings[0].ItemID = "changed"
	cloned[0].Replay.Provider.Data[0] = 0
	if !reflect.DeepEqual(original, []Message{replayMessageWithMedia()}) {
		t.Fatal("clone aliases nested message storage")
	}
	if CloneMessages(nil) != nil {
		t.Fatal("nil clone must remain nil")
	}
}

func replayMessageWithMedia() Message {
	m := replayMessage()
	m.Parts = []Content{{Data: []byte{1}, Audience: []string{"user"}}}
	m.ToolResult = &ToolResult{CallID: "engine-1", Parts: []Content{{Data: []byte{2}, Audience: []string{"assistant"}}}}
	return m
}

func TestHistoryMethodsOwnReplay(t *testing.T) {
	for _, name := range []string{"seed", "replace", "boundary"} {
		t.Run(name, func(t *testing.T) {
			s := newTestSession(Limits{})
			messages := []Message{replayMessage(), NewToolMessage(NewToolResult("engine-1", "ok"))}
			want := CloneMessages(messages)
			var err error
			switch name {
			case "seed":
				err = s.SeedHistory(messages)
			case "replace":
				mustOK(t, s.BeginTurn())
				err = s.ReplaceHistory(messages)
			case "boundary":
				err = s.ReplaceHistoryAtBoundary(messages)
			}
			mustOK(t, err)
			messages[0].Replay.Provider.Data[0] = 0
			messages[0].Replay.Bindings[0].PairingID = "changed"
			messages[0].ToolCalls[0].Args[0] = '!'
			messages[1].ToolResult.Content = "changed"
			if !reflect.DeepEqual(s.Conversation.Messages, want) {
				t.Fatal("history aliases caller")
			}
		})
	}
}
