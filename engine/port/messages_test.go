package port_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func canonicalHistory() []session.Message {
	var messages []session.Message
	for _, id := range []session.ToolCallID{"engine-1", "engine-2"} {
		messages = append(messages, session.Message{
			Role: session.RoleAssistant, Text: string(id), ProviderPhase: "commentary",
			ToolCalls: []session.ToolCall{{ID: id, Name: "Read", Args: []byte(`{"x":1}`)}},
			Replay: &session.MessageReplay{
				Bindings: []session.ToolCallBinding{{CallID: id, PairingID: "same-provider-key", ItemID: session.ProviderItemID("item-" + string(id))}},
				Provider: &session.ProviderReplay{Format: "future/items", Version: 99, Data: []byte{0xff, 0, '\n'}},
			},
		}, session.NewToolMessage(session.ToolResult{
			CallID: id, Content: string(id), IsError: true,
			Parts: []session.Content{{Data: []byte{1}, Audience: []string{"assistant"}}},
		}))
	}
	return append(messages, session.Message{
		Role: session.RoleUser, Text: "principal prompt", UserPromptProvenance: session.UserPromptProvenancePrincipal,
		Parts: []session.Content{{Data: []byte{2}, Audience: []string{"user"}}},
	})
}

func TestProjectMessagesOccurrenceBindingsAndOwnership(t *testing.T) {
	history := canonicalHistory()
	before := session.CloneMessages(history)
	projected, err := port.ProjectMessages(history)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 2} {
		call := projected[i].ToolCalls[0]
		result := projected[i+1].ToolResult
		if call.ID != "same-provider-key" || result.CallID != call.ID || call.ItemID != session.ProviderItemID("item-"+history[i].Text) {
			t.Fatalf("incorrect occurrence pairing at %d: %+v / %+v", i, call, result)
		}
		if result.Content != history[i+1].ToolResult.Content || !result.IsError || projected[i].ProviderPhase != "commentary" {
			t.Fatal("projection lost display/result fields")
		}
		if !reflect.DeepEqual(projected[i].Replay, history[i].Replay.Provider) {
			t.Fatal("opaque replay bytes changed")
		}
	}
	if projected[4].UserPromptProvenance != session.UserPromptProvenancePrincipal {
		t.Fatal("lost prompt provenance")
	}
	projected[0].ToolCalls[0].Args[0] = '!'
	projected[0].Replay.Data[0] = 0
	projected[0].Replay.Version = 1
	projected[1].ToolResult.Content = "changed"
	projected[1].ToolResult.Parts[0].Data[0] = 3
	projected[1].ToolResult.Parts[0].Audience[0] = "changed"
	projected[4].Parts[0].Data[0] = 4
	projected[4].Parts[0].Audience[0] = "changed"
	if !reflect.DeepEqual(history, before) {
		t.Fatal("projection aliases or mutated domain history")
	}
}

func TestProjectMessagesBindingOrder(t *testing.T) {
	history := canonicalHistory()
	second := history[2].Replay.Bindings[0]
	second.PairingID = "Provider_Key-2"
	history[0].ToolCalls = append(history[0].ToolCalls, history[2].ToolCalls[0])
	history[0].Replay.Bindings = append([]session.ToolCallBinding{second}, history[0].Replay.Bindings...)
	projected, err := port.ProjectMessages([]session.Message{history[0], history[3], history[1]})
	if err != nil {
		t.Fatal(err)
	}
	if projected[0].ToolCalls[0].ID != "same-provider-key" || projected[0].ToolCalls[1].ID != second.PairingID || projected[0].ToolCalls[1].ItemID != second.ItemID || projected[1].ToolResult.CallID != second.PairingID || projected[2].ToolResult.CallID != "same-provider-key" {
		t.Fatal("projection paired by slice order rather than canonical occurrence")
	}
}

func TestProjectMessagesReplayFreeCanonical(t *testing.T) {
	history := canonicalHistory()
	history[0].Replay = nil
	history[2].Replay = nil
	first, err := port.ProjectMessages(history)
	if err != nil {
		t.Fatal(err)
	}
	second, err := port.ProjectMessages(session.CloneMessages(history))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("synthesized tokens must be stable")
	}
	if first[0].ToolCalls[0].ID == "" || first[0].ToolCalls[0].ID == first[2].ToolCalls[0].ID || first[1].ToolResult.CallID != first[0].ToolCalls[0].ID || first[3].ToolResult.CallID != first[2].ToolCalls[0].ID {
		t.Fatal("incorrect synthesized pairing")
	}
	if first[0].ToolCalls[0].ItemID != "" || first[0].Replay != nil {
		t.Fatal("replay-free projection invented private item state")
	}
}

