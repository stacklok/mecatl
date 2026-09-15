package server

import (
	"encoding/json"
	"iter"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSyntheticUserPromptReplay_Scenario1_PersistenceProtoAndFoldRoundTrip(t *testing.T) {
	part := session.Content{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte("pixels")}
	original := session.Event{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{
		Text: "continue", Parts: []session.Content{part}, Synthetic: true,
	}}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var persisted session.Event
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.UserPrompt == nil || !persisted.UserPrompt.Synthetic {
		t.Fatalf("event-log JSON lost synthetic origin: %s", encoded)
	}
	protoPrompt := toProto(persisted).GetUserPrompt()
	if protoPrompt == nil || !protoPrompt.GetSynthetic() {
		t.Fatalf("protobuf lost synthetic origin: %+v", protoPrompt)
	}

	var legacy session.Event
	if err := json.Unmarshal([]byte(`{"Type":"user_prompt","UserPrompt":{"Text":"legacy"}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.UserPrompt == nil || legacy.UserPrompt.Synthetic || toProto(legacy).GetUserPrompt().GetSynthetic() {
		t.Fatalf("absent legacy origin must remain false: %+v", legacy.UserPrompt)
	}

	fold := func(synthetic bool) []session.Message {
		t.Helper()
		events := []session.Event{
			{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "first", Parts: []session.Content{part}}},
			{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "second", Synthetic: synthetic}},
		}
		seq := iter.Seq2[session.Event, error](func(yield func(session.Event, error) bool) {
			for _, ev := range events {
				if !yield(ev, nil) {
					return
				}
			}
		})
		folded, foldErr := eventsource.Fold(eventsource.SessionMeta{
			ID: "s1", Mode: session.ModeDefault,
			EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"},
			CreatedAt:      time.Unix(0, 0),
		}, seq)
		if foldErr != nil {
			t.Fatalf("Fold: %v", foldErr)
		}
		return folded.Conversation.Messages
	}
	withoutFlag, withFlag := fold(false), fold(true)
	if !reflect.DeepEqual(withoutFlag, withFlag) {
		t.Fatalf("folded conversation changed with origin flag:\nfalse=%+v\ntrue=%+v", withoutFlag, withFlag)
	}
	if len(withFlag) != 2 || withFlag[0].Text != "first" || len(withFlag[0].Parts) != 1 || withFlag[1].Text != "second" {
		t.Fatalf("folded ordered conversation = %+v", withFlag)
	}
}
