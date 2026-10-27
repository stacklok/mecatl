package sessnap

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyAmbiguousNonpendingHistorySalvaged(t *testing.T) {
	for _, key := range []string{"", "duplicate"} {
		data := []byte(`{"id":"history","state":"idle","environment_ref":{"Kind":"mem","ID":"workspace","Revision":"v1"},"messages":[{"role":"assistant","tool_calls":[{"ID":"` + key + `","Name":"Read","Args":{}},{"ID":"` + key + `","Name":"Read","Args":{}}]},{"role":"tool","tool_result":{"CallID":"` + key + `","Content":"unattached"}}]}`)
		first := restoreFixture(t, data)
		second := restoreFixture(t, data)
		messages := first.Conversation.Messages
		if !reflect.DeepEqual(messages, second.Conversation.Messages) || len(messages) != 2 || len(messages[0].ToolCalls) != 0 || messages[1].ToolResult != nil || messages[1].Text != "unattached" {
			t.Fatal("ambiguous history gained guessed links or lost content")
		}
		for i, m := range messages {
			if m.Legacy == nil || m.Legacy.Ordinal != i || len(m.Legacy.Unsupported) == 0 {
				t.Fatal("ambiguous history lost source provenance")
			}
		}
		encoded, err := Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		if got := restoreFixture(t, encoded); !sameStoredHistory(t, got.Conversation.Messages, messages) {
			t.Fatal("salvaged history changed on reload")
		}
	}
}

func TestUnattachedLegacyResultPreservesTypedContent(t *testing.T) {
	var old Snapshot
	if err := json.Unmarshal(legacyFixture(t, "legacy-completed"), &old); err != nil {
		t.Fatal(err)
	}
	parts := []session.Content{
		session.NewTextBlock("typed result text"),
		session.NewResourceLinkBlock("fixture:asset", "asset", "title", "description", "image/png", 3, []string{"user"}),
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{1, 2, 3}},
	}
	old.legacyMessages = append(old.legacyMessages, legacyMessageDTO{Role: session.RoleTool, Text: "row text", Parts: []contentDTO{{Kind: session.MediaAudio, MIMEType: "audio/wav", Data: []byte{4, 5, 6}}}, ToolResult: &legacyResultDTO{CallID: "missing", Content: "result summary", Parts: parts}})
	s, err := old.Restore()
	if err != nil {
		t.Fatal(err)
	}
	index := len(s.Conversation.Messages) - 1
	m := s.Conversation.Messages[index]
	history := session.HistoricalMessage(m)
	if m.ToolResult != nil || m.Legacy.ResultOrdinal != -1 || m.Legacy.ResultSlot != -1 || history == nil || history.Text != "row text\nresult summary" || !history.AttachmentUnavailable {
		t.Fatal("unattached result lost text or gained an authoritative link")
	}
	if len(history.Parts) != 4 || !bytes.Equal(history.Parts[0].Data, []byte{4, 5, 6}) || !reflect.DeepEqual(history.Parts[1:], parts) {
		t.Fatal("unattached result lost typed content/media")
	}
	if len(m.Parts) != 1 || m.Parts[0].BlockKind != "" {
		t.Fatal("tool-result blocks leaked into message media")
	}
	want := session.CloneMessages(s.Conversation.Messages)
	for range 2 {
		snap, err := Of(s)
		if err != nil {
			t.Fatal(err)
		}
		s, err = snap.Restore()
		if err != nil {
			t.Fatal(err)
		}
		data, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		s = restoreFixture(t, data)
		if !sameStoredHistory(t, s.Conversation.Messages, want) {
			t.Fatal("cycles changed migrated history")
		}
	}
}
