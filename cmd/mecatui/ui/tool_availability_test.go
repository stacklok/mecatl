package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestADR_0370_Scenario3_ClientConfirmationAndReplacement(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m, client.ToolCallMsg{ID: "fast", Name: "Read"}, client.ToolCallMsg{ID: "slow", Name: "Grep"}, client.ToolProgressMsg{Text: "working"})
	available := &mecatlv1.Event{Type: "tool.result.available", ToolResult: &mecatlv1.ToolResult{CallId: "fast", Content: "available", Blocks: []*mecatlv1.ContentBlock{{Kind: mecatlv1.ContentBlock_KIND_RESOURCE_LINK, Name: "item", Url: "https://example.invalid/item"}}}}
	msg, ok := client.EventToMsg(available).(client.ToolResultMsg)
	if !ok {
		t.Fatal("availability not mapped to tool result")
	}
	m = applyAll(m, msg)
	snap, found := m.conv.scrollback.SnapshotForCall("fast")
	if !found || m.conv.scrollback.Len() != 2 || m.activeTool != "Grep" || m.toolProgress != "" {
		t.Fatalf("availability did not settle matching card: found=%v len=%d active=%q progress=%q", found, m.conv.scrollback.Len(), m.activeTool, m.toolProgress)
	}
	card := snap.Payload.(scrollback.ToolCardSnapshot)
	if !card.Resolved || card.Result.Body != "available" || len(card.Result.Artifacts) != 1 || card.Result.Artifacts[0].Name != "item" {
		t.Fatalf("available card = %+v", card)
	}
	available.Type = "tool.result"
	m = applyAll(m, client.EventToMsg(available))
	confirmed, _ := m.conv.scrollback.SnapshotForCall("fast")
	confirmedCard := confirmed.Payload.(scrollback.ToolCardSnapshot)
	if confirmed.ID != snap.ID || !reflect.DeepEqual(confirmedCard.Call, card.Call) ||
		confirmedCard.Resolved != card.Resolved || confirmedCard.Finished != card.Finished ||
		confirmedCard.Failed != card.Failed || !reflect.DeepEqual(confirmedCard.Result, card.Result) || m.conv.scrollback.Len() != 2 {
		t.Fatalf("identical canonical changed displayed card or identity: before=%+v after=%+v len=%d", snap, confirmed, m.conv.scrollback.Len())
	}
	available.Type = "tool.result.available"
	available.ToolResult.CallId = "slow"
	m = applyAll(m, client.EventToMsg(available))
	snap, _ = m.conv.scrollback.SnapshotForCall("slow")
	available.Type = "tool.result"
	available.ToolResult = &mecatlv1.ToolResult{CallId: "slow", Content: "cancelled", IsError: true}
	m = applyAll(m, client.EventToMsg(available))
	replaced, _ := m.conv.scrollback.SnapshotForCall("slow")
	changed := replaced.Payload.(scrollback.ToolCardSnapshot)
	if replaced.ID != snap.ID || replaced.Revision != snap.Revision+1 || m.conv.scrollback.Len() != 2 || !changed.Result.IsError || changed.Result.Body != "cancelled" || len(changed.Result.Artifacts) != 0 {
		t.Fatalf("canonical replacement = %+v, card=%+v", replaced, changed)
	}
	if strings.Contains(m.View().Content, "orphan tool result") {
		t.Fatal("canonical confirmation or replacement produced orphan notice")
	}
	// Reconnect can miss the transient event; canonical alone must still settle.
	other := newMCPModel(t, aztec(), nil)
	other = applyAll(other, client.ToolCallMsg{ID: "slow", Name: "Grep"}, client.EventToMsg(available))
	fallback, _ := other.conv.scrollback.SnapshotForCall("slow")
	if !fallback.Payload.(scrollback.ToolCardSnapshot).Result.IsError {
		t.Fatalf("canonical-only card = %+v", fallback)
	}
}
