package session_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestCopiedHistoryRetainsCanonicalIdentityAndOwnsReplay(t *testing.T) {
	t.Parallel()
	callID, delegationID := session.NewToolCallID(), session.NewToolCallID()
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}
	parent := session.New("parent", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	assistant := session.NewAssistantMessage("history", []session.ToolCall{session.NewToolCall(callID, "Read", []byte(`{"path":"a"}`))})
	assistant.Replay = &session.MessageReplay{
		Bindings: []session.ToolCallBinding{{CallID: callID, PairingID: "reused", ItemID: "item"}},
		Provider: &session.ProviderReplay{Format: "test", Version: 1, Data: []byte("opaque")},
	}
	history := []session.Message{assistant, session.NewToolMessage(session.NewToolResult(callID, "read"))}
	if err := parent.SeedHistory(history); err != nil {
		t.Fatal(err)
	}
	want := session.CloneMessages(parent.Conversation.Messages)
	for _, path := range []string{"fork", "seed"} {
		t.Run(path, func(t *testing.T) {
			var historyCopy []session.Message
			if path == "fork" {
				historyCopy = session.ForkSnapshot(parent.Conversation)
			} else {
				historyCopy = session.CloneMessages(parent.Conversation.Messages)
			}
			child, err := session.NewSubagent(session.SessionID(path), session.ModeDefault, ref, session.Limits{}, time.Unix(2, 0), parent.ID, parent.Incarnation(), delegationID)
			if err != nil {
				t.Fatal(err)
			}
			if err := child.SeedHistory(historyCopy); err != nil {
				t.Fatal(err)
			}
			historyCopy[0].ToolCalls[0].Args[0] = '!'
			historyCopy[0].Replay.Bindings[0].PairingID = "changed"
			historyCopy[0].Replay.Provider.Data[0] = '!'
			historyCopy[1].ToolResult.CallID = session.NewToolCallID()
			if !reflect.DeepEqual(child.Conversation.Messages, want) || !reflect.DeepEqual(parent.Conversation.Messages, want) {
				t.Fatal("SeedHistory aliases its input or copied history changed invocation/provenance")
			}
			snap, err := sessnap.Of(child)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := snap.Restore()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored.Conversation.Messages, want) || restored.Relationship.CallID != delegationID {
				t.Fatal("snapshot reminted copied identity or changed provenance/parent relationship")
			}
			child.Conversation.Messages[0].Replay.Provider.Data[0] = '!'
			if !reflect.DeepEqual(parent.Conversation.Messages, want) {
				t.Fatal("copied child history aliases its source")
			}
		})
	}
}
