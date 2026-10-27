package memstore

import (
	"bytes"
	"context"
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
		Replay: &session.MessageReplay{Bindings: []session.ToolCallBinding{{CallID: "canonical-call", PairingID: "provider", ItemID: "item"}}, Provider: &session.ProviderReplay{Format: "future.provider", Version: 99, Data: bytes.Repeat([]byte{0xff, 0, '\n'}, 4096)}},
		Legacy: &session.LegacyMessageIdentity{Incarnation: s.Incarnation(), Namespace: "sessnap/legacy-v1", Ordinal: 17, Calls: []session.LegacyCallIdentity{{CallID: "canonical-call", Slot: 3, ProviderID: "original-provider", ItemID: "original-item"}}}}); err != nil {
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
	s.Conversation.Messages[0].Legacy.Calls[0].ProviderID = "changed"
	for range 2 {
		loaded, err := store.Load(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(loaded.Conversation.Messages, want) {
			t.Fatal("direct memstore aliases or loses carrier/provenance")
		}
		loaded.Conversation.Messages[0].Replay.Provider.Data[0] = 0
		loaded.Conversation.Messages[0].Replay.Bindings[0].ItemID = "changed"
		loaded.Conversation.Messages[0].Legacy.Ordinal = 0
		loaded.Conversation.Messages[0].Legacy.Calls[0].Slot = 0
	}
	snap := store.sessions[s.ID]
	base, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	base.Conversation.Messages[0].Replay = nil
	base.Conversation.Messages[0].Legacy = nil
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

func TestUnsupportedLegacyDirectMemstoreOwnershipAndAccounting(t *testing.T) {
	for _, name := range []string{"legacy-completed"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("../sessnap/testdata/" + name + ".json")
			ordinal := 2
			data = bytes.Replace(data, []byte(`"CallID":"reused", "Content":"first"`), []byte(`"CallID":"missing", "Content":"first"`), 1)
			if err != nil {
				t.Fatal(err)
			}
			s, err := sessnap.Unmarshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Reopen(); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			store := New()
			wantHistory := session.CloneMessages(s.Conversation.Messages)
			if err := store.Save(ctx, s); err != nil {
				t.Fatal(err)
			}
			s.Conversation.Messages[ordinal].Legacy.Unsupported[0] = '!'
			for range 2 {
				loaded, err := store.Load(ctx, s.ID)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.State != session.StateIdle || !reflect.DeepEqual(loaded.Conversation.Messages, wantHistory) {
					t.Fatal("direct memstore lost historical content")
				}
				if _, ok := loaded.PendingAsk(); ok {
					t.Fatal("obsolete approval resurrected")
				}
				if _, ok := loaded.PendingAuthorization(); ok {
					t.Fatal("obsolete authorization resurrected")
				}
				if err := store.Save(ctx, loaded); err != nil {
					t.Fatal(err)
				}
				if err := loaded.BeginTurn(); err != nil {
					t.Fatal(err)
				}
				loaded.Conversation.Messages[ordinal].Legacy.Unsupported[0] = '!'
			}
			snap := store.sessions[s.ID]
			withoutSession, err := snap.Restore()
			if err != nil {
				t.Fatal(err)
			}
			withoutSession.Conversation.Messages[ordinal].Legacy.Unsupported = nil
			withoutMetadata, err := sessnap.Of(withoutSession)
			if err != nil {
				t.Fatal(err)
			}
			if delta := estimateSnapshotBytes(snap) - estimateSnapshotBytes(withoutMetadata); delta < int64(encodedBytesLen(len(wantHistory[ordinal].Legacy.Unsupported))) {
				t.Fatal("unsupported historical bytes missing from size accounting")
			}
		})
	}
}