// This guards prompt-cache reuse across session reloads with common providers.
// Live probes through our proxy to Anthropic and OpenAI showed that argument
// whitespace/escaping changes retained cache hits, but JSON key reordering did not.
// Do not weaken this to semantic JSON equality (e.g. decoded-map comparison):
// it would hide order changes that can invalidate the cached prefix after reload.
func TestProjectMessagesSnapshotRoundTripPreservesPairingAndArgumentOrder(t *testing.T) {
	for _, replayFree := range []bool{false, true} {
		for _, tc := range []struct{ name, args string }{
			{"compact", `{"z":"first","a":"last"}`},
			{"whitespace", `{ "z": 1, "a": { "y": 2, "b": 3 } }`},
			{"escaping", `{"z":"<tag>&","a":"line\n雪"}`},
			{"string whitespace", `{"z":"keep  spaces","a":"{}"}`},
		} {
			name := "stored/" + tc.name
			if replayFree {
				name = "replay-free/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				history := append([]session.Message{session.NewUserMessage("go")}, canonicalHistory()[:4]...)
				history = append(history, session.Message{Role: session.RoleUser, Text: "resume", UserPromptProvenance: session.UserPromptProvenancePrincipal})
				for i := range history {
					for j := range history[i].ToolCalls {
						history[i].ToolCalls[j].Args = json.RawMessage(tc.args)
					}
					if replayFree {
						history[i].Replay = nil
					}
				}
				s := session.New("projection", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
				if err := s.SeedHistory(history); err != nil {
					t.Fatal(err)
				}
				want, err := port.ProjectMessages(s.Conversation.Messages)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := sessnap.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				restored, err := sessnap.Unmarshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				got, err := port.ProjectMessages(restored.Conversation.Messages)
				if err != nil {
					t.Fatal(err)
				}
				// Snapshot JSON can compact and escape raw arguments. Keep key order
				// observable: decoding into maps would hide cache-relevant changes.
				for i := range want {
					for j := range want[i].ToolCalls {
						args, err := json.Marshal(want[i].ToolCalls[j].Args)
						if err != nil {
							t.Fatal(err)
						}
						want[i].ToolCalls[j].Args = args
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("snapshot changed projection beyond argument JSON encoding:\n got: %#v\nwant: %#v", got, want)
				}
			})
		}
	}
}

func TestProjectMessagesRejectsMalformedHistory(t *testing.T) {
	tests := []struct {
		name string
		edit func([]session.Message) []session.Message
	}{
		{"invalid role", func(m []session.Message) []session.Message { return append(m, session.Message{Role: "invalid"}) }},
		{"missing binding", func(m []session.Message) []session.Message { m[0].Replay.Bindings = nil; return m }},
		{"empty provider key", func(m []session.Message) []session.Message { m[0].Replay.Bindings[0].PairingID = ""; return m }},
		{"foreign binding", func(m []session.Message) []session.Message { m[0].Replay.Bindings[0].CallID = "foreign"; return m }},
		{"duplicate binding", func(m []session.Message) []session.Message {
			m[0].Replay.Bindings = append(m[0].Replay.Bindings, m[0].Replay.Bindings[0])
			return m
		}},
		{"orphan result", func(m []session.Message) []session.Message { m[1].ToolResult.CallID = "orphan"; return m }},
		{"result before call", func(m []session.Message) []session.Message { m[0], m[1] = m[1], m[0]; return m }},
		{"duplicate result", func(m []session.Message) []session.Message {
			return append(m[:2:2], append([]session.Message{m[1]}, m[2:]...)...)
		}},
		{"missing result", func(m []session.Message) []session.Message { return m[:1] }},
		{"duplicate canonical occurrence", func(m []session.Message) []session.Message {
			m[2].ToolCalls[0].ID = "engine-1"
			m[2].Replay.Bindings[0].CallID = "engine-1"
			m[3].ToolResult.CallID = "engine-1"
			return m
		}},
		{"simultaneous provider reuse", func(m []session.Message) []session.Message { m[1], m[2] = m[2], m[1]; return m }},
		{"empty replay-free ID", func(m []session.Message) []session.Message { m[0].Replay = nil; m[0].ToolCalls[0].ID = ""; return m }},
		{"tool role without result", func(m []session.Message) []session.Message { m[1].ToolResult = nil; return m }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			history := tt.edit(canonicalHistory())
			before := session.CloneMessages(history)
			got, err := port.ProjectMessages(history)
			if err == nil || got != nil {
				t.Fatalf("malformed history returned %v, %v", got, err)
			}
			if !reflect.DeepEqual(history, before) {
				t.Fatal("rejected projection mutated domain history")
			}
		})
	}
}
