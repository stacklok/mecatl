package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

type replayMutatingCompactor struct{ err error }

func (c replayMutatingCompactor) Compact(_ context.Context, conv *session.Conversation) ([]session.Message, string, session.AuxiliaryUsage, error) {
	conv.Messages[0].Replay.Bindings[0].PairingID = "changed"
	conv.Messages[0].Replay.Provider.Data[0] = 0
	conv.Messages[0].ToolCalls[0].Args[0] = '!'
	conv.Messages[1].ToolResult.Parts[0].Data[0] = 0
	return nil, "", session.AuxiliaryUsage{}, c.err
}

func TestManualCompactionReplayOwnership(t *testing.T) {
	sess := manualSession(t)
	messages := []session.Message{
		{
			Role:      session.RoleAssistant,
			ToolCalls: []session.ToolCall{{ID: "engine-1", Name: "Read", Args: []byte(`{"x":1}`)}},
			Replay: &session.MessageReplay{
				Bindings: []session.ToolCallBinding{{CallID: "engine-1", PairingID: "provider-1"}},
				Provider: &session.ProviderReplay{Format: "future/items", Version: 99, Data: []byte{0xff}},
			},
		},
		session.NewToolMessage(session.ToolResult{CallID: "engine-1", Parts: []session.Content{{Data: []byte{1}}}}),
	}
	if err := sess.ReplaceHistoryAtBoundary(messages); err != nil {
		t.Fatal(err)
	}
	want := session.CloneMessages(sess.Conversation.Messages)
	boom := errors.New("compactor failed")
	eng := agent.NewEngine(agent.Deps{Compactor: replayMutatingCompactor{err: boom}, TokenCounter: textLengthCounter{}})
	if _, err := eng.CompactSession(context.Background(), sess); !errors.Is(err, boom) {
		t.Fatalf("CompactSession error = %v", err)
	}
	if !reflect.DeepEqual(sess.Conversation.Messages, want) {
		t.Fatal("compactor mutation escaped into aggregate replay storage")
	}
}

func TestCompactionRetainsAndPrunesReplayAtomically(t *testing.T) {
	sess := manualSession(t)
	var history []session.Message
	for i, id := range []session.ToolCallID{"removed", "retained"} {
		message := session.NewAssistantMessage("original long assistant text", []session.ToolCall{session.NewToolCall(id, "Read", []byte(`{}`))})
		message.Replay = &session.MessageReplay{
			Bindings: []session.ToolCallBinding{{CallID: id, PairingID: session.ProviderCallID("provider_" + string(id)), ItemID: "item"}},
			Provider: &session.ProviderReplay{Format: "opaque", Version: 3, Data: []byte("private " + string(id))},
		}
		message.Legacy = &session.LegacyMessageIdentity{Namespace: "source", Ordinal: i + 8}
		history = append(history, message, session.NewToolMessage(session.NewToolResult(id, "read")))
	}
	if err := sess.ReplaceHistoryAtBoundary(history); err != nil {
		t.Fatal(err)
	}
	want := session.CloneMessages(sess.Conversation.Messages)
	compactor := &manualCompactor{out: session.CloneMessages(want[2:]), summary: "reduced"}
	eng := agent.NewEngine(agent.Deps{Compactor: compactor, TokenCounter: textLengthCounter{}})
	result, err := eng.CompactSession(t.Context(), sess)
	if err != nil || !result.Changed {
		t.Fatalf("compaction failed: %v", err)
	}
	if !reflect.DeepEqual(sess.Conversation.Messages, want[2:]) || !reflect.DeepEqual(result.Archive, want) {
		t.Fatal("retained active replay or existing archive retention changed")
	}
	compactor.out[0].Replay.Provider.Data[0] = '!'
	result.Archive[2].Replay.Bindings[0].PairingID = "changed"
	result.Archive[2].Legacy.Ordinal = 0
	if !reflect.DeepEqual(sess.Conversation.Messages, want[2:]) {
		t.Fatal("candidate/archive aliases active replay or legacy coordinates")
	}
}
