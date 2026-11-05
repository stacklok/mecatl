package server_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

func TestManualCompactionRejectsOverlappingProviderPairingBeforeSave(t *testing.T) {
	store, s, owner := compactFixture(t, session.StateIdle)
	var original []session.Message
	for _, id := range []session.ToolCallID{"a", "b"} {
		m := session.NewAssistantMessage(strings.Repeat("original ", 30), []session.ToolCall{session.NewToolCall(id, "Read", nil)})
		m.Replay = &session.MessageReplay{Bindings: []session.ToolCallBinding{{CallID: id, PairingID: "private-pair"}}, Provider: &session.ProviderReplay{Format: "opaque", Version: 1, Data: []byte("original private bytes")}}
		original = append(original, m, session.NewToolMessage(session.NewToolResult(id, "read")))
	}
	if err := s.ReplaceHistoryAtBoundary(original); err != nil {
		t.Fatal(err)
	}
	if err := store.Store.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	candidate := session.CloneMessages(original)
	candidate[0].Text = ""
	candidate[2].Text = ""
	candidate[1], candidate[2] = candidate[2], candidate[1]
	if err := session.ValidateToolPairing(candidate); err != nil {
		t.Fatalf("fixture not canonically valid: %v", err)
	}
	svc := newCompactService(t, store, serviceCompactCompactor{out: candidate}, true, nil, nil)
	result, err := svc.CompactSession(t.Context(), s.ID, owner)
	if !errors.Is(err, agent.ErrCompactionWouldOrphan) || result.Changed || len(result.Archive) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	reloaded, err := store.Load(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Conversation.Messages, original) {
		t.Fatal("rejected replacement changed saved history/replay")
	}
	_, _, events := store.snapshot()
	if len(events) != 0 {
		t.Fatal("rejected replacement published successful compaction")
	}
}
