package openai

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestReasoningItemIDMixedItemBoundary is the cold reproducer for the
// reasoning-item id="" Responses-replay fix. It drives a recorded SSE fixture
// end-to-end through decodeSSE (the exact translate path the live adapter uses)
// and asserts the reasoning item's OWN provider id ("rs_123") survives the
// SSE→port.Chunk boundary intact and is NOT confused with the assistant message
// item's id ("msg_abc") that the text deltas ride on.
//
// The fixture models the mixed-item stream the bug lives in: a reasoning item
// rs_123 emits summary deltas and an assembled output_item.done carrying the
// encrypted replay blob, THEN a message item msg_abc emits the visible text.
// Under the PRE-FIX conversion the reasoning arm emitted the ChunkReasoningItem
// with NO id (it dropped item.ID), so the loop could not stamp
// Message.ReasoningItemID, replay serialised id:"" and strict gateways 400'd
// on turn 2+ (see HANDOVER-reasoning-item-id-replay.md). This test fails on that
// old behavior and passes now.
//
// The id now rides INSIDE the packed reasoning envelope (reasoning.go) rather
// than on port.Chunk.ReasoningItemID, because a turn may carry several items and
// each blob only verifies under its own id. The pairing asserted here is the
// same one; only where it is written down moved.
func TestReasoningItemIDMixedItemBoundary(t *testing.T) {
	chunks := decodeFixture(t, "reasoning_item_id_mixed.sse")

	// (a) The reasoning replay chunk pairs the blob with the reasoning item's OWN
	// id "rs_123" — not the message item's "msg_abc", and not empty. Under the
	// pre-fix code item.ID was dropped entirely, so this assertion discriminates.
	var reasoningItemChunk *port.Chunk
	for i := range chunks {
		if chunks[i].Kind == port.ChunkReasoningItem {
			reasoningItemChunk = &chunks[i]
		}
	}
	if reasoningItemChunk == nil {
		t.Fatalf("no ChunkReasoningItem in stream; chunks=%+v", chunks)
	}
	items := unpackReasoningItems(reasoningItemChunk.Text, "")
	if len(items) != 1 {
		t.Fatalf("unpacked %d reasoning items, want 1; packed=%q", len(items), reasoningItemChunk.Text)
	}
	if items[0].ID != "rs_123" {
		t.Errorf("reasoning item id = %q, want %q (must be the reasoning item's own id, not the message item's and not empty)",
			items[0].ID, "rs_123")
	}
	if items[0].Blob != "ENCRYPTED_RS_123" {
		t.Errorf("reasoning item blob = %q, want %q (the encrypted_content replay blob)", items[0].Blob, "ENCRYPTED_RS_123")
	}

	// (b) The assistant TEXT chunks must NOT carry the reasoning item id — the
	// reasoning id belongs only to the reasoning item, not the message item.
	// (ChunkText has no ReasoningItemID carrier at all, so any non-empty value
	// here — including "rs_123" or "msg_abc" — is a boundary leak.)
	for i, c := range chunks {
		if c.Kind != port.ChunkText {
			continue
		}
		if c.ReasoningItemID != "" {
			t.Errorf("text chunk %d leaks ReasoningItemID = %q; text deltas must carry no reasoning item id", i, c.ReasoningItemID)
		}
	}

	// (c) The final converted assistant message — as the loop folds it — must
	// replay the blob under "rs_123". Asserting this through assistantItems (the
	// real replay path) rather than the Message field directly keeps the test on
	// the behaviour that matters: what goes on the wire. Under the pre-fix code
	// the blob threaded through but the id was lost, serialising `"id":""`.
	msg := aggregateAssistantMessage(chunks)
	if msg.Text != "The answer is 42." {
		t.Errorf("Message.Text = %q, want %q", msg.Text, "The answer is 42.")
	}
	var replayed []reasoningItem
	for _, it := range assistantItems(msg) {
		if it.OfReasoning != nil {
			replayed = append(replayed, reasoningItem{ID: it.OfReasoning.ID, Blob: it.OfReasoning.EncryptedContent.Value})
		}
	}
	want := []reasoningItem{{ID: "rs_123", Blob: "ENCRYPTED_RS_123"}}
	if !reflect.DeepEqual(replayed, want) {
		t.Errorf("replayed reasoning items = %+v, want %+v (blob replayed under the reasoning item's own id)", replayed, want)
	}
}

// aggregateAssistantMessage mirrors the chunk→session.Message aggregation the
// engine loop performs (engine/agent/loop.go: ReasoningItemID is
// last-non-empty-wins alongside the additive Reasoning blob). It lives here as
// the provider package's view of what the loop will build from this stream, so
// the regression is pinned at the adapter boundary without importing engine/agent
// (which the provider module deliberately does not depend on).
func aggregateAssistantMessage(chunks []port.Chunk) session.Message {
	var text, reasoning, reasoningItemID string
	var calls []session.ToolCall
	stop := session.StopNone
	var usage session.Usage
	for _, c := range chunks {
		switch c.Kind {
		case port.ChunkText:
			text += c.Text
		case port.ChunkReasoning:
			// display-only summary; not the replay blob
		case port.ChunkReasoningItem:
			reasoning += c.Text
			if c.ReasoningItemID != "" {
				reasoningItemID = c.ReasoningItemID
			}
		case port.ChunkToolCall:
			if c.ToolCall != nil {
				calls = append(calls, *c.ToolCall)
			}
		case port.ChunkUsage:
			if c.Usage != nil {
				usage = usage.Add(*c.Usage)
			}
		case port.ChunkDone:
			stop = c.Stop
		}
	}
	msg := session.NewAssistantMessage(text, reasoning, calls)
	msg.ReasoningItemID = reasoningItemID
	_ = stop
	_ = usage
	return msg
}
