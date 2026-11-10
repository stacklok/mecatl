package memstore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestCarrierDirectMemstoreOwnershipAndAccounting(t *testing.T) {
	ctx := context.Background()
	s := session.New("carrier", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAssistant(session.Message{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "canonical-call", Name: "Read", Args: []byte(`{}`)}},
		Replay: &session.MessageReplay{Bindings: []session.ToolCallBinding{{CallID: "canonical-call", PairingID: "provider", ItemID: "item"}}, Provider: &session.ProviderReplay{Format: "future.provider", Version: 99, Data: bytes.Repeat([]byte{0xff, 0, '\n'}, 4096)}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolResults([]session.ToolResult{{CallID: "canonical-call", Content: "result"}}); err != nil {
		t.Fatal(err)
	}
	want := session.CloneMessages(s.Conversation.Messages)
	store := New()
	if err := store.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.Conversation.Messages[0].Replay.Provider.Data[0] = 0
	s.Conversation.Messages[0].Replay.Bindings[0].PairingID = "changed"
	for range 2 {
		loaded, err := store.Load(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !loaded.LearningEligible() || !reflect.DeepEqual(loaded.Conversation.Messages, want) {
			t.Fatal("direct memstore aliases or loses replay or eligibility")
		}
		loaded.Conversation.Messages[0].Replay.Provider.Data[0] = 0
		loaded.Conversation.Messages[0].Replay.Bindings[0].ItemID = "changed"
	}
	snap := store.sessions[s.ID]
	base, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	base.Conversation.Messages[0].Replay = nil
	without, err := sessnap.Of(base)
	if err != nil {
		t.Fatal(err)
	}
	delta := estimateSnapshotBytes(snap) - estimateSnapshotBytes(without)
	if delta < int64(encodedBytesLen(len(want[0].Replay.Provider.Data))) {
		t.Fatal("opaque replay bytes missing from size accounting")
	}
	if store.estimatedBytes[s.ID] != estimateSnapshotBytes(snap) {
		t.Fatal("save did not cache complete carrier size")
	}
}

func TestLearningExclusionSurvivesMemstoreAndCodecRestart(t *testing.T) {
	data, err := os.ReadFile("../sessnap/testdata/legacy-completed.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, history := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`[{"role":"user","text":"legacy text"}]`)} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		fields["learning_eligible"] = json.RawMessage(`true`)
		incarnation, err := json.Marshal(session.NewIncarnationID())
		if err != nil {
			t.Fatal(err)
		}
		fields["incarnation"] = incarnation
		if history != nil {
			fields["messages"] = history
		}
		wire, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		s, err := sessnap.UnmarshalFormat(wire, "sessnap-json/1")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		store := New()
		for range 2 {
			wantHistory := session.CloneMessages(s.Conversation.Messages)
			if err := store.Save(ctx, s); err != nil {
				t.Fatal(err)
			}
			s, err = store.Load(ctx, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if s.LearningEligible() || !reflect.DeepEqual(s.Conversation.Messages, wantHistory) {
				t.Fatal("memstore reload enabled learning or changed legacy history/replay")
			}
			if err := s.Reopen(); err != nil {
				t.Fatal(err)
			}
			if err := s.ReplaceHistoryAtBoundary([]session.Message{session.NewAssistantMessage("compacted", nil)}); err != nil {
				t.Fatal(err)
			}
			s.BeginRun("later-current-run")
			if err := s.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordAssistant(session.NewAssistantMessage("new answer", nil)); err != nil {
				t.Fatal(err)
			}
			if err := s.Complete(); err != nil {
				t.Fatal(err)
			}
			wire, err = sessnap.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			s, err = sessnap.UnmarshalFormat(wire, sessnap.Format)
			if err != nil {
				t.Fatal(err)
			}
			store = New()
		}
	}
}
