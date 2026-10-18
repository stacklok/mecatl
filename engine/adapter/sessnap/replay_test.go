package sessnap_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestReplaySnapshotOwnership(t *testing.T) {
	for _, wire := range []bool{false, true} {
		name := "direct"
		if wire {
			name = "JSON"
		}
		t.Run(name, func(t *testing.T) {
			s := session.New("replay", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
			if err := s.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordAssistant(session.Message{
				Role:      session.RoleAssistant,
				ToolCalls: []session.ToolCall{{ID: "engine-1", Name: "Read", Args: []byte(`{"x":1}`)}},
				Replay: &session.MessageReplay{
					Bindings: []session.ToolCallBinding{{CallID: "engine-1", PairingID: "provider-1", ItemID: "item-1"}},
					Provider: &session.ProviderReplay{Format: "future/ordered-items", Version: 99, Data: []byte{0xff, 0, ' ', '\n'}},
				},
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordToolResults([]session.ToolResult{{CallID: "engine-1", Content: "result", Parts: []session.Content{{Data: []byte{1}, Audience: []string{"assistant"}}}}}); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordPrincipalPromptWithParts("prompt", []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{2}}}, nil); err != nil {
				t.Fatal(err)
			}
			want := session.CloneMessages(s.Conversation.Messages)
			snap, err := sessnap.Of(s)
			if err != nil {
				t.Fatal(err)
			}
			// Of must isolate the snapshot even without a serialization boundary.
			s.Conversation.Messages[0].Replay.Provider.Data[0] = 0
			s.Conversation.Messages[0].Replay.Bindings[0].PairingID = "changed"
			s.Conversation.Messages[0].ToolCalls[0].Args[0] = '!'
			s.Conversation.Messages[1].ToolResult.Parts[0].Data[0] = 3
			s.Conversation.Messages[1].ToolResult.Parts[0].Audience[0] = "changed"
			s.Conversation.Messages[2].Parts[0].Data[0] = 4
			if wire {
				data, err := json.Marshal(snap)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(data, []byte(`"Data":"/wAgCg=="`)) {
					t.Fatalf("opaque bytes are not JSON base64: %s", data)
				}
				var decoded sessnap.Snapshot
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatal(err)
				}
				snap = decoded
			}
			restored, err := snap.Restore()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored.Conversation.Messages, want) {
				t.Fatal("snapshot lost opaque carrier or message payload")
			}
			// Two direct restores must also be independent of each other and snap.
			restored.Conversation.Messages[0].Replay.Provider.Data[0] = 0
			restored.Conversation.Messages[0].Replay.Provider.Format = "changed"
			restored.Conversation.Messages[0].Replay.Bindings[0].ItemID = "changed"
			restored.Conversation.Messages[0].ToolCalls[0].Args[0] = '!'
			restored.Conversation.Messages[1].ToolResult.Content = "changed"
			restored.Conversation.Messages[1].ToolResult.Parts[0].Data[0] = 5
			restored.Conversation.Messages[1].ToolResult.Parts[0].Audience[0] = "changed"
			restored.Conversation.Messages[2].Parts[0].Data[0] = 6
			again, err := snap.Restore()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again.Conversation.Messages, want) {
				t.Fatal("Restore aliases snapshot storage")
			}
			snap.Messages[0].Replay.Provider.Data[0] = 0
			snap.Messages[0].Replay.Bindings[0].CallID = "changed"
			snap.Messages[0].ToolCalls[0].Args[0] = '!'
			snap.Messages[1].ToolResult.Parts[0].Data[0] = 7
			snap.Messages[2].Parts[0].Data[0] = 8
			if !reflect.DeepEqual(again.Conversation.Messages, want) {
				t.Fatal("restored aggregate aliases snapshot")
			}
		})
	}
}
