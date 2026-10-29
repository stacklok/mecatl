package memstore_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestEventLogOwnsArchiveStorage(t *testing.T) {
	ctx := context.Background()
	log := memstore.NewEventLog()
	message := session.NewAssistantMessage("visible", []session.ToolCall{session.NewToolCall("call-current", "Read", []byte(`{}`))})
	message.Replay = &session.MessageReplay{
		Bindings: []session.ToolCallBinding{{CallID: "call-current", PairingID: "provider-private"}},
		Provider: &session.ProviderReplay{Format: "test/1", Version: 1, Data: []byte{1, 2}},
	}
	e := session.Event{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: []session.Message{message}}}
	if err := log.Append(ctx, "s", e); err != nil {
		t.Fatal(err)
	}
	e.CompactionArchive.Replaced[0].Replay.Provider.Data[0] = 9
	e.CompactionArchive.Replaced[0].ToolCalls[0].Args[0] = 'x'
	for stored, err := range log.Read(ctx, "s") {
		if err != nil {
			t.Fatal(err)
		}
		m := stored.CompactionArchive.Replaced[0]
		if m.Replay.Provider.Data[0] != 1 || m.ToolCalls[0].Args[0] != '{' {
			t.Fatal("append alias")
		}
		m.Replay.Provider.Data[0] = 8
	}
	for rec, err := range log.ReadAfter(ctx, "s", "", port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Event.CompactionArchive.Replaced[0].Replay.Provider.Data[0] != 1 {
			t.Fatal("read alias")
		}
		rec.Event.CompactionArchive.Replaced[0].Replay.Provider.Data[0] = 7
	}
	for stored, err := range log.Read(ctx, "s") {
		if err != nil {
			t.Fatal(err)
		}
		if stored.CompactionArchive.Replaced[0].Replay.Provider.Data[0] != 1 {
			t.Fatal("cursor read alias")
		}
	}
}
