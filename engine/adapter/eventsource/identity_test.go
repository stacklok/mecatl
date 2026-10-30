package eventsource_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func fold(es []session.Event) (*session.Session, error) {
	return eventsource.Fold(meta(), seq(es))
}

func TestCurrentArchiveRetainsReplayAcrossCodecFoldAndSnapshot(t *testing.T) {
	call := session.NewToolCall("call-archive", "Read", json.RawMessage(`{"path":"x"}`))
	assistant := session.NewAssistantMessage("archived", []session.ToolCall{call})
	assistant.ProviderPhase = "commentary"
	assistant.Replay = &session.MessageReplay{
		Bindings: []session.ToolCallBinding{{CallID: call.ID, PairingID: "provider-token", ItemID: "provider-item"}},
		Provider: &session.ProviderReplay{Format: "test/1", Version: 1, Data: []byte("opaque-private")},
	}
	messages := []session.Message{assistant, session.NewToolMessage(session.NewToolResult(call.ID, "result"))}
	e := session.Event{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: messages}}
	raw, err := eventcodec.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := eventcodec.UnmarshalFormat(raw, eventcodec.Format)
	if err != nil || !reflect.DeepEqual(e, reloaded) {
		t.Fatal("current archive codec changed replay or bindings", err)
	}
	folded, err := fold([]session.Event{reloaded})
	if err != nil || !reflect.DeepEqual(messages, folded.Conversation.Messages) {
		t.Fatal("current archive reconstruction changed replay or bindings", err)
	}
	raw, err = sessnap.Marshal(folded)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sessnap.Unmarshal(raw)
	if err != nil || !reflect.DeepEqual(messages, restored.Conversation.Messages) {
		t.Fatal("snapshot restart changed archive replay or bindings", err)
	}
}

func TestCanonicalDuplicateAndOrphanReferencesRejected(t *testing.T) {
	c := session.NewToolCall("call-current", "Read", nil)
	call := session.Event{Type: session.EvToolCall, ToolCall: &c}
	result := session.Event{Type: session.EvToolResult, ToolResult: ptr(session.NewToolResult(c.ID, "done"))}
	for name, events := range map[string][]session.Event{
		"duplicate outstanding": {call, call, result},
		"completed reuse":       {call, result, call, result},
		"orphan":                {result},
		"duplicate result":      {call, result, result},
		"unanswered":            {call},
	} {
		t.Run(name, func(t *testing.T) { requireFoldError(t, events, eventsource.ErrReconstruct) })
	}
}

func TestCanonicalAwaitingRejectsUnprovenReference(t *testing.T) {
	c := session.NewToolCall(session.NewToolCallID(), "Read", nil)
	es := []session.Event{{Type: session.EvToolCall, ToolCall: &c}, {Type: session.EvPermissionAsk, Ask: &session.PendingAsk{AskID: "ask", Tool: "Read", Call: "wrong"}}}
	if _, err := fold(es); !errors.Is(err, eventsource.ErrReconstruct) {
		t.Fatal("unproven canonical pending reference accepted", err)
	}
}

func TestFoldLearningEligibilityRequiresExplicitMetadata(t *testing.T) {
	for _, eligible := range []bool{false, true} {
		m := meta()
		m.LearningEligible = eligible
		for _, events := range [][]session.Event{
			nil,
			{{Type: session.EvToolCall, ToolCall: ptr(session.NewToolCall("call-pending", "Read", nil))}, {Type: session.EvPermissionAsk, Ask: &session.PendingAsk{AskID: "ask", Tool: "Read", Call: "call-pending"}}},
		} {
			s, err := eventsource.Fold(m, seq(events))
			if err != nil {
				t.Fatal(err)
			}
			if s.LearningEligible() != eligible {
				t.Fatal("fold inferred eligibility instead of restoring metadata")
			}
			raw, err := sessnap.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := sessnap.Unmarshal(raw)
			if err != nil || restored.LearningEligible() != eligible {
				t.Fatal("restart changed eligibility", err)
			}
		}
	}
}
